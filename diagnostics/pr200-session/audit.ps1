param([Parameter(Mandatory)][string]$ApplicationPath,
      [Parameter(Mandatory)][string]$ToolsDirectory,
      [Parameter(Mandatory)][string]$HarnessDirectory,
      [Parameter(Mandatory)][string]$OutputPath)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
# Read-only post-cleanup audit. Never kill by name, path, or snapshot.
$facts = [ordered]@{ queryComplete = $false; appRemaining = $null; driversRemaining = $null
    profileProcessesRemaining = $null; harnessNodesRemaining = $null; listenersRemaining = $null }
try {
    $snapshot = @(Get-CimInstance Win32_Process -ErrorAction Stop)
    if ($snapshot.Count -gt 4096) { throw 'AUDIT_SNAPSHOT_LIMIT' }
    $names = @('rescue-host-shell.exe', 'tauri-driver.exe', 'msedgedriver.exe', 'msedgewebview2.exe', 'node.exe')
    if (@($snapshot | Where-Object { $_.Name -in $names -and (-not $_.ExecutablePath -or -not $_.CommandLine) }).Count -ne 0) { throw 'AUDIT_PROCESS_QUERY_INCOMPLETE' }
    $driverPaths = @((Join-Path $ToolsDirectory 'edge/msedgedriver.exe').Replace('/', '\'),
                    (Join-Path $ToolsDirectory 'tauri/bin/tauri-driver.exe').Replace('/', '\'))
    $profile = (Join-Path $ToolsDirectory 'webview-profile').Replace('/', '\')
    $runScript = (Join-Path $HarnessDirectory 'run.ts').Replace('/', '\')
    $facts.appRemaining = @($snapshot | Where-Object ExecutablePath -eq $ApplicationPath).Count
    $facts.driversRemaining = @($snapshot | Where-Object ExecutablePath -in $driverPaths).Count
    $facts.profileProcessesRemaining = @($snapshot | Where-Object {
        $_.CommandLine -and $_.CommandLine.Replace('/', '\').IndexOf($profile, [StringComparison]::OrdinalIgnoreCase) -ge 0
    }).Count
    $facts.harnessNodesRemaining = @($snapshot | Where-Object {
        $_.Name -eq 'node.exe' -and $_.CommandLine -and
        $_.CommandLine.Replace('/', '\').IndexOf($runScript, [StringComparison]::OrdinalIgnoreCase) -ge 0
    }).Count
    $facts.listenersRemaining = @(Get-NetTCPConnection -State Listen -ErrorAction Stop |
        Where-Object LocalPort -in @(4444, 4445)).Count
    $facts.queryComplete = $true
} catch { # Preserve unknowns; the controller must stop.
} finally {
    $facts | ConvertTo-Json | Set-Content -LiteralPath $OutputPath -Encoding utf8
}
if (-not $facts.queryComplete -or $facts.appRemaining -ne 0 -or $facts.driversRemaining -ne 0 -or
    $facts.profileProcessesRemaining -ne 0 -or $facts.harnessNodesRemaining -ne 0 -or $facts.listenersRemaining -ne 0) { exit 1 }
