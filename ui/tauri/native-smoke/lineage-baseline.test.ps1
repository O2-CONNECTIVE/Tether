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
function Equal($Actual, $Expected, [string]$Code = 'BASELINE_FIXTURE_ASSERTION_FAILED') {
    if ($Actual -cne $Expected) { throw $Code }
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

# #365 d168a6f: driver起動前から居たotherの子の親PIDを、後から作られたWebViewが再利用した形。
# 起動前のsnapshotに同じ身元・同じ親PIDで載り、親はabsentなので非所有と確定する。時刻の前後は根拠にしない。
$reuseParent = Row 14 13 'msedgewebview2.exe' 205
$reuseChild = Row 15 14 'PRIVATE_CHILD.exe' 0
$reuseChild.CreationDate = $reuseParent.CreationDate.AddTicks(-2149276180)
$reuseGrandchild = Row 16 15 'PRIVATE_GRANDCHILD.exe' 206
$reuseBefore = Row 15 14 'PRIVATE_CHILD.exe' 0
$reuseBefore.CreationDate = $reuseChild.CreationDate
$reuseGraph = @($driver, $edge, $app, $webview, $reuseParent, $reuseChild, $reuseGrandchild)
$facts = @(); $caught = 'none'; $observed = $null
try { $observed = Get-OwnedApp $driver $edge 'app.exe' $reuseGraph ([ref]$facts) @($reuseBefore) }
catch { $caught = $_.Exception.Message }
Equal $caught 'none' 'PREEXISTING_CHILD_NOT_EXCLUDED'
Equal (@($observed.Children | ForEach-Object ProcessId) -join ',') '13,14' 'PREEXISTING_CHILD_OR_SUBTREE_CLAIMED'
Equal $facts.Count 2 'PREEXISTING_EXCLUSION_NOT_RECORDED'
Equal "$($facts[0].reason)/$($facts[0].searchRoot)/$($facts[0].parentDepth)/$($facts[1].searchRoot)/$($facts[1].parentDepth)" 'excluded-preexisting-child/driver/4/app/2'
Equal $facts[0].parentRole 'webview-name'
Equal $facts[0].childMinusParentCreationMs -214927.618
Equal "$($facts[0].parentBeforeDriverIdentity)/$($facts[0].childBeforeDriverIdentity)/$($facts[0].childBaselineParentPidRelation)" 'absent/same/same'
$json = $facts | ConvertTo-Json -Depth 6
if ($json.Contains('PRIVATE_') -or $json.Contains('2026-11-03') -or $json.Contains('ProcessId')) { throw 'BASELINE_FIXTURE_PRIVATE_DATA_LEAK' }
$checks += 1
# 時計の向きによらない。基準で説明できる子は、親より新しく見えても所有にしない。
$reuseChild.CreationDate = $reuseParent.CreationDate.AddSeconds(1)
$reuseBefore.CreationDate = $reuseChild.CreationDate
$observed = Get-OwnedApp $driver $edge 'app.exe' $reuseGraph ([ref]$facts) @($reuseBefore)
Equal (@($observed.Children | ForEach-Object ProcessId) -join ',') '13,14' 'PREEXISTING_NEWER_CHILD_CLAIMED'
# 逆向き: 条件を一つでも欠く子は基準で説明しない。逆順なら従来どおり止め、除外で清掃を漏らさない。
$reuseChild.CreationDate = $reuseParent.CreationDate.AddTicks(-2149276180)
$reuseBefore.CreationDate = $reuseChild.CreationDate.AddSeconds(-1)
$facts = @(); $caught = 'none'
try { Get-OwnedApp $driver $edge 'app.exe' $reuseGraph ([ref]$facts) @($reuseBefore) | Out-Null }
catch { $caught = $_.Exception.Message }
Equal $caught 'NATIVE_LINEAGE_OWNER_UNCONFIRMED' 'REUSED_PID_GENERATION_EXCLUDED'
Equal "$($facts[0].reason)/$($facts[0].childBeforeDriverIdentity)" 'child-older/different'
# 親も起動前から居たなら、親が自分の辺で先に除外され、その下は探索しない。
$reuseBefore.CreationDate = $reuseChild.CreationDate
$facts = @(); $caught = 'none'; $observed = $null
try { $observed = Get-OwnedApp $driver $edge 'app.exe' $reuseGraph ([ref]$facts) @($reuseBefore, (Row 14 13 'msedgewebview2.exe' 205)) }
catch { $caught = $_.Exception.Message }
Equal "$caught/$($facts.Count)/$($facts[0].reason)/$($facts[0].parentDepth)" 'none/2/excluded-preexisting-child/3' 'PREEXISTING_PARENT_NOT_EXCLUDED'
Equal (@($observed.Children | ForEach-Object ProcessId) -join ',') '13' 'PREEXISTING_PARENT_CLAIMED'
$facts = @(); $caught = 'none'
try { Get-OwnedApp $driver $edge 'app.exe' $reuseGraph ([ref]$facts) @($reuseBefore, $reuseBefore) | Out-Null }
catch { $caught = $_.Exception.Message }
Equal $caught 'NATIVE_LINEAGE_OWNER_UNCONFIRMED' 'AMBIGUOUS_BASELINE_EXCLUDED'
Equal "$($facts[0].reason)/$($facts[0].childBaselineParentPidRelation)" 'child-older/ambiguous'
if ($checks -ne 23) { throw 'BASELINE_FIXTURE_COUNT_MISMATCH' }

# 共有の行は、起動前にも同じ子が同じ親PIDで居たことを表す。現在の親はdifferentなので除外する。
$graph = @($driver, $edge, $app, $webview, $current, $child)
$baseline = @($earlier, $child)
$facts = @()
$caught = 'none'
try { Get-OwnedApp $driver $edge 'app.exe' $graph ([ref]$facts) $baseline | Out-Null }
catch { $caught = $_.Exception.Message }
Equal $caught 'none' 'PREEXISTING_CHILD_NOT_EXCLUDED'
Equal $facts.Count 2
Equal $facts[0].reason 'excluded-preexisting-child'
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
# A new child that reuses an old PID with the same parent PID is a different identity and stays owned.
$child.CreationDate = $current.CreationDate.AddSeconds(1)
$observed = Get-OwnedApp $driver $edge 'app.exe' $graph ([ref]$facts) @($earlier, (Row 15 14 'PRIVATE_CHILD.exe' 0))
Equal $observed.App.ProcessId 12
Equal $observed.Children.Count 3 'NEW_CHILD_WITH_REUSED_PID_EXCLUDED'
Equal @($observed.Children | Where-Object Name -eq 'PRIVATE_CHILD.exe').Count 1
if ($checks -ne 39) { throw 'BASELINE_FIXTURE_COUNT_MISMATCH' }
Write-Output "LINEAGE_BASELINE_FIXTURE_PASS checks=$checks (synthetic only; no Windows root cause proof)"

# Release failure shape: other/other at depth4 with an earlier child and absent parent.
# The shared row also records the same parent PID, so the child is excluded as preexisting.
$current.Name = 'PRIVATE_PARENT.exe'
$current.ExecutablePath = 'PRIVATE_PARENT.exe'
$child.CreationDate = $current.CreationDate.AddTicks(-2998987230)
$facts = @(); $caught = 'none'
try { Get-OwnedApp $driver $edge 'app.exe' $graph ([ref]$facts) @($child) | Out-Null }
catch { $caught = $_.Exception.Message }
Equal $caught 'none' 'PREEXISTING_CHILD_NOT_EXCLUDED'
Equal $facts.Count 2
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
if ($checks -ne 50) { throw 'BASELINE_FIXTURE_COUNT_MISMATCH' }
Write-Output "LINEAGE_BASELINE_RELEASE_FIXTURE_PASS checks=11 total=$checks (synthetic only; no Windows root cause proof)"

# Separate objects model separate snapshots. Never mutate the current row through
# a shared baseline reference. Only 'same' lets Test-PreexistingChild exclude a child.
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

# Exercise the real boundary, once for each meaningful old edge.
# The old child matches PID+CreationDate; candidate parent is absent from baseline.
# The same old parent PID proves a reused PID and excludes; another one stays an unexplained stop.
foreach ($oldParent in @(14, 99)) {
    $sameEdge = $oldParent -eq 14
    $oldChild = Row 15 $oldParent 'PRIVATE_CHILD.exe' 0
    $oldChild.CreationDate = $child.CreationDate
    $facts = @(); $caught = 'none'
    try { Get-OwnedApp $driver $edge 'app.exe' $graph ([ref]$facts) @($oldChild) | Out-Null }
    catch { $caught = $_.Exception.Message }
    Equal $caught $(if ($sameEdge) { 'none' } else { 'NATIVE_LINEAGE_OWNER_UNCONFIRMED' }) 'OLD_EDGE_VERDICT_WRONG'
    Equal $facts.Count $(if ($sameEdge) { 2 } else { 1 })
    Equal $facts[0].reason $(if ($sameEdge) { 'excluded-preexisting-child' } else { 'child-older' })
    Equal $facts[0].parentBeforeDriverIdentity 'absent'
    Equal $facts[0].childBeforeDriverIdentity 'same'
    Equal $facts[0].childBaselineParentPidRelation $(if ($sameEdge) { 'same' } else { 'different' })
    $json = $facts | ConvertTo-Json -Depth 6
    if ($json.Contains('PRIVATE_') -or $json.Contains('2026-11-03') -or $json.Contains('ProcessId')) { throw 'BASELINE_FIXTURE_PRIVATE_DATA_LEAK' }
    $checks += 1
}
if ($checks -ne 81) { throw 'BASELINE_FIXTURE_COUNT_MISMATCH' }
Write-Output 'LINEAGE_BASELINE_PARENT_EDGE_FIXTURE_PASS checks=31 total=81 (synthetic only; no ancestry proof)'
