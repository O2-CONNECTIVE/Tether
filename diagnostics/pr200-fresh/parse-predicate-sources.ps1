# Parse only; no script is dot-sourced or executed here.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
foreach ($name in @('observe-profile.ps1', 'observer-detail.ps1', 'observer-reasons.test.ps1', 'observer-detail.test.ps1', 'audit.ps1')) {
    $tokens = $null; $parseErrors = $null
    [System.Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot $name), [ref]$tokens, [ref]$parseErrors) | Out-Null
    if ($parseErrors.Count -ne 0) { throw 'PREDICATE_SOURCE_PARSE_REJECTED' }
}
Write-Output 'POWERSHELL_PREDICATE_PARSE_PASS files=5'
