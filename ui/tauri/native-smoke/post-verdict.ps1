# Dedicated trusted CI job only. No hostile same-user authentication or hard-I/O deadline.
function Assert-PostVerdictBudget($Clock, [string]$Directory) {
    if ($Clock.ElapsedMilliseconds -ge 10000 -or (Test-Path -LiteralPath (Join-Path $Directory 'post-verdict.cancel'))) {
        throw 'POST_VERDICT_BUDGET_OR_CANCEL'
    }
}
function Read-PostVerdictBytes([string]$Path, [int]$Limit = 65536) {
    $item = Get-Item -LiteralPath $Path -Force -ErrorAction Stop
    if ($item.PSIsContainer -or $item.LinkType -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -or $item.Length -le 0 -or $item.Length -gt $Limit) { throw 'POST_VERDICT_FILE' }
    $stream = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::None)
    try {
        if ($stream.Length -ne $item.Length) { throw 'POST_VERDICT_FILE' }
        $bytes = [byte[]]::new([int]$stream.Length)
        $stream.ReadExactly($bytes, 0, $bytes.Length)
        return ,$bytes
    } finally { $stream.Dispose() }
}
function Get-PostVerdictHash([byte[]]$Bytes) {
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($Bytes)).ToLowerInvariant()
}
function Write-PostVerdictBytes([string]$Path, [byte[]]$Bytes) {
    $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try { $stream.Write($Bytes, 0, $Bytes.Length); $stream.Flush($true) }
    finally { $stream.Dispose() }
    if ((Get-PostVerdictHash (Read-PostVerdictBytes $Path)) -cne (Get-PostVerdictHash $Bytes)) { throw 'POST_VERDICT_COPY' }
}
function Initialize-PostVerdictManifest([string]$Manifest, [string]$Directory, [string]$AppPath) {
    $entry = Get-Item -LiteralPath $Directory -Force
    if (-not $entry.PSIsContainer -or ($entry.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'POST_VERDICT_DIRECTORY' }
    $raw = Read-PostVerdictBytes $Manifest 2048
    if ((Get-PostVerdictHash $raw) -cne $env:RESCUE_HOST_POST_VERDICT_MANIFEST_SHA256) { throw 'POST_VERDICT_MANIFEST' }
    $value = [Text.Encoding]::UTF8.GetString($raw) | ConvertFrom-Json
    if ((Get-FileHash -LiteralPath $AppPath -Algorithm SHA256).Hash.ToLowerInvariant() -cne $value.specimenSha256) { throw 'POST_VERDICT_SPECIMEN' }
    Write-PostVerdictBytes (Join-Path $Directory 'post-verdict.manifest.json') $raw
}
function Invoke-PostVerdictObservation($NodeProcess, $Report, [string]$OutputDirectory, [string]$Directory) {
    # Start once BEFORE all new failure admission/copy/hash/flush work. Never reset.
    $clock = [Diagnostics.Stopwatch]::StartNew()
    $state = 'rejected'
    try {
        if ($null -eq $NodeProcess -or -not $NodeProcess.HasExited -or $NodeProcess.ExitCode -eq 0 -or
            $Report.nativeTestsRun -ne 1 -or $Report.nativeTestsPassed -ne 0 -or
            $Report.failurePhase -cne 'webview-and-ipc' -or $Report.failureCode -cne 'NATIVE_WEBVIEW_OR_IPC_ASSERTION_FAILED') { return }
        Assert-PostVerdictBudget $clock $Directory
        foreach ($pair in @(@('native-observation.json', 'post-verdict-native.json'), @('session-startup-timing.json', 'post-verdict-timing.json'))) {
            $bytes = Read-PostVerdictBytes (Join-Path $OutputDirectory $pair[0])
            Assert-PostVerdictBudget $clock $Directory
            Write-PostVerdictBytes (Join-Path $Directory $pair[1]) $bytes
            Assert-PostVerdictBudget $clock $Directory
        }
        # Actual typed reader emits only a closed receipt; non-target copies never yield a signal.
        $text = & python -B "$PSScriptRoot/../../../scripts/post_verdict_failure.py" $Directory 2>$null
        if ($LASTEXITCODE -ne 0 -or $text -is [array]) { throw 'POST_VERDICT_ADMISSION' }
        Assert-PostVerdictBudget $clock $Directory
        $receiptBytes = [Text.Encoding]::ASCII.GetBytes($text)
        Write-PostVerdictBytes (Join-Path $Directory 'post-verdict-failure.json') $receiptBytes
        Assert-PostVerdictBudget $clock $Directory
        $manifestBytes = Read-PostVerdictBytes (Join-Path $Directory 'post-verdict.manifest.json') 2048
        $manifest = [Text.Encoding]::UTF8.GetString($manifestBytes) | ConvertFrom-Json
        # Alphabetical keys, all values closed ASCII strings: exact canonical JSON.
        $signal = [ordered]@{
            controlCommit = $manifest.controlCommit
            failureSha256 = Get-PostVerdictHash $receiptBytes
            manifestSha256 = Get-PostVerdictHash $manifestBytes
            protocol = 'post-verdict-signal-1'
            sourceCommit = $manifest.sourceCommit
            sourceTree = $manifest.sourceTree
            specimenSha256 = $manifest.specimenSha256
        }
        $signalBytes = [Text.Encoding]::ASCII.GetBytes(($signal | ConvertTo-Json -Compress))
        if ($signalBytes.Length -gt 2048) { throw 'POST_VERDICT_SIGNAL' }
        $staging = Join-Path $Directory 'post-verdict.pending'
        Write-PostVerdictBytes $staging $signalBytes
        Assert-PostVerdictBudget $clock $Directory
        # Two-argument File.Move is no-replace, unlike shell Move-Item force behavior.
        [IO.File]::Move($staging, (Join-Path $Directory 'post-verdict.signal'))
        $state = 'published'
        while ($clock.ElapsedMilliseconds -lt 10000) {
            Assert-PostVerdictBudget $clock $Directory
            Start-Sleep -Milliseconds ([Math]::Min(25, 10000 - $clock.ElapsedMilliseconds))
        }
    } catch { $state = if ($state -eq 'published') { 'published-interrupted' } else { 'rejected' } }
    finally {
        $Report.postVerdictState = $state
        $Report.postVerdictElapsedMs = $clock.ElapsedMilliseconds
        $Report.postVerdictBudgetOverrun = $clock.ElapsedMilliseconds -gt 10000
        # Never change nativeTestsRun/Passed, normalCloseVerified or original failure.
    }
}
