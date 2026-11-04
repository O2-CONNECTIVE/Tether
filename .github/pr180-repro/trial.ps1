param(
    [Parameter(Mandatory)][string]$SourceDirectory,
    [Parameter(Mandatory)][string]$BundleDirectory,
    [Parameter(Mandatory)][string]$ManifestSha256,
    [Parameter(Mandatory)][ValidateRange(1, 4)][int]$Trial,
    [Parameter(Mandatory)][string]$EvidenceDirectory
)
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
Set-StrictMode -Version Latest
$source = (Resolve-Path $SourceDirectory).Path
$bundle = (Resolve-Path $BundleDirectory).Path
$app = (Resolve-Path "$bundle/app/rescue-host-shell.exe").Path
$tools = (Resolve-Path "$bundle/tools").Path
$verifier = Join-Path $PSScriptRoot 'bundle.py'
$native = Join-Path $EvidenceDirectory 'native'
New-Item -ItemType Directory $EvidenceDirectory -ErrorAction Stop | Out-Null
$report = [ordered]@{
    schema = 1; purpose = 'pr180-finite-reproduction'; trial = $Trial; trialsMaximum = 4
    sourceCommit = '16a1de9e48f8ac4559b81feb2ac7a1c71a02ecf4'
    manifestSha256 = $ManifestSha256; status = 'not-started'; nativeInvocationStarted = $false
    nativeExit = $null; driverInspectionExit = $null; targetReproduced = $false
    sameBinariesBefore = $false; sameBinariesAfter = $false; childrenCleanupConfirmed = $false
    cleanBoundary = $false; continue = $false
}
function Save-TrialReport {
    [IO.File]::WriteAllText((Join-Path $EvidenceDirectory 'trial-result.json'), ($report | ConvertTo-Json -Depth 5 -Compress))
}
function Test-ObservedChildrenExited($Result) {
    if ('afterDriverCleanupFacts' -notin $Result.PSObject.Properties.Name -or 'afterDriverCleanupObservation' -in $Result.PSObject.Properties.Name) { return $false }
    $facts = $Result.afterDriverCleanupFacts
    $rows = @($facts.rows)
    $unknown = @($rows | Where-Object { $_.handleIdentityVerified -ne $true -or $_.handleState -ne 'exited' -or $_.snapshotState -notin @('absent', 'pid-reused') })
    return $facts.observed -gt 0 -and $facts.omittedRows -eq 0 -and $rows.Count -eq $facts.observed -and $facts.remainingSnapshot -eq 0 -and $unknown.Count -eq 0
}
Save-TrialReport
try {
    Set-Location $source
    if ((git rev-parse HEAD).Trim() -cne $report.sourceCommit) { throw 'REPRO_SOURCE_DRIFT' }
    & python -B $verifier verify --directory $bundle --manifest-sha256 $ManifestSha256
    if ($LASTEXITCODE -ne 0) { throw 'REPRO_BUNDLE_DRIFT' }
    $report.sameBinariesBefore = $true
    Copy-Item "$bundle/BUILD.json" "$EvidenceDirectory/BUILD.json"
    $report.status = 'native-running'
    $report.nativeInvocationStarted = $true
    Save-TrialReport
    & pwsh -NoProfile -File "$source/ui/tauri/native-smoke/supervise.ps1" -ToolsDirectory $tools -OutputDirectory $native -ApplicationPath $app
    $report.nativeExit = $LASTEXITCODE
    $observation = Get-Content "$native/native-observation.json" -Raw | ConvertFrom-Json
    $report.targetReproduced = $report.nativeExit -ne 0 -and $observation.phase -eq 'session-create' -and $observation.errorCode -eq 'NATIVE_DRIVER_TIMEOUT' -and $observation.documentObservations -eq 0
    $report.status = if ($report.targetReproduced) { 'target-timeout' } elseif ($report.nativeExit -ne 0) { 'other-native-failure' } else { 'verifying-close-and-cleanup' }
    Save-TrialReport
    & pwsh -NoProfile -File "$source/ui/tauri/native-smoke/inspect-driver-trace.ps1" -ToolsDirectory $tools -OutputDirectory $native
    $report.driverInspectionExit = $LASTEXITCODE
    & python -B $verifier verify --directory $bundle --manifest-sha256 $ManifestSha256 --after-native
    if ($LASTEXITCODE -ne 0) { throw 'REPRO_BUNDLE_DRIFT' }
    $report.sameBinariesAfter = $true
    git diff --exit-code -- .
    if ($LASTEXITCODE -ne 0) { throw 'REPRO_SOURCE_DRIFT' }
    $result = Get-Content "$native/native-result.json" -Raw | ConvertFrom-Json
    $report.childrenCleanupConfirmed = Test-ObservedChildrenExited $result
    $cleanup = $result.cleanupStarted -eq $true -and $result.cleanupFailures -eq 0 -and $result.driverExitObservedAfterCleanup -eq $true
    $paths = @($app, (Resolve-Path "$tools/tauri/bin/tauri-driver.exe").Path, (Resolve-Path "$tools/edge/msedgedriver.exe").Path)
    $remaining = @(Get-CimInstance Win32_Process -ErrorAction Stop | Where-Object { $_.ExecutablePath -in $paths })
    $listeners = @(Get-NetTCPConnection -State Listen -ErrorAction Stop | Where-Object LocalPort -in @(4444, 4445))
    $report.cleanBoundary = $cleanup -and $report.childrenCleanupConfirmed -and $remaining.Count -eq 0 -and $listeners.Count -eq 0
    $report.continue = $report.nativeExit -eq 0 -and $report.driverInspectionExit -eq 0 -and $result.nativeTestsRun -eq 1 -and $result.nativeTestsPassed -eq 1 -and $result.normalCloseVerified -eq $true -and $report.cleanBoundary -and $report.sameBinariesAfter
    if ($report.continue) { $report.status = 'passed-original-native-contract' }
    elseif (-not $report.targetReproduced -and $report.nativeExit -eq 0) { $report.status = 'observation-or-cleanup-unconfirmed' }
} catch {
    if ($report.status -notin @('target-timeout', 'other-native-failure')) { $report.status = 'setup-or-observation-incomplete' }
    $report.continue = $false
    Write-Output 'REPRO_TRIAL_STOPPED'
} finally {
    Save-TrialReport
}
if (-not $report.continue) { exit 1 }
"continue=true" >> $env:GITHUB_OUTPUT
Write-Output 'REPRO_TRIAL_COMPLETE_NEXT_FRESH_RUNNER_PERMITTED'
