param([string]$ChildDirectory)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. "$PSScriptRoot/post-verdict.ps1"
if ($ChildDirectory) {
    $report = [ordered]@{ nativeTestsRun = 1; nativeTestsPassed = 0; normalCloseVerified = $false; failurePhase = 'webview-and-ipc'; failureCode = 'NATIVE_WEBVIEW_OR_IPC_ASSERTION_FAILED' }
    try {
        Invoke-PostVerdictObservation ([pscustomobject]@{ HasExited = $true; ExitCode = 1 }) $report (Join-Path $ChildDirectory 'output') $ChildDirectory
    } finally {
        # This fixture runs no Host/driver. Cleanup ownership is the child itself.
        $report.cleanupFixtureRan = $true
        [IO.File]::WriteAllText((Join-Path $ChildDirectory 'fixture-result.json'), ($report | ConvertTo-Json -Compress))
    }
    exit 1
}
if (-not $IsWindows) { throw 'POST_VERDICT_WINDOWS_FIXTURE_REQUIRED' }
$directory = Join-Path ([IO.Path]::GetTempPath()) ('post-verdict-contract-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory (Join-Path $directory 'output') | Out-Null
$child = $null
try {
    $native = Get-Content "$PSScriptRoot/../../../scripts/tests/fixtures/wry240-baseline.json" -Raw | ConvertFrom-Json
    [IO.File]::WriteAllText((Join-Path $directory 'output/native-observation.json'), ($native.observation | ConvertTo-Json -Depth 20 -Compress))
    [IO.File]::WriteAllText((Join-Path $directory 'output/session-startup-timing.json'), '{"schema":1,"purpose":"diagnostic-hints-only","settledMs":15009,"outcome":"timeout","samples":[{"stage":"after-settlement","startedMs":15009,"completedMs":15010,"requestPendingAtStart":false,"facts":null}]}')
    $manifest = [ordered]@{ attempt = 1; controlCommit = 'a' * 40; nonce = 'b' * 64; protocol = 'post-verdict-manifest-1'; run = '1'; sourceCommit = 'c' * 40; sourceTree = 'd' * 40; specimenSha256 = 'e' * 64; trial = 1 }
    Write-PostVerdictBytes (Join-Path $directory 'post-verdict.manifest.json') ([Text.Encoding]::ASCII.GetBytes(($manifest | ConvertTo-Json -Compress)))
    # Real child pwsh, same -NoLogo/-NoProfile/-File style as CI. Cancellation
    # is cooperative file signaling ONLY; this does not simulate runner Ctrl-C.
    $child = Start-Process (Get-Command pwsh).Source -ArgumentList @('-NoLogo', '-NoProfile', '-File', "`"$PSCommandPath`"", '-ChildDirectory', "`"$directory`"") -PassThru -NoNewWindow
    $clock = [Diagnostics.Stopwatch]::StartNew()
    while (-not (Test-Path (Join-Path $directory 'post-verdict.signal')) -and -not $child.HasExited -and $clock.ElapsedMilliseconds -lt 15000) { Start-Sleep -Milliseconds 20 }
    if (-not (Test-Path (Join-Path $directory 'post-verdict.signal'))) { throw 'POST_VERDICT_FIXTURE_SIGNAL_MISSING' }
    [IO.File]::WriteAllText((Join-Path $directory 'post-verdict.cancel'), 'cancel')
    if (-not $child.WaitForExit(15000)) { throw 'POST_VERDICT_FIXTURE_CHILD_EXIT_UNKNOWN' }
    $result = Get-Content (Join-Path $directory 'fixture-result.json') -Raw | ConvertFrom-Json
    if ($child.ExitCode -ne 1 -or -not $result.cleanupFixtureRan -or $result.nativeTestsPassed -ne 0 -or $result.nativeTestsRun -ne 1 -or $result.normalCloseVerified -or $result.postVerdictState -cne 'published-interrupted' -or $result.postVerdictElapsedMs -ge 10000) { throw 'POST_VERDICT_FIXTURE_CANCEL_FAILED' }
    # An exhausted budget and canceled grace never acquire a new allowance.
    $budgetRejected = $false
    try { Assert-PostVerdictBudget ([pscustomobject]@{ ElapsedMilliseconds = 10000 }) $directory } catch { $budgetRejected = $true }
    if (-not $budgetRejected) { throw 'POST_VERDICT_FIXTURE_BUDGET_RESET' }
    $nonTarget = [ordered]@{ nativeTestsRun = 1; nativeTestsPassed = 1; normalCloseVerified = $true; failurePhase = $null; failureCode = $null }
    Invoke-PostVerdictObservation ([pscustomobject]@{ HasExited = $true; ExitCode = 0 }) $nonTarget (Join-Path $directory 'output') $directory
    if ($nonTarget.postVerdictState -cne 'rejected' -or $nonTarget.nativeTestsPassed -ne 1 -or $nonTarget.postVerdictElapsedMs -ge 1000) { throw 'POST_VERDICT_FIXTURE_HEALTHY_GRACE' }
    # The exact no-replace API must reject the existing target without mutation.
    $target = Join-Path $directory 'post-verdict.signal'
    $before = Get-PostVerdictHash (Read-PostVerdictBytes $target)
    $duplicate = Join-Path $directory 'duplicate.pending'
    Write-PostVerdictBytes $duplicate ([Text.Encoding]::ASCII.GetBytes('{}'))
    $rejected = $false
    try { [IO.File]::Move($duplicate, $target) } catch { $rejected = $true }
    if (-not $rejected -or (Get-PostVerdictHash (Read-PostVerdictBytes $target)) -cne $before) { throw 'POST_VERDICT_FIXTURE_REPLACE' }
    Write-Output 'POST_VERDICT_CHILD_COOPERATIVE_CANCEL_ONLY_PASSED'
} finally {
    if ($null -eq $child -or $child.HasExited) { Remove-Item -LiteralPath $directory -Recurse -Force }
    # Never kill by PID or claim cleanup if the owned child has not exited.
}
