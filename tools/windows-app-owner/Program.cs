using System;
using System.Globalization;
using Tether.OwnerProbe;

namespace Tether.AppOwnerFixture {
    internal static class Program {
        private static int Helper(string role, IntPtr[] handles) {
            FixtureNative.Member(DescendantNative.GetCurrentProcess(), handles[0]);
            if (role == "child") {
                FixtureNative.Require(FixtureNative.SetEvent(handles[1]));
                FixtureNative.Require(Native.WaitForSingleObject(handles[2], 20000) == 0);
                FixtureNative.Require(FixtureNative.SetEvent(handles[3]));
                return 0;
            }
            Native.ProcessInfo child = FixtureNative.Create("child", handles);
            // Child inherits membership through CreateProcess, without explicit child assignment.
            // Keep its original handles until this fixed root exits. The external owner retains
            // its root handles and Job even after this root exits while the child remains alive.
            FixtureNative.Member(child.process, handles[0]);
            FixtureNative.Require(Native.ResumeThread(child.thread) == 1);
            FixtureNative.Require(Native.WaitForSingleObject(handles[1], 5000) == 0);
            return 0;
        }
        public static int Main(string[] args) {
            try {
                FixtureNative.Require(Environment.Is64BitProcess);
                if (args.Length == 5 && (args[0] == "root" || args[0] == "child")) {
                    var handles = new IntPtr[4];
                    for (int i = 0; i < handles.Length; i++) {
                        long value;
                        FixtureNative.Require(long.TryParse(args[i + 1], NumberStyles.None,
                            CultureInfo.InvariantCulture, out value) && value > 0);
                        handles[i] = new IntPtr(value);
                    }
                    return Helper(args[0], handles);
                }
                FixtureNative.Require(args.Length == 0);
                FixtureNative.Require(AppOwner.Run(Scenario.LateChild) == Outcome.NormalExit);
                FixtureNative.Require(AppOwner.Run(Scenario.ForcedCleanup) == Outcome.ForcedCleanup);
                FixtureNative.Require(AppOwner.Run(Scenario.CancelBeforeResume) == Outcome.Cancelled);
                FixtureNative.Require(AppOwner.Run(Scenario.AssignmentDenied) == Outcome.AssignmentDenied);
                Console.WriteLine("APP_OWNER_FIXED_CASES_PASSED_4");
                return 0;
            } catch (Exception) {
                // Never publish raw errors, process IDs, paths, commands, or handle values.
                Console.WriteLine("APP_OWNER_FIXTURE_FAILED");
                return 1;
            }
        }
    }
}
