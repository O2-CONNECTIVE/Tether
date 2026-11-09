using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;

namespace Tether.OwnerProbe {
    // A runner compatibility experiment. No PID lookup, external command, role classifier,
    // bearer receipt, production caller, or authority to certify a driver/app process tree.
    public static class Probe {
        private sealed class Owner {
            internal IntPtr job;
            internal Native.ProcessInfo process;
            internal bool created, assigned, resumed, forced;
        }
        // Unconfirmed cleanup keeps the exact handles until this disposable host exits.
        private static readonly List<Owner> retained = new List<Owner>();
        private static void Require(bool condition, string code) {
            if (!condition) throw new InvalidOperationException(code);
        }
        private static bool Empty(Owner owner) {
            Native.Accounting accounting;
            Require(Native.QueryInformationJobObject(owner.job, 1, out accounting,
                (uint)Marshal.SizeOf<Native.Accounting>(), IntPtr.Zero), "OWNER_PROBE_JOB_QUERY_FAILED");
            return accounting.active == 0;
        }
        private static bool WaitEmpty(Owner owner) {
            var elapsed = Stopwatch.StartNew();
            do {
                if (Empty(owner)) return true;
                Thread.Sleep(10);
            } while (elapsed.ElapsedMilliseconds < 5000);
            return false;
        }
        private static void Create(Owner owner) {
            owner.job = Native.CreateJobObjectW(IntPtr.Zero, null);
            Require(owner.job != IntPtr.Zero, "OWNER_PROBE_JOB_CREATE_FAILED");
            var limits = new Native.ExtendedLimits();
            // No BREAKAWAY_OK or SILENT_BREAKAWAY_OK. No privilege/token/runner-job mutation.
            limits.basic.flags = 0x2000; // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
            Require(Native.SetInformationJobObject(owner.job, 9, ref limits,
                (uint)Marshal.SizeOf<Native.ExtendedLimits>()), "OWNER_PROBE_JOB_LIMIT_FAILED");
            var startup = new Native.Startup { cb = (uint)Marshal.SizeOf<Native.Startup>() };
            string image = Path.Combine(Environment.SystemDirectory, "cmd.exe");
            IntPtr environment = Marshal.AllocHGlobal(4);
            try {
                Marshal.WriteInt32(environment, 0); // Empty UTF-16 environment, no parent secrets.
                owner.created = Native.CreateProcessW(image, new StringBuilder("\"" + image + "\" /d /c exit 0"),
                    IntPtr.Zero, IntPtr.Zero, false, 0x08000404, environment, Environment.SystemDirectory,
                    ref startup, out owner.process); // NO_WINDOW | UNICODE_ENVIRONMENT | SUSPENDED
                Require(owner.created, "OWNER_PROBE_PROCESS_CREATE_FAILED");
            } finally { Marshal.FreeHGlobal(environment); }
        }
        private static void Cleanup(Owner owner) {
            bool confirmed = !owner.created;
            try {
                if (owner.created) {
                    // Stop only original handles. Try both cleanup operations even if one fails.
                    bool rootExited = Native.WaitForSingleObject(owner.process.process, 0) == 0;
                    bool rootStopped = rootExited;
                    if (!rootExited) {
                        owner.forced = true;
                        rootStopped = Native.TerminateProcess(owner.process.process, 77);
                    }
                    // A job query error must not prevent attempting owned-job cleanup.
                    bool empty = false;
                    try { empty = Empty(owner); } catch (InvalidOperationException) { }
                    bool jobStopped = empty;
                    if (!empty) {
                        owner.forced = true;
                        jobStopped = Native.TerminateJobObject(owner.job, 77);
                    }
                    Require(rootStopped && jobStopped, "OWNER_PROBE_STOP_UNCONFIRMED");
                    Require(Native.WaitForSingleObject(owner.process.process, 5000) == 0,
                        "OWNER_PROBE_PROCESS_EXIT_UNCONFIRMED");
                    Require(WaitEmpty(owner), "OWNER_PROBE_JOB_EXIT_UNCONFIRMED");
                    confirmed = true;
                }
            } finally {
                if (!confirmed) {
                    retained.Add(owner);
                } else {
                    // Any close failure fails this experiment; never retry a possibly reused handle.
                    bool closed = true;
                    if (owner.process.thread != IntPtr.Zero) closed &= Native.CloseHandle(owner.process.thread);
                    if (owner.process.process != IntPtr.Zero) closed &= Native.CloseHandle(owner.process.process);
                    if (owner.job != IntPtr.Zero) closed &= Native.CloseHandle(owner.job);
                    Require(closed, "OWNER_PROBE_HANDLE_CLOSE_FAILED");
                }
            }
        }
        public static string Run(string scenario) {
            Require(scenario == "normal-exit" || scenario == "assignment-rejected" || scenario == "cancel-before-resume",
                "OWNER_PROBE_UNKNOWN_CASE");
            var owner = new Owner();
            bool normal = false;
            try {
                Create(owner);
                if (scenario == "assignment-rejected") {
                    // Real OS rejection with a null job; never resume after this failure.
                    Require(!Native.AssignProcessToJobObject(IntPtr.Zero, owner.process.process),
                        "OWNER_PROBE_REJECTION_MISSING");
                    bool member;
                    Require(Native.IsProcessInJob(owner.process.process, owner.job, out member) && !member,
                        "OWNER_PROBE_UNEXPECTED_MEMBERSHIP");
                } else {
                    owner.assigned = Native.AssignProcessToJobObject(owner.job, owner.process.process);
                    Require(owner.assigned, "OWNER_PROBE_ASSIGN_FAILED");
                    bool member;
                    Require(Native.IsProcessInJob(owner.process.process, owner.job, out member) && member,
                        "OWNER_PROBE_MEMBERSHIP_UNCONFIRMED");
                    if (scenario == "normal-exit") {
                        Require(Native.ResumeThread(owner.process.thread) == 1, "OWNER_PROBE_RESUME_FAILED");
                        owner.resumed = true;
                        Require(Native.WaitForSingleObject(owner.process.process, 10000) == 0,
                            "OWNER_PROBE_NORMAL_EXIT_TIMEOUT");
                        uint code;
                        Require(Native.GetExitCodeProcess(owner.process.process, out code) && code == 0,
                            "OWNER_PROBE_EXIT_CODE_FAILED");
                        Require(WaitEmpty(owner), "OWNER_PROBE_NORMAL_JOB_NOT_EMPTY");
                        normal = true;
                    }
                }
            } finally { Cleanup(owner); }
            Require(scenario == "normal-exit" ? normal && !owner.forced && owner.resumed && owner.assigned
                : !normal && owner.forced && !owner.resumed, "OWNER_PROBE_VERDICT_MISMATCH");
            return scenario + ":passed";
        }
    }
}
