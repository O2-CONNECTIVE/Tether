param([Parameter(Mandatory)][string]$ToolsDirectory, [Parameter(Mandatory)][string]$BuildDirectory,
    [Parameter(Mandatory)][string]$ApplicationPath, [Parameter(Mandatory)][string]$PrivateDirectory,
    [Parameter(Mandatory)][string]$ResultPath)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$report = [ordered]@{ schema = 1; variant = 'windows-owned-attach-1'; source = (& git rev-parse HEAD).Trim(); testsRun = 0;
    normalCloseVerified = $false; cleanupVerified = $false; cleanupStatus = 'unconfirmed'; passed = $false; failure = 'environment' }
try {
    if (-not $IsWindows -or -not [Environment]::Is64BitProcess) { throw 'unsupported' }
    New-Item -ItemType Directory $PrivateDirectory -ErrorAction Stop | Out-Null
    $tauri = (Resolve-Path "$BuildDirectory/target/release/tauri-driver.exe").Path
    $launcher = (Resolve-Path "$BuildDirectory/owned-attach.exe").Path
    $edge = (Resolve-Path "$ToolsDirectory/edge/msedgedriver.exe").Path
    $app = (Resolve-Path $ApplicationPath).Path
    $node = (Get-Command node.exe -ErrorAction Stop).Source
    $provenance = Get-Content "$BuildDirectory/provenance.json" -Raw | ConvertFrom-Json
    $build = Get-Content "$BuildDirectory/invocation-build.json" -Raw | ConvertFrom-Json
    $pins = Get-Content "$PSScriptRoot/../../ui/tauri/native-smoke/tools.json" -Raw | ConvertFrom-Json
    $hash = { param($file) (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLowerInvariant() }
    $root = (Resolve-Path "$PSScriptRoot/../..").Path
    if ($build.source -cne $report.source -or (& $hash $launcher) -cne $build.launcherSha256) { throw 'build drift' }
    foreach ($property in $build.inputs.PSObject.Properties) {
        if ((& $hash (Join-Path $root $property.Name)) -cne $property.Value) { throw 'input drift' }
    }
    $report.build = $build
    if ((& $hash $tauri) -cne $provenance.binarySha256 -or (& $hash $edge) -cne $pins.driverExeSha256) { throw 'binary drift' }
    if (@(Get-NetTCPConnection -State Listen -ErrorAction Stop | Where-Object LocalPort -in @(4444,4445,9222)).Count -ne 0) { throw 'ports occupied' }
    $report.binarySha256 = [ordered]@{ tauri = (& $hash $tauri); edge = (& $hash $edge); app = (& $hash $app); launcher = (& $hash $launcher); node = (& $hash $node) }
    $report.invocation = 'fixed-ports-4444-4445-9222-fresh-profile-private-pipes-single-session'
    $report.inputs = @{}
    Get-ChildItem $PSScriptRoot -File | Where-Object Extension -in @('.rs','.cs','.ts','.py','.ps1','.patch') | ForEach-Object { $report.inputs[$_.Name] = (& $hash $_.FullName) }
    $report.testsRun = 1
    $report.failure = 'owned-attach-or-normal-close'
    & $launcher $tauri $edge $app (Join-Path $PrivateDirectory 'profile') $node "$PSScriptRoot/attach.ts" "$PSScriptRoot/checks.ts" $PrivateDirectory *> "$PrivateDirectory/launcher.private.log"
    $code = $LASTEXITCODE
    $lines = @(Get-Content "$PrivateDirectory/launcher.private.log")
    $phasePath = Join-Path $PrivateDirectory 'phase.private.txt'
    if (Test-Path $phasePath) {
        $phase = @(Get-Content $phasePath)
        if ($phase.Count -eq 2 -and $phase[0] -cin @('environment','owner-start','prepare','attach','binding-after-attach','webview-and-ipc','normal-close') -and $phase[1] -cin @('cleanup-verified','cleanup-unconfirmed')) {
            $report.failure = $phase[0]
            $report.cleanupStatus = $phase[1]
            $report.cleanupVerified = $phase[1] -ceq 'cleanup-verified'
        }
    }
    $ownerPath = Join-Path $PrivateDirectory 'failure.private.txt'
    if (Test-Path $ownerPath) {
        $ownerFailure = (Get-Content $ownerPath -Raw).Trim()
        $allowed = @('environment','profile','job-create','job-limit','edge-in-app-job','app-create','app-assign','app-membership','app-outer-membership','edge-outer-membership','app-exited','app-resume','job-query','edge-exited','app-exited-before-close','window-query','window-ambiguous','window-changed','window-close','app-close-timeout','app-wait','app-exit-nonzero','job-close-timeout','listener-table','listener-state','listener-nonloopback','listener-open','listener-missing','listener-changed','listener-role','listener-exited','listener-outside-app-job','command-timeout','command-disconnected','command-overflow','command-read','command-sequence','result-overflow','result-write','listener-ready-timeout','listener-lost','listener-close')
        if ($ownerFailure -cmatch '^failure ([a-z0-9-]{1,64}) (cleaned|cleanup-unconfirmed)$' -and $Matches[1] -cin $allowed) {
            $report.ownerFailure = $Matches[1]
            $report.ownerCleanup = $Matches[2]
        }
    }
    if ($code -ne 0 -or $lines.Count -ne 1 -or $lines[0] -cne 'OWNED_ATTACH_EXPERIMENT_PASSED') { throw 'experiment failed' }
    $view = Get-Content "$PrivateDirectory/webview-result.json" -Raw | ConvertFrom-Json
    if ($view.nativeTestsStarted -ne 1 -or $view.checks -ne 3 -or -not $view.actualRustIpc -or $view.keyboard.checks -ne 22 -or $view.browserVersion -cne $pins.runtimeVersion) { throw 'incomplete checks' }
    $report.normalCloseVerified = $true
    $report.cleanupVerified = $true
    $report.cleanupStatus = 'verified'
    $report.passed = $true
    $report.failure = $null
} catch {
    Write-Output 'OWNED_ATTACH_EXPERIMENT_FAILED'
} finally {
    $report | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $ResultPath -Encoding utf8
}
if (-not $report.passed) { exit 1 }
Write-Output 'OWNED_ATTACH_EXPERIMENT_PASSED'
