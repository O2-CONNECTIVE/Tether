# Explicit diagnostic overlay only. No ownership decision or original verdict mutation.
function Initialize-OwnedNative {
    if (-not ('OwnedCaptureNative' -as [type])) { Add-Type -TypeDefinition ([IO.File]::ReadAllText((Join-Path $PSScriptRoot 'owned-native.cs'))) -ErrorAction Stop }
}
function New-OwnedCaptureRecord($Count = $null) {
    return [ordered]@{ schema = 1; instrumented = $true; originalGatesChanged = $false; point = 'owned-before-close'
        budgetMs = 2000; elapsedMs = 0; elapsedClamped = $false; overrun = $false
        directoryGuarantee = 'per-row-handle-boundaries-nonatomic'; atomicNamespace = $false
        childrenExpected = $Count; omittedRows = $(if ($null -eq $Count) { $null } else { [Math]::Max(0, $Count - 32) })
        rows = @(); complete = $false; code = 'hook-not-reached'; setAgreement = 'not-observed' }
}
function Convert-OwnedPath($Value) {
    if ($Value -isnot [string] -or $Value.Length -gt 32768 -or $Value -cnotmatch '^[A-Za-z]:[\\/].+' -or $Value -match '[\x00\r\n"*?<>|]' -or $Value.Substring(2).Contains(':')) { return $null }
    $value = $Value.Replace('/', '\')
    foreach ($part in $value.Substring(3).Split('\')) { if (-not $part -or $part -in @('.', '..') -or $part -match '[ .]$') { return $null } }
    return $value.ToLowerInvariant()
}
function Read-OwnedHandle($Child, $Entry, $NativeState = $null) {
    $safe = $null; $added = $false; $state = 'unavailable'
    try {
        if ($Child.CreationDate -isnot [DateTime]) { return 'creation-unknown' }
        $process = $Entry.Process
        $start = $process.StartTime
        # Preserve the original microsecond CIM contract. Exact full time is bound to the same held handle below.
        if ($start.ToUniversalTime().ToString('yyyyMMddHHmmss.ffffff') -cne $Child.CreationDate.ToUniversalTime().ToString('yyyyMMddHHmmss.ffffff')) { return 'creation-mismatch' }
        $safe = $process.SafeHandle
        $safe.DangerousAddRef([ref]$added)
        if ($null -eq $NativeState) { $state = [OwnedCaptureNative]::State($safe.DangerousGetHandle(), [uint32]$Child.ProcessId, $start.ToFileTimeUtc(), $Child.ExecutablePath) }
        else { $state = & $NativeState ($safe.DangerousGetHandle()) ([uint32]$Child.ProcessId) ($start.ToFileTimeUtc()) $Child.ExecutablePath }
    } catch { $state = 'unavailable' }
    finally {
        if ($added) { try { $safe.DangerousRelease() } catch { $state = 'handle-release-uncertain' } }
    }
    return $state
}
function Read-OwnedSetAgreement($Children, $App) {
    try {
        $current = @(Get-CimInstance Win32_Process -OperationTimeoutSec 1 -ErrorAction Stop)
        if ($current.Count -gt 4096) { return 'unknown' }
        $appRows = @($current | Where-Object { Same-Process $App $_ })
        if ($appRows.Count -ne 1) { return 'changed' }
        # Change detection only. Never adopts this second set as an ownership authority.
        $now = @(Get-Descendants $App.ProcessId $current)
        if ($now.Count -ne $Children.Count) { return 'changed' }
        foreach ($child in $Children) {
            $same = @($now | Where-Object { Same-Process $child $_ })
            if ($same.Count -ne 1 -or $same[0].ExecutablePath -cne $child.ExecutablePath) { return 'changed' }
        }
        return 'unchanged'
    } catch { return 'unknown' }
}
function New-OwnedOperations {
    return @{
        State = { param($child, $entry) Read-OwnedHandle $child $entry }
        Argv = { param($text) [OwnedCaptureNative]::Argv($text) }
        Directories = { param($root, $actual) [OwnedCaptureNative]::Directories($root, $actual) }
        Set = { param($children, $app) Read-OwnedSetAgreement $children $app }
        Elapsed = { param($timer) $timer.ElapsedMilliseconds }
    }
}
function Update-OwnedTime($Data, $Operations, $Timer) {
    try {
        $elapsed = & $Operations.Elapsed $Timer
        if ($elapsed -isnot [long] -and $elapsed -isnot [int]) { throw 'OWNED_TIME_UNKNOWN' }
        if ($elapsed -lt 0) { throw 'OWNED_TIME_UNKNOWN' }
        $Data.elapsedClamped = $elapsed -gt 60000
        $Data.elapsedMs = [int][Math]::Min(60000, $elapsed)
        $Data.overrun = $Data.overrun -or $elapsed -gt $Data.budgetMs
    } catch { $Data.overrun = $true }
    if ($Data.overrun) { $Data.complete = $false; if ($Data.code -in @('hook-not-reached', 'ok')) { $Data.code = 'overrun' } }
    return -not $Data.overrun
}
function Invoke-OwnedCollectionCapture($Children, $App, $Handles, [string]$ExpectedRoot, $Operations = $null) {
    $timer = [Diagnostics.Stopwatch]::StartNew()
    $childrenArray = @($Children); $entries = @($Handles)
    $data = New-OwnedCaptureRecord $(if ($childrenArray.Count -le 4096) { $childrenArray.Count } else { $null })
    if ($null -eq $Operations) { $Operations = New-OwnedOperations }
    try {
        # Allocate slot-only skeletons BEFORE touching any row/handle metadata.
        for ($i = 0; $i -lt [Math]::Min(32, $childrenArray.Count); $i += 1) {
            $data.rows += [ordered]@{ slot = $i + 1; parentSlot = -1; role = 'other'
                originalHandleVerified = $false; identityBound = $null; before = 'not-observed'; after = 'not-observed'
                argvStatus = 'not-observed'; mapping = 'unknown'; directoryStatus = 'not-observed'; code = 'not-observed' }
        }
        $data.code = 'invalid-set'
        if ($childrenArray.Count -lt 1 -or $childrenArray.Count -gt 32 -or $entries.Count -ne $childrenArray.Count) { return $data }
        for ($i = 0; $i -lt $childrenArray.Count; $i += 1) {
            $child = $childrenArray[$i]; $row = $data.rows[$i]
            $row.role = switch ($child.Name) { 'msedgewebview2.exe' { 'webview' } 'conhost.exe' { 'console-host' } default { 'other' } }
            if ($child.ParentProcessId -eq $App.ProcessId) { $row.parentSlot = 0 }
            else {
                for ($j = 0; $j -lt $childrenArray.Count; $j += 1) {
                    if ($childrenArray[$j].ProcessId -eq $child.ParentProcessId) {
                        if ($row.parentSlot -ne -1) { $row.parentSlot = -1; break }
                        $row.parentSlot = $j + 1
                    }
                }
            }
            $row.originalHandleVerified = $entries[$i].Identity -ceq 'verified'
        }
        $ids = [Collections.Generic.HashSet[int]]::new()
        for ($i = 0; $i -lt $childrenArray.Count; $i += 1) {
            $child = $childrenArray[$i]; $entry = $entries[$i]
            if ($child.ProcessId -isnot [int] -and $child.ProcessId -isnot [uint32]) { return $data }
            if (-not $ids.Add([int]$child.ProcessId)) { $data.code = 'duplicate-identity'; return $data }
            if ($entry.Slot -isnot [int] -or $entry.Slot -ne $i + 1 -or $entry.Identity -cne 'verified' -or $null -eq $entry.Process -or $data.rows[$i].parentSlot -lt 0) { $data.code = 'slot-mismatch'; return $data }
            for ($j = 0; $j -lt $i; $j += 1) { if ([object]::ReferenceEquals($entry.Process, $entries[$j].Process)) { $data.code = 'duplicate-identity'; return $data } }
        }
        if (@($data.rows | Where-Object role -eq 'webview').Count -lt 1) { $data.code = 'invalid-set'; return $data }
        $root = Convert-OwnedPath $ExpectedRoot
        if ($null -eq $root) { $data.code = 'path-invalid'; return $data }
        $data.code = 'ok'
        for ($i = 0; $i -lt $childrenArray.Count; $i += 1) {
            if (-not (Update-OwnedTime $data $Operations $timer)) { return $data }
            $row = $data.rows[$i]; $child = $childrenArray[$i]
            $state = & $Operations.State $child $entries[$i]
            if ($state -cnotin @('live', 'exited', 'identity-mismatch', 'unavailable', 'creation-unknown', 'creation-mismatch', 'handle-release-uncertain')) { $state = 'unavailable' }
            $row.before = $state; $row.identityBound = $(if ($state -in @('live', 'exited')) { $true } elseif ($state -in @('identity-mismatch', 'creation-mismatch')) { $false } else { $null })
            if ($state -cne 'live') { $row.code = $(if ($state -ceq 'exited') { 'exited-before' } else { $state }); $data.code = $row.code; return $data }
            if (-not (Update-OwnedTime $data $Operations $timer)) { return $data }
            if ($row.role -cne 'webview') { $row.argvStatus = 'not-applicable'; $row.mapping = 'not-applicable'; $row.directoryStatus = 'not-applicable'; $row.code = 'ok'; continue }
            # Once for this hook, from the original authorized row only; old diagnostics may read it later.
            $command = $child.CommandLine
            if ($command -isnot [string] -or -not $command) { $row.code = 'argv-missing'; $data.code = $row.code; return $data }
            $row.argvStatus = 'unknown'
            try { $argv = @(& $Operations.Argv $command) } catch { $row.code = 'argv-unavailable'; $data.code = $row.code; return $data }
            $arguments = @($argv | Where-Object { $_ -is [string] -and $_.StartsWith('--user-data-dir=', [StringComparison]::Ordinal) })
            if ($argv.Count -gt 256 -or $arguments.Count -ne 1 -or '--user-data-dir' -in $argv) { $row.argvStatus = 'ambiguous'; $row.code = 'argv-ambiguous'; $data.code = $row.code; return $data }
            $row.argvStatus = 'single'
            $actual = Convert-OwnedPath $arguments[0].Substring(16)
            if ($null -eq $actual) { $row.code = 'path-invalid'; $data.code = $row.code; return $data }
            $row.mapping = if ($actual -ceq $root) { 'requested-root' } elseif ($actual -ceq ($root + '\ebwebview')) { 'exact-runtime-ebwebview-child' } else { 'outside' }
            if ($row.mapping -ceq 'outside') { $row.code = 'profile-outside'; $data.code = $row.code; return $data }
            if (-not (Update-OwnedTime $data $Operations $timer)) { return $data }
            $directory = & $Operations.Directories $root $actual
            if ($directory -cnotin @('ok','directory-unavailable','directory-reparse','directory-alias','directory-changed','directory-limit','directory-dispose-uncertain')) { $directory = 'directory-unavailable' }
            $row.directoryStatus = $directory; $row.code = $directory
            if ($directory -cne 'ok') { $data.code = $directory; return $data }
        }
        if (-not (Update-OwnedTime $data $Operations $timer)) { return $data }
        $agreement = & $Operations.Set $childrenArray $App
        $data.setAgreement = if ($agreement -cin @('unchanged','changed','unknown')) { $agreement } else { 'unknown' }
        if (-not (Update-OwnedTime $data $Operations $timer)) { return $data }
        if ($data.setAgreement -cne 'unchanged') { $data.code = if ($data.setAgreement -ceq 'changed') { 'set-changed' } else { 'set-unknown' }; return $data }
        for ($i = 0; $i -lt $childrenArray.Count; $i += 1) {
            if (-not (Update-OwnedTime $data $Operations $timer)) { return $data }
            $row = $data.rows[$i]; $state = & $Operations.State $childrenArray[$i] $entries[$i]
            if ($state -cnotin @('live','exited','identity-mismatch','unavailable','creation-unknown','creation-mismatch','handle-release-uncertain')) { $state = 'unavailable' }
            $row.after = $state
            if ($state -cne 'live') { if ($state -in @('identity-mismatch','creation-mismatch')) { $row.identityBound = $false }; $row.code = if ($state -ceq 'exited') { 'exited-during' } else { $state }; $data.code = $row.code; return $data }
        }
        if (-not (Update-OwnedTime $data $Operations $timer)) { return $data }
        $data.complete = $true
    } catch { $data.complete = $false; $data.code = 'hook-failed' }
    finally { $null = Update-OwnedTime $data $Operations $timer }
    return $data
}
