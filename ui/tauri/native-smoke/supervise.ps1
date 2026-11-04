param([Parameter(Mandatory)][string]$ToolsDirectory, [Parameter(Mandatory)][string]$OutputDirectory, [string]$ApplicationPath,
    [switch]$StartupDiagnostic, [string]$StartupManifest, [string]$StartupSource)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. "$PSScriptRoot/processes.ps1"
. "$PSScriptRoot/close-observation.ps1"
. "$PSScriptRoot/startup-observation.ps1"
. "$PSScriptRoot/app-startup-observation.ps1"
New-Item -ItemType Directory -Path $OutputDirectory -ErrorAction Stop | Out-Null
$appPath = (Resolve-Path $(if ($ApplicationPath) { $ApplicationPath } else { "$PSScriptRoot/../src-tauri/target/debug/rescue-host-shell.exe" })).Path
$edgePath = (Resolve-Path "$ToolsDirectory/edge/msedgedriver.exe").Path
$tauriPath = (Resolve-Path "$ToolsDirectory/tauri/bin/tauri-driver.exe").Path
$driverProcess = $null
$nodeProcess = $null
$childHandles = @()
$children = @()
$app = $null
$childWaitStartedUtc = $null
$report = [ordered]@{ kind = 'windows-webview2-native-smoke'; nativeTestsRun = 0; nativeTestsPassed = 0; cleanupStarted = $false; normalCloseVerified = $false; failurePhase = $null }
$phase = 'environment'
$previousDriverTrace = $env:RESCUE_DRIVER_TRACE
$previousAppTrace = $env:RESCUE_HOST_STARTUP_TRACE_DIR
$appTraceDirectory = $null
$appTraceCollected = $false
$diagnosticBudget = [Diagnostics.Stopwatch]::new()
try {
    $env:RESCUE_HOST_STARTUP_TRACE_DIR = $null
    if ($StartupDiagnostic) {
        & node "$PSScriptRoot/inspect-app-startup.ts" --manifest $StartupManifest --exe $appPath --source $StartupSource --build-only
        if ($LASTEXITCODE -ne 0) { throw 'NATIVE_APP_DIAGNOSTIC_BUILD_REJECTED' }
        $appTraceDirectory = Join-Path $ToolsDirectory 'app-startup'
        New-Item -ItemType Directory -Path $appTraceDirectory -ErrorAction Stop | Out-Null
        $env:RESCUE_HOST_STARTUP_TRACE_DIR = $appTraceDirectory
    }
    $before = Get-Snapshot
    if (@($before | Where-Object ExecutablePath -eq $appPath).Count -ne 0) { throw 'NATIVE_PREEXISTING_APP_REJECTED' }
    if (@(Get-NetTCPConnection -State Listen -ErrorAction Stop | Where-Object LocalPort -in @(4444, 4445)).Count -ne 0) { throw 'NATIVE_PORT_ALREADY_IN_USE' }
    $stageProvenance = Get-Content "$ToolsDirectory/driver-provenance.json" -Raw | ConvertFrom-Json
    if ($stageProvenance.variant -ne 'tauri-driver-2.1.0-stage-1' -or $stageProvenance.binarySha256 -cnotmatch '^[0-9a-f]{64}$' -or (Get-FileHash $tauriPath -Algorithm SHA256).Hash.ToLowerInvariant() -cne $stageProvenance.binarySha256) { throw 'NATIVE_DIAGNOSTIC_BINARY_DRIFT' }
    $env:RESCUE_DRIVER_TRACE = Join-Path $ToolsDirectory 'driver-stage.jsonl'
    $driverProcess = Start-Process $tauriPath -ArgumentList @('--port', '4444', '--native-port', '4445', '--native-driver', "`"$edgePath`"") -PassThru -RedirectStandardOutput "$ToolsDirectory/driver.stdout.log" -RedirectStandardError "$ToolsDirectory/driver.stderr.log"
    $until = [DateTime]::UtcNow.AddSeconds(15)
    $ready = $false
    do {
        if ($driverProcess.HasExited) { throw 'NATIVE_DRIVER_START_FAILED' }
        try { $ready = (Invoke-RestMethod 'http://127.0.0.1:4444/status' -TimeoutSec 2).value.ready } catch { Start-Sleep -Milliseconds 200 }
    } while (-not $ready -and [DateTime]::UtcNow -lt $until)
    if (-not $ready) { throw 'NATIVE_DRIVER_READY_TIMEOUT' }
    $snapshot = Get-Snapshot
    $driver = @($snapshot | Where-Object ProcessId -eq $driverProcess.Id)
    $edge = @($snapshot | Where-Object { $_.ParentProcessId -eq $driverProcess.Id -and $_.ExecutablePath -eq $edgePath })
    if ($driver.Count -ne 1 -or $edge.Count -ne 1 -or $driverProcess.HasExited -or $driver[0].ExecutablePath -ne $tauriPath) { throw 'NATIVE_DRIVER_IDENTITY_AMBIGUOUS' }
    $listeners = @(Get-NetTCPConnection -State Listen -ErrorAction Stop | Where-Object LocalPort -in @(4444, 4445))
    $report.listenerFacts = @($listeners | ForEach-Object {
        $address = [Net.IPAddress]::Parse($_.LocalAddress)
        [ordered]@{ port = $_.LocalPort; loopback = [Net.IPAddress]::IsLoopback($address); family = $address.AddressFamily.ToString(); owned = $_.OwningProcess -eq $(if ($_.LocalPort -eq 4444) { $driver[0].ProcessId } else { $edge[0].ProcessId }) }
    })
    Assert-ListenerSnapshot $listeners $driver[0].ProcessId $edge[0].ProcessId
    try { $report.beforeSessionFacts = Get-StartupFacts $driver[0] $edge[0] $appPath $snapshot $listeners }
    catch { $report.beforeSessionObservation = 'unavailable' }
    $phase = 'webview-and-ipc'
    $report.nativeTestsRun = 1
    $env:NATIVE_OUTPUT_DIR = $OutputDirectory
    $env:NATIVE_APP_EXE = $appPath
    $env:NATIVE_PROFILE = Join-Path $ToolsDirectory 'webview-profile'
    $env:NATIVE_TOOLS_DIR = $ToolsDirectory
    $nodeProcess = Start-Process (Get-Command node.exe).Source -ArgumentList "$PSScriptRoot/run.ts" -PassThru -NoNewWindow -RedirectStandardOutput "$ToolsDirectory/test.stdout.log" -RedirectStandardError "$ToolsDirectory/test.stderr.log"
    if (-not $nodeProcess.WaitForExit(80000)) { throw 'NATIVE_TEST_PROCESS_TIMEOUT' }
    if ($nodeProcess.ExitCode -ne 0) { throw 'NATIVE_WEBVIEW_OR_IPC_ASSERTION_FAILED' }
    $view = Get-Content "$OutputDirectory/webview-result.json" -Raw | ConvertFrom-Json
    if ($view.browserVersion -ne (Get-Content "$PSScriptRoot/tools.json" -Raw | ConvertFrom-Json).runtimeVersion) { throw 'NATIVE_ACTUAL_RUNTIME_MISMATCH' }
    if ($view.nativeTestsStarted -ne 1 -or $view.checks -ne 3 -or -not $view.actualRustIpc) { throw 'NATIVE_ZERO_OR_INCOMPLETE_TESTS' }
    if ($StartupDiagnostic) {
        # Node成功後かつ通常close前に回収する。native合否とは別の観測。
        $report.appStartupFileClosed = Wait-AppStartupTrace $appTraceDirectory $diagnosticBudget
        $appTraceCollected = $true
    }
    $snapshot = Get-Snapshot
    $phase = 'ownership-confirmation'
    $lineageEvidence = @()
    try { $observation = Get-OwnedApp $driver[0] $edge[0] $appPath $snapshot ([ref]$lineageEvidence) $before }
    finally { $report.initialLineageFacts = @($lineageEvidence) }
    $app = $observation.App
    $children = $observation.Children
    $childHandles = @(Open-ChildObservationHandles $children)
    $report.childHandleOwner = 'harness-read-only-before-app-close'
    $report.webviewChildrenObserved = $observation.Webviews.Count
    $phase = 'normal-close'
    $appProcess = Get-Process -Id $app.ProcessId -ErrorAction Stop
    $appProcess.Refresh()
    $appWait = [Diagnostics.Stopwatch]::StartNew()
    try { Request-NormalClose $app $appProcess 10000 }
    finally { $report.appWaitElapsedMs = $appWait.ElapsedMilliseconds }
    $report.appCloseObservedBeforeCleanup = $true
    $report.appExitCode = $appProcess.ExitCode
    $childWaitStartedUtc = [DateTime]::UtcNow
    $childWait = [Diagnostics.Stopwatch]::StartNew()
    $report.childSnapshotCount = 0
    $report.childSnapshotMaxMs = 0
    $until = $childWaitStartedUtc.AddSeconds(10)
    do {
        $report.childSnapshotStartMs = $childWait.ElapsedMilliseconds
        $snapshotTimer = [Diagnostics.Stopwatch]::StartNew()
        $snapshot = Get-Snapshot
        $report.childSnapshotEndMs = $childWait.ElapsedMilliseconds
        $report.childSnapshotCount += 1
        $report.childSnapshotMaxMs = [Math]::Max($report.childSnapshotMaxMs, $snapshotTimer.ElapsedMilliseconds)
        Require-Live $driver[0] $snapshot
        Require-Live $edge[0] $snapshot
        $remaining = @($children | Where-Object {
            $previous = $_
            @($snapshot | Where-Object { Same-Process $previous $_ }).Count -gt 0
        })
        if ($remaining.Count -gt 0) { Start-Sleep -Milliseconds 200 }
    } while ($remaining.Count -gt 0 -and [DateTime]::UtcNow -lt $until)
    $report.childWaitElapsedMs = $childWait.ElapsedMilliseconds
    $report.childCloseFacts = Get-ChildCloseFacts $children $app $snapshot $childHandles $childWaitStartedUtc $env:NATIVE_PROFILE
    if ($remaining.Count -ne 0) {
        # 旧判定は既にtimeout。後続観測で消えてもこのFAILを変えない。
        try {
            $report.afterWaitProbeFacts = Get-ChildCloseFacts $children $app (Get-Snapshot) $childHandles $childWaitStartedUtc $env:NATIVE_PROFILE
            $report.afterWaitProbeElapsedMs = $childWait.ElapsedMilliseconds
        } catch { $report.afterWaitProbeObservation = 'unavailable' }
        throw 'NATIVE_OBSERVED_CHILD_CLOSE_TIMEOUT'
    }
    if ($report.cleanupStarted) { throw 'NATIVE_CLOSE_OBSERVED_AFTER_CLEANUP' }
    $report.closeObservedBeforeCleanup = $true
    $report.normalCloseVerified = $true
    $report.appExitCode = $appProcess.ExitCode
    $report.nativeTestsPassed = 1
    $report.driverStableThroughClose = $true
    $report.browserVersion = $view.browserVersion
    Write-Output 'NATIVE_NORMAL_CLOSE_AND_OBSERVED_PROCESS_EXIT_VERIFIED'
} catch {
    $report.failurePhase = $phase
    $report.failureCode = if ($_.Exception.Message -match '^NATIVE_[A-Z_]+$') { $_.Exception.Message } else { 'NATIVE_ENVIRONMENT_OR_HARNESS_ERROR' }
    $report.failureType = $_.Exception.GetType().Name
    $report.failureScript = Split-Path $_.InvocationInfo.ScriptName -Leaf
    $report.failureLine = $_.InvocationInfo.ScriptLineNumber
    Write-Output $report.failureCode
    if ($phase -eq 'webview-and-ipc') {
        # 元のFAIL確定後、cleanupで状態を変える前の一回だけの診断。再session要求はしない。
        $startupQuery = [Diagnostics.Stopwatch]::StartNew()
        try {
            $failedSnapshot = Get-Snapshot
            $failedListeners = @(Get-NetTCPConnection -State Listen -ErrorAction Stop | Where-Object LocalPort -in @(4444, 4445))
            $report.startupFailureFacts = Get-StartupFacts $driver[0] $edge[0] $appPath $failedSnapshot $failedListeners
        } catch { $report.startupFailureObservation = 'unavailable' }
        finally { $report.startupFailureQueryMs = $startupQuery.ElapsedMilliseconds }
    }
} finally {
    if ($null -ne $appTraceDirectory -and -not $appTraceCollected) {
        $report.appStartupFileClosed = Wait-AppStartupTrace $appTraceDirectory $diagnosticBudget
    }
    # 合否確定後の診断cutoff。新requestは送らず、自己所有workerを最大1秒だけ待つ。
    try {
        [IO.File]::WriteAllText((Join-Path $ToolsDirectory 'driver-stage.finish'), 'finish')
        if (-not $diagnosticBudget.IsRunning) { $diagnosticBudget.Start() }
        do {
            if (Test-Path (Join-Path $ToolsDirectory 'driver-stage.jsonl')) { break }
            if ($diagnosticBudget.ElapsedMilliseconds -ge 1000) { break }
            Start-Sleep -Milliseconds 20
        } while ($diagnosticBudget.ElapsedMilliseconds -lt 1000)
        $report.driverStageFileObserved = Test-Path (Join-Path $ToolsDirectory 'driver-stage.jsonl')
    } catch { Write-Output 'NATIVE_STAGE_FINISH_FAILED' }
    # 通常closeの合否確定後にのみcleanupし、元のFAILと記録を保持する。
    try { Complete-NativeCleanup $nodeProcess $driverProcess $children $app $childHandles $report $OutputDirectory $childWaitStartedUtc $env:NATIVE_PROFILE }
    finally { $env:RESCUE_DRIVER_TRACE = $previousDriverTrace; $env:RESCUE_HOST_STARTUP_TRACE_DIR = $previousAppTrace }
    if ($phase -eq 'webview-and-ipc' -and $report.nativeTestsPassed -eq 0) {
        try {
            $afterSnapshot = Get-Snapshot
            $afterListeners = @(Get-NetTCPConnection -State Listen -ErrorAction Stop | Where-Object LocalPort -in @(4444, 4445))
            $report.startupAfterCleanupFacts = Get-StartupFacts $driver[0] $edge[0] $appPath $afterSnapshot $afterListeners
        } catch { $report.startupAfterCleanupObservation = 'unavailable' }
        finally { Save-NativeReport $report $OutputDirectory }
    }
}
if ($report.cleanupFailures -gt 0 -or $report.nativeTestsRun -ne 1 -or $report.nativeTestsPassed -ne 1 -or -not $report.normalCloseVerified) { exit 1 }
