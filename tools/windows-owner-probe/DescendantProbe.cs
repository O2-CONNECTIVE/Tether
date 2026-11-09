using System;
using System.Diagnostics;
using System.IO;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;

namespace Tether.OwnerProbe {
    // Fixed executable fixture only. The parent stays alive and retains its child's
    // creation-time handles. No PID lookup, owner transfer, or production role proof.
    internal static class DescendantProbe {
        private static void Require(bool condition) {
            if (!condition) throw new InvalidOperationException("OWNER_PROBE_DESCENDANT_FAILED");
        }
        private static void Member(IntPtr process, IntPtr job) {
            bool member;
            Require(Native.IsProcessInJob(process, job, out member) && member);
        }
        private static void Exited(IntPtr process, uint expected) {
            Require(Native.WaitForSingleObject(process, 10000) == 0);
            uint code;
            Require(Native.GetExitCodeProcess(process, out code) && code == expected);
        }
        private static bool Empty(IntPtr job) {
            Native.Accounting accounting;
            Require(Native.QueryInformationJobObject(job, 1, out accounting,
                (uint)Marshal.SizeOf(typeof(Native.Accounting)), IntPtr.Zero));
            return accounting.active == 0;
        }
        private static void WaitEmpty(IntPtr job) {
            var timer = Stopwatch.StartNew();
            while (!Empty(job)) {
                Require(timer.ElapsedMilliseconds < 5000);
                Thread.Sleep(10);
            }
        }
        private static IntPtr EmptyEnvironment() {
            IntPtr environment = Marshal.AllocHGlobal(4);
            Marshal.WriteInt32(environment, 0);
            return environment;
        }
        private static Native.ProcessInfo StartParent(IntPtr queryJob, bool cancel) {
            IntPtr size = IntPtr.Zero;
            Require(!DescendantNative.InitializeProcThreadAttributeList(IntPtr.Zero, 1, 0, ref size)
                && Marshal.GetLastWin32Error() == 122 && size.ToInt64() > 0);
            IntPtr list = Marshal.AllocHGlobal(size);
            IntPtr handles = Marshal.AllocHGlobal(IntPtr.Size);
            IntPtr environment = EmptyEnvironment();
            bool initialized = false;
            try {
                initialized = DescendantNative.InitializeProcThreadAttributeList(list, 1, 0, ref size);
                Require(initialized);
                Marshal.WriteIntPtr(handles, queryJob);
                // PROC_THREAD_ATTRIBUTE_HANDLE_LIST: exactly the query-only Job duplicate.
                Require(DescendantNative.UpdateProcThreadAttribute(list, 0, new IntPtr(0x20002),
                    handles, new IntPtr(IntPtr.Size), IntPtr.Zero, IntPtr.Zero));
                var startup = new DescendantNative.StartupEx();
                startup.startup.cb = (uint)Marshal.SizeOf(typeof(DescendantNative.StartupEx));
                startup.attributes = list;
                string image = Assembly.GetExecutingAssembly().Location;
                var command = new StringBuilder("\"" + image + "\" parent " +
                    queryJob.ToInt64().ToString(System.Globalization.CultureInfo.InvariantCulture) +
                    (cancel ? " cancel" : " normal"));
                Native.ProcessInfo process;
                Require(DescendantNative.CreateProcessW(image, command, IntPtr.Zero, IntPtr.Zero,
                    true, 0x08080404, environment, Environment.SystemDirectory, ref startup, out process));
                return process; // NO_WINDOW | EXTENDED_STARTUPINFO_PRESENT | UNICODE | SUSPENDED
            } finally {
                if (initialized) DescendantNative.DeleteProcThreadAttributeList(list);
                Marshal.FreeHGlobal(environment);
                Marshal.FreeHGlobal(handles);
                Marshal.FreeHGlobal(list);
            }
        }
        private static int Parent(IntPtr queryJob, bool cancel) {
            var child = new Native.ProcessInfo();
            bool created = false, confirmed = false;
            IntPtr environment = EmptyEnvironment();
            try {
                Member(DescendantNative.GetCurrentProcess(), queryJob);
                string image = Path.Combine(Environment.SystemDirectory, "cmd.exe");
                var startup = new Native.Startup { cb = (uint)Marshal.SizeOf(typeof(Native.Startup)) };
                created = Native.CreateProcessW(image, new StringBuilder("\"" + image + "\" /d /c exit 0"),
                    IntPtr.Zero, IntPtr.Zero, false, 0x08000404, environment,
                    Environment.SystemDirectory, ref startup, out child);
                Require(created);
                // No child assignment: membership must come from real OS Job inheritance.
                Member(child.process, queryJob);
                Require(Native.WaitForSingleObject(child.process, 0) == 258);
                if (cancel) {
                    Require(Native.TerminateProcess(child.process, 77));
                    Exited(child.process, 77);
                } else {
                    Require(Native.ResumeThread(child.thread) == 1);
                    Exited(child.process, 0);
                }
                confirmed = true;
                // Distinct fixed exit code: forced child cleanup is never normal completion.
                return cancel ? 42 : 0;
            } finally {
                Marshal.FreeHGlobal(environment);
                if (created && !confirmed) {
                    bool stopped = Native.WaitForSingleObject(child.process, 0) == 0;
                    if (!stopped) stopped = Native.TerminateProcess(child.process, 77);
                    confirmed = stopped && Native.WaitForSingleObject(child.process, 5000) == 0;
                }
                // On unconfirmed exit keep original handles until this disposable helper exits.
                // The outer owner still holds the Job and performs its own bounded cleanup.
                if (!created || confirmed) {
                    bool closed = true;
                    if (child.thread != IntPtr.Zero) closed &= Native.CloseHandle(child.thread);
                    if (child.process != IntPtr.Zero) closed &= Native.CloseHandle(child.process);
                    closed &= Native.CloseHandle(queryJob);
                    Require(closed);
                }
                Require(!created || confirmed);
            }
        }
        private static void Run(bool cancel) {
            IntPtr job = Native.CreateJobObjectW(IntPtr.Zero, null);
            Require(job != IntPtr.Zero);
            IntPtr queryJob = IntPtr.Zero;
            var parent = new Native.ProcessInfo();
            bool created = false, confirmed = false, completed = false;
            try {
                var limits = new Native.ExtendedLimits();
                limits.basic.flags = 0x2000; // KILL_ON_JOB_CLOSE, no breakaway flags.
                Require(Native.SetInformationJobObject(job, 9, ref limits,
                    (uint)Marshal.SizeOf(typeof(Native.ExtendedLimits))));
                IntPtr self = DescendantNative.GetCurrentProcess();
                Require(DescendantNative.DuplicateHandle(self, job, self, out queryJob, 4, true, 0));
                parent = StartParent(queryJob, cancel);
                created = true;
                Require(Native.AssignProcessToJobObject(job, parent.process));
                Member(parent.process, job);
                Require(Native.ResumeThread(parent.thread) == 1);
                Exited(parent.process, cancel ? 42u : 0u);
                WaitEmpty(job);
                completed = true;
            } finally {
                bool rootStopped = !created || Native.WaitForSingleObject(parent.process, 0) == 0;
                if (!rootStopped) rootStopped = Native.TerminateProcess(parent.process, 77);
                // Always attempt Job cleanup even if the root stop failed or query throws.
                bool empty = false;
                try { empty = Empty(job); } catch (InvalidOperationException) { }
                bool jobStopped = empty || Native.TerminateJobObject(job, 77);
                confirmed = rootStopped && jobStopped;
                if (created) confirmed &= Native.WaitForSingleObject(parent.process, 5000) == 0;
                if (confirmed) {
                    try { WaitEmpty(job); } catch (InvalidOperationException) { confirmed = false; }
                }
                // Keep exact handles until host exit on unconfirmed cleanup; do not close/retry.
                if (confirmed) {
                    bool closed = true;
                    if (parent.thread != IntPtr.Zero) closed &= Native.CloseHandle(parent.thread);
                    if (parent.process != IntPtr.Zero) closed &= Native.CloseHandle(parent.process);
                    if (queryJob != IntPtr.Zero) closed &= Native.CloseHandle(queryJob);
                    closed &= Native.CloseHandle(job);
                    Require(closed);
                }
                Require(confirmed);
            }
            Require(completed);
        }
        public static int Main(string[] args) {
            try {
                if (args.Length == 3 && args[0] == "parent" &&
                    (args[2] == "normal" || args[2] == "cancel")) {
                    long handle;
                    Require(long.TryParse(args[1], System.Globalization.NumberStyles.None,
                        System.Globalization.CultureInfo.InvariantCulture, out handle) && handle > 0);
                    return Parent(new IntPtr(handle), args[2] == "cancel");
                }
                Require(args.Length == 0 && Environment.Is64BitProcess);
                Run(false);
                Console.WriteLine("descendant-normal-exit:passed");
                Run(true);
                Console.WriteLine("descendant-cancel-before-resume:passed");
                Console.WriteLine("OWNER_PROBE_DESCENDANT_CASES_PASSED_2");
                return 0;
            } catch (Exception) {
                // No raw exception, PID, path, command line, absolute time, or OS error output.
                Console.WriteLine("OWNER_PROBE_DESCENDANT_FAILED");
                return 1;
            }
        }
    }
}
