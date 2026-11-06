# Parse fixed source only; never dot-source a native/OS script.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
try {
    $source = Join-Path $PSScriptRoot '../../../fixture-source'
    foreach ($relative in @('diagnostics/pr200-fresh/observer-poll.test.ps1',
        'diagnostics/pr200-fresh/observe-profile.ps1', 'diagnostics/pr200-fresh/observer-detail.ps1',
        'ui/tauri/native-smoke/processes.ps1')) {
        $tokens = $null; $errors = $null
        [System.Management.Automation.Language.Parser]::ParseFile((Join-Path $source $relative), [ref]$tokens, [ref]$errors) | Out-Null
        if ($errors.Count -ne 0) { [Console]::Out.WriteLine('POLL_PARSER_REJECTED'); exit 1 }
    }
    [Console]::Out.WriteLine('POLL_PARSER_PASS files=4')
} catch { [Console]::Out.WriteLine('POLL_PARSER_REJECTED'); exit 1 }
