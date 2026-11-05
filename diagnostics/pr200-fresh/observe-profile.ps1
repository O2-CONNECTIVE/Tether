param([Parameter(Mandatory)][string]$SourceDirectory, [Parameter(Mandatory)][string]$ToolsDirectory,
      [Parameter(Mandatory)][string]$ApplicationPath, [Parameter(Mandatory)][string]$OutputPath,
      [Parameter(Mandatory)][string]$ReadyPath, [Parameter(Mandatory)][string]$FinishPath)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. "$SourceDirectory/ui/tauri/native-smoke/processes.ps1"

# Observation sites only: these codes never classify discarded exception text.
function New-ObserverFailures {
    return [ordered]@{ schema = 1; failureCount = 0; sites = @(); saturated = $false; truncated = $false }
}
function Add-ObserverFailure($Ledger, [string]$Site) {
    $known = @('platform-check', 'profile-path-validation', 'prelaunch-existence', 'owned-parent-path',
        'owned-parent-reparse', 'prelaunch-validation', 'baseline-snapshot', 'ready-marker-write',
        'snapshot-query', 'snapshot-limit', 'candidate-selection', 'candidate-count', 'driver-count',
        'lineage-query', 'webview-metadata', 'row-limit', 'process-handle-open', 'process-handle-identity',
        'argv-parse', 'profile-argument-check', 'observed-path-validation', 'actual-directory-existence',
        'actual-directory-reparse', 'actual-directory-canonical', 'requested-directory-existence',
        'requested-directory-reparse', 'row-record', 'process-handle-dispose', 'finish-marker-query',
        'poll-wait', 'observer-deadline', 'unclassified-site')
    $safeSite = if ($Site -cin $known) { $Site } else { 'unclassified-site' }
    if ($Ledger.failureCount -lt 4096) { $Ledger.failureCount += 1 } else { $Ledger.saturated = $true }
    if ($safeSite -cnotin $Ledger.sites) {
        if ($Ledger.sites.Count -lt 16) { $Ledger.sites += $safeSite } else { $Ledger.truncated = $true }
    }
}

# Native argv parsing; never publish the command line, path, PID, timestamp or exception.
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class ProfileArgv {
  [DllImport("shell32.dll", SetLastError=true, CharSet=CharSet.Unicode)]
  static extern IntPtr CommandLineToArgvW(string command, out int count);
  [DllImport("kernel32.dll")] static extern IntPtr LocalFree(IntPtr memory);
  public static string[] Parse(string command) {
    int count; IntPtr memory = CommandLineToArgvW(command, out count);
    if (memory == IntPtr.Zero) throw new Exception("ARGUMENT_UNKNOWN");
    try {
      if (count < 1 || count > 256) throw new Exception("ARGUMENT_UNKNOWN");
      string[] result = new string[count];
      for (int i=0; i<count; i++) result[i] = Marshal.PtrToStringUni(Marshal.ReadIntPtr(memory, i*IntPtr.Size));
      return result;
    } finally { LocalFree(memory); }
  }
}
'@
function Normal-Path([string]$Value) {
    if ($Value -cnotmatch '^[A-Za-z]:[\\/].+' -or $Value -match '[\x00\r\n"*?<>|]' -or $Value.Substring(2).Contains(':')) { throw 'PROFILE_PATH_UNKNOWN' }
    $value = $Value.Replace('/', '\')
    foreach ($part in $value.Substring(3).Split('\')) {
        if (-not $part -or $part -in @('.', '..') -or $part.EndsWith(' ') -or $part.EndsWith('.')) { throw 'PROFILE_PATH_UNKNOWN' }
    }
    return [IO.Path]::GetFullPath($value).ToLowerInvariant()
}
function Non-Reparse-Directory([string]$Value) {
    $current = Get-Item -LiteralPath $Value -Force -ErrorAction Stop
    if (-not $current.PSIsContainer) { return $false }
    while ($null -ne $current) {
        if (($current.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { return $false }
        $current = $current.Parent
    }
    return $true
}
$profile = $null
$observerFailures = New-ObserverFailures
$site = 'unclassified-site'
$facts = [ordered]@{ schema = 1; prelaunchAbsent = $false; parentOwned = $false; parentReparseFree = $false
    queryComplete = $false; lineageConfirmed = $false; allObservedWebviewsCovered = $false; omittedRows = 0; rows = @() }
$tracked = @{}
$queryFailed = $false
$before = $null
try {
    $site = 'profile-path-validation'
    $profile = Normal-Path (Join-Path $ToolsDirectory 'webview-profile')
    $site = 'platform-check'
    if (-not $IsWindows) { throw 'PROFILE_WINDOWS_REQUIRED' }
    $site = 'prelaunch-existence'
    $facts.prelaunchAbsent = -not (Test-Path -LiteralPath $profile)
    $site = 'owned-parent-path'
    $facts.parentOwned = (Normal-Path $ToolsDirectory).StartsWith((Normal-Path $env:RUNNER_TEMP) + '\', [StringComparison]::Ordinal)
    $site = 'owned-parent-reparse'
    $facts.parentReparseFree = Non-Reparse-Directory $ToolsDirectory
    $site = 'prelaunch-validation'
    if (-not $facts.prelaunchAbsent -or -not $facts.parentOwned -or -not $facts.parentReparseFree) { throw 'PROFILE_PRELAUNCH_UNKNOWN' }
    $site = 'baseline-snapshot'
    $before = Get-Snapshot
    $site = 'ready-marker-write'
    [IO.File]::WriteAllText($ReadyPath, 'ready')
    $timer = [Diagnostics.Stopwatch]::StartNew()
    do {
        try {
            $site = 'snapshot-query'
            $snapshot = Get-Snapshot
            $site = 'snapshot-limit'
            if ($snapshot.Count -gt 4096) { throw 'PROFILE_SNAPSHOT_LIMIT' }
            $site = 'candidate-selection'
            $drivers = @($snapshot | Where-Object ExecutablePath -eq (Join-Path $ToolsDirectory 'tauri/bin/tauri-driver.exe').Replace('/', '\'))
            $edges = @($snapshot | Where-Object ExecutablePath -eq (Join-Path $ToolsDirectory 'edge/msedgedriver.exe').Replace('/', '\'))
            $apps = @($snapshot | Where-Object ExecutablePath -eq $ApplicationPath)
            $site = 'candidate-count'
            if ($drivers.Count -gt 1 -or $edges.Count -gt 1 -or $apps.Count -gt 1) { throw 'PROFILE_OWNER_AMBIGUOUS' }
            if ($apps.Count -eq 1) {
                $site = 'driver-count'
                if ($drivers.Count -ne 1 -or $edges.Count -ne 1) { throw 'PROFILE_DRIVER_UNKNOWN' }
                $site = 'lineage-query'
                $owned = Get-OwnedApp $drivers[0] $edges[0] $ApplicationPath $snapshot $null $before
                $facts.lineageConfirmed = $true
                foreach ($view in $owned.Webviews) {
                    $site = 'webview-metadata'
                    if (-not (Test-LineageCreation $view) -or -not $view.CommandLine) { throw 'PROFILE_IDENTITY_UNKNOWN' }
                    $key = "$($view.ProcessId):$($view.CreationDate.ToUniversalTime().Ticks)"
                    if ($tracked.ContainsKey($key)) { continue }
                    $site = 'row-limit'
                    if ($tracked.Count -ge 32) { $facts.omittedRows += 1; throw 'PROFILE_ROW_LIMIT' }
                    $site = 'process-handle-open'
                    $handle = Get-Process -Id $view.ProcessId -ErrorAction Stop
                    try {
                        $site = 'process-handle-identity'
                        $null = $handle.SafeHandle
                        if ($handle.Path -ne $view.ExecutablePath -or $handle.StartTime.ToUniversalTime().ToString('yyyyMMddHHmmss.ffffff') -ne $view.CreationDate.ToUniversalTime().ToString('yyyyMMddHHmmss.ffffff')) { throw 'PROFILE_HANDLE_UNKNOWN' }
                        $site = 'argv-parse'
                        $argv = [ProfileArgv]::Parse($view.CommandLine)
                        $site = 'profile-argument-check'
                        $arguments = @($argv | Where-Object { $_.StartsWith('--user-data-dir=', [StringComparison]::Ordinal) })
                        if ($arguments.Count -ne 1 -or '--user-data-dir' -in $argv) { throw 'PROFILE_ARGUMENT_UNKNOWN' }
                        $site = 'observed-path-validation'
                        $actual = Normal-Path $arguments[0].Substring(16)
                        $mapping = if ($actual -ceq $profile) { 'requested-root' } elseif ($actual -ceq (Join-Path $profile 'EBWebView').ToLowerInvariant()) { 'requested-child-ebwebview' } else { 'outside-requested-profile' }
                        $site = 'actual-directory-existence'
                        $actualExists = Test-Path -LiteralPath $actual -PathType Container
                        $site = 'actual-directory-reparse'
                        $actualReparseFree = Non-Reparse-Directory $actual
                        $site = 'actual-directory-canonical'
                        $actualCanonical = (Normal-Path (Resolve-Path -LiteralPath $actual).ProviderPath) -ceq $actual
                        $site = 'requested-directory-existence'
                        $requestedCreated = Test-Path -LiteralPath $profile -PathType Container
                        $site = 'requested-directory-reparse'
                        $requestedReparseFree = Non-Reparse-Directory $profile
                        $site = 'row-record'
                        $row = [ordered]@{ identityVerified = $true; mapping = $mapping
                            actualDirectoryExists = $actualExists; actualPathReparseFree = $actualReparseFree
                            actualCanonicalPathMatches = $actualCanonical; requestedDirectoryCreated = $requestedCreated
                            requestedPathReparseFree = $requestedReparseFree }
                        $tracked[$key] = $row
                    } finally {
                        try { $handle.Dispose() } catch { $site = 'process-handle-dispose'; throw }
                    }
                }
            }
        } catch {
            # Site identifies the failed observation, not its OS/runtime cause.
            Add-ObserverFailure $observerFailures $site
            $queryFailed = $true
        }
        $site = 'finish-marker-query'
        if (-not (Test-Path -LiteralPath $FinishPath)) { $site = 'poll-wait'; Start-Sleep -Milliseconds 100 }
        $site = 'finish-marker-query'
    } while (-not (Test-Path -LiteralPath $FinishPath) -and $timer.ElapsedMilliseconds -lt 120000)
    if (-not (Test-Path -LiteralPath $FinishPath)) {
        Add-ObserverFailure $observerFailures 'observer-deadline'
        $queryFailed = $true
    }
    $facts.queryComplete = -not $queryFailed
} catch { Add-ObserverFailure $observerFailures $site; $queryFailed = $true }
finally {
    $facts.rows = @($tracked.Values)
    # Coverage is set only by the controller against the unchanged supervisor's count.
    $facts.queryComplete = ($facts.queryComplete -and -not $queryFailed)
    $facts | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $OutputPath -Encoding utf8
    # A missing/failed sidecar stays unknown at the controller; never alter the native verdict.
    try {
        $observerFailures | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath (Join-Path (Split-Path $OutputPath -Parent) 'observer-failure-sites.json') -Encoding utf8
    } catch { }
}
if (-not $facts.queryComplete) { exit 1 }
