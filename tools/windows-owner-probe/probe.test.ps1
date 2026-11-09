$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $IsWindows -or -not [Environment]::Is64BitProcess) { throw 'OWNER_PROBE_WINDOWS_X64_REQUIRED' }
Add-Type -Path @((Join-Path $PSScriptRoot 'Native.cs'), (Join-Path $PSScriptRoot 'Probe.cs'))
$cases = @('normal-exit', 'assignment-rejected', 'cancel-before-resume')
$passed = 0
foreach ($case in $cases) {
    $result = [Tether.OwnerProbe.Probe]::Run($case)
    if ($result -cne "${case}:passed") { throw 'OWNER_PROBE_CASE_FAILED' }
    $passed += 1
    Write-Output $result
}
if ($passed -ne 3) { throw 'OWNER_PROBE_INCOMPLETE_TESTS' }
# No PID, path, absolute time, command line, raw OS error, or production success claim.
Write-Output 'OWNER_PROBE_FIXED_COMMAND_CASES_PASSED_3'
# Independent source-built executable retains child handles in the living fixture parent.
& (Join-Path $PSScriptRoot 'descendant.test.ps1')
