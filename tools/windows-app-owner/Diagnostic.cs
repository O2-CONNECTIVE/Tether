using System;
using System.Globalization;
using System.Runtime.CompilerServices;
using System.Runtime.InteropServices;

namespace Tether.AppOwnerFixture {
    // Fixed categories only. Never serialize exception text, paths, PID or handle values.
    internal sealed class FixtureFailure : InvalidOperationException {
        internal readonly string Phase, Operation, Category, RootExit, Active, Wait, Assertion;
        internal FixtureFailure(string phase, string operation, string category, string assertion = "managed-operation") {
            Phase = phase; Operation = operation; Category = category; Assertion = assertion;
            RootExit = Diagnostic.RootExit; Active = Diagnostic.Active; Wait = Diagnostic.WaitResult;
        }
    }
    internal static class Diagnostic {
        internal static string Scenario = "not-started", Phase = "entry";
        internal static string RootExit = "unobserved", Active = "unobserved", WaitResult = "unobserved";
        internal static string ApiFailureOperation, ApiFailureCategory;
        internal static FixtureFailure Primary, Cleanup;
        internal static int Completed;
        internal static void Begin(Tether.AppOwnerFixture.Scenario scenario) {
            Scenario = scenario.ToString(); Phase = "prepare";
            RootExit = Active = WaitResult = "unobserved";
            ApiFailureOperation = ApiFailureCategory = null;
            Primary = Cleanup = null;
        }
        internal static void CleanupPhase() {
            Phase = "cleanup"; ApiFailureOperation = ApiFailureCategory = null;
        }
        internal static string ErrorCategory(int error) {
            return error == 5 ? "access-denied" : error == 6 ? "invalid-handle" :
                error == 87 ? "invalid-parameter" : "other";
        }
        internal static bool Api(bool value, string operation) {
            // Capture immediately after the P/Invoke result. No intervening native call.
            int error = Marshal.GetLastWin32Error();
            if (!value && ApiFailureOperation == null) {
                ApiFailureOperation = operation; ApiFailureCategory = ErrorCategory(error);
            }
            return value;
        }
        internal static IntPtr Handle(IntPtr value, string operation) {
            Api(value != IntPtr.Zero, operation); return value;
        }
        internal static uint Resume(uint result) {
            Api(result != UInt32.MaxValue, "ResumeThread"); return result;
        }
        internal static uint Wait(uint result) {
            if (result == UInt32.MaxValue) Api(false, "WaitForSingleObject");
            WaitResult = result == 0 ? "signaled" : result == 258 ? "timeout" :
                result == UInt32.MaxValue ? "failed" : "unexpected";
            return result;
        }
        internal static void Require(bool condition, string member, int line) {
            if (!condition) throw new FixtureFailure(Phase,
                ApiFailureOperation ?? member + "-" + line.ToString(CultureInfo.InvariantCulture),
                ApiFailureCategory ?? "assertion", member + "-" + line.ToString(CultureInfo.InvariantCulture));
        }
        internal static FixtureFailure Capture(Exception error) {
            return error as FixtureFailure ?? new FixtureFailure(Phase, "managed-operation", "managed-exception");
        }
        private static string FailureJson(FixtureFailure failure) {
            if (failure == null) return "null";
            return "{\"phase\":\"" + failure.Phase + "\",\"operation\":\"" + failure.Operation +
                "\",\"assertion\":\"" + failure.Assertion + "\",\"category\":\"" + failure.Category + "\",\"rootExit\":\"" + failure.RootExit +
                "\",\"active\":\"" + failure.Active + "\",\"wait\":\"" + failure.Wait + "\"}";
        }
        internal static string Json(bool passed) {
            return "{\"schema\":1,\"kind\":\"app-owner-prerequisite-diagnostic\",\"passed\":" +
                (passed ? "true" : "false") + ",\"completedCases\":" + Completed.ToString(CultureInfo.InvariantCulture) +
                ",\"scenario\":\"" + Scenario + "\",\"phase\":\"" + Phase +
                "\",\"rootExit\":\"" + RootExit + "\",\"active\":\"" + Active +
                "\",\"wait\":\"" + WaitResult + "\",\"primary\":" + FailureJson(Primary) +
                ",\"cleanup\":" + FailureJson(Cleanup) + "}";
        }
    }
}
