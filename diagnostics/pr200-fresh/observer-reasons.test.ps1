# Synthetic, source-only ledger seam. Does not invoke CIM, native argv, or the supervisor.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$tokens = $null; $parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot 'observe-profile.ps1'), [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -ne 0) { throw 'OBSERVER_SOURCE_SYNTAX_FAILED' }
$functions = @($ast.FindAll({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -in @('New-ObserverFailures', 'Add-ObserverFailure') }, $true))
if ($functions.Count -ne 2) { throw 'OBSERVER_LEDGER_SEAM_MISSING' }
foreach ($definition in $functions) { . ([scriptblock]::Create($definition.Extent.Text)) }
$ledger = New-ObserverFailures
if ($ledger.failureCount -ne 0 -or $ledger.sites.Count -ne 0 -or $ledger.saturated -or $ledger.truncated) { throw 'LEDGER_INITIAL_STATE_FAILED' }
if (@(Add-ObserverFailure $ledger 'snapshot-query').Count -ne 0) { throw 'LEDGER_UNEXPECTED_OUTPUT' }
Add-ObserverFailure $ledger 'snapshot-query'
if ($ledger.failureCount -ne 2 -or $ledger.sites.Count -ne 1) { throw 'LEDGER_DUPLICATE_SITE_FAILED' }
Add-ObserverFailure $ledger 'PRIVATE_EXCEPTION_OR_PATH'
if ($ledger.sites[-1] -cne 'unclassified-site' -or ($ledger | ConvertTo-Json).Contains('PRIVATE_')) { throw 'LEDGER_REDACTION_FAILED' }
$ledger = New-ObserverFailures
for ($index = 0; $index -lt 5000; $index += 1) { Add-ObserverFailure $ledger 'snapshot-query' }
if ($ledger.failureCount -ne 4096 -or -not $ledger.saturated -or $ledger.sites.Count -ne 1) { throw 'LEDGER_SATURATION_FAILED' }
$ledger = New-ObserverFailures
foreach ($site in @('platform-check', 'profile-path-validation', 'prelaunch-existence', 'owned-parent-path',
    'owned-parent-reparse', 'prelaunch-validation', 'baseline-snapshot', 'ready-marker-write', 'snapshot-query',
    'snapshot-limit', 'candidate-selection', 'candidate-count', 'driver-count', 'lineage-query', 'webview-metadata',
    'row-limit', 'process-handle-open')) { Add-ObserverFailure $ledger $site }
if ($ledger.failureCount -ne 17 -or $ledger.sites.Count -ne 16 -or -not $ledger.truncated) { throw 'LEDGER_SITE_BOUND_FAILED' }
Write-Output 'OBSERVER_LEDGER_SYNTHETIC_PASS checks=5'
