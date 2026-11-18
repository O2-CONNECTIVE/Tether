# 作りたてのrunnerでは、WebView2 runtimeの初回起動がdiskの初回読込を待ち、WebViewの構築に5〜15秒以上かかる。
# session要求の前にpin済み版のfileを一度だけ読み、OSのcacheへ載せる。読むだけで、runtimeの更新やOS設定の変更はしない。
# 合否・各期限・要求数・再試行は変えない。予算を超えたら読込を打ち切り、未完了として記録する。
function Read-FilesOnce([string]$Root, [int]$BudgetMs) {
    $timer = [Diagnostics.Stopwatch]::StartNew()
    $files = 0
    $bytes = [long]0
    $complete = $true
    foreach ($file in @(Get-ChildItem -LiteralPath $Root -Recurse -File -ErrorAction Stop)) {
        if ($timer.ElapsedMilliseconds -ge $BudgetMs) { $complete = $false; break }
        $stream = [IO.File]::OpenRead($file.FullName)
        try { $stream.CopyTo([IO.Stream]::Null, 1MB) } finally { $stream.Dispose() }
        $files += 1
        $bytes += $file.Length
    }
    [ordered]@{ files = $files; megabytes = [long][Math]::Round($bytes / 1MB); elapsedMs = $timer.ElapsedMilliseconds; complete = $complete }
}
function Read-PinnedRuntime([string]$Version, [int]$BudgetMs) {
    try {
        $entry = Get-ItemProperty 'HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}' -ErrorAction Stop
        $root = Join-Path $entry.location $Version
        if ($entry.pv -ne $Version -or -not (Test-Path -LiteralPath $root -PathType Container)) { throw 'drift' }
        return Read-FilesOnce $root $BudgetMs
    } catch { throw 'NATIVE_RUNTIME_PREWARM_FAILED' }
}
