# Source-only approval packet. No Windows execution is authorized by this file.
[CmdletBinding()]
param(
    [ValidateSet('None', 'Prepare', 'Run', 'Cleanup')][string]$Phase = 'None',
    [switch]$Approved
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if (-not $Approved -or $Phase -eq 'None') {
    throw 'Disabled by default. Obtain action-time approval for this exact packet and explicitly select -Phase and -Approved.'
}
if ($PSVersionTable.PSEdition -ne 'Desktop' -or $PSVersionTable.PSVersion.Major -ne 5) {
    throw 'Use a fresh Windows PowerShell 5.1 process with -NoProfile; no automatic elevation.'
}
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
if (-not ([Security.Principal.WindowsPrincipal]::new($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'An already elevated human controller is required.'
}
$identity.Dispose()
if ([IO.DriveInfo]::new('C:\').DriveFormat -cne 'NTFS') { throw 'This source packet supports a local NTFS C: volume only.' }

# Security attributes are supplied to CREATE_NEW/CreateDirectory at creation.
# Every existing filesystem object is opened without delete sharing and inspected
# by handle. This code intentionally does not use New-Item followed by Set-Acl.
Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Text;
using System.Collections.Generic;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Security.AccessControl;
using System.Security.Principal;
using System.Security.Cryptography;
using Microsoft.Win32.SafeHandles;

namespace ProtectionProbe {
public sealed class Native : IDisposable {
    public const string Name = "RescueHostProtectionProbe-20261030";
    public const string Root = @"C:\ProgramData\RescueHost-ProtectionProbe-20261030";
    public const string Evidence = Root + "-evidence";
    public const string Image = Root + @"\scm-protection-probe.exe";
    public const string ImageCommand = "\"" + Image + "\" --service";
    public const int MaxReport = 262144;
    const string BA = "S-1-5-32-544", SY = "S-1-5-18";
    const int FileAll = 0x1f01ff, ServiceAll = 0xf01ff;
    public string Sid;
    public uint ProcessExitCode;
    IntPtr scm, service;
    readonly System.Threading.Mutex controller;
    readonly List<SafeFileHandle> pins = new List<SafeFileHandle>();

    public Native() {
        var security = new MutexSecurity();
        security.SetOwner(new SecurityIdentifier(BA)); security.SetGroup(new SecurityIdentifier(BA));
        security.SetAccessRuleProtection(true, false);
        foreach (string sid in new string[] { BA, SY })
            security.AddAccessRule(new MutexAccessRule(new SecurityIdentifier(sid), MutexRights.FullControl, AccessControlType.Allow));
        bool created;
        controller = new System.Threading.Mutex(false, @"Global\RescueHostProtectionProbe-20261030-controller", out created, security);
        if (!created) { controller.Dispose(); throw new InvalidOperationException("Controller mutex already exists; another phase or name collision must be reviewed first."); }
    }

    [StructLayout(LayoutKind.Sequential)] struct SA { public int Length; public IntPtr Descriptor; public int Inherit; }
    [StructLayout(LayoutKind.Sequential)] struct FI {
        public uint Attributes; public System.Runtime.InteropServices.ComTypes.FILETIME Creation, Access, Write;
        public uint Volume, SizeHigh, SizeLow, Links, IndexHigh, IndexLow;
    }
    [StructLayout(LayoutKind.Sequential)] public struct Status {
        public uint Type, State, Controls, Win32Exit, ServiceExit, Checkpoint, WaitHint, Pid, Flags;
    }
    [StructLayout(LayoutKind.Sequential)] struct Config {
        public uint Type, Start, Error; public IntPtr Binary, Group; public uint Tag;
        public IntPtr Dependencies, Account, Display;
    }
    [StructLayout(LayoutKind.Sequential)] struct FailureActions {
        public uint Reset; public IntPtr Reboot, Command; public uint Count; public IntPtr Actions;
    }
    [DllImport("advapi32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern bool ConvertStringSecurityDescriptorToSecurityDescriptor(string s, uint revision, out IntPtr descriptor, out uint size);
    [DllImport("kernel32.dll")] static extern IntPtr LocalFree(IntPtr value);
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern bool CreateDirectory(string path, ref SA attributes);
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern SafeFileHandle CreateFile(string path, uint access, uint share, IntPtr attributes, uint creation, uint flags, IntPtr template);
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern SafeFileHandle CreateFile(string path, uint access, uint share, ref SA attributes, uint creation, uint flags, IntPtr template);
    [DllImport("kernel32.dll", SetLastError=true)] static extern bool GetFileInformationByHandle(SafeFileHandle file, out FI info);
    [DllImport("advapi32.dll")] static extern uint GetSecurityInfo(SafeFileHandle handle, uint type, uint info, out IntPtr owner, out IntPtr group, out IntPtr dacl, out IntPtr sacl, out IntPtr descriptor);
    [DllImport("advapi32.dll")] static extern uint GetSecurityDescriptorLength(IntPtr descriptor);
    [DllImport("advapi32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern IntPtr OpenSCManager(string machine, string database, uint access);
    [DllImport("advapi32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern IntPtr OpenService(IntPtr manager, string name, uint access);
    [DllImport("advapi32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern IntPtr CreateService(IntPtr manager, string name, string display, uint access, uint type, uint start, uint error, string binary, string group, IntPtr tag, string dependencies, string account, string password);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool CloseServiceHandle(IntPtr handle);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool SetServiceObjectSecurity(IntPtr service, uint info, IntPtr descriptor);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool QueryServiceObjectSecurity(IntPtr service, uint info, byte[] descriptor, uint capacity, out uint needed);
    [DllImport("advapi32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern bool ChangeServiceConfig2(IntPtr service, uint level, ref uint value);
    [DllImport("advapi32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern bool QueryServiceConfig(IntPtr service, IntPtr buffer, uint size, out uint needed);
    [DllImport("advapi32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern bool QueryServiceConfig2(IntPtr service, uint level, IntPtr buffer, uint size, out uint needed);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool QueryServiceStatusEx(IntPtr service, uint level, out Status status, int size, out int needed);
    [DllImport("advapi32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern bool StartService(IntPtr service, uint count, IntPtr arguments);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool ControlService(IntPtr service, uint control, out Status status);
    [DllImport("advapi32.dll", SetLastError=true)] static extern bool DeleteService(IntPtr service);
    [DllImport("kernel32.dll", SetLastError=true)] static extern SafeFileHandle OpenProcess(uint access, bool inherit, uint pid);
    [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)] static extern bool QueryFullProcessImageName(SafeFileHandle process, uint flags, StringBuilder name, ref uint length);
    [DllImport("kernel32.dll", SetLastError=true)] static extern bool GetProcessTimes(SafeFileHandle process, out long created, out long exited, out long kernel, out long user);
    [DllImport("kernel32.dll", SetLastError=true)] static extern bool GetExitCodeProcess(SafeFileHandle process, out uint code);
    [DllImport("kernel32.dll", SetLastError=true)] static extern uint WaitForSingleObject(SafeFileHandle handle, uint milliseconds);

    static void Check(bool ok, string action) { if (!ok) throw new Win32Exception(Marshal.GetLastWin32Error(), action); }
    static void Require(bool ok, string message) { if (!ok) throw new InvalidOperationException(message); }
    static string PtrText(IntPtr p) { return p == IntPtr.Zero ? "" : Marshal.PtrToStringUni(p); }
    static byte[] DescriptorBytes(IntPtr descriptor) {
        byte[] bytes = new byte[GetSecurityDescriptorLength(descriptor)];
        Marshal.Copy(descriptor, bytes, 0, bytes.Length); return bytes;
    }
    static IntPtr Descriptor(string sid, int mask, bool serviceObject) {
        int full = serviceObject ? ServiceAll : FileAll;
        string sddl = "O:BAG:BAD:P(A;;0x" + full.ToString("x") + ";;;SY)(A;;0x" + full.ToString("x") + ";;;BA)";
        if (sid != null) sddl += "(A;;0x" + mask.ToString("x") + ";;;" + sid + ")";
        IntPtr descriptor; uint size;
        Check(ConvertStringSecurityDescriptorToSecurityDescriptor(sddl, 1, out descriptor, out size), "Parse fixed security descriptor");
        return descriptor;
    }
    static RawSecurityDescriptor FileSecurity(SafeFileHandle handle) {
        IntPtr owner, group, dacl, sacl, descriptor;
        uint error = GetSecurityInfo(handle, 1, 7, out owner, out group, out dacl, out sacl, out descriptor);
        if (error != 0) throw new Win32Exception((int)error, "Read handle security descriptor");
        try { return new RawSecurityDescriptor(DescriptorBytes(descriptor), 0); }
        finally { LocalFree(descriptor); }
    }
    static void ExactSecurity(RawSecurityDescriptor sd, string sid, int mask, bool serviceObject) {
        Require(sd.Owner != null && sd.Owner.Value == BA, "Owner must be Builtin Administrators");
        Require(sd.Group != null && sd.Group.Value == BA, "Group must be Builtin Administrators");
        if (!serviceObject) Require((sd.ControlFlags & ControlFlags.DiscretionaryAclProtected) != 0, "DACL must be protected");
        Require(sd.DiscretionaryAcl != null && sd.DiscretionaryAcl.Count == (sid == null ? 2 : 3), "Unexpected DACL size");
        var expected = new Dictionary<string, int>(); expected.Add(SY, serviceObject ? ServiceAll : FileAll); expected.Add(BA, serviceObject ? ServiceAll : FileAll);
        if (sid != null) expected.Add(sid, mask);
        foreach (GenericAce generic in sd.DiscretionaryAcl) {
            CommonAce ace = generic as CommonAce;
            Require(ace != null && !ace.IsCallback && ace.AceQualifier == AceQualifier.AccessAllowed && ace.AceFlags == AceFlags.None, "Unexpected ACE type/flags");
            int right;
            Require(expected.TryGetValue(ace.SecurityIdentifier.Value, out right) && ace.AccessMask == right, "Unexpected ACE principal/mask");
            expected.Remove(ace.SecurityIdentifier.Value);
        }
        Require(expected.Count == 0, "Missing ACE");
    }
    static void FileShape(SafeFileHandle handle, bool directory) {
        FI info; Check(GetFileInformationByHandle(handle, out info), "Inspect file identity");
        Require((info.Attributes & 0x400) == 0, "Reparse point refused");
        Require(((info.Attributes & 0x10) != 0) == directory, "Unexpected object kind");
        if (!directory) Require(info.Links == 1, "Hard linked file refused");
    }
    public static string Hash(byte[] bytes) {
        using (SHA256 sha = SHA256.Create()) return BitConverter.ToString(sha.ComputeHash(bytes)).Replace("-", "").ToLowerInvariant();
    }
    public void PinParent(string path) {
        SafeFileHandle handle = CreateFile(path, 0x80, 3, IntPtr.Zero, 3, 0x02200000, IntPtr.Zero);
        if (handle.IsInvalid) { handle.Dispose(); throw new Win32Exception(Marshal.GetLastWin32Error(), "Pin ancestor " + path); }
        try {
            FileShape(handle, true); pins.Add(handle);
        } catch { handle.Dispose(); throw; }
    }
    public void PinExact(string path, bool directory, string sid, int mask) {
        SafeFileHandle handle = CreateFile(path, 0x20080, 3, IntPtr.Zero, 3, 0x02200000, IntPtr.Zero);
        if (handle.IsInvalid) { handle.Dispose(); throw new Win32Exception(Marshal.GetLastWin32Error(), "Pin owned object " + path); }
        try { FileShape(handle, directory); ExactSecurity(FileSecurity(handle), sid, mask, false); pins.Add(handle); }
        catch { handle.Dispose(); throw; }
    }
    public void NewDirectory(string path, string sid, int mask) {
        IntPtr descriptor = Descriptor(sid, mask, false);
        try { SA attributes = new SA { Length = Marshal.SizeOf(typeof(SA)), Descriptor = descriptor, Inherit = 0 };
            Check(CreateDirectory(path, ref attributes), "CREATE_NEW protected directory " + path);
        } finally { LocalFree(descriptor); }
        PinExact(path, true, sid, mask);
    }
    public void NewFile(string path, byte[] bytes, string sid, int mask) {
        IntPtr descriptor = Descriptor(sid, mask, false);
        try {
            SA attributes = new SA { Length = Marshal.SizeOf(typeof(SA)), Descriptor = descriptor, Inherit = 0 };
            using (SafeFileHandle handle = CreateFile(path, 0xc0020080, 1, ref attributes, 1, 0x00200080, IntPtr.Zero)) {
                if (handle.IsInvalid) throw new Win32Exception(Marshal.GetLastWin32Error(), "CREATE_NEW protected file " + path);
                FileShape(handle, false); ExactSecurity(FileSecurity(handle), sid, mask, false);
                using (FileStream stream = new FileStream(handle, FileAccess.ReadWrite)) { stream.Write(bytes, 0, bytes.Length); stream.Flush(true); }
            }
        } finally { LocalFree(descriptor); }
        PinExact(path, false, sid, mask);
    }
    public byte[] ReadFile(string path, int maximum) {
        using (SafeFileHandle handle = CreateFile(path, 0x80020080, 3, IntPtr.Zero, 3, 0x00200000, IntPtr.Zero)) {
            if (handle.IsInvalid) throw new Win32Exception(Marshal.GetLastWin32Error(), "Read owned file " + path);
            FileShape(handle, false);
            using (FileStream stream = new FileStream(handle, FileAccess.Read)) {
                Require(stream.Length <= maximum, "File exceeds bound");
                byte[] bytes = new byte[(int)stream.Length]; int count = 0;
                while (count < bytes.Length) { int read = stream.Read(bytes, count, bytes.Length - count); Require(read != 0, "File changed while reading"); count += read; }
                Require(stream.ReadByte() == -1, "File grew while reading"); return bytes;
            }
        }
    }
    public void Connect(bool create) {
        scm = OpenSCManager(null, null, create ? 3u : 1u);
        if (scm == IntPtr.Zero) throw new Win32Exception(Marshal.GetLastWin32Error(), "Open SCM");
        if (create) {
            IntPtr found = OpenService(scm, Name, 1);
            if (found != IntPtr.Zero) { CloseServiceHandle(found); throw new InvalidOperationException("Service name collision"); }
            Require(Marshal.GetLastWin32Error() == 1060, "Cannot prove service name absent");
        } else {
            service = OpenService(scm, Name, ServiceAll);
            if (service == IntPtr.Zero) throw new Win32Exception(Marshal.GetLastWin32Error(), "Open exact service");
            DeriveSid(); InspectService();
        }
    }
    public void Register() {
        service = CreateService(scm, Name, Name, ServiceAll, 0x10, 3, 1, ImageCommand, null, IntPtr.Zero, null, @"NT AUTHORITY\LocalService", null);
        if (service == IntPtr.Zero) throw new Win32Exception(Marshal.GetLastWin32Error(), "Create stopped LocalService OWN_PROCESS service");
        IntPtr descriptor = Descriptor(null, 0, true);
        try { Check(SetServiceObjectSecurity(service, 7, descriptor), "Protect dedicated service object"); }
        finally { LocalFree(descriptor); }
        uint sidType = 1; Check(ChangeServiceConfig2(service, 5, ref sidType), "Enable unrestricted dedicated service SID");
        DeriveSid(); InspectService(); RequireStopped();
    }
    void DeriveSid() {
        Sid = ((SecurityIdentifier)new NTAccount("NT SERVICE", Name).Translate(typeof(SecurityIdentifier))).Value;
        Require(Sid.StartsWith("S-1-5-80-", StringComparison.Ordinal), "SCM name did not resolve to service SID");
    }
    IntPtr Config2(uint level) {
        uint needed; QueryServiceConfig2(service, level, IntPtr.Zero, 0, out needed);
        Require(needed > 0 && needed <= 65536 && Marshal.GetLastWin32Error() == 122, "Invalid service config size");
        IntPtr buffer = Marshal.AllocHGlobal((int)needed);
        try { Check(QueryServiceConfig2(service, level, buffer, needed, out needed), "Read service config2"); return buffer; }
        catch { Marshal.FreeHGlobal(buffer); throw; }
    }
    public void InspectService() {
        uint needed; QueryServiceConfig(service, IntPtr.Zero, 0, out needed);
        Require(needed > 0 && needed <= 65536 && Marshal.GetLastWin32Error() == 122, "Invalid config size");
        IntPtr buffer = Marshal.AllocHGlobal((int)needed);
        try {
            Check(QueryServiceConfig(service, buffer, needed, out needed), "Inspect service config");
            Config c = (Config)Marshal.PtrToStructure(buffer, typeof(Config));
            Require(c.Type == 0x10 && c.Start == 3 && c.Error == 1 && c.Tag == 0, "Unexpected type/start/error/tag");
            Require(PtrText(c.Binary) == ImageCommand && PtrText(c.Display) == Name && PtrText(c.Group) == "" && PtrText(c.Dependencies) == "", "Unexpected image/display/group/dependencies");
            Require(String.Equals(PtrText(c.Account), @"NT AUTHORITY\LocalService", StringComparison.OrdinalIgnoreCase), "Unexpected service account");
        } finally { Marshal.FreeHGlobal(buffer); }
        buffer = Config2(5);
        try { Require(Marshal.ReadInt32(buffer) == 1, "Unexpected service SID type"); } finally { Marshal.FreeHGlobal(buffer); }
        buffer = Config2(2);
        try { FailureActions f = (FailureActions)Marshal.PtrToStructure(buffer, typeof(FailureActions)); Require(f.Count == 0 && PtrText(f.Reboot) == "" && PtrText(f.Command) == "", "Unexpected recovery configuration"); } finally { Marshal.FreeHGlobal(buffer); }
        buffer = Config2(8);
        try { Require(Marshal.ReadInt32(buffer) == 0, "Unexpected service triggers"); } finally { Marshal.FreeHGlobal(buffer); }
        buffer = Config2(12);
        try { Require(Marshal.ReadInt32(buffer) == 0, "Unexpected launch protection"); } finally { Marshal.FreeHGlobal(buffer); }
        QueryServiceObjectSecurity(service, 7, null, 0, out needed);
        Require(needed > 0 && needed <= 65536 && Marshal.GetLastWin32Error() == 122, "Invalid service SD size");
        byte[] sd = new byte[needed]; Check(QueryServiceObjectSecurity(service, 7, sd, needed, out needed), "Inspect service object security");
        ExactSecurity(new RawSecurityDescriptor(sd, 0), null, 0, true);
    }
    public Status GetStatus() {
        Status status; int needed;
        Check(QueryServiceStatusEx(service, 0, out status, Marshal.SizeOf(typeof(Status)), out needed), "Read service status");
        Require(status.Type == 0x10, "Service type changed"); return status;
    }
    public void RequireStopped() { Status status = GetStatus(); Require(status.State == 1 && status.Pid == 0, "Service is not stopped with PID zero"); }
    string ExitProof(SafeFileHandle process, uint pid, long created) {
        Check(GetExitCodeProcess(process, out ProcessExitCode), "Read retained process exit code"); return pid + ":" + created;
    }
    public string RunOnce() {
        RequireStopped(); InspectService(); long before = DateTime.UtcNow.ToFileTimeUtc();
        var elapsed = System.Diagnostics.Stopwatch.StartNew();
        Check(StartService(service, 0, IntPtr.Zero), "Start exact service once (consumed marker is already durable)");
        SafeFileHandle process = null; uint ownedPid = 0; long created = 0;
        try {
            while (elapsed.Elapsed.TotalSeconds < 25) {
                Status status = GetStatus();
                if (process == null && status.State == 4 && status.Pid != 0) {
                    process = OpenProcess(0x101000, false, status.Pid);
                    if (process.IsInvalid) throw new Win32Exception(Marshal.GetLastWin32Error(), "Retain service process handle");
                    var path = new StringBuilder(32768); uint length = (uint)path.Capacity;
                    Check(QueryFullProcessImageName(process, 0, path, ref length), "Bind service process image");
                    long exited, kernel, user; Check(GetProcessTimes(process, out created, out exited, out kernel, out user), "Bind process creation time");
                    Status again = GetStatus();
                    Require(String.Equals(path.ToString(), Image, StringComparison.OrdinalIgnoreCase) && created >= before && again.Pid == status.Pid && again.State == 4, "Service process generation not proven");
                    ownedPid = status.Pid;
                }
                if (process != null && WaitForSingleObject(process, 0) == 0 && status.State == 1 && status.Pid == 0)
                    return ExitProof(process, ownedPid, created);
                System.Threading.Thread.Sleep(50);
            }
            Status end = GetStatus();
            if (end.State != 1) {
                Status ignored;
                if (!ControlService(service, 1, out ignored) && Marshal.GetLastWin32Error() != 1062)
                    throw new Win32Exception(Marshal.GetLastWin32Error(), "Request bounded cooperative service stop");
            }
            var grace = System.Diagnostics.Stopwatch.StartNew();
            while (grace.Elapsed.TotalSeconds < 5) {
                Status status = GetStatus();
                if (process != null && WaitForSingleObject(process, 0) == 0 && status.State == 1 && status.Pid == 0)
                    return ExitProof(process, ownedPid, created);
                System.Threading.Thread.Sleep(50);
            }
            throw new InvalidOperationException("Process exit not confirmed; preserve service, files, report and consumed marker. Do not rerun or kill a PID.");
        } finally { if (process != null) process.Dispose(); }
    }
    public void DeleteOwnedService() {
        InspectService(); RequireStopped(); Check(DeleteService(service), "Delete exact owned stopped service");
        CloseServiceHandle(service); service = IntPtr.Zero;
        IntPtr found = OpenService(scm, Name, 1);
        if (found != IntPtr.Zero) { CloseServiceHandle(found); throw new InvalidOperationException("Service still visible; retain filesystem evidence"); }
        Require(Marshal.GetLastWin32Error() == 1060, "Service deletion is pending/uncertain; retain filesystem evidence");
    }
    public void ReleasePins() { for (int i = pins.Count - 1; i >= 0; i--) pins[i].Dispose(); pins.Clear(); }
    public void Dispose() { ReleasePins(); if (service != IntPtr.Zero) CloseServiceHandle(service); if (scm != IntPtr.Zero) CloseServiceHandle(scm); controller.Dispose(); }
}
}
'@

$root = [ProtectionProbe.Native]::Root
$evidence = [ProtectionProbe.Native]::Evidence
$utf8 = [Text.UTF8Encoding]::new($false, $true)
$manifestPath = Join-Path $PSScriptRoot 'manifest.json'
$manifestBytes = [IO.File]::ReadAllBytes($manifestPath)
if ($manifestBytes.Length -gt 4096) { throw 'Manifest exceeds bound.' }
$manifest = $utf8.GetString($manifestBytes) | ConvertFrom-Json
if ((@($manifest.PSObject.Properties.Name | Sort-Object) -join ',') -cne 'executableSha256,files,goarch,goos,schema,signed,sourceCommit,toolchain,windowsExecuted' -or
    $manifest.schema -ne 1 -or $manifest.executableSha256 -cnotmatch '^[0-9a-f]{64}$' -or
    $manifest.sourceCommit -cnotmatch '^[0-9a-f]{40}$' -or $manifest.toolchain -cne 'go1.27.3' -or
    $manifest.goos -cne 'windows' -or $manifest.goarch -cne 'amd64' -or
    $manifest.signed -isnot [bool] -or $manifest.signed -ne $false -or
    $manifest.windowsExecuted -isnot [bool] -or $manifest.windowsExecuted -ne $false) { throw 'Invalid approved artifact manifest.' }
$manifestHash = [ProtectionProbe.Native]::Hash($manifestBytes)
$scriptHash = [ProtectionProbe.Native]::Hash([IO.File]::ReadAllBytes($PSCommandPath))
if ($manifest.files.'Invoke-ProtectionProbe.ps1' -cne $scriptHash -or
    $manifest.files.'scm-protection-probe.exe' -cne $manifest.executableSha256) { throw 'Manifest script/image binding mismatch.' }
$native = [ProtectionProbe.Native]::new()

function JsonBytes($Value) { return ,$utf8.GetBytes(($Value | ConvertTo-Json -Compress)) }
function ReadJson([string]$Path) { return ($utf8.GetString($native.ReadFile($Path, 16384)) | ConvertFrom-Json) }
function AssertBinding($Receipt) {
    if ($Receipt.schema -ne 1 -or $Receipt.service -cne [ProtectionProbe.Native]::Name -or
        $Receipt.serviceSid -cne $native.Sid -or $Receipt.root -cne $root -or
        $Receipt.manifestSha256 -cne $manifestHash -or $Receipt.scriptSha256 -cne $scriptHash -or
        $Receipt.executableSha256 -cne $manifest.executableSha256 -or $Receipt.sourceCommit -cne $manifest.sourceCommit) {
        throw 'Receipt does not bind this exact approved packet/service/root.'
    }
}
function AssertContents {
    $expected = @('manifest.json', 'owner.json', 'reports', 'scm-protection-probe.exe')
    foreach ($optional in @('consumed.json', 'exited.json')) {
        if ([IO.File]::Exists("$root\$optional")) { $expected += $optional }
    }
    if ((@([IO.Directory]::GetFileSystemEntries($root) | ForEach-Object { [IO.Path]::GetFileName($_) } | Sort-Object) -join '|') -cne
        (@($expected | Sort-Object) -join '|')) { throw 'Unexpected/missing root entries; preserve and review.' }
    if ((@([IO.Directory]::GetFileSystemEntries("$root\reports") | ForEach-Object { [IO.Path]::GetFileName($_) }) -join '|') -cne 'subject.json') {
        throw 'Unexpected/missing reports entries; preserve and review.'
    }
}
try {
    $native.PinParent('C:\')
    $native.PinParent('C:\ProgramData')
    if ($Phase -eq 'Prepare') {
        if ([IO.Directory]::Exists($root) -or [IO.File]::Exists($root) -or [IO.Directory]::Exists($evidence) -or [IO.File]::Exists($evidence)) {
            throw 'Fixed root/evidence collision; do not overwrite or reuse.'
        }
        # Read exactly once, then hash and write these same bytes.
        $imageBytes = [IO.File]::ReadAllBytes((Join-Path $PSScriptRoot 'scm-protection-probe.exe'))
        if ($imageBytes.Length -gt 16777216 -or [ProtectionProbe.Native]::Hash($imageBytes) -cne $manifest.executableSha256) {
            throw 'Approved executable hash/size mismatch.'
        }
        $native.Connect($true)
        $native.Register()
        $native.NewDirectory($root, $native.Sid, 0x1200a9)
        $native.NewDirectory("$root\reports", $native.Sid, 0x1200a9)
        $native.NewFile([ProtectionProbe.Native]::Image, $imageBytes, $native.Sid, 0x1200a9)
        $native.NewFile("$root\reports\subject.json", [byte[]]@(), $native.Sid, 0x120116)
        $native.NewFile("$root\manifest.json", $manifestBytes, $null, 0)
        $receipt = [ordered]@{ schema = 1; service = [ProtectionProbe.Native]::Name; serviceSid = $native.Sid; root = $root;
            sourceCommit = $manifest.sourceCommit; executableSha256 = $manifest.executableSha256; manifestSha256 = $manifestHash; scriptSha256 = $scriptHash }
        $native.NewFile("$root\owner.json", (JsonBytes $receipt), $null, 0)
        $native.InspectService(); $native.RequireStopped(); AssertContents
        Write-Output 'Prepared and stopped. No StartService has been called. Run is not automatic and must be within the approved packet.'
    } else {
        $native.Connect($false)
        $native.PinExact($root, $true, $native.Sid, 0x1200a9)
        $native.PinExact("$root\reports", $true, $native.Sid, 0x1200a9)
        $native.PinExact([ProtectionProbe.Native]::Image, $false, $native.Sid, 0x1200a9)
        $native.PinExact("$root\reports\subject.json", $false, $native.Sid, 0x120116)
        foreach ($name in @('manifest.json', 'owner.json')) { $native.PinExact("$root\$name", $false, $null, 0) }
        $receipt = ReadJson "$root\owner.json"; AssertBinding $receipt
        if ([ProtectionProbe.Native]::Hash($native.ReadFile("$root\manifest.json", 4096)) -cne $manifestHash -or
            [ProtectionProbe.Native]::Hash($native.ReadFile([ProtectionProbe.Native]::Image, 16777216)) -cne $manifest.executableSha256) {
            throw 'Installed artifact binding mismatch.'
        }
        AssertContents
        $native.RequireStopped()
        if ($Phase -eq 'Run') {
            if ([IO.File]::Exists("$root\consumed.json") -or [IO.File]::Exists("$root\exited.json") -or $native.ReadFile("$root\reports\subject.json", 262144).Length -ne 0) {
                throw 'Run already consumed, or report not empty. Never rerun this registration.'
            }
            $native.NewFile("$root\consumed.json", (JsonBytes $receipt), $null, 0)
            $processIdentity = $native.RunOnce()
            # Only a signaled retained process handle AND SCM stopped/PID zero reach here.
            $finalStatus = $native.GetStatus()
            $exitReceipt = [ordered]@{ binding = $receipt; process = $processIdentity; exited = $true; processExitCode = $native.ProcessExitCode;
                serviceWin32Exit = $finalStatus.Win32Exit; serviceSpecificExit = $finalStatus.ServiceExit }
            $native.NewFile("$root\exited.json", (JsonBytes $exitReceipt), $null, 0)
            $reportBytes = $native.ReadFile("$root\reports\subject.json", 262144)
            Write-Output ("Process exit confirmed (exit code {0}); capture success is not implied. Private report bytes={1}, SHA256={2}. Cleanup is not automatic and must be within the approved packet." -f $native.ProcessExitCode, $reportBytes.Length, [ProtectionProbe.Native]::Hash($reportBytes))
        } else {
            # Cleanup is permitted only for a proven never-run installation or a proven exit.
            $consumed = [IO.File]::Exists("$root\consumed.json")
            $exited = [IO.File]::Exists("$root\exited.json")
            if ($consumed -ne $exited) { throw 'Run outcome unconfirmed. Retain all resources; manual reviewed recovery is required.' }
            if ($consumed) {
                foreach ($name in @('consumed.json', 'exited.json')) { $native.PinExact("$root\$name", $false, $null, 0) }
                AssertBinding (ReadJson "$root\consumed.json")
                $exitReceipt = ReadJson "$root\exited.json"; AssertBinding $exitReceipt.binding
                if ($exitReceipt.exited -ne $true -or $exitReceipt.process -notmatch '^[1-9][0-9]*:[1-9][0-9]*$') { throw 'Invalid retained-process exit receipt.' }
            } elseif ($native.ReadFile("$root\reports\subject.json", 262144).Length -ne 0) { throw 'Never-run installation contains report data.' }
            # CREATE_NEW evidence directory deliberately refuses preexisting/partial evidence.
            $native.NewDirectory($evidence, $null, 0)
            foreach ($name in @('manifest.json', 'owner.json')) {
                $bytes = $native.ReadFile("$root\$name", 16384); $native.NewFile("$evidence\$name", $bytes, $null, 0)
                if ([ProtectionProbe.Native]::Hash($native.ReadFile("$evidence\$name", 16384)) -cne [ProtectionProbe.Native]::Hash($bytes)) { throw 'Evidence verification failed.' }
            }
            $reportBytes = $native.ReadFile("$root\reports\subject.json", 262144)
            $native.NewFile("$evidence\subject.json", $reportBytes, $null, 0)
            if ([ProtectionProbe.Native]::Hash($native.ReadFile("$evidence\subject.json", 262144)) -cne [ProtectionProbe.Native]::Hash($reportBytes)) { throw 'Report preservation failed.' }
            $proof = $null
            if ($consumed) { $proof = $exitReceipt }
            $native.NewFile("$evidence\exit.json", (JsonBytes ([ordered]@{ consumed = $consumed; processExitConfirmed = $exited; proof = $proof; reportSha256 = [ProtectionProbe.Native]::Hash($reportBytes) })), $null, 0)
            $native.InspectService(); $native.RequireStopped(); AssertContents
            $native.DeleteOwnedService()
            $native.ReleasePins()
            # No recursive deletion, globbing, arbitrary path, or deletion before service absence.
            [IO.File]::Delete("$root\reports\subject.json")
            [IO.Directory]::Delete("$root\reports", $false)
            foreach ($name in @('scm-protection-probe.exe', 'manifest.json', 'owner.json', 'consumed.json', 'exited.json')) {
                [IO.File]::Delete("$root\$name")
            }
            [IO.Directory]::Delete($root, $false)
            Write-Output ("Cleanup completed. Private protected evidence retained at {0}; report SHA256={1}." -f $evidence, [ProtectionProbe.Native]::Hash($reportBytes))
        }
    }
} catch {
    Write-Warning 'Stopped without retry or rollback. Retain any exact service/root/evidence already created. See SETUP-CONTRACT.md for partial-state recovery.'
    throw
} finally { $native.Dispose() }
