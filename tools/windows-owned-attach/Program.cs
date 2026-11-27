using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Text;
using System.Threading;

namespace Tether.OwnedAttach {
    // The executable is a fixed experimental orchestrator, not a reusable public launcher.
    internal static class Program {
        private const string Failure = "OWNED_ATTACH_EXPERIMENT_FAILED";
        private static readonly List<NodeRun> RetainedNodes = new List<NodeRun>();
        private static void Require(bool value) {
            if (!value) throw new InvalidOperationException(Failure);
        }
        private static string Absolute(string value, bool file) {
            Require(!String.IsNullOrEmpty(value) && value.Length < 32700 &&
                value.IndexOfAny(new char[] { '\0', '\r', '\n', '"' }) < 0);
            Require(value.Length >= 3 && Char.IsLetter(value[0]) && value[1] == ':' &&
                (value[2] == '\\' || value[2] == '/'));
            string path = Path.GetFullPath(value);
            if (file) Require(File.Exists(path));
            return path;
        }
        private static void Artifact(string directory, string name) {
            var file = new FileInfo(Path.Combine(directory, name));
            Require(file.Exists && file.Length > 0 && file.Length <= 65536 &&
                (file.Attributes & FileAttributes.ReparsePoint) == 0);
        }
        private sealed class NodeRun {
            internal readonly Process Process = new Process();
            private readonly object logGate = new object();
            private readonly ManualResetEvent outputDone = new ManualResetEvent(false);
            private readonly ManualResetEvent errorDone = new ManualResetEvent(false);
            private StreamWriter output, error;
            private volatile bool logFailed;
            private bool started, outputReading, errorReading;
            private int logSize;

            private void Capture(string value, StreamWriter log, ManualResetEvent done) {
                if (value == null) { done.Set(); return; }
                lock (logGate) {
                    // Fixed scripts are quiet on success. Bound diagnostic storage and do not
                    // forward exception text, paths, session IDs or browser output to the console.
                    int bytes = Encoding.UTF8.GetByteCount(value) + 2;
                    if (value.Length > 4096 || logSize + bytes > 65536) {
                        logFailed = true;
                        return;
                    }
                    logSize += bytes;
                    try { log.WriteLine(value); log.Flush(); }
                    catch { logFailed = true; }
                }
            }
            internal void Run(string node, string script, string directory, string phase,
                int timeoutMs, Launcher owner, Stopwatch overall) {
                try {
                    output = new StreamWriter(new FileStream(Path.Combine(directory, phase + ".stdout.private.log"),
                        FileMode.CreateNew, FileAccess.Write, FileShare.Read), new UTF8Encoding(false));
                    error = new StreamWriter(new FileStream(Path.Combine(directory, phase + ".stderr.private.log"),
                        FileMode.CreateNew, FileAccess.Write, FileShare.Read), new UTF8Encoding(false));
                    var info = new ProcessStartInfo(node, "--experimental-strip-types \"" + script + "\"");
                    info.UseShellExecute = false;
                    info.CreateNoWindow = true;
                    info.RedirectStandardInput = true;
                    info.RedirectStandardOutput = true;
                    info.RedirectStandardError = true;
                    info.WorkingDirectory = Path.GetDirectoryName(script);
                    info.EnvironmentVariables.Clear();
                    string windows = Directory.GetParent(Environment.SystemDirectory).FullName;
                    info.EnvironmentVariables["SystemRoot"] = windows;
                    info.EnvironmentVariables["WINDIR"] = windows;
                    info.EnvironmentVariables["TEMP"] = Path.Combine(directory, "temp");
                    info.EnvironmentVariables["TMP"] = Path.Combine(directory, "temp");
                    info.EnvironmentVariables["OWNED_ATTACH_OUTPUT"] = directory;
                    Process.StartInfo = info;
                    Process.OutputDataReceived += delegate(object sender, DataReceivedEventArgs e) {
                        Capture(e.Data, output, outputDone);
                    };
                    Process.ErrorDataReceived += delegate(object sender, DataReceivedEventArgs e) {
                        Capture(e.Data, error, errorDone);
                    };
                    owner.CheckLive();
                    var timer = Stopwatch.StartNew();
                    started = Process.Start();
                    Require(started);
                    // Process.Start retains the creation-time handle on Windows. Keep this
                    // exact Process object until exit/cleanup; never reopen by numeric PID.
                    Require(Process.Handle != IntPtr.Zero);
                    Process.BeginOutputReadLine(); outputReading = true;
                    Process.BeginErrorReadLine(); errorReading = true;
                    Process.StandardInput.Close();
                    for (;;) {
                        Require(timer.ElapsedMilliseconds < timeoutMs && overall.ElapsedMilliseconds < 180000);
                        owner.CheckLive();
                        Require(!logFailed);
                        if (Process.WaitForExit(25)) break;
                    }
                    Require(timer.ElapsedMilliseconds < timeoutMs && Process.ExitCode == 0);
                    owner.CheckLive();
                    // Never call the unbounded Process.WaitForExit() overload to drain output.
                    Require(outputDone.WaitOne(2000) && errorDone.WaitOne(2000));
                    Require(!logFailed);
                } finally { Cleanup(); }
            }
            private void Cleanup() {
                bool stopped = !started;
                if (started) {
                    try {
                        stopped = Process.WaitForExit(0);
                        if (!stopped) {
                            try { Process.Kill(); } catch { /* Verify a possible exit race below. */ }
                            stopped = Process.WaitForExit(5000);
                        }
                    } catch { stopped = false; }
                }
                // Stream callbacks must have ended before disposing their private writers.
                bool drained = (!outputReading || outputDone.WaitOne(2000)) &&
                    (!errorReading || errorDone.WaitOne(2000));
                if (!stopped || !drained) {
                    RetainedNodes.Add(this);
                    throw new InvalidOperationException(Failure);
                }
                bool closed = true;
                try { Process.Close(); } catch { closed = false; }
                try { if (output != null) output.Dispose(); } catch { closed = false; }
                try { if (error != null) error.Dispose(); } catch { closed = false; }
                outputDone.Close();
                errorDone.Close();
                Require(closed);
            }
        }
        public static int Main(string[] args) {
            Launcher owner = null;
            bool passed = false, outputVerified = false, cleanupVerified = false;
            string phase = "environment";
            string directory = null;
            try {
                Require(args.Length == 8 && Environment.OSVersion.Platform == PlatformID.Win32NT &&
                    Environment.Is64BitProcess);
                string tauri = Absolute(args[0], true), edge = Absolute(args[1], true);
                string app = Absolute(args[2], true), profile = Absolute(args[3], false);
                string node = Absolute(args[4], true), attach = Absolute(args[5], true);
                string checks = Absolute(args[6], true);
                directory = Absolute(args[7], false);
                Require(String.Equals(Path.GetExtension(node), ".exe", StringComparison.OrdinalIgnoreCase));
                Require(String.Equals(Path.GetDirectoryName(profile), directory, StringComparison.OrdinalIgnoreCase));
                Require(Directory.Exists(directory) &&
                    (File.GetAttributes(directory) & FileAttributes.ReparsePoint) == 0);
                // The runner created this directory exclusively. Shell redirection may
                // already have created its single private launcher log before Main starts.
                foreach (string entry in Directory.GetFileSystemEntries(directory)) {
                    Require(Path.GetFileName(entry) == "launcher.private.log" && File.Exists(entry) &&
                        (File.GetAttributes(entry) & FileAttributes.ReparsePoint) == 0);
                }
                outputVerified = true;
                var overall = Stopwatch.StartNew();
                try {
                    phase = "owner-start";
                    owner = Launcher.Start(tauri, edge, app, profile);
                    phase = "prepare";
                    owner.Send("1 prepare");
                    owner.Read("1 ready", 22000);
                    phase = "attach";
                    new NodeRun().Run(node, attach, directory, "attach", 20000, owner, overall);
                    Artifact(directory, "session.private.json");
                    // Attach is acknowledged before UI work, while the recorded listeners
                    // and original driver handles can still be checked by the owner thread.
                    phase = "binding-after-attach";
                    owner.Send("2 attached");
                    owner.Read("2 bound", 10000);
                    phase = "webview-and-ipc";
                    new NodeRun().Run(node, checks, directory, "checks", 80000, owner, overall);
                    Artifact(directory, "webview-result.json");
                    Require(overall.ElapsedMilliseconds < 150000);
                    phase = "normal-close";
                    owner.ConfirmNormal(28000);
                    owner.CheckLive();
                    Require(overall.ElapsedMilliseconds < 180000);
                } finally {
                    if (owner != null) { owner.Cleanup(); cleanupVerified = true; }
                }
                passed = owner != null && owner.Passed;
            } catch {
                // Every protocol, child, timeout, native, file, and cleanup failure remains failure.
                passed = false;
                if (outputVerified && owner != null && owner.OwnerFailure != null) {
                    try {
                        using (var file = new FileStream(Path.Combine(directory, "failure.private.txt"),
                            FileMode.CreateNew, FileAccess.Write, FileShare.None))
                        using (var writer = new StreamWriter(file, Encoding.ASCII)) {
                            writer.WriteLine(owner.OwnerFailure);
                        }
                    } catch { /* Diagnostic write failure cannot turn this run into success. */ }
                }
            }
            if (outputVerified) {
                try {
                    using (var file = new FileStream(Path.Combine(directory, "phase.private.txt"),
                        FileMode.CreateNew, FileAccess.Write, FileShare.None))
                    using (var writer = new StreamWriter(file, Encoding.ASCII)) {
                        writer.WriteLine(phase);
                        writer.WriteLine(cleanupVerified ? "cleanup-verified" : "cleanup-unconfirmed");
                    }
                } catch { passed = false; }
            }
            Console.WriteLine(passed ? "OWNED_ATTACH_EXPERIMENT_PASSED" : Failure);
            return passed ? 0 : 1;
        }
    }
}
