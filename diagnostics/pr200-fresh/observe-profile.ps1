param([Parameter(Mandatory)][string]$SourceDirectory, [Parameter(Mandatory)][string]$ToolsDirectory,
      [Parameter(Mandatory)][string]$ApplicationPath, [Parameter(Mandatory)][string]$OutputPath,
      [Parameter(Mandatory)][string]$ReadyPath, [Parameter(Mandatory)][string]$FinishPath)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. "$SourceDirectory/ui/tauri/native-smoke/processes.ps1"

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
$profile = Normal-Path (Join-Path $ToolsDirectory 'webview-profile')
$facts = [ordered]@{ schema = 1; prelaunchAbsent = $false; parentOwned = $false; parentReparseFree = $false
    queryComplete = $false; lineageConfirmed = $false; allObservedWebviewsCovered = $false; omittedRows = 0; rows = @() }
$tracked = @{}
$queryFailed = $false
$before = $null
try {
    if (-not $IsWindows) { throw 'PROFILE_WINDOWS_REQUIRED' }
    $facts.prelaunchAbsent = -not (Test-Path -LiteralPath $profile)
    $facts.parentOwned = (Normal-Path $ToolsDirectory).StartsWith((Normal-Path $env:RUNNER_TEMP) + '\', [StringComparison]::Ordinal)
    $facts.parentReparseFree = Non-Reparse-Directory $ToolsDirectory
    if (-not $facts.prelaunchAbsent -or -not $facts.parentOwned -or -not $facts.parentReparseFree) { throw 'PROFILE_PRELAUNCH_UNKNOWN' }
    $before = Get-Snapshot
    [IO.File]::WriteAllText($ReadyPath, 'ready')
    $timer = [Diagnostics.Stopwatch]::StartNew()
    do {
        try {
            $snapshot = Get-Snapshot
            if ($snapshot.Count -gt 4096) { throw 'PROFILE_SNAPSHOT_LIMIT' }
            $drivers = @($snapshot | Where-Object ExecutablePath -eq (Join-Path $ToolsDirectory 'tauri/bin/tauri-driver.exe').Replace('/', '\'))
            $edges = @($snapshot | Where-Object ExecutablePath -eq (Join-Path $ToolsDirectory 'edge/msedgedriver.exe').Replace('/', '\'))
            $apps = @($snapshot | Where-Object ExecutablePath -eq $ApplicationPath)
            if ($drivers.Count -gt 1 -or $edges.Count -gt 1 -or $apps.Count -gt 1) { throw 'PROFILE_OWNER_AMBIGUOUS' }
            if ($apps.Count -eq 1) {
                if ($drivers.Count -ne 1 -or $edges.Count -ne 1) { throw 'PROFILE_DRIVER_UNKNOWN' }
                $owned = Get-OwnedApp $drivers[0] $edges[0] $ApplicationPath $snapshot $null $before
                $facts.lineageConfirmed = $true
                foreach ($view in $owned.Webviews) {
                    if (-not (Test-LineageCreation $view) -or -not $view.CommandLine) { throw 'PROFILE_IDENTITY_UNKNOWN' }
                    $key = "$($view.ProcessId):$($view.CreationDate.ToUniversalTime().Ticks)"
                    if ($tracked.ContainsKey($key)) { continue }
                    if ($tracked.Count -ge 32) { $facts.omittedRows += 1; throw 'PROFILE_ROW_LIMIT' }
                    $handle = Get-Process -Id $view.ProcessId -ErrorAction Stop
                    try {
                        $null = $handle.SafeHandle
                        if ($handle.Path -ne $view.ExecutablePath -or $handle.StartTime.ToUniversalTime().ToString('yyyyMMddHHmmss.ffffff') -ne $view.CreationDate.ToUniversalTime().ToString('yyyyMMddHHmmss.ffffff')) { throw 'PROFILE_HANDLE_UNKNOWN' }
                        $argv = [ProfileArgv]::Parse($view.CommandLine)
                        $arguments = @($argv | Where-Object { $_.StartsWith('--user-data-dir=', [StringComparison]::Ordinal) })
                        if ($arguments.Count -ne 1 -or '--user-data-dir' -in $argv) { throw 'PROFILE_ARGUMENT_UNKNOWN' }
                        $actual = Normal-Path $arguments[0].Substring(16)
                        $mapping = if ($actual -ceq $profile) { 'requested-root' } elseif ($actual -ceq (Join-Path $profile 'EBWebView').ToLowerInvariant()) { 'requested-child-ebwebview' } else { 'outside-requested-profile' }
                        $row = [ordered]@{ identityVerified = $true; mapping = $mapping
                            actualDirectoryExists = (Test-Path -LiteralPath $actual -PathType Container)
                            actualPathReparseFree = (Non-Reparse-Directory $actual)
                            actualCanonicalPathMatches = ((Normal-Path (Resolve-Path -LiteralPath $actual).ProviderPath) -ceq $actual)
                            requestedDirectoryCreated = (Test-Path -LiteralPath $profile -PathType Container)
                            requestedPathReparseFree = (Non-Reparse-Directory $profile) }
                        $tracked[$key] = $row
                    } finally { $handle.Dispose() }
                }
            }
        } catch {
            # A failed query during a process-exit race is unknown; never replaced by a later pass.
            $queryFailed = $true
        }
        if (-not (Test-Path -LiteralPath $FinishPath)) { Start-Sleep -Milliseconds 100 }
    } while (-not (Test-Path -LiteralPath $FinishPath) -and $timer.ElapsedMilliseconds -lt 120000)
    if (-not (Test-Path -LiteralPath $FinishPath)) { $queryFailed = $true }
    $facts.queryComplete = -not $queryFailed
} catch { $queryFailed = $true }
finally {
    $facts.rows = @($tracked.Values)
    # Coverage is set only by the controller against the unchanged supervisor's count.
    $facts.queryComplete = ($facts.queryComplete -and -not $queryFailed)
    $facts | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $OutputPath -Encoding utf8
}
if (-not $facts.queryComplete) { exit 1 }
