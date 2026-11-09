param([string]$SourceDirectory = $PSScriptRoot)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $SourceDirectory 'processes.ps1')
# Synthetic rows only. No OS process query, app launch, stop, or real handle operation.
function Row([int]$Id, [int]$ParentId, [string]$Name, [double]$Seconds) {
    return [pscustomobject]@{ ProcessId = $Id; ParentProcessId = $ParentId; Name = $Name;
        ExecutablePath = $Name; CreationDate = [DateTime]::Parse('2026-11-03T00:00:00Z').ToUniversalTime().AddSeconds($Seconds) }
}
$script:checks = 0
function Equal($Actual, $Expected) {
    if ($Actual -cne $Expected) { throw 'BASELINE_FIXTURE_ASSERTION_FAILED' }
    $script:checks += 1
}
$current = Row 14 13 'msedgewebview2.exe' 205
$earlier = Row 14 1 'old-parent.exe' 50
$child = Row 15 14 'PRIVATE_CHILD.exe' 0
$child.CreationDate = $current.CreationDate.AddTicks(-1001328070)
Equal (Get-LineageBaselineIdentity $current $null) 'not-recorded'
Equal (Get-LineageBaselineIdentity $current @()) 'absent'
Equal (Get-LineageBaselineIdentity $current @($current)) 'same'
Equal (Get-LineageBaselineIdentity $current @($earlier)) 'different'
Equal (Get-LineageBaselineIdentity $current @($current, $earlier)) 'ambiguous'
$invalid = Row 14 1 'old-parent.exe' 50
$invalid.CreationDate = $null
Equal (Get-LineageBaselineIdentity $current @($invalid)) 'unavailable'
Equal (Get-LineageBaselineIdentity $invalid @($current)) 'unavailable'
Equal (Get-LineageBaselineIdentity $current (@($earlier) * 4097)) 'unavailable'

$driver = Row 10 1 'driver.exe' 200
$edge = Row 11 10 'edge.exe' 201
$app = Row 12 11 'app.exe' 202
$webview = Row 13 12 'msedgewebview2.exe' 203
$graph = @($driver, $edge, $app, $webview, $current, $child)
$baseline = @($earlier, $child)
$facts = @()
$caught = 'none'
try { Get-OwnedApp $driver $edge 'app.exe' $graph ([ref]$facts) $baseline | Out-Null }
catch { $caught = $_.Exception.Message }
Equal $caught 'NATIVE_LINEAGE_OWNER_UNCONFIRMED'
Equal $facts.Count 1
Equal $facts[0].reason 'child-older'
Equal $facts[0].parentDepth 4
Equal $facts[0].parentRole 'webview-name'
Equal $facts[0].childMinusParentCreationMs -100132.807
Equal $facts[0].parentBeforeDriverIdentity 'different'
Equal $facts[0].childBeforeDriverIdentity 'same'
Equal $facts[0].parentSnapshotIdentity 'same'
Equal $facts[0].childSnapshotIdentity 'same'
$json = $facts | ConvertTo-Json -Depth 6
if ($json.Contains('PRIVATE_') -or $json.Contains('2026-11-03') -or $json.Contains('ProcessId')) { throw 'BASELINE_FIXTURE_PRIVATE_DATA_LEAK' }
$checks += 1

# Missing baseline cannot turn the same uncertainty into success.
$facts = @(); $caught = 'none'
try { Get-OwnedApp $driver $edge 'app.exe' $graph ([ref]$facts) | Out-Null }
catch { $caught = $_.Exception.Message }
Equal $caught 'NATIVE_LINEAGE_OWNER_UNCONFIRMED'
Equal $facts[0].childBeforeDriverIdentity 'not-recorded'

# No new filter of legitimate non-WebView children is introduced.
$child.CreationDate = $current.CreationDate.AddSeconds(1)
$observed = Get-OwnedApp $driver $edge 'app.exe' $graph ([ref]$facts) $baseline
Equal $observed.App.ProcessId 12
Equal $observed.Children.Count 3
Equal @($observed.Children | Where-Object Name -eq 'PRIVATE_CHILD.exe').Count 1
if ($checks -ne 24) { throw 'BASELINE_FIXTURE_COUNT_MISMATCH' }
Write-Output "LINEAGE_BASELINE_FIXTURE_PASS checks=$checks (synthetic only; no Windows root cause proof)"

# Release failure shape: other/other at depth4 with an earlier child and absent parent.
$current.Name = 'PRIVATE_PARENT.exe'
$current.ExecutablePath = 'PRIVATE_PARENT.exe'
$child.CreationDate = $current.CreationDate.AddTicks(-2998987230)
$facts = @(); $caught = 'none'
try { Get-OwnedApp $driver $edge 'app.exe' $graph ([ref]$facts) @($child) | Out-Null }
catch { $caught = $_.Exception.Message }
Equal $caught 'NATIVE_LINEAGE_OWNER_UNCONFIRMED'
Equal $facts.Count 1
Equal $facts[0].parentDepth 4
Equal $facts[0].parentRole 'other'
Equal $facts[0].childRole 'other'
Equal $facts[0].childMinusParentCreationMs -299898.723
Equal $facts[0].parentBeforeDriverIdentity 'absent'
Equal $facts[0].childBeforeDriverIdentity 'same'
Equal $facts[0].parentSnapshotIdentity 'same'
Equal $facts[0].childSnapshotIdentity 'same'
$json = $facts | ConvertTo-Json -Depth 6
if ($json.Contains('PRIVATE_') -or $json.Contains('2026-11-03') -or $json.Contains('ProcessId')) { throw 'BASELINE_FIXTURE_PRIVATE_DATA_LEAK' }
$checks += 1
if ($checks -ne 35) { throw 'BASELINE_FIXTURE_COUNT_MISMATCH' }
Write-Output "LINEAGE_BASELINE_RELEASE_FIXTURE_PASS checks=11 total=$checks (synthetic only; no Windows root cause proof)"

# Separate objects model separate snapshots. Never mutate the current row through
# a shared baseline reference. Only the anonymous relation changes, not the verdict.
$edgeCurrent = Row 25 24 'PRIVATE_EDGE_CHILD.exe' 0
$edgeBefore = Row 25 24 'PRIVATE_EDGE_CHILD.exe' 0
Equal (Get-LineageBaselineParentRelation $edgeCurrent @($edgeBefore)) 'same'
$edgeBefore.ParentProcessId = 99
Equal (Get-LineageBaselineParentRelation $edgeCurrent @($edgeBefore)) 'different'
Equal $edgeCurrent.ParentProcessId 24
$edgeBefore.CreationDate = $edgeBefore.CreationDate.AddSeconds(1)
Equal (Get-LineageBaselineParentRelation $edgeCurrent @($edgeBefore)) 'unavailable'
$edgeBefore.CreationDate = $edgeCurrent.CreationDate
Equal (Get-LineageBaselineParentRelation $edgeCurrent @($edgeBefore, $edgeBefore)) 'ambiguous'
Equal (Get-LineageBaselineParentRelation $edgeCurrent @()) 'unavailable'
Equal (Get-LineageBaselineParentRelation $edgeCurrent $null) 'unavailable'
Equal (Get-LineageBaselineParentRelation $edgeCurrent (@($edgeBefore) * 4097)) 'unavailable'
foreach ($bad in @($null, -1, 4294967296L, '24', $true, 24.5)) {
    $edgeBefore.ParentProcessId = $bad
    Equal (Get-LineageBaselineParentRelation $edgeCurrent @($edgeBefore)) 'unavailable'
}
$edgeBefore.PSObject.Properties.Remove('ParentProcessId')
Equal (Get-LineageBaselineParentRelation $edgeCurrent @($edgeBefore)) 'unavailable'
$edgeBefore = Row 25 24 'PRIVATE_EDGE_CHILD.exe' 0
$edgeCurrent.ParentProcessId = '24'
Equal (Get-LineageBaselineParentRelation $edgeCurrent @($edgeBefore)) 'unavailable'
$edgeCurrent.ParentProcessId = 24
$edgeCurrent.PSObject.Properties.Remove('ParentProcessId')
Equal (Get-LineageBaselineParentRelation $edgeCurrent @($edgeBefore)) 'unavailable'

# Exercise the real rejection boundary, once for each meaningful old edge.
# The old child matches PID+CreationDate; candidate parent is absent from baseline.
foreach ($oldParent in @(14, 99)) {
    $oldChild = Row 15 $oldParent 'PRIVATE_CHILD.exe' 0
    $oldChild.CreationDate = $child.CreationDate
    $facts = @(); $caught = 'none'
    try { Get-OwnedApp $driver $edge 'app.exe' $graph ([ref]$facts) @($oldChild) | Out-Null }
    catch { $caught = $_.Exception.Message }
    Equal $caught 'NATIVE_LINEAGE_OWNER_UNCONFIRMED'
    Equal $facts.Count 1
    Equal $facts[0].reason 'child-older'
    Equal $facts[0].parentBeforeDriverIdentity 'absent'
    Equal $facts[0].childBeforeDriverIdentity 'same'
    Equal $facts[0].childBaselineParentPidRelation $(if ($oldParent -eq 14) { 'same' } else { 'different' })
    $json = $facts | ConvertTo-Json -Depth 6
    if ($json.Contains('PRIVATE_') -or $json.Contains('2026-11-03') -or $json.Contains('ProcessId')) { throw 'BASELINE_FIXTURE_PRIVATE_DATA_LEAK' }
    $checks += 1
}
if ($checks -ne 66) { throw 'BASELINE_FIXTURE_COUNT_MISMATCH' }
Write-Output 'LINEAGE_BASELINE_PARENT_EDGE_FIXTURE_PASS checks=31 total=66 (synthetic only; no ancestry proof)'
