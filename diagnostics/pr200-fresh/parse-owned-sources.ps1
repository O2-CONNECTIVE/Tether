$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
try {
    foreach ($path in @((Join-Path $PSScriptRoot 'owned-capture.ps1'), (Join-Path $PSScriptRoot 'owned-capture.test.ps1'), (Join-Path $env:OWNED_OVERLAY_DIRECTORY 'supervise.ps1'))) {
        $tokens = $null; $errors = $null
        [System.Management.Automation.Language.Parser]::ParseFile($path, [ref]$tokens, [ref]$errors) | Out-Null
        if ($errors.Count -ne 0) { [Console]::Out.WriteLine('OWNED_CAPTURE_PARSE_REJECTED'); exit 1 }
    }
    [Console]::Out.WriteLine('OWNED_CAPTURE_PARSE_PASS files=3')
} catch { [Console]::Out.WriteLine('OWNED_CAPTURE_PARSE_REJECTED'); exit 1 }
