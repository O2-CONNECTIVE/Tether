using System;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Threading;
using Tether.OwnerProbe;

namespace Tether.AppOwnerFixture {
    internal enum Scenario { LateChild, ForcedCleanup, CancelBeforeResume, AssignmentDenied }
    internal enum Outcome { NormalExit, ForcedCleanup, Cancelled, AssignmentDenied }

    // One disposable, single-threaded instance owns the original handles for its entire run.
    // This is an internal finite fixture, not a production ownership capability.
    internal sealed class AppOwner {
        private IntPtr job;
        private readonly IntPtr[] inherited = new IntPtr[4]; // query Job, ready, release, done
        private readonly IntPtr[] events = new IntPtr[3]; // owner-only, full event rights
        private Native.ProcessInfo root;
        private bool created, forced, normal;

        private void Prepare() {
            job = Diagnostic.Handle(Native.CreateJobObjectW(IntPtr.Zero, null), "CreateJobObjectW");
            FixtureNative.Require(job != IntPtr.Zero);
            var limits = new Native.ExtendedLimits();
            limits.basic.flags = 0x2000; // KILL_ON_JOB_CLOSE, neither breakaway permission.
            FixtureNative.Require(Diagnostic.Api(Native.SetInformationJobObject(job, 9, ref limits,
                (uint)Marshal.SizeOf(typeof(Native.ExtendedLimits))), "SetInformationJobObject"));
            IntPtr self = DescendantNative.GetCurrentProcess();
            FixtureNative.Require(Diagnostic.Api(DescendantNative.DuplicateHandle(self, job, self,
                out inherited[0], 4, true, 0), "DuplicateHandle")); // JOB_OBJECT_QUERY only
            for (int i = 1; i < inherited.Length; i++) {
                events[i - 1] = Diagnostic.Handle(FixtureNative.CreateEventW(IntPtr.Zero, true, false, null), "CreateEventW");
                FixtureNative.Require(events[i - 1] != IntPtr.Zero);
                // Root waits for ready and delegates its signal authority to its child.
                // Helpers can only wait on release; only the owner can signal it.
                // Helpers only signal done; the owner waits using its own handle.
                uint access = i == 1 ? 0x100002u : (i == 2 ? 0x100000u : 2u);
                FixtureNative.Require(Diagnostic.Api(DescendantNative.DuplicateHandle(self, events[i - 1], self,
                    out inherited[i], access, true, 0), "DuplicateHandle"));
            }
            root = FixtureNative.Create("root", inherited);
            created = true;
        }
        private uint Active() {
            Native.Accounting value;
            FixtureNative.Require(Diagnostic.Api(Native.QueryInformationJobObject(job, 1, out value,
                (uint)Marshal.SizeOf(typeof(Native.Accounting)), IntPtr.Zero), "QueryInformationJobObject"));
            Diagnostic.Active = value.active == 0 ? "zero" : value.active == 1 ? "one" : "more-than-one";
            return value.active;
        }
        private void WaitEmpty() {
            var timer = Stopwatch.StartNew();
            while (Active() != 0) {
                FixtureNative.Require(timer.ElapsedMilliseconds < 5000);
                Thread.Sleep(10); // bounded query polling, never a fixture ordering assumption
            }
        }
        private void RootExited(uint expected) {
            FixtureNative.Require(Diagnostic.Wait(Native.WaitForSingleObject(root.process, 5000)) == 0);
            uint code;
            bool queried = Diagnostic.Api(Native.GetExitCodeProcess(root.process, out code), "GetExitCodeProcess");
            Diagnostic.RootExit = !queried ? "unobserved" : code == 0 ? "zero" : code == 1 ? "fixture-failure" : code == 77 ? "forced" : "other";
            FixtureNative.Require(queried && code == expected);
        }
        private Outcome Observe(Scenario scenario) {
            Prepare();
            Diagnostic.Phase = "observe";
            if (scenario == Scenario.AssignmentDenied) {
                // Real API access denial, not a fabricated callback result. Root stays suspended.
                bool assigned = Native.AssignProcessToJobObject(inherited[0], root.process);
                int error = Marshal.GetLastWin32Error();
                FixtureNative.Require(!assigned && error == 5);
                return Outcome.AssignmentDenied;
            }
            FixtureNative.Require(Diagnostic.Api(Native.AssignProcessToJobObject(job, root.process), "AssignProcessToJobObject"));
            FixtureNative.Member(root.process, job);
            FixtureNative.Require(Diagnostic.Wait(Native.WaitForSingleObject(root.process, 0)) == 258);
            if (scenario == Scenario.CancelBeforeResume) return Outcome.Cancelled;
            FixtureNative.Require(Diagnostic.Resume(Native.ResumeThread(root.thread)) == 1);
            FixtureNative.Require(Diagnostic.Wait(Native.WaitForSingleObject(events[0], 5000)) == 0);
            RootExited(0);
            // Ready child is held at release. Root exit is explicitly insufficient.
            FixtureNative.Require(Active() == 1);
            FixtureNative.Require(!normal && Diagnostic.Wait(Native.WaitForSingleObject(events[2], 0)) == 258);
            if (scenario == Scenario.ForcedCleanup) return Outcome.ForcedCleanup;
            FixtureNative.Require(Diagnostic.Api(FixtureNative.SetEvent(events[1]), "SetEvent"));
            FixtureNative.Require(Diagnostic.Wait(Native.WaitForSingleObject(events[2], 5000)) == 0);
            WaitEmpty();
            normal = true; // Only before cleanup, with original root handle and private Job.
            return Outcome.NormalExit;
        }
        private void Cleanup() {
            bool rootStopped = !created || Diagnostic.Wait(Native.WaitForSingleObject(root.process, 0)) == 0;
            if (!rootStopped) {
                forced = true;
                rootStopped = Diagnostic.Api(Native.TerminateProcess(root.process, 77), "TerminateProcess");
            }
            bool empty = job == IntPtr.Zero;
            if (!empty) { try { empty = Active() == 0; } catch (InvalidOperationException) { } }
            bool jobStopped = empty;
            if (!empty) {
                forced = true;
                jobStopped = Diagnostic.Api(Native.TerminateJobObject(job, 77), "TerminateJobObject");
            }
            bool confirmed = rootStopped && jobStopped;
            if (created) confirmed &= Diagnostic.Wait(Native.WaitForSingleObject(root.process, 5000)) == 0;
            if (confirmed && job != IntPtr.Zero) {
                try { WaitEmpty(); } catch (InvalidOperationException) { confirmed = false; }
            }
            // No handle closure on uncertain exit; retain resources until this host exits.
            // Host exit does NOT guarantee cleanup: descendants can retain query Job handles,
            // and a failed termination may leave an unassigned suspended root. Fail only.
            FixtureNative.Require(confirmed);
            bool closed = true;
            if (root.thread != IntPtr.Zero) closed &= Diagnostic.Api(Native.CloseHandle(root.thread), "CloseHandle");
            if (root.process != IntPtr.Zero) closed &= Diagnostic.Api(Native.CloseHandle(root.process), "CloseHandle");
            foreach (IntPtr handle in inherited)
                if (handle != IntPtr.Zero) closed &= Diagnostic.Api(Native.CloseHandle(handle), "CloseHandle");
            foreach (IntPtr handle in events)
                if (handle != IntPtr.Zero) closed &= Diagnostic.Api(Native.CloseHandle(handle), "CloseHandle");
            if (job != IntPtr.Zero) closed &= Diagnostic.Api(Native.CloseHandle(job), "CloseHandle");
            FixtureNative.Require(closed);
        }
        internal static Outcome Run(Scenario scenario) {
            var owner = new AppOwner();
            Diagnostic.Begin(scenario);
            Outcome result = default(Outcome);
            // Preserve both failures. Cleanup cannot overwrite the original observation.
            try { result = owner.Observe(scenario); }
            catch (Exception error) { Diagnostic.Primary = Diagnostic.Capture(error); }
            finally {
                Diagnostic.CleanupPhase();
                try { owner.Cleanup(); }
                catch (Exception error) { Diagnostic.Cleanup = Diagnostic.Capture(error); }
            }
            if (Diagnostic.Primary != null) throw Diagnostic.Primary;
            if (Diagnostic.Cleanup != null) throw Diagnostic.Cleanup;
            Diagnostic.Phase = "outcome";
            FixtureNative.Require(result != Outcome.NormalExit || (owner.normal && !owner.forced));
            FixtureNative.Require(result == Outcome.NormalExit || (!owner.normal && owner.forced));
            return result;
        }
    }
}
