$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. "$PSScriptRoot/processes.ps1"
function Entry($Number, $Parent, $Path, $Name) {
    return [pscustomobject]@{ ProcessId = $Number; ParentProcessId = $Parent; ExecutablePath = $Path; Name = $Name; CreationDate = [DateTime]::Parse('2026-10-27T00:00:00Z').ToUniversalTime() }
}
$driver = Entry 10 1 'driver.exe' 'driver.exe'
$edge = Entry 11 10 'edge.exe' 'edge.exe'
$app = Entry 12 11 'app.exe' 'app.exe'
$webview = Entry 13 12 'webview.exe' 'msedgewebview2.exe'
$other = Entry 14 1 'app.exe' 'app.exe'
$valid = @($driver, $edge, $app, $webview, $other)
$observed = Get-OwnedApp $driver $edge 'app.exe' $valid
if ($observed.App.ProcessId -ne 12 -or $observed.Webviews.Count -ne 1) { throw 'PROCESS_CONTRACT_POSITIVE_FAILED' }
$script:checks = 1
function Reject($Body, [string]$Expected) {
    $rejected = $false
    try { & $Body | Out-Null } catch {
        if ($_.Exception.Message -ne $Expected) { throw }
        $rejected = $true
    }
    if (-not $rejected) { throw 'PROCESS_CONTRACT_FALSE_SUCCESS' }
    $script:checks += 1
}
Reject { Get-OwnedApp $driver $edge 'wrong.exe' $valid } 'NATIVE_APP_OWNERSHIP_AMBIGUOUS'
Reject { Get-OwnedApp $driver $edge 'app.exe' @($driver, $edge, $app) } 'NATIVE_WEBVIEW_CHILD_NOT_OBSERVED'
Reject { Get-OwnedApp $driver $edge 'app.exe' @($edge, $app, $webview) } 'NATIVE_DRIVER_EXITED_OR_IDENTITY_CHANGED'
$reused = Entry 10 1 'driver.exe' 'driver.exe'
$reused.CreationDate = 'different-creation'
Reject { Get-OwnedApp $driver $edge 'app.exe' @($reused, $edge, $app, $webview) } 'NATIVE_DRIVER_EXITED_OR_IDENTITY_CHANGED'
$fake = [pscustomobject]@{ ExitCode = 0; Id = 12; Path = 'app.exe'; HasExited = $false; SafeHandle = 1; StartTime = $app.CreationDate }
$fake | Add-Member ScriptMethod CloseMainWindow { return $true }
$fake | Add-Member ScriptMethod WaitForExit { param($TimeoutMs) return $false }
Reject { Request-NormalClose $app $fake 1 } 'NATIVE_APP_CLOSE_TIMEOUT'
$fake.StartTime = $app.CreationDate.AddSeconds(1)
Reject { Request-NormalClose $app $fake 1 } 'NATIVE_CLOSE_TARGET_IDENTITY_MISMATCH'
$fake.StartTime = $app.CreationDate
$fake.Path = 'unowned.exe'
Reject { Request-NormalClose $app $fake 1 } 'NATIVE_CLOSE_TARGET_IDENTITY_MISMATCH'
$loop4 = [pscustomobject]@{ LocalPort = 4444; LocalAddress = '127.0.0.1'; OwningProcess = 10 }
$edge4 = [pscustomobject]@{ LocalPort = 4445; LocalAddress = '127.0.0.1'; OwningProcess = 11 }
$edge6 = [pscustomobject]@{ LocalPort = 4445; LocalAddress = '::1'; OwningProcess = 11 }
Assert-ListenerSnapshot @($loop4, $edge4, $edge6) 10 11
$checks += 1
$wildcard = [pscustomobject]@{ LocalPort = 4445; LocalAddress = '0.0.0.0'; OwningProcess = 11 }
Reject { Assert-ListenerSnapshot @($loop4, $wildcard) 10 11 } 'NATIVE_LISTENER_OWNERSHIP_MISMATCH'
$unowned = [pscustomobject]@{ LocalPort = 4445; LocalAddress = '::1'; OwningProcess = 99 }
Reject { Assert-ListenerSnapshot @($loop4, $unowned) 10 11 } 'NATIVE_LISTENER_OWNERSHIP_MISMATCH'
Reject { Assert-ListenerSnapshot @($loop4) 10 11 } 'NATIVE_LISTENER_OWNERSHIP_MISMATCH'
if ($checks -ne 12) { throw 'PROCESS_CONTRACT_ZERO_OR_INCOMPLETE_TESTS' }
Write-Output "PROCESS_CONTRACT_PASS checks=$checks (fixtures only; not native evidence)"

# 終了の合否を変えず、OS観測の不一致を保持する公開harness境界。
. "$PSScriptRoot/close-observation.ps1"
$handle = [pscustomobject]@{ Slot = 1; Identity = 'verified'; Process = [pscustomobject]@{ HasExited = $false } }
$facts = Get-ChildCloseFacts @($webview) $app $valid @($handle)
if ($facts.observed -ne 1 -or $facts.remainingSnapshot -ne 1 -or $facts.rows[0].snapshotState -ne 'same-identity' -or $facts.rows[0].handleState -ne 'live') { throw 'CLOSE_FACTS_LIVE_NOT_RECORDED' }
$handle.Process.HasExited = $true
$facts = Get-ChildCloseFacts @($webview) $app $valid @($handle)
if ($facts.rows[0].snapshotState -ne 'same-identity' -or $facts.rows[0].handleState -ne 'exited') { throw 'CLOSE_FACTS_DISAGREEMENT_LOST' }
if ($facts.PSObject.Properties.Name -contains 'normalCloseVerified') { throw 'CLOSE_FACTS_FALSE_VERDICT' }
$handle.Identity = 'unavailable'
$facts = Get-ChildCloseFacts @($webview) $app $valid @($handle)
if ($facts.rows[0].handleState -ne 'unavailable' -or $facts.remainingSnapshot -ne 1) { throw 'CLOSE_FACTS_UNAVAILABLE_WAS_SUCCESS' }
$old = Entry 15 12 'PRIVATE_CHILD_PATH.exe' 'other.exe'
$old.CreationDate = $app.CreationDate.AddSeconds(-1)
$facts = Get-ChildCloseFacts @($old) $app @($old) @()
if ($facts.observed -ne 1 -or $facts.remainingSnapshot -ne 1 -or $facts.rows[0].parentOrder -ne 'child-older' -or $facts.rows[0].role -ne 'other') { throw 'CLOSE_FACTS_CHILD_WAS_FILTERED' }
$json = $facts | ConvertTo-Json -Depth 5
if ($json.Contains('PRIVATE_CHILD_PATH') -or $json.Contains('2026-10-27') -or $json.Contains('ProcessId')) { throw 'CLOSE_FACTS_PRIVATE_METADATA_LEAK' }
Write-Output 'CLOSE_OBSERVATION_CONTRACT_PASS checks=5 (fixtures only; not native evidence)'

# cleanupのOS操作が失敗しても、元FAILの保存と所有handle解放を試す。
$cleanupFolder = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $cleanupFolder -ErrorAction Stop | Out-Null
try {
    $broken = [pscustomobject]@{ HasExited = $false }
    $broken | Add-Member ScriptMethod Kill { throw 'PRIVATE_CLEANUP_FAILURE' }
    $broken | Add-Member ScriptMethod WaitForExit { param($Ms) throw 'PRIVATE_WAIT_FAILURE' }
    $disposable = [pscustomobject]@{ Disposed = $false }
    $disposable | Add-Member ScriptMethod Dispose { $this.Disposed = $true }
    $cleanupReport = [ordered]@{ normalCloseVerified = $false; nativeTestsRun = 1; nativeTestsPassed = 0 }
    Complete-NativeCleanup $broken $broken @() $app @([pscustomobject]@{ Process = $disposable }) $cleanupReport $cleanupFolder
    $saved = Get-Content (Join-Path $cleanupFolder 'native-result.json') -Raw | ConvertFrom-Json
    if (-not $disposable.Disposed -or $saved.normalCloseVerified -or $saved.nativeTestsPassed -ne 0 -or $saved.cleanupFailures -lt 1) { throw 'CLEANUP_FAILURE_LOST_VERDICT_OR_HANDLE' }
    if (($saved | ConvertTo-Json -Depth 6).Contains('PRIVATE_')) { throw 'CLEANUP_PRIVATE_EXCEPTION_LEAK' }
} finally { Remove-Item -LiteralPath $cleanupFolder -Recurse -Force }
Write-Output 'CLEANUP_FAILURE_CONTRACT_PASS checks=2 (fixtures only; not native evidence)'

$profileHandle = [pscustomobject]@{ Slot = 1; Identity = 'verified'; Process = [pscustomobject]@{ HasExited = $false } }
$webview | Add-Member NoteProperty CommandLine 'fixture --type="renderer"suffix --user-data-dir="SAFE_PROFILE"suffix PRIVATE_ARGUMENT_DETAIL' -Force
$profileFacts = Get-ChildCloseFacts @($webview) $app $valid @($profileHandle) $null 'SAFE_PROFILE'
if ($profileFacts.rows[0].profileMatch -eq $true -or $profileFacts.rows[0].webviewType -eq 'renderer') { throw 'CLOSE_FACTS_QUOTED_SUFFIX_FALSE_MATCH' }
if (($profileFacts | ConvertTo-Json -Depth 5).Contains('PRIVATE_ARGUMENT_DETAIL')) { throw 'CLOSE_FACTS_ARGUMENT_LEAK' }
Write-Output 'PROFILE_TOKEN_CONTRACT_PASS checks=2 (fixtures only; not native evidence)'

# 初回所有が不確定な候補を後から除外して成功にしない。
$legitimate = Entry 16 13 'legitimate-other.exe' 'other.exe'
$legitimate.CreationDate = $webview.CreationDate.AddSeconds(1)
$lineageGraph = @($driver, $edge, $app, $webview, $legitimate)
$ownedWithOther = Get-OwnedApp $driver $edge 'app.exe' $lineageGraph
if ($ownedWithOther.Children.Count -ne 2 -or @($ownedWithOther.Children | Where-Object Name -eq 'other.exe').Count -ne 1) { throw 'LINEAGE_LEGITIMATE_OTHER_WAS_FILTERED' }
$legitimate.CreationDate = $webview.CreationDate.AddSeconds(-1)
$lineageEvidence = @()
Reject { Get-OwnedApp $driver $edge 'app.exe' $lineageGraph ([ref]$lineageEvidence) } 'NATIVE_LINEAGE_OWNER_UNCONFIRMED'
if ($lineageEvidence.Count -ne 1 -or $lineageEvidence[0].reason -ne 'child-older') { throw 'LINEAGE_UNCERTAINTY_EVIDENCE_MISSING' }
$legitimate.CreationDate = $null
Reject { Get-OwnedApp $driver $edge 'app.exe' $lineageGraph } 'NATIVE_LINEAGE_OWNER_UNCONFIRMED'
$legitimate.CreationDate = $webview.CreationDate.AddSeconds(1)
$duplicateGeneration = Entry 16 13 'legitimate-other.exe' 'other.exe'
$duplicateGeneration.CreationDate = $legitimate.CreationDate.AddSeconds(1)
Reject { Get-OwnedApp $driver $edge 'app.exe' @($lineageGraph + $duplicateGeneration) } 'NATIVE_LINEAGE_OWNER_UNCONFIRMED'
if (($lineageEvidence | ConvertTo-Json -Depth 5).Contains('ProcessId')) { throw 'LINEAGE_IDENTITY_LEAK' }
Write-Output 'LINEAGE_CONTRACT_PASS checks=4 (fixtures only; not native evidence)'

# 実際の差分を模した合成データ。再利用や時計変動の原因自体を再現するものではない。
$observedOther = Entry 27 13 'PRIVATE_CHILD_PATH.exe' 'other.exe'
$observedOther.CreationDate = $webview.CreationDate.AddTicks(-2720437740)
$observedFacts = @()
Reject { Get-OwnedApp $driver $edge 'app.exe' @($driver, $edge, $app, $webview, $observedOther) ([ref]$observedFacts) } 'NATIVE_LINEAGE_OWNER_UNCONFIRMED'
if ($observedFacts.Count -ne 1) { throw 'LINEAGE_CONTEXT_MISSING' }
$fact = $observedFacts[0]
foreach ($name in @('searchRoot', 'parentRole', 'parentDepth', 'parentSnapshotIdentity', 'childSnapshotIdentity', 'timeOrder')) {
    if ($null -eq $fact.PSObject.Properties[$name]) { throw 'LINEAGE_CONTEXT_MISSING' }
}
if ($fact.searchRoot -ne 'driver' -or $fact.parentRole -ne 'webview-name' -or $fact.parentDepth -ne 3 -or
    $fact.parentSnapshotIdentity -ne 'same' -or $fact.childSnapshotIdentity -ne 'same' -or
    $fact.timeOrder -ne 'child-older' -or $fact.childMinusParentCreationMs -ne -272043.774) { throw 'LINEAGE_CONTEXT_INCORRECT' }
$factJson = $fact | ConvertTo-Json -Depth 5
if ($factJson.Contains('PRIVATE_CHILD_PATH') -or $factJson.Contains('2026-10-27') -or $factJson.Contains('ProcessId')) { throw 'LINEAGE_CONTEXT_PRIVATE_DATA_LEAK' }
$duplicateObserved = Entry 27 13 'PRIVATE_DUPLICATE_PATH.exe' 'other.exe'
$duplicateObserved.CreationDate = $observedOther.CreationDate.AddSeconds(1)
$duplicateFacts = @()
Reject { Get-OwnedApp $driver $edge 'app.exe' @($driver, $edge, $app, $webview, $observedOther, $duplicateObserved) ([ref]$duplicateFacts) } 'NATIVE_LINEAGE_OWNER_UNCONFIRMED'
if ($duplicateFacts.Count -ne 1 -or $duplicateFacts[0].reason -ne 'child-row-ambiguous' -or $duplicateFacts[0].childSnapshotIdentity -ne 'ambiguous') { throw 'LINEAGE_CONTEXT_DUPLICATE_LOST' }
$observedOther.CreationDate = $null
$missingFacts = @()
Reject { Get-OwnedApp $driver $edge 'app.exe' @($driver, $edge, $app, $webview, $observedOther) ([ref]$missingFacts) } 'NATIVE_LINEAGE_OWNER_UNCONFIRMED'
if ($missingFacts.Count -ne 1 -or $missingFacts[0].reason -ne 'child-creation-unavailable' -or $missingFacts[0].childSnapshotIdentity -ne 'unknown' -or $missingFacts[0].timeOrder -ne 'unknown') { throw 'LINEAGE_CONTEXT_UNKNOWN_LOST' }
Write-Output 'LINEAGE_CONTEXT_CONTRACT_PASS checks=3 (fixtures only; not native evidence)'

# session作成の失敗を通常closeや所有保証へ読み替えず、匿名の起動状態を残す。
. "$PSScriptRoot/startup-observation.ps1"
$startup = Get-StartupFacts $driver $edge 'app.exe' $valid @($loop4, $edge4, $edge6)
if ($startup.driverIdentity -ne 'same-identity' -or $startup.edgeIdentity -ne 'same-identity' -or $startup.appCandidates -ne 2 -or $startup.correlatedApps -ne 1 -or $startup.correlatedWebviews -ne 1) { throw 'STARTUP_CORRELATION_FACTS_MISSING' }
$startup = Get-StartupFacts $driver $edge 'app.exe' @($driver, $edge) @($loop4, $edge4)
if ($startup.appCandidates -ne 0 -or $startup.correlatedApps -ne 0 -or $startup.PSObject.Properties.Name -contains 'normalCloseVerified') { throw 'STARTUP_ABSENCE_BECAME_SUCCESS' }
$startup = Get-StartupFacts $driver $edge 'PRIVATE_APP.exe' @($driver, $reused, $edge) @($loop4, $edge4)
$serialized = $startup | ConvertTo-Json -Depth 6
if ($startup.driverIdentity -ne 'ambiguous' -or $serialized.Contains('PRIVATE_') -or $serialized.Contains('ProcessId') -or $serialized.Contains('2026-10-27')) { throw 'STARTUP_IDENTITY_OR_PRIVATE_DATA_LOST' }
Write-Output 'STARTUP_OBSERVATION_CONTRACT_PASS checks=3 (fixtures only; not native evidence)'

$startupFolder = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $startupFolder -ErrorAction Stop | Out-Null
try {
    $notDirectory = Join-Path $startupFolder 'PRIVATE_OUTPUT'
    Set-Content -LiteralPath $notDirectory -Value 'fixture'
    $failedReport = [ordered]@{ nativeTestsPassed = 0; normalCloseVerified = $false; cleanupFailures = 0 }
    $saveCode = 'none'
    try { Save-NativeReport $failedReport $notDirectory }
    catch { $saveCode = if ($_.Exception.Message -eq 'NATIVE_CLEANUP_EVIDENCE_WRITE_FAILED') { $_.Exception.Message } else { 'unclassified' } }
    $unknownAddress = [pscustomobject]@{ LocalPort = 4444; LocalAddress = 'PRIVATE_INVALID_ADDRESS'; OwningProcess = 10 }
    $facts = Get-StartupFacts $driver $edge 'app.exe' $valid @($unknownAddress)
    $failures = 0
    if ($saveCode -ne 'NATIVE_CLEANUP_EVIDENCE_WRITE_FAILED') { Write-Output 'STARTUP_SAVE_FAILURE_UNCLASSIFIED'; $failures += 1 }
    if ($null -ne $facts.listeners[0].loopback) { Write-Output 'STARTUP_UNKNOWN_ADDRESS_FALSE_FACT'; $failures += 1 }
    if ($failedReport.nativeTestsPassed -ne 0 -or $failedReport.normalCloseVerified -or $failedReport.cleanupFailures -ne 0) { throw 'STARTUP_SAVE_CHANGED_ORIGINAL_VERDICT' }
    if ($failures -ne 0) { throw 'STARTUP_DIAGNOSTIC_FAILURE_CONTRACT' }
} finally { Remove-Item -LiteralPath $startupFolder -Recurse -Force }
Write-Output 'STARTUP_DIAGNOSTIC_FAILURE_CONTRACT_PASS checks=2 (fixtures only; not native evidence)'

# Separate scope keeps synthetic fixture helpers from changing the existing contract.
& "$PSScriptRoot/lineage-baseline.test.ps1"
