using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;
using Tether.OwnerProbe;

namespace Tether.OwnedAttach {
    // Not a product launcher, IPC authentication boundary, or ownership receipt.
    // Only the fixed patched tauri-driver and its trusted owner thread use this channel.
    internal sealed class Launcher {
        private const string Failure = "OWNED_ATTACH_LAUNCHER_FAILED";
        private const int MaxFrame = 127;
        private const int DefaultDeadline = 10000;
        private static readonly string[] Commands = { "1 prepare", "2 attached", "3 close" };
        private static readonly string[] Replies = { "1 ready", "2 bound", "3 normal" };
        // No finalizer closes an unconfirmed process/job or retries a failed close.
        private static readonly List<Launcher> Retained = new List<Launcher>();
        private static readonly List<IntPtr> Unclosed = new List<IntPtr>();
        private readonly object gate = new object();
        private IntPtr job, requestWrite, requestMonitor, responseRead, responseWrite;
        private IntPtr childRead, childWrite;
        private Native.ProcessInfo process;
        private bool created, started, pending, failed, normal, cleanupAttempted, cleaned;
        private int phase;
        private string tempDirectory, ownerFailure;

        internal bool NormalObserved { get { return normal; } }
        internal string OwnerFailure { get { return ownerFailure; } }
        internal bool Passed { get { return normal && cleaned && !failed; } }

        private void Require(bool condition) {
            if (!condition) {
                failed = true;
                throw new InvalidOperationException(Failure);
            }
        }
        private string AbsolutePath(string path, bool executable) {
            Require(!String.IsNullOrEmpty(path) && path.Length < 32700 &&
                path.IndexOfAny(new char[] { '\0', '\r', '\n', '"' }) < 0);
            // Local drive paths only. No relative, drive-relative, UNC or device paths.
            Require(path.Length >= 3 && Char.IsLetter(path[0]) && path[1] == ':' &&
                (path[2] == '\\' || path[2] == '/'));
            string full = Path.GetFullPath(path);
            if (executable) Require(File.Exists(full) &&
                String.Equals(Path.GetExtension(full), ".exe", StringComparison.OrdinalIgnoreCase));
            return full;
        }
        private void NonInheritable(IntPtr handle) {
            Require(handle != IntPtr.Zero && LauncherNative.SetHandleInformation(handle, 1, 0));
            uint flags;
            Require(LauncherNative.GetHandleInformation(handle, out flags) && (flags & 1) == 0);
        }
        private void CreateChannel() {
            // CreatePipe's null SECURITY_ATTRIBUTES creates noninheritable parent ends.
            Require(LauncherNative.CreatePipe(out requestMonitor, out requestWrite, IntPtr.Zero, 4096));
            Require(LauncherNative.CreatePipe(out responseRead, out responseWrite, IntPtr.Zero, 4096));
            NonInheritable(requestMonitor);
            NonInheritable(requestWrite);
            NonInheritable(responseRead);
            NonInheritable(responseWrite);
            IntPtr self = DescendantNative.GetCurrentProcess();
            Require(DescendantNative.DuplicateHandle(self, requestMonitor, self,
                out childRead, 0, true, 2)); // DUPLICATE_SAME_ACCESS
            Require(DescendantNative.DuplicateHandle(self, responseWrite, self,
                out childWrite, 0, true, 2));
            Require(Close(ref responseWrite));
            // The read-only monitor stays here solely to prove the request pipe is empty
            // before a bounded small write. It never consumes a command.
        }
        private IntPtr EnvironmentBlock(string app, string profile) {
            var values = new SortedDictionary<string, string>(StringComparer.OrdinalIgnoreCase);
            string windows = Directory.GetParent(Environment.SystemDirectory).FullName;
            values.Add("SystemRoot", windows);
            values.Add("TEMP", tempDirectory);
            values.Add("TETHER_OWNER_APP", app);
            values.Add("TETHER_OWNER_PROFILE", profile);
            values.Add("TETHER_OWNER_READ", childRead.ToInt64().ToString(CultureInfo.InvariantCulture));
            values.Add("TETHER_OWNER_WRITE", childWrite.ToInt64().ToString(CultureInfo.InvariantCulture));
            values.Add("TMP", tempDirectory);
            values.Add("WINDIR", windows);
            var block = new StringBuilder();
            foreach (var value in values) block.Append(value.Key).Append('=').Append(value.Value).Append('\0');
            block.Append('\0');
            // No inherited environment, PATH, logging switches, or credentials.
            return Marshal.StringToHGlobalUni(block.ToString());
        }
        private void CreateSuspended(string image, string nativeDriver, string app, string profile) {
            IntPtr size = IntPtr.Zero, list = IntPtr.Zero, handles = IntPtr.Zero, environment = IntPtr.Zero;
            bool initialized = false;
            try {
                Require(!DescendantNative.InitializeProcThreadAttributeList(IntPtr.Zero, 1, 0, ref size)
                    && Marshal.GetLastWin32Error() == 122 && size.ToInt64() > 0);
                list = Marshal.AllocHGlobal(size);
                handles = Marshal.AllocHGlobal(2 * IntPtr.Size);
                environment = EnvironmentBlock(app, profile);
                initialized = DescendantNative.InitializeProcThreadAttributeList(list, 1, 0, ref size);
                Require(initialized);
                Marshal.WriteIntPtr(handles, 0, childRead);
                Marshal.WriteIntPtr(handles, IntPtr.Size, childWrite);
                // Exactly two private pipe ends. No stdio or Job handle is inherited.
                Require(DescendantNative.UpdateProcThreadAttribute(list, 0, new IntPtr(0x20002),
                    handles, new IntPtr(2 * IntPtr.Size), IntPtr.Zero, IntPtr.Zero));
                var startup = new DescendantNative.StartupEx();
                startup.startup.cb = (uint)Marshal.SizeOf(typeof(DescendantNative.StartupEx));
                startup.attributes = list; // STARTF_USESTDHANDLES is deliberately absent.
                var command = new StringBuilder("\"" + image + "\" --port 4444 --native-port 4445 --native-driver \"" +
                    nativeDriver + "\"");
                created = DescendantNative.CreateProcessW(image, command, IntPtr.Zero, IntPtr.Zero,
                    true, 0x08080404, environment, Path.GetDirectoryName(image), ref startup, out process);
                // NO_WINDOW | EXTENDED_STARTUPINFO_PRESENT | UNICODE_ENVIRONMENT | SUSPENDED
                Require(created);
                NonInheritable(process.process);
                NonInheritable(process.thread);
            } finally {
                if (initialized) DescendantNative.DeleteProcThreadAttributeList(list);
                if (environment != IntPtr.Zero) Marshal.FreeHGlobal(environment);
                if (handles != IntPtr.Zero) Marshal.FreeHGlobal(handles);
                if (list != IntPtr.Zero) Marshal.FreeHGlobal(list);
            }
        }
        internal static Launcher Start(string tauriDriver, string nativeDriver, string app, string profile) {
            var owner = new Launcher();
            try {
                owner.Require(Environment.OSVersion.Platform == PlatformID.Win32NT && Environment.Is64BitProcess);
                tauriDriver = owner.AbsolutePath(tauriDriver, true);
                nativeDriver = owner.AbsolutePath(nativeDriver, true);
                app = owner.AbsolutePath(app, true);
                profile = owner.AbsolutePath(profile, false);
                owner.tempDirectory = Path.Combine(Path.GetDirectoryName(profile), "temp");
                owner.Require(!String.Equals(profile, owner.tempDirectory, StringComparison.OrdinalIgnoreCase) &&
                    !Directory.Exists(profile) && !File.Exists(profile) &&
                    !Directory.Exists(owner.tempDirectory) && !File.Exists(owner.tempDirectory));
                Directory.CreateDirectory(owner.tempDirectory);
                Directory.CreateDirectory(profile); // A fresh disposable profile, never an existing user's profile.
                owner.Require(Directory.GetFileSystemEntries(profile).Length == 0);
                // Driver temporary files cannot populate the fresh WebView profile before
                // the owner thread independently checks and starts the application.
                owner.job = Native.CreateJobObjectW(IntPtr.Zero, null);
                owner.Require(owner.job != IntPtr.Zero);
                owner.NonInheritable(owner.job);
                var limits = new Native.ExtendedLimits();
                limits.basic.flags = 0x2000; // KILL_ON_JOB_CLOSE only. No breakaway flags.
                owner.Require(Native.SetInformationJobObject(owner.job, 9, ref limits,
                    (uint)Marshal.SizeOf(typeof(Native.ExtendedLimits))));
                owner.CreateChannel();
                owner.CreateSuspended(tauriDriver, nativeDriver, app, profile);
                // Parent copies of the transfer handles must not keep either direction open.
                bool transferClosed = Close(ref owner.childRead);
                transferClosed &= Close(ref owner.childWrite);
                owner.Require(transferClosed);
                owner.Require(Native.AssignProcessToJobObject(owner.job, owner.process.process));
                owner.Member();
                owner.Require(Native.WaitForSingleObject(owner.process.process, 0) == 258);
                owner.Require(Native.ResumeThread(owner.process.thread) == 1);
                owner.started = true;
                owner.CheckLive();
                return owner;
            } catch {
                owner.failed = true;
                owner.Cleanup();
                throw new InvalidOperationException(Failure);
            }
        }
        private void Member() {
            bool member;
            Require(Native.IsProcessInJob(process.process, job, out member) && member);
        }
        private void Live() {
            Require(created && started && !cleanupAttempted && !failed);
            Require(Native.WaitForSingleObject(process.process, 0) == 258);
            uint code;
            Require(Native.GetExitCodeProcess(process.process, out code) && code == 259);
            Member();
            Require(Native.WaitForSingleObject(process.process, 0) == 258);
        }
        internal void CheckLive() { lock (gate) { Live(); } }
        private uint Available(IntPtr pipe) {
            uint count;
            Require(LauncherNative.PeekNamedPipe(pipe, IntPtr.Zero, 0, IntPtr.Zero, out count, IntPtr.Zero));
            return count;
        }
        private void Deadline(Stopwatch timer, int timeoutMs) {
            Require(timeoutMs > 0 && timeoutMs <= 120000 && timer.ElapsedMilliseconds < timeoutMs);
        }
        private void SendCore(string command, int timeoutMs) {
            var timer = Stopwatch.StartNew();
            Deadline(timer, timeoutMs);
            Live();
            Require(!pending && phase < Commands.Length && command == Commands[phase]);
            Require(Available(requestMonitor) == 0 && Available(responseRead) == 0);
            byte[] bytes = Encoding.ASCII.GetBytes(command + "\n");
            Require(bytes.Length <= MaxFrame);
            Deadline(timer, timeoutMs);
            // One writer, one outstanding request, <128 bytes and an empty 4096-byte pipe.
            // Never issue a synchronous write that needs the reader to drain buffer space.
            uint written;
            Require(LauncherNative.WriteFile(requestWrite, bytes, (uint)bytes.Length, out written, IntPtr.Zero)
                && written == bytes.Length);
            Deadline(timer, timeoutMs);
            Live();
            pending = true;
        }
        internal void Send(string command) { lock (gate) { SendCore(command, DefaultDeadline); } }
        private void CaptureOwnerFailure(string frame) {
            // Retain only a short fixed-token classification, never arbitrary diagnostics.
            string[] parts = frame.Split(' ');
            if (parts.Length != 3 || parts[0] != "failure" || parts[1].Length == 0 ||
                parts[1].Length > 64 || (parts[2] != "cleaned" && parts[2] != "cleanup-unconfirmed")) return;
            if (parts[1][0] < 'a' || parts[1][0] > 'z') return;
            foreach (char value in parts[1]) {
                if (!((value >= 'a' && value <= 'z') || (value >= '0' && value <= '9') || value == '-')) return;
            }
            ownerFailure = frame;
        }
        private void ReadCore(string expected, int timeoutMs) {
            var timer = Stopwatch.StartNew();
            Deadline(timer, timeoutMs);
            Live();
            Require(pending && phase < Replies.Length && expected == Replies[phase]);
            var frame = new StringBuilder();
            byte[] one = new byte[1];
            for (;;) {
                Deadline(timer, timeoutMs);
                Live();
                uint available = Available(responseRead);
                Require(available + (uint)frame.Length <= MaxFrame);
                if (available == 0) { Thread.Sleep(5); continue; }
                // Only this reader consumes this direction; Peek prevents a blocking read.
                uint count;
                Require(LauncherNative.ReadFile(responseRead, one, 1, out count, IntPtr.Zero) && count == 1);
                Deadline(timer, timeoutMs);
                if (one[0] == 10) {
                    string result = frame.ToString();
                    Require(Available(responseRead) == 0 && Available(requestMonitor) == 0);
                    if (result != expected) CaptureOwnerFailure(result);
                    Require(result == expected);
                    Live(); // Normal verdict requires the original tauri handle still live.
                    pending = false;
                    phase++;
                    if (phase == Replies.Length) normal = true;
                    return;
                }
                Require(one[0] >= 32 && one[0] <= 126 && frame.Length < MaxFrame - 1);
                frame.Append((char)one[0]);
            }
        }
        internal void Read(string expected, int timeoutMs) { lock (gate) { ReadCore(expected, timeoutMs); } }
        internal void ConfirmNormal(int timeoutMs) {
            lock (gate) {
                Require(phase == 2 && !pending);
                SendCore("3 close", timeoutMs);
                ReadCore("3 normal", timeoutMs);
            }
        }
        private bool Empty() {
            Native.Accounting accounting;
            return job == IntPtr.Zero || (Native.QueryInformationJobObject(job, 1, out accounting,
                (uint)Marshal.SizeOf(typeof(Native.Accounting)), IntPtr.Zero) && accounting.active == 0);
        }
        private static bool Close(ref IntPtr handle) {
            if (handle == IntPtr.Zero) return true;
            IntPtr original = handle;
            handle = IntPtr.Zero; // A failed close is never attempted again, even in Start cleanup.
            if (Native.CloseHandle(original)) return true;
            lock (Unclosed) { Unclosed.Add(original); }
            return false;
        }
        internal void Cleanup() {
            lock (gate) {
                if (cleanupAttempted) { Require(cleaned); return; }
                bool verdictLive = true;
                if (normal) {
                    try { Live(); Require(Available(responseRead) == 0); }
                    catch { failed = true; verdictLive = false; }
                }
                cleanupAttempted = true;
                bool confirmed = false;
                try {
                    // This is forced collection AFTER the verdict, never proof of normal close.
                    bool rootStopped = !created || Native.WaitForSingleObject(process.process, 0) == 0;
                    if (!rootStopped) rootStopped = Native.TerminateProcess(process.process, 77);
                    bool jobStopped = Empty();
                    if (!jobStopped && job != IntPtr.Zero) jobStopped = Native.TerminateJobObject(job, 77);
                    // Always attempt both original-root and owned-Job termination.
                    Require(rootStopped && jobStopped);
                    if (created) Require(Native.WaitForSingleObject(process.process, 5000) == 0);
                    var timer = Stopwatch.StartNew();
                    while (!Empty()) { Require(timer.ElapsedMilliseconds < 5000); Thread.Sleep(10); }
                    confirmed = true;
                    bool closed = Close(ref process.thread);
                    closed &= Close(ref process.process);
                    closed &= Close(ref childRead);
                    closed &= Close(ref childWrite);
                    closed &= Close(ref responseWrite);
                    closed &= Close(ref requestWrite);
                    closed &= Close(ref requestMonitor);
                    closed &= Close(ref responseRead);
                    closed &= Close(ref job);
                    Require(closed);
                    cleaned = true;
                    Require(verdictLive);
                } finally {
                    if (!confirmed || !cleaned) {
                        failed = true;
                        lock (Retained) { Retained.Add(this); }
                        // Exact handles remain here until disposable host exit, without retry.
                    }
                }
            }
        }
    }
}
