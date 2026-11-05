# Service-free binding and in-memory parser regression only.
# No SCM/file/token/security-setting mutation API. Add-Type uses compiler temp files.
[CmdletBinding()]
param([ValidateSet('Original', 'ExplicitNull')][string]$Mode = 'Original')
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ($PSVersionTable.PSEdition -ne 'Desktop' -or $PSVersionTable.PSVersion.Major -ne 5) {
    throw 'Exact Windows PowerShell 5.1 required; no compatibility inference.'
}
Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Security.AccessControl;

public sealed class DescriptorBindingResult {
    public bool WasClrNull, WasEmpty, Parsed, OwnerGroupCorrect, DaclProtected, AcePolicyCorrect;
    public int Win32Error, AceCount;
}
public sealed class DescriptorBindingProbe {
    [DllImport("advapi32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
    static extern bool ConvertStringSecurityDescriptorToSecurityDescriptor(string s, uint revision, out IntPtr descriptor, out uint size);
    [DllImport("kernel32.dll")] static extern IntPtr LocalFree(IntPtr value);
    // Same four-argument instance binding shape and descriptor-string builder.
    // Path/bytes are fixture placeholders; this method never creates a file.
    public DescriptorBindingResult NewFile(string unusedFixturePath, byte[] unusedFixtureBytes, string sid, int mask) {
        int full = 0x1f01ff;
        string sddl = "O:BAG:BAD:P(A;;0x" + full.ToString("x") + ";;;SY)(A;;0x" + full.ToString("x") + ";;;BA)";
        if (sid != null) sddl += "(A;;0x" + mask.ToString("x") + ";;;" + sid + ")";
        var result = new DescriptorBindingResult { WasClrNull = sid == null, WasEmpty = sid == "" };
        IntPtr descriptor = IntPtr.Zero; uint size;
        try {
            result.Parsed = ConvertStringSecurityDescriptorToSecurityDescriptor(sddl, 1, out descriptor, out size);
            result.Win32Error = result.Parsed ? 0 : Marshal.GetLastWin32Error();
            if (result.Parsed) {
                var sd = new RawSecurityDescriptor(sddl);
                result.AceCount = sd.DiscretionaryAcl.Count;
                result.OwnerGroupCorrect = sd.Owner.Value == "S-1-5-32-544" && sd.Group.Value == "S-1-5-32-544";
                result.DaclProtected = (sd.ControlFlags & ControlFlags.DiscretionaryAclProtected) != 0;
                result.AcePolicyCorrect = result.AceCount == (sid == null ? 2 : 3);
                for (int i = 0; i < result.AceCount; i++) {
                    var ace = sd.DiscretionaryAcl[i] as CommonAce;
                    string expected = i == 0 ? "S-1-5-18" : i == 1 ? "S-1-5-32-544" : sid;
                    int rights = i < 2 ? full : mask;
                    result.AcePolicyCorrect &= ace != null && !ace.IsCallback &&
                        ace.AceQualifier == AceQualifier.AccessAllowed && ace.AceFlags == AceFlags.None &&
                        ace.SecurityIdentifier.Value == expected && ace.AccessMask == rights;
                }
            }
            return result;
        } finally { if (descriptor != IntPtr.Zero) LocalFree(descriptor); }
    }
}
'@
# This fixture SID is synthetic; it is never resolved or used as authority.
$probe = [DescriptorBindingProbe]::new()
$control = $probe.NewFile('fixture-only', [byte[]]@(), 'S-1-5-80-1-2-3-4-5', 0x1200a9)
if (-not ($control.Parsed -and $control.OwnerGroupCorrect -and $control.DaclProtected -and $control.AcePolicyCorrect)) {
    throw 'Nonempty synthetic SID control failed; hypothesis not isolated.'
}
$writeControl = $probe.NewFile('fixture-only', [byte[]]@(), 'S-1-5-80-1-2-3-4-5', 0x120116)
if (-not ($writeControl.Parsed -and $writeControl.OwnerGroupCorrect -and $writeControl.DaclProtected -and $writeControl.AcePolicyCorrect)) {
    throw 'Synthetic SID write-mask control failed; hypothesis not isolated.'
}
if ($Mode -eq 'Original') {
    $result = $probe.NewFile('fixture-only', [byte[]]@(), $null, 0)
} else {
    $result = $probe.NewFile('fixture-only', [byte[]]@(), [System.Management.Automation.Language.NullString]::Value, 0)
}
[ordered]@{
    scope = 'STRING_BINDING_AND_IN_MEMORY_SDDL_PARSE_ONLY'
    mode = $Mode
    psVersion = $PSVersionTable.PSVersion.ToString()
    clrNull = $result.WasClrNull
    emptyString = $result.WasEmpty
    parsed = $result.Parsed
    win32Error = $result.Win32Error
    aceCount = $result.AceCount
    ownerGroupCorrect = $result.OwnerGroupCorrect
    daclProtected = $result.DaclProtected
    exactAcePolicy = $result.AcePolicyCorrect
    serviceOperations = $false
} | ConvertTo-Json -Compress
if ($Mode -eq 'Original') {
    if ($result.WasClrNull -or -not $result.WasEmpty -or $result.Parsed -or $result.Win32Error -eq 0) {
        throw 'Original binding did not reproduce the empty-trustee native parse failure.'
    }
    throw 'EXPECTED_ORIGINAL_EMPTY_TRUSTEE_PARSE_FAILURE'
}
if (-not ($result.WasClrNull -and $result.Parsed -and $result.AceCount -eq 2 -and
          $result.OwnerGroupCorrect -and $result.DaclProtected -and $result.AcePolicyCorrect)) {
    throw 'Private descriptor contract not satisfied; expected red for Original binding.'
}
