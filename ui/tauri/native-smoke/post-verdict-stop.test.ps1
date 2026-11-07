# True PowerShell pipeline stopping; NOT runner -> child console propagation.
param([string]$OwnedChildDirectory, [string]$QualificationPath)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $IsWindows) { throw 'POST_VERDICT_WINDOWS_STOP_FIXTURE_REQUIRED' }
if ($PSVersionTable.PSVersion.ToString() -cne '7.6.9') { throw 'POST_VERDICT_STOP_ENGINE_VERSION' }
if (-not $OwnedChildDirectory) {
    if (-not $QualificationPath -or $env:WRY_PIN -cnotmatch '^[0-9a-f]{40}$' -or
        $env:WRY_TREE -cnotmatch '^[0-9a-f]{40}$' -or $env:CONTROL_HEAD -cnotmatch '^[0-9a-f]{40}$') { throw 'POST_VERDICT_STOP_BINDING' }
    $root = Join-Path ([IO.Path]::GetTempPath()) ('post-verdict-engine-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory $root | Out-Null
    $child = Start-Process (Get-Command pwsh).Source -ArgumentList @('-NoLogo', '-NoProfile', '-File', "`"$PSCommandPath`"", '-OwnedChildDirectory', "`"$root`"") -PassThru -NoNewWindow
    # Unknown exit is a failed gate, never an excuse to signal/kill an arbitrary PID.
    if (-not $child.WaitForExit(90000)) { throw 'POST_VERDICT_STOP_CHILD_EXIT_UNKNOWN' }
    if ($child.ExitCode -ne 0) { throw 'POST_VERDICT_STOP_CHILD_FAILED' }
    $results = @()
    foreach ($name in @('before-entry', 'during-grace', 'admission-overbudget', 'diagnostic-fault', 'finish-fault', 'cleanup-fault')) {
        $receipt = Get-Content (Join-Path $root ($name + '.json')) -Raw | ConvertFrom-Json
        $expectedState = if ($name.EndsWith('-fault')) { 'Failed' } elseif ($name -eq 'admission-overbudget') { 'Completed' } else { 'Stopped' }
        if ($receipt.protocol -cne 'post-verdict-engine-stop-1' -or $receipt.engineVersion -cne '7.6.9' -or $receipt.case -cne $name -or
            $receipt.state -cne $expectedState -or $receipt.cleanupCount -ne 1 -or $receipt.finishCount -ne 1 -or
            $receipt.nativeTestsRun -ne 1 -or $receipt.nativeTestsPassed -ne 0 -or $receipt.normalCloseVerified -or -not $receipt.environmentRestored -or -not $receipt.passed) { throw 'POST_VERDICT_STOP_RECEIPT' }
        $expectedStop = if ($expectedState -eq 'Stopped') { 'completed' } else { 'not-requested' }
        if ($receipt.stopState -cne $expectedStop) { throw 'POST_VERDICT_STOP_TERMINAL' }
        # Alphabetical closed keys form canonical JSON; no child paths or PIDs.
        $results += [ordered]@{ case = $receipt.case; cleanupCount = $receipt.cleanupCount; engineVersion = $receipt.engineVersion;
            environmentRestored = $receipt.environmentRestored; finishCount = $receipt.finishCount; nativeTestsPassed = $receipt.nativeTestsPassed;
            nativeTestsRun = $receipt.nativeTestsRun; normalCloseVerified = $receipt.normalCloseVerified; passed = $receipt.passed;
            protocol = $receipt.protocol; state = $receipt.state; stopElapsedMs = $receipt.stopElapsedMs; stopState = $receipt.stopState }
    }
    $summary = [ordered]@{ childExitCode = $child.ExitCode; childState = 'exited'; controlCommit = $env:CONTROL_HEAD;
        engineVersion = $results[0].engineVersion; protocol = 'post-verdict-engine-qualification-1'; results = $results;
        sourceCommit = $env:WRY_PIN; sourceTree = $env:WRY_TREE }
    $bytes = [Text.Encoding]::ASCII.GetBytes(($summary | ConvertTo-Json -Depth 8 -Compress))
    if ($bytes.Length -gt 8192) { throw 'POST_VERDICT_STOP_RECEIPT_BOUND' }
    $file = [IO.File]::Open($QualificationPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try { $file.Write($bytes, 0, $bytes.Length); $file.Flush($true) } finally { $file.Dispose() }
    Remove-Item -LiteralPath $root -Recurse -Force
    Write-Output 'POST_VERDICT_OWNED_CHILD_ENGINE_STOP_ONLY_PASSED'
    exit 0
}
$tokens = $null; $errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile("$PSScriptRoot/supervise.ps1", [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw 'POST_VERDICT_SUPERVISOR_PARSE' }
$blocks = @($ast.FindAll({ param($node)
    $node -is [Management.Automation.Language.TryStatementAst] -and $node.CatchClauses.Count -eq 1 -and
    $node.CatchClauses[0].Extent.Text.Contains('$report.failurePhase = $phase')
}, $true))
if ($blocks.Count -ne 1) { throw 'POST_VERDICT_SUPERVISOR_SEAM' }
# Exercise the actual production catch/finally, not a handwritten lifecycle model.
$body = "try { throw 'NATIVE_WEBVIEW_OR_IPC_ASSERTION_FAILED' } " + $blocks[0].CatchClauses[0].Extent.Text + ' finally ' + $blocks[0].Finally.Extent.Text
$setup = @'
param($Shared, $Directory, $Helper, $BodyPath)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. $Helper
$report = $Shared.Report
$phase = 'webview-and-ipc'
$PostVerdictManifest = if ($Shared.Case -in @('finish-fault', 'cleanup-fault')) { $null } else { 'fixture' }
# Test-only exception injection at the caller boundary, not a production alternate.
if ($Shared.Case -eq 'diagnostic-fault') { function Invoke-PostVerdictObservation { throw 'POST_VERDICT_INJECTED_DIAGNOSTIC' } }
$appTraceDirectory = $Directory
$OutputDirectory = Join-Path $Directory 'output'
$ToolsDirectory = $Directory
$appTraceCollected = $false
$diagnosticBudget = [Diagnostics.Stopwatch]::new()
$nodeProcess = [pscustomobject]@{ HasExited = $true; ExitCode = 1 }
$driverProcess = $null; $children = @(); $app = $null; $childHandles = @()
$childWaitStartedUtc = $null; $appPath = 'fixture'
$driver = @([pscustomobject]@{ ProcessId = 1 }); $edge = $driver
$env:RESCUE_DRIVER_TRACE = 'post-verdict-original-driver'
$env:RESCUE_HOST_STARTUP_TRACE_DIR = 'post-verdict-original-host'
$previousDriverTrace = $env:RESCUE_DRIVER_TRACE
$previousAppTrace = $env:RESCUE_HOST_STARTUP_TRACE_DIR
$env:RESCUE_DRIVER_TRACE = 'post-verdict-active-driver'
$env:RESCUE_HOST_STARTUP_TRACE_DIR = 'post-verdict-active-host'
function Get-Snapshot {
    if ($Shared.Case -eq 'before-entry' -and $Shared.Cleanup -eq 0) {
        $Shared.QueryEntered.Set()
        # Finite even if the fixture fails to issue Stop; no wait in production.
        for ($i = 0; $i -lt 600; $i++) { Start-Sleep -Milliseconds 25 }
    }
    return @()
}
# The actual admission read function is retained. Only its first read is delayed
# synchronously in this fixture to expose the cooperative (not hard-I/O) bound.
$Shared.OriginalRead = ${function:Read-PostVerdictBytes}
function Read-PostVerdictBytes([string]$Path, [int]$Limit = 65536) {
    if ($Shared.Case -eq 'admission-overbudget' -and -not $Shared.AdmissionEntered.IsSet) {
        $Shared.AdmissionEntered.Set()
        [Threading.Thread]::Sleep(10500)
    }
    & $Shared.OriginalRead $Path $Limit
}
function Get-NetTCPConnection { return @() }
function Get-StartupFacts { return @{} }
function Wait-AppStartupTrace {
    $Shared.Finish++
    if ($Shared.Case -eq 'finish-fault') { throw 'POST_VERDICT_INJECTED_FINISH' }
    return $false
}
function Complete-NativeCleanup {
    $Shared.Cleanup++
    [IO.File]::WriteAllText((Join-Path $Directory 'cleanup-receipt.json'), ('{"cleanupCount":' + $Shared.Cleanup + ',"nativeTestsRun":1,"nativeTestsPassed":0}'))
    if ($Shared.Case -eq 'cleanup-fault') { throw 'POST_VERDICT_INJECTED_CLEANUP' }
}
function Save-NativeReport { }
& $BodyPath
'@
foreach ($case in @('before-entry', 'during-grace', 'admission-overbudget', 'diagnostic-fault', 'finish-fault', 'cleanup-fault')) {
    $directory = Join-Path $OwnedChildDirectory $case
    New-Item -ItemType Directory (Join-Path $directory 'output') | Out-Null
    $pipeline = [PowerShell]::Create()
    $pending = $null
    $shared = [hashtable]::Synchronized(@{ Case = $case; AdmissionEntered = [Threading.ManualResetEventSlim]::new($false); QueryEntered = [Threading.ManualResetEventSlim]::new($false); Cleanup = 0; Finish = 0;
        Report = [ordered]@{ nativeTestsRun = 1; nativeTestsPassed = 0; normalCloseVerified = $false; failurePhase = $null } })
    try {
        $native = Get-Content "$PSScriptRoot/../../../scripts/tests/fixtures/wry240-baseline.json" -Raw | ConvertFrom-Json
        [IO.File]::WriteAllText((Join-Path $directory 'output/native-observation.json'), ($native.observation | ConvertTo-Json -Depth 20 -Compress))
        [IO.File]::WriteAllText((Join-Path $directory 'output/session-startup-timing.json'), '{"schema":1,"purpose":"diagnostic-hints-only","settledMs":15009,"outcome":"timeout","samples":[{"stage":"after-settlement","startedMs":15009,"completedMs":15010,"requestPendingAtStart":false,"facts":null}]}')
        $manifest = [ordered]@{ attempt = 1; controlCommit = 'a' * 40; nonce = 'b' * 64; protocol = 'post-verdict-manifest-1'; run = '1'; sourceCommit = 'c' * 40; sourceTree = 'd' * 40; specimenSha256 = 'e' * 64; trial = 1 }
        [IO.File]::WriteAllText((Join-Path $directory 'post-verdict.manifest.json'), ($manifest | ConvertTo-Json -Compress))
        # The production driver-finish path observes an existing bounded trace.
        [IO.File]::WriteAllText((Join-Path $directory 'driver-stage.jsonl'), '{}')
        # A real script path preserves InvocationInfo.ScriptName in production catch.
        $bodyPath = Join-Path $directory 'actual-failure-body.ps1'
        if ([Text.Encoding]::UTF8.GetByteCount($body) -gt 65536) { throw 'POST_VERDICT_STOP_BODY_BOUND' }
        [IO.File]::WriteAllText($bodyPath, $body)
        [void]$pipeline.AddScript($setup).AddArgument($shared).AddArgument($directory).AddArgument("$PSScriptRoot/post-verdict.ps1").AddArgument($bodyPath)
        $pending = $pipeline.BeginInvoke()
        $wait = [Diagnostics.Stopwatch]::StartNew()
        do {
            $ready = if ($case.EndsWith('-fault')) { $true } elseif ($case -eq 'before-entry') { $shared.QueryEntered.IsSet } elseif ($case -eq 'admission-overbudget') { $shared.AdmissionEntered.IsSet } else { Test-Path (Join-Path $directory 'post-verdict.signal') }
            if ($ready -or $pending.IsCompleted) { break }
            Start-Sleep -Milliseconds 10
        } while ($wait.ElapsedMilliseconds -lt 15000)
        if (-not $ready) { throw 'POST_VERDICT_STOP_SEAM_NOT_REACHED' }
        $stop = [Diagnostics.Stopwatch]::StartNew()
        if ($case.EndsWith('-fault')) {
            if (-not $pending.AsyncWaitHandle.WaitOne(5000)) { throw 'POST_VERDICT_FAULT_RUNSPACE_EXIT_UNKNOWN' }
            $expectedState = 'Failed'
        } elseif ($case -eq 'admission-overbudget') {
            # Do NOT cancel: absence of a signal must be caused by the real budget
            # check after the synchronous read, not by PipelineStopped instead.
            if (-not $pending.AsyncWaitHandle.WaitOne(15000)) { throw 'POST_VERDICT_BUDGET_RUNSPACE_EXIT_UNKNOWN' }
            $expectedState = 'Completed'
        } else {
            # Only this positively owned same-process runspace; no PID/console signal.
            $stopping = $pipeline.BeginStop($null, $null)
            if (-not $stopping.AsyncWaitHandle.WaitOne(15000) -or -not $pending.IsCompleted -or $pipeline.InvocationStateInfo.State -notin @('Stopped', 'Failed', 'Completed')) { throw 'POST_VERDICT_STOP_RUNSPACE_EXIT_UNKNOWN' }
            $pipeline.EndStop($stopping)
            if ($stop.ElapsedMilliseconds -ge 3000) { throw 'POST_VERDICT_STOP_STARTED_OR_CONTINUED_GRACE' }
            $expectedState = 'Stopped'
        }
        if (-not $pending.IsCompleted -or $pipeline.InvocationStateInfo.State -notin @('Stopped', 'Failed', 'Completed')) { throw 'POST_VERDICT_RUNSPACE_EXIT_UNKNOWN' }
        try { [void]$pipeline.EndInvoke($pending) } catch [Management.Automation.PipelineStoppedException] {
            if ($expectedState -cne 'Stopped') { throw 'POST_VERDICT_UNEXPECTED_STOP' }
        } catch {
            if ($expectedState -cne 'Failed') { throw 'POST_VERDICT_UNEXPECTED_FAILURE' }
        }
        if ($pipeline.InvocationStateInfo.State.ToString() -cne $expectedState -or
            $shared.Cleanup -ne 1 -or $shared.Finish -ne 1 -or $shared.Report.nativeTestsRun -ne 1 -or
            $shared.Report.nativeTestsPassed -ne 0 -or $shared.Report.normalCloseVerified -or
            $shared.Report.failureCode -cne 'NATIVE_WEBVIEW_OR_IPC_ASSERTION_FAILED') { throw 'POST_VERDICT_STOP_CLEANUP_OR_VERDICT' }
        if ($case -eq 'before-entry' -and ((Test-Path (Join-Path $directory 'post-verdict.signal')) -or $shared.Report.Contains('postVerdictElapsedMs'))) { throw 'POST_VERDICT_STOP_ENTERED_HELPER_DURING_UNWIND' }
        if ($case -eq 'admission-overbudget' -and ((Test-Path (Join-Path $directory 'post-verdict.signal')) -or
            -not $shared.Report.postVerdictBudgetOverrun -or $shared.Report.postVerdictElapsedMs -lt 10000)) { throw 'POST_VERDICT_STOP_ADMISSION_BUDGET' }
        if ($case.EndsWith('-fault') -and (Test-Path (Join-Path $directory 'post-verdict.signal'))) { throw 'POST_VERDICT_FAULT_PUBLISHED_SIGNAL' }
        $cleanupReceipt = Get-Content (Join-Path $directory 'cleanup-receipt.json') -Raw | ConvertFrom-Json
        if ($cleanupReceipt.cleanupCount -ne 1 -or $cleanupReceipt.nativeTestsRun -ne 1 -or $cleanupReceipt.nativeTestsPassed -ne 0) { throw 'POST_VERDICT_STOP_CLEANUP_RECEIPT' }
        $environmentRestored = $env:RESCUE_DRIVER_TRACE -ceq 'post-verdict-original-driver' -and $env:RESCUE_HOST_STARTUP_TRACE_DIR -ceq 'post-verdict-original-host'
        if (-not $environmentRestored) { throw 'POST_VERDICT_STOP_ENVIRONMENT_NOT_RESTORED' }
        # File receipts survive suppression of stopped-pipeline output. No raw errors.
        $receipt = [ordered]@{ protocol = 'post-verdict-engine-stop-1'; engineVersion = $PSVersionTable.PSVersion.ToString(); case = $case;
            state = $pipeline.InvocationStateInfo.State.ToString(); cleanupCount = $shared.Cleanup; finishCount = $shared.Finish;
            nativeTestsRun = $shared.Report.nativeTestsRun; nativeTestsPassed = $shared.Report.nativeTestsPassed; normalCloseVerified = $shared.Report.normalCloseVerified;
            stopElapsedMs = $stop.ElapsedMilliseconds; stopState = $(if ($expectedState -eq 'Stopped') { 'completed' } else { 'not-requested' }); environmentRestored = $environmentRestored; passed = $true }
        [IO.File]::WriteAllText((Join-Path $OwnedChildDirectory ($case + '.json')), ($receipt | ConvertTo-Json -Compress))
    } finally {
        # Never Dispose/EndInvoke a still-running pipeline: those can block forever.
        if ($null -eq $pending -or ($pending.IsCompleted -and $pipeline.InvocationStateInfo.State -in @('Stopped', 'Failed', 'Completed'))) {
            $pipeline.Dispose()
            $shared.QueryEntered.Dispose()
            $shared.AdmissionEntered.Dispose()
            Remove-Item -LiteralPath $directory -Recurse -Force
        }
    }
}
