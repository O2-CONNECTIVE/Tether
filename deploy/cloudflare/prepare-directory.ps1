#Requires -Version 5.1
#Requires -PSEdition Desktop
<#
.SYNOPSIS
Tether: create the protected directory C:\Tether once. Human administrator only.

.DESCRIPTION
Source only. Never executed, parsed or compiled by its authors, and no
automated tests exist for it. The Linux container is the preferred deployment;
this Windows script is a retained, unexecuted fallback. Its source was approved;
running it was not. Read prepare-directory.md next to this file before any use.

Creates exactly one new directory, C:\Tether, atomically with an initial
protected security descriptor:
  owner  BUILTIN\Administrators (S-1-5-32-544)
  DACL   protected, exactly three explicit allow entries (OI|CI):
         SYSTEM FullControl, BUILTIN\Administrators FullControl,
         RuntimeSid ReadAndExecute (0x001200A9)
It never adopts, repairs, re-ACLs or deletes anything, including a directory
it created itself (there is no automatic rollback), never changes C:\ or
ProgramData and never elevates itself.

-Preview compiles no helper and does not open C:\ or C:\Tether. It is not
offline: translating RuntimeSid and ExpectedRuntimeAccount queries the local
security authority, which may contact a domain controller. Typed arguments may
be kept in the shell history.

.PARAMETER RuntimeSid
Separately verified S-1-5-21-a-b-c-rid SID of the ordinary runtime account.

.PARAMETER ExpectedRuntimeAccount
Separately verified AUTHORITY\name that RuntimeSid must translate to exactly.
Names ending with a dollar sign (computer accounts, gMSA) are refused.

.PARAMETER Preview
Preview. Nothing is created, no helper is compiled and C:\ is not opened; the
SID/account translation still queries LSA.
#>
[CmdletBinding()]
param(
    [string]$RuntimeSid = '',
    [string]$ExpectedRuntimeAccount = '',
    [switch]$Preview
)

# Fixed values. The target is never taken from a parameter or the environment.
$TetherTargetPath = 'C:\Tether'
$TetherTargetNativePath = '\\?\C:\Tether'
$TetherRootNativePath = '\\?\C:\'
$TetherSidSystem = 'S-1-5-18'
$TetherSidAdministrators = 'S-1-5-32-544'
$TetherMaskFullControl = 0x001F01FF
$TetherMaskReadAndExecute = 0x001200A9
$TetherClockSlack = [long]50000000 # 5 seconds in FILETIME units
$TetherConfirmation = 'CREATE C:\Tether'

# ---------------------------------------------------------------------------
# Pure validation helpers. They read only their arguments and emit one string
# per problem; no output means the check passed.
# ---------------------------------------------------------------------------

function Test-TetherRuntimeSidText {
    param([string]$Sid)
    if ([string]::IsNullOrEmpty($Sid)) { 'RuntimeSid is required (there is no default).'; return }
    $match = [regex]::Match($Sid, '\AS-1-5-21-([0-9]{1,10})-([0-9]{1,10})-([0-9]{1,10})-([0-9]{1,10})\z')
    if (-not $match.Success) { 'RuntimeSid must be a canonical S-1-5-21-a-b-c-rid account SID.'; return }
    for ($i = 1; $i -le 4; $i++) {
        $part = $match.Groups[$i].Value
        $value = [uint64]0
        if (-not [uint64]::TryParse($part, [ref]$value) -or $value -gt [uint32]::MaxValue -or
            $value.ToString([Globalization.CultureInfo]::InvariantCulture) -cne $part) {
            "RuntimeSid sub-authority $i is not a canonical 32-bit value."
            return
        }
    }
    if ([uint64]$match.Groups[4].Value -lt 1000) {
        'RuntimeSid RID is below 1000 (built-in account or group such as Administrator or Guest).'
    }
}

function Test-TetherAccountText {
    param([string]$Account)
    if ([string]::IsNullOrEmpty($Account)) { 'ExpectedRuntimeAccount is required (there is no default).'; return }
    if ($Account.Length -gt 300 -or -not [regex]::IsMatch($Account, '\A[^\\\x00-\x1F\x7F]+\\[^\\\x00-\x1F\x7F]+\z')) {
        'ExpectedRuntimeAccount must be exactly one AUTHORITY\name pair without control characters.'
        return
    }
    $authority = $Account.Substring(0, $Account.IndexOf('\'))
    foreach ($reserved in 'NT AUTHORITY', 'BUILTIN', 'NT SERVICE') {
        if ([string]::Equals($authority, $reserved, [StringComparison]::OrdinalIgnoreCase)) {
            "ExpectedRuntimeAccount authority '$reserved' is not an ordinary account authority."
        }
    }
    if ($Account.EndsWith('$')) {
        'ExpectedRuntimeAccount ends with a dollar sign (computer account or gMSA); an ordinary user account is required.'
    }
}

# Shape of the account running this script. That a human administrator is
# operating it remains the operator's attestation; it cannot be checked here.
function Test-TetherOperatorIdentity {
    param([string]$Sid, [string]$Name)
    if (-not [regex]::IsMatch($Sid, '\AS-1-5-21-[0-9]{1,10}-[0-9]{1,10}-[0-9]{1,10}-[0-9]{1,10}\z')) {
        'the account running this script is not an S-1-5-21 local or domain account (SYSTEM, service and other identities are refused)'
    }
    if ([string]::IsNullOrEmpty($Name) -or $Name.EndsWith('$')) {
        'the account running this script has no name or ends with a dollar sign (computer account or gMSA)'
    }
}

function Get-TetherIntendedSddl {
    param([string]$Sid)
    'O:BAD:P(A;OICI;0x1f01ff;;;SY)(A;OICI;0x1f01ff;;;BA)(A;OICI;0x1200a9;;;{0})' -f $Sid
}

# Owner, protection and the exact three DACL entries, in order. The primary
# group is not set by this script and is not checked (it grants no access).
function Test-TetherDescriptor {
    param([System.Security.AccessControl.RawSecurityDescriptor]$Descriptor, [string]$Sid)
    if ($null -eq $Descriptor) { 'security descriptor is missing'; return }
    if ($null -eq $Descriptor.Owner -or $Descriptor.Owner.Value -cne $TetherSidAdministrators) {
        'owner is not BUILTIN\Administrators (S-1-5-32-544)'
    }
    $flags = $Descriptor.ControlFlags
    if (-not $flags.HasFlag([System.Security.AccessControl.ControlFlags]::DiscretionaryAclPresent)) { 'DACL is not present' }
    if (-not $flags.HasFlag([System.Security.AccessControl.ControlFlags]::DiscretionaryAclProtected)) {
        'DACL is not protected (entries from C:\ could be inherited)'
    }
    $acl = $Descriptor.DiscretionaryAcl
    if ($null -eq $acl) { 'DACL is NULL'; return }
    if ($acl.Count -ne 3) { "DACL has $($acl.Count) entries; exactly 3 are required"; return }
    $expected = @(
        @($TetherSidSystem, $TetherMaskFullControl, 'SYSTEM FullControl'),
        @($TetherSidAdministrators, $TetherMaskFullControl, 'Administrators FullControl'),
        @($Sid, $TetherMaskReadAndExecute, 'runtime ReadAndExecute')
    )
    $inherit = [System.Security.AccessControl.AceFlags]::ObjectInherit -bor [System.Security.AccessControl.AceFlags]::ContainerInherit
    $index = 0
    foreach ($ace in $acl) {
        $want = $expected[$index]
        if ($ace -isnot [System.Security.AccessControl.CommonAce] -or $ace.AceType -ne [System.Security.AccessControl.AceType]::AccessAllowed) {
            "entry $index is not a plain allow entry ($($want[2]) expected)"
        } else {
            if ($ace.AceFlags -ne $inherit) { "entry $index flags are '$($ace.AceFlags)'; only ObjectInherit, ContainerInherit allowed" }
            if ($ace.AccessMask -ne $want[1]) { 'entry {0} mask is 0x{1:X8}; 0x{2:X8} required ({3})' -f $index, $ace.AccessMask, $want[1], $want[2] }
            if ($ace.SecurityIdentifier.Value -cne $want[0]) { "entry $index principal is $($ace.SecurityIdentifier.Value); $($want[0]) required ($($want[2]))" }
        }
        $index++
    }
}

function Test-TetherRootEvidence {
    param($Evidence, [string]$FileSystem, [uint32]$DriveType)
    if ($Evidence.FileType -ne 1) { 'C:\ is not a disk file object' }
    if (($Evidence.Attributes -band 0x10) -eq 0) { 'C:\ is not a directory' }
    if (($Evidence.Attributes -band 0x400) -ne 0) { 'C:\ is a reparse point' }
    if ($Evidence.DosPath -cne '\\?\C:\') { "C:\ final path is '$($Evidence.DosPath)'; '\\?\C:\' required" }
    if (-not [regex]::IsMatch([string]$Evidence.GuidPath, '\A\\\\\?\\Volume\{[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}\}\\\z')) {
        'C:\ has no volume GUID root path'
    }
    if ($FileSystem -cne 'NTFS' -and $FileSystem -cne 'ReFS') { "C: file system '$FileSystem' is not NTFS or ReFS" }
    if ($DriveType -ne 3) { "C: drive type $DriveType is not DRIVE_FIXED (3)" }
    if ($Evidence.VolumeSerial -eq 0) { 'C: volume serial is zero' }
}

# Identity of the object behind the held target handle: a real directory named
# exactly C:\Tether directly under the held C:\ root, with a creation time in
# this run's window. Path and creation time are supporting evidence only; they
# do not prove that this run created the object (creation time can be set and
# name tunneling can carry an older one over).
function Test-TetherTargetIdentity {
    param($Evidence, $RootEvidence, [long]$NotBefore, [long]$NotAfter)
    if ($Evidence.FileType -ne 1) { 'C:\Tether is not a disk file object' }
    if (($Evidence.Attributes -band 0x10) -eq 0) { 'C:\Tether is not a directory' }
    if (($Evidence.Attributes -band 0x400) -ne 0) { 'C:\Tether is a reparse point' }
    if ($Evidence.DosPath -cne '\\?\C:\Tether') { "final path is '$($Evidence.DosPath)'; '\\?\C:\Tether' required" }
    if ($Evidence.GuidPath -cne ([string]$RootEvidence.GuidPath + 'Tether')) { 'C:\Tether is not directly under the held C:\ volume root' }
    if ($Evidence.VolumeSerial -ne $RootEvidence.VolumeSerial) { 'C:\Tether is on a different volume than the held C:\ root' }
    if ($Evidence.CreationTime -lt $NotBefore -or $Evidence.CreationTime -gt $NotAfter) { 'C:\Tether creation time is outside this run' }
}

function Test-TetherSameObject {
    param($First, $Second)
    foreach ($name in 'VolumeSerial', 'FileId', 'DosPath', 'GuidPath', 'CreationTime') {
        if ($First.$name -cne $Second.$name) { "C:\Tether $name changed during verification" }
    }
    if (($Second.Attributes -band 0x410) -ne 0x10) { 'C:\Tether is no longer a non-reparse directory' }
}

# Loading this file with "." defines only the helpers above and runs nothing
# below. No tests that do so exist in this repository.
if ($MyInvocation.InvocationName -eq '.') { return }

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'

# ---------------------------------------------------------------------------
# Native code, compiled only in execution mode after confirmation. Handles are
# never exposed to PowerShell; each is closed at most once and never reused.
# ---------------------------------------------------------------------------
$TetherNativeSource = @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Text;

namespace TetherPrepareV1
{
    public sealed class HeldHandle
    {
        private IntPtr value;
        private bool closeAttempted;
        private bool closeSucceeded;
        private int closeError;

        internal HeldHandle(IntPtr handle) { value = handle; }

        public bool CloseSucceeded { get { return closeSucceeded; } }
        public int CloseError { get { return closeError; } }

        internal IntPtr Value
        {
            get
            {
                if (closeAttempted) { throw new InvalidOperationException("handle was already closed"); }
                return value;
            }
        }

        // CloseHandle is called at most once and never retried; later calls
        // report the first result.
        public bool Close()
        {
            if (closeAttempted) { return closeSucceeded; }
            closeAttempted = true;
            IntPtr handle = value;
            value = IntPtr.Zero;
            closeSucceeded = Native.CloseHandle(handle);
            closeError = closeSucceeded ? 0 : Marshal.GetLastWin32Error();
            return closeSucceeded;
        }
    }

    public sealed class Evidence
    {
        public uint FileType;
        public uint Attributes;
        public long CreationTime;
        public ulong VolumeSerial;
        public string FileId;
        public string DosPath;
        public string GuidPath;
    }

    public static class Native
    {
        private const uint FILE_READ_ATTRIBUTES = 0x00000080;
        private const uint READ_CONTROL = 0x00020000;
        private const uint FILE_SHARE_READ = 1, FILE_SHARE_WRITE = 2, FILE_SHARE_DELETE = 4;
        private const uint OPEN_EXISTING = 3;
        private const uint FILE_FLAG_OPEN_REPARSE_POINT = 0x00200000;
        private const uint FILE_FLAG_BACKUP_SEMANTICS = 0x02000000;
        private const uint OWNER_SECURITY_INFORMATION = 1, DACL_SECURITY_INFORMATION = 4;
        private const int FileBasicInfo = 0, FileIdInfo = 18;
        private const int ERROR_INSUFFICIENT_BUFFER = 122;

        [StructLayout(LayoutKind.Sequential)]
        private struct SECURITY_ATTRIBUTES { public int nLength; public IntPtr lpSecurityDescriptor; public int bInheritHandle; }

        // These fields are filled by the OS through marshaling, not by C# code.
#pragma warning disable 0169, 0649
        [StructLayout(LayoutKind.Sequential)]
        private struct FILE_BASIC_INFO { public long CreationTime; public long LastAccessTime; public long LastWriteTime; public long ChangeTime; public uint FileAttributes; }

        [StructLayout(LayoutKind.Sequential)]
        private struct FILE_ID_INFO { public ulong VolumeSerialNumber; public ulong FileIdLow; public ulong FileIdHigh; }
#pragma warning restore 0169, 0649

        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern IntPtr CreateFileW(string name, uint access, uint share, IntPtr security, uint disposition, uint flags, IntPtr template);
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern bool CreateDirectoryW(string name, ref SECURITY_ATTRIBUTES security);
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern uint GetFileAttributesW(string name);
        [DllImport("kernel32.dll", SetLastError = true)]
        internal static extern bool CloseHandle(IntPtr handle);
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern uint GetFileType(IntPtr handle);
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern bool GetFileInformationByHandleEx(IntPtr handle, int infoClass, out FILE_BASIC_INFO info, uint size);
        [DllImport("kernel32.dll", SetLastError = true)]
        private static extern bool GetFileInformationByHandleEx(IntPtr handle, int infoClass, out FILE_ID_INFO info, uint size);
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern uint GetFinalPathNameByHandleW(IntPtr handle, StringBuilder path, uint length, uint flags);
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern bool GetVolumeInformationByHandleW(IntPtr handle, StringBuilder volumeName, uint volumeNameSize, IntPtr serial, IntPtr maxComponent, IntPtr flags, StringBuilder fileSystem, uint fileSystemSize);
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern uint GetDriveTypeW(string root);
        [DllImport("advapi32.dll", SetLastError = true)]
        private static extern bool GetKernelObjectSecurity(IntPtr handle, uint information, byte[] descriptor, uint length, out uint needed);
        [DllImport("advapi32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern bool LookupAccountSidW(string system, byte[] sid, StringBuilder name, ref uint nameLength, StringBuilder domain, ref uint domainLength, out int use);

        private static Win32Exception Failure(string stage)
        {
            int error = Marshal.GetLastWin32Error();
            return new Win32Exception(error, stage + ": " + new Win32Exception(error).Message);
        }

        // Preliminary, path-based and read-only.
        public static uint QueryAttributes(string path, out int error)
        {
            uint attributes = GetFileAttributesW(path);
            error = attributes == 0xFFFFFFFF ? Marshal.GetLastWin32Error() : 0;
            return attributes;
        }

        // No-follow open of an existing object; never creates anything.
        private static HeldHandle Open(string path, uint access, uint share, string stage)
        {
            IntPtr handle = CreateFileW(path, access, share, IntPtr.Zero, OPEN_EXISTING, FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT, IntPtr.Zero);
            if (handle == new IntPtr(-1) || handle == IntPtr.Zero) { throw Failure(stage); }
            return new HeldHandle(handle);
        }

        public static HeldHandle OpenRoot(string path)
        {
            return Open(path, FILE_READ_ATTRIBUTES, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE, "open C:\\ root");
        }

        // Read attributes and the security descriptor only; no DELETE access.
        // Attribute/security-only access does not enforce share-delete locking.
        // Principals with delete rights may rename/delete while this is held;
        // before/after identity inspection detects only observed changes.
        public static HeldHandle OpenTarget(string path)
        {
            return Open(path, FILE_READ_ATTRIBUTES | READ_CONTROL, FILE_SHARE_READ | FILE_SHARE_WRITE, "open new directory");
        }

        public static Evidence Inspect(HeldHandle handle)
        {
            IntPtr h = handle.Value;
            Evidence e = new Evidence();
            e.FileType = GetFileType(h);
            FILE_BASIC_INFO basic;
            if (!GetFileInformationByHandleEx(h, FileBasicInfo, out basic, (uint)Marshal.SizeOf(typeof(FILE_BASIC_INFO)))) { throw Failure("read basic information"); }
            e.Attributes = basic.FileAttributes;
            e.CreationTime = basic.CreationTime;
            FILE_ID_INFO id;
            if (!GetFileInformationByHandleEx(h, FileIdInfo, out id, (uint)Marshal.SizeOf(typeof(FILE_ID_INFO)))) { throw Failure("read file ID"); }
            e.VolumeSerial = id.VolumeSerialNumber;
            e.FileId = id.FileIdHigh.ToString("x16") + id.FileIdLow.ToString("x16");
            e.DosPath = FinalPath(h, 0, "read final path");
            e.GuidPath = FinalPath(h, 1, "read volume GUID path");
            return e;
        }

        private static string FinalPath(IntPtr h, uint flags, string stage)
        {
            StringBuilder path = new StringBuilder(1024);
            uint length = GetFinalPathNameByHandleW(h, path, (uint)path.Capacity, flags);
            if (length == 0) { throw Failure(stage); }
            if (length >= path.Capacity) { throw new Win32Exception(ERROR_INSUFFICIENT_BUFFER, stage + ": path is too long"); }
            return path.ToString();
        }

        public static string FileSystemName(HeldHandle handle)
        {
            StringBuilder name = new StringBuilder(261);
            if (!GetVolumeInformationByHandleW(handle.Value, null, 0, IntPtr.Zero, IntPtr.Zero, IntPtr.Zero, name, (uint)name.Capacity)) { throw Failure("read file system name"); }
            return name.ToString();
        }

        public static uint DriveType(string volumeGuidRoot)
        {
            return GetDriveTypeW(volumeGuidRoot);
        }

        // The descriptor is applied by the OS when the directory is created.
        // Returns 0 or the Win32 error; never opens or changes an existing object.
        public static int CreateDirectory(string path, byte[] descriptor)
        {
            GCHandle pin = GCHandle.Alloc(descriptor, GCHandleType.Pinned);
            try
            {
                SECURITY_ATTRIBUTES sa = new SECURITY_ATTRIBUTES();
                sa.nLength = Marshal.SizeOf(typeof(SECURITY_ATTRIBUTES));
                sa.lpSecurityDescriptor = pin.AddrOfPinnedObject();
                sa.bInheritHandle = 0;
                if (CreateDirectoryW(path, ref sa)) { return 0; }
                int error = Marshal.GetLastWin32Error();
                return error == 0 ? -1 : error;
            }
            finally
            {
                pin.Free();
            }
        }

        public static byte[] ReadDescriptor(HeldHandle handle)
        {
            IntPtr h = handle.Value;
            uint information = OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION;
            uint needed;
            if (GetKernelObjectSecurity(h, information, null, 0, out needed)) { throw new Win32Exception(13, "read security descriptor: unexpected empty result"); }
            int error = Marshal.GetLastWin32Error();
            if (error != ERROR_INSUFFICIENT_BUFFER) { throw new Win32Exception(error, "read security descriptor size: " + new Win32Exception(error).Message); }
            if (needed == 0 || needed > 65536) { throw new Win32Exception(13, "read security descriptor size: unexpected length"); }
            byte[] descriptor = new byte[needed];
            uint actual;
            if (!GetKernelObjectSecurity(h, information, descriptor, needed, out actual)) { throw Failure("read security descriptor"); }
            return descriptor;
        }

        // SID_NAME_USE of the SID (1 = SidTypeUser) and its AUTHORITY\name.
        public static int LookupSidUse(byte[] sid, out string account)
        {
            StringBuilder name = new StringBuilder(257), domain = new StringBuilder(257);
            uint nameLength = (uint)name.Capacity, domainLength = (uint)domain.Capacity;
            int use;
            if (!LookupAccountSidW(null, sid, name, ref nameLength, domain, ref domainLength, out use)) { throw Failure("LookupAccountSidW"); }
            account = domain.ToString() + "\\" + name.ToString();
            return use;
        }
    }
}
'@

function Format-TetherException {
    param($Record)
    $exception = $Record.Exception
    while ($exception -is [System.Management.Automation.MethodInvocationException] -and $null -ne $exception.InnerException) {
        $exception = $exception.InnerException
    }
    if ($exception -is [System.ComponentModel.Win32Exception]) {
        return ('{0} (Win32 error {1})' -f $exception.Message, $exception.NativeErrorCode)
    }
    return $exception.Message
}

function Stop-TetherBeforeNative {
    param([string[]]$Problems)
    foreach ($problem in $Problems) { Write-Host "STOP: $problem" }
    Write-Host 'RESULT: STOPPED. C:\Tether was not created and no ownership or ACL was changed.'
    exit 1
}

function Show-TetherPlan {
    param([string]$Sid, [string]$Account, [string]$Sddl)
    Write-Host ''
    Write-Host 'Planned operation (one new directory only):'
    Write-Host "  Target      : $TetherTargetPath  (fixed; must not exist in any form)"
    Write-Host '  Owner       : BUILTIN\Administrators (S-1-5-32-544)'
    Write-Host '  DACL        : protected (nothing inherited from C:\), exactly these entries:'
    Write-Host '    1. allow  SYSTEM (S-1-5-18)                FullControl     0x001F01FF  OI|CI'
    Write-Host '    2. allow  Administrators (S-1-5-32-544)    FullControl     0x001F01FF  OI|CI'
    Write-Host "    3. allow  $Account"
    Write-Host "              ($Sid)"
    Write-Host '                                               ReadAndExecute  0x001200A9  OI|CI'
    Write-Host '              (no create, write, delete, WRITE_DAC or WRITE_OWNER)'
    Write-Host "  SDDL        : $Sddl"
    Write-Host '  Not changed : C:\, ProgramData, any existing object, services, accounts,'
    Write-Host '                keys, config, binaries, Tunnel'
    Write-Host '  Not deleted : anything, including C:\Tether after a failed or interrupted run'
    Write-Host '  Operator attests (NOT checked by this script):'
    Write-Host '    - a human administrator is operating this console'
    Write-Host '    - the runtime account is an ordinary user (not a service account or gMSA)'
    Write-Host '      and is not a direct or nested member of Administrators or Backup Operators'
    Write-Host ''
}

# Native phase. Every handle is stored in $Ctx so Complete-TetherRun can close
# it exactly once, whatever happens here.
function Invoke-TetherCreate {
    param([hashtable]$Ctx)

    $Ctx.Root = [TetherPrepareV1.Native]::OpenRoot($TetherRootNativePath)
    $Ctx.RootEvidence = [TetherPrepareV1.Native]::Inspect($Ctx.Root)
    $fileSystem = [TetherPrepareV1.Native]::FileSystemName($Ctx.Root)
    $driveType = [TetherPrepareV1.Native]::DriveType($Ctx.RootEvidence.GuidPath)
    $problems = @(Test-TetherRootEvidence $Ctx.RootEvidence $fileSystem $driveType)
    if ($problems.Count -gt 0) { $Ctx.Failure = 'C:\ checks failed: ' + ($problems -join '; '); return }

    $code = 0
    $attributes = [TetherPrepareV1.Native]::QueryAttributes($TetherTargetNativePath, [ref]$code)
    if ($attributes -ne [uint32]::MaxValue) { $Ctx.Failure = 'C:\Tether already exists in some form. It was not opened or modified.'; return }
    if ($code -ne 2) { $Ctx.Failure = "C:\Tether state could not be confirmed as absent (Win32 error $code). Nothing was created."; return }

    $descriptor = [System.Security.AccessControl.RawSecurityDescriptor]::new((Get-TetherIntendedSddl $Ctx.Sid))
    $problems = @(Test-TetherDescriptor $descriptor $Ctx.Sid)
    if ($problems.Count -gt 0) { $Ctx.Failure = 'internal descriptor self-check failed: ' + ($problems -join '; '); return }
    $bytes = New-Object byte[] $descriptor.BinaryLength
    $descriptor.GetBinaryForm($bytes, 0)

    $notBefore = [DateTime]::UtcNow.ToFileTimeUtc() - $TetherClockSlack
    # Unknown until the native result is recorded below: an interruption between
    # here and that assignment must never be reported as 'not created'.
    $Ctx.CreateState = 'unknown'
    $code = [TetherPrepareV1.Native]::CreateDirectory($TetherTargetNativePath, $bytes)
    if ($code -eq 183) {
        $Ctx.CreateState = 'not-created'
        $Ctx.Failure = 'CreateDirectoryW: C:\Tether already exists (ERROR_ALREADY_EXISTS). It was not opened or modified.'
        return
    }
    if ($code -ne 0) {
        $Ctx.CreateState = 'not-created'
        $Ctx.Failure = "CreateDirectoryW failed (Win32 error $code). This run did not create C:\Tether."
        return
    }
    $Ctx.CreateState = 'created'

    # Everything below runs on one held handle. The object behind it is accepted
    # only if identity, the Administrators owner and the exact protected
    # descriptor all match; otherwise it is left untouched and reported.
    $Ctx.Target = [TetherPrepareV1.Native]::OpenTarget($TetherTargetNativePath)
    $notAfter = [DateTime]::UtcNow.ToFileTimeUtc() + $TetherClockSlack
    $first = [TetherPrepareV1.Native]::Inspect($Ctx.Target)
    $problems = @(Test-TetherTargetIdentity $first $Ctx.RootEvidence $notBefore $notAfter)
    if ($problems.Count -gt 0) { $Ctx.Failure = 'the object now at C:\Tether failed the identity checks: ' + ($problems -join '; '); return }

    $actual = [System.Security.AccessControl.RawSecurityDescriptor]::new([TetherPrepareV1.Native]::ReadDescriptor($Ctx.Target), 0)
    $problems = @(Test-TetherDescriptor $actual $Ctx.Sid)
    if ($problems.Count -gt 0) { $Ctx.Failure = 'owner or security descriptor on the held handle differs from the plan: ' + ($problems -join '; '); return }

    $second = [TetherPrepareV1.Native]::Inspect($Ctx.Target)
    $problems = @(Test-TetherSameObject $first $second)
    if ($problems.Count -gt 0) { $Ctx.Failure = 'identity changed during verification: ' + ($problems -join '; '); return }
    $Ctx.TargetEvidence = $first
    $Ctx.Verified = $true
}

# Close each handle exactly once and report. Nothing is ever deleted: whatever
# a failed or interrupted run leaves at C:\Tether, whoever owns it, is reported
# for manual inspection. Returns the exit code.
function Complete-TetherRun {
    param([hashtable]$Ctx)
    $manual = $false
    $cleanup = New-Object 'System.Collections.Generic.List[string]'
    if ($null -eq $Ctx.Failure -and -not $Ctx.Verified) { $Ctx.Failure = 'the run stopped before verification completed (interrupted or no detail)' }
    $mayExist = $Ctx.CreateState -cne 'not-attempted' -and $Ctx.CreateState -cne 'not-created'

    if ($null -ne $Ctx.Target -and -not $Ctx.Target.Close()) {
        $cleanup.Add("closing the C:\Tether handle failed (Win32 error $($Ctx.Target.CloseError)); not retried")
        $manual = $true
    }
    if ($null -ne $Ctx.Root -and -not $Ctx.Root.Close()) {
        $cleanup.Add("closing the C:\ handle failed (Win32 error $($Ctx.Root.CloseError)); not retried")
        $manual = $true
    }

    if ($mayExist -and -not $Ctx.Verified) { $manual = $true }

    Write-Host ''
    if ($Ctx.Verified -and -not $manual) {
        $e = $Ctx.TargetEvidence
        Write-Host 'RESULT: SUCCESS. CreateDirectoryW succeeded, the directory at C:\Tether passed the owner, descriptor'
        Write-Host '  and identity checks on the held handle, and all handles were closed.'
        Write-Host ('  Local record only: volume serial 0x{0:X16}, file ID {1}' -f $e.VolumeSerial, $e.FileId)
        Write-Host 'This does not prove it is still empty or unchanged later; re-check before any placement.'
        Write-Host 'Key/config/binary placement and the native loader check are separate approved steps.'
        return 0
    }
    if ($Ctx.Verified) { Write-Host 'C:\Tether passed the checks on the held handle, but a handle close was not confirmed. This is NOT reported as success.' }
    if ($null -ne $Ctx.Failure) { Write-Host "PRIMARY ERROR: $($Ctx.Failure)" }
    foreach ($line in $cleanup) { Write-Host "CLEANUP UNKNOWN: $line" }
    if (-not $manual) {
        Write-Host 'RESULT: STOPPED. This run did not create C:\Tether and changed no ownership or ACL.'
        return 1
    }
    Write-Host 'RESULT: MANUAL INSPECTION REQUIRED.'
    if ($Ctx.CreateState -ceq 'unknown') {
        Write-Host 'Whether this run created C:\Tether is UNKNOWN (the run ended around CreateDirectoryW). Treat it as possibly created.'
    }
    if ($mayExist) {
        Write-Host 'Nothing was deleted. C:\Tether may exist and may not be the directory this run asked for'
        Write-Host '(a substituted or altered object is possible). Do not delete, repair, take ownership of,'
        Write-Host 're-ACL or use it without separate approval.'
    }
    return 3
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
if ($Preview) {
    Write-Host 'MODE: PREVIEW (creates nothing; compiles no helper; does not open C:\ or C:\Tether)'
    Write-Host 'Not offline: SID/account translation queries LSA and may contact a domain controller.'
} else {
    Write-Host 'MODE: EXECUTION (creates C:\Tether only after typed confirmation)'
}

if ($ExecutionContext.SessionState.LanguageMode -ne 'FullLanguage') {
    Stop-TetherBeforeNative @("PowerShell language mode is $($ExecutionContext.SessionState.LanguageMode). Stop and ask for an approved delivery method; do not bypass it.")
}

$inputProblems = @(Test-TetherRuntimeSidText $RuntimeSid) + @(Test-TetherAccountText $ExpectedRuntimeAccount)
if ($inputProblems.Count -gt 0) { Stop-TetherBeforeNative $inputProblems }

$currentIdentity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
try {
    $elevated = ([System.Security.Principal.WindowsPrincipal]::new($currentIdentity)).IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)
    $currentSid = ''
    if ($null -ne $currentIdentity.User) { $currentSid = $currentIdentity.User.Value }
    $currentName = $currentIdentity.Name
} finally {
    $currentIdentity.Dispose()
}
$operatorProblems = @(Test-TetherOperatorIdentity $currentSid $currentName)
if ($operatorProblems.Count -gt 0) { Stop-TetherBeforeNative $operatorProblems }

try {
    $sidObject = [System.Security.Principal.SecurityIdentifier]::new($RuntimeSid)
    $translated = $sidObject.Translate([System.Security.Principal.NTAccount]).Value
    $roundTrip = [System.Security.Principal.NTAccount]::new($ExpectedRuntimeAccount).Translate([System.Security.Principal.SecurityIdentifier]).Value
} catch {
    Stop-TetherBeforeNative @('RuntimeSid / ExpectedRuntimeAccount could not be translated: ' + (Format-TetherException $_))
}
$identityProblems = @()
if ($sidObject.Value -cne $RuntimeSid) { $identityProblems += 'RuntimeSid is not in canonical form.' }
if (-not [string]::Equals($translated, $ExpectedRuntimeAccount, [StringComparison]::Ordinal)) {
    $identityProblems += "RuntimeSid translates to '$translated', not exactly '$ExpectedRuntimeAccount'."
}
if ($roundTrip -cne $RuntimeSid) { $identityProblems += 'ExpectedRuntimeAccount does not translate back to RuntimeSid.' }
if ($RuntimeSid -ceq $currentSid) { $identityProblems += 'RuntimeSid is the account running this script; the runtime must be a separate ordinary account.' }
if ($identityProblems.Count -gt 0) { Stop-TetherBeforeNative $identityProblems }

$intendedSddl = Get-TetherIntendedSddl $RuntimeSid
$selfCheck = @(Test-TetherDescriptor ([System.Security.AccessControl.RawSecurityDescriptor]::new($intendedSddl)) $RuntimeSid)
if ($selfCheck.Count -gt 0) { Stop-TetherBeforeNative (@('internal descriptor self-check failed:') + $selfCheck) }

Show-TetherPlan $RuntimeSid $ExpectedRuntimeAccount $intendedSddl

if ($Preview) {
    Write-Host "Current session elevated: $elevated"
    Write-Host 'PREVIEW ONLY. Done: input syntax, operator account shape, SID <-> account translation'
    Write-Host '  (LSA query; may contact a domain controller), descriptor self-check.'
    Write-Host 'NOT done: helper compilation, opening C:\ or C:\Tether, target existence, account type'
    Write-Host '  (SidTypeUser), group membership (operator attestation only).'
    Write-Host 'Nothing was created.'
    exit 0
}

if (-not $elevated) {
    Stop-TetherBeforeNative @('This session is not elevated. Open Windows PowerShell with "Run as administrator" manually; this script never elevates itself.')
}
if ($Host.Name -cne 'ConsoleHost' -or [Console]::IsInputRedirected -or -not [Environment]::UserInteractive) {
    Stop-TetherBeforeNative @('An interactive Windows PowerShell console is required for the confirmation; non-interactive or redirected execution is refused.')
}

$answer = Read-Host "Type exactly  $TetherConfirmation  to continue (anything else stops)"
if (-not [string]::Equals($answer, $TetherConfirmation, [StringComparison]::Ordinal)) {
    Stop-TetherBeforeNative @('Not confirmed.')
}

if ($null -ne ('TetherPrepareV1.Native' -as [type])) {
    Stop-TetherBeforeNative @('Native helper type is already loaded in this session. Open a new elevated Windows PowerShell window for each run.')
}
try {
    Add-Type -TypeDefinition $TetherNativeSource -Language CSharp
} catch {
    Stop-TetherBeforeNative @('Add-Type failed (blocked or unavailable). Stop and ask for an approved delivery method: ' + (Format-TetherException $_))
}

$sidBytes = New-Object byte[] $sidObject.BinaryLength
$sidObject.GetBinaryForm($sidBytes, 0)
$nativeAccount = $null
try {
    $sidUse = [TetherPrepareV1.Native]::LookupSidUse($sidBytes, [ref]$nativeAccount)
} catch {
    Stop-TetherBeforeNative @('LookupAccountSidW failed: ' + (Format-TetherException $_))
}
if ($sidUse -ne 1) { Stop-TetherBeforeNative @("RuntimeSid is not a user account (SID_NAME_USE $sidUse).") }
if (-not [string]::Equals($nativeAccount, $ExpectedRuntimeAccount, [StringComparison]::Ordinal)) {
    Stop-TetherBeforeNative @("LookupAccountSidW returned '$nativeAccount', not exactly '$ExpectedRuntimeAccount'.")
}

$TetherExit = 3
$ctx = @{
    Sid = $RuntimeSid; Root = $null; Target = $null; RootEvidence = $null; TargetEvidence = $null
    CreateState = 'not-attempted'; Verified = $false; Failure = $null
}
try {
    Invoke-TetherCreate $ctx
} catch {
    if ($null -eq $ctx.Failure) { $ctx.Failure = Format-TetherException $_ }
} finally {
    $TetherExit = Complete-TetherRun $ctx
}
exit $TetherExit
