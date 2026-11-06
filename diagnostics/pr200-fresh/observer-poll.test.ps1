# Synthetic snapshots only. Extract actual functions/poll text; never load OS scripts.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
function Stop-Fixture { [Console]::Out.WriteLine('PROFILE_OBSERVER_POLL_FIXTURE_FAILED'); exit 1 }
try {
    . "$PSScriptRoot/observer-detail.ps1"
    $observer = Join-Path $PSScriptRoot 'observe-profile.ps1'
    $processes = Join-Path $PSScriptRoot '../../ui/tauri/native-smoke/processes.ps1'
    function Load-Functions($Path, $Names) {
        $tokens = $null; $errors = $null
        $ast = [System.Management.Automation.Language.Parser]::ParseFile($Path, [ref]$tokens, [ref]$errors)
        if ($errors.Count -ne 0) { Stop-Fixture }
        $definitions = @($ast.FindAll({ param($n) $n -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -in $Names }, $true))
        if ($definitions.Count -ne $Names.Count) { Stop-Fixture }
        foreach ($definition in $definitions) { . ([scriptblock]::Create($definition.Extent.Text)) }
    }
    . Load-Functions $observer @('New-ObserverFailures', 'Add-ObserverFailure')
    . Load-Functions $processes @('Get-OwnedApp', 'Require-Live', 'Same-Process', 'Test-LineageCreation')
    $text = [IO.File]::ReadAllText($observer)
    $start = $text.IndexOf('    do {', [StringComparison]::Ordinal)
    $last = '    $facts.queryComplete = -not $queryFailed'
    $end = $text.IndexOf($last, [StringComparison]::Ordinal) + $last.Length
    if ($start -lt 0 -or $end -le $start) { Stop-Fixture }
    $poll = [scriptblock]::Create($text.Substring($start, $end - $start))
    # Only OS boundaries are replaced. Already-tracked synthetic view bypasses handles/argv.
    function Get-Descendants($Parent, $Snapshot, $Evidence, $Context, $Stage) {
        if ($Stage -ceq 'driver') { return @($Snapshot | Where-Object Name -eq 'SYNTHETIC_APP') }
        if ($Stage -ceq 'app') { return @($Snapshot | Where-Object Name -eq 'msedgewebview2.exe') }
        Stop-Fixture
    }
    function Get-Process { Stop-Fixture }
    function Test-Path($LiteralPath) {
        if ($LiteralPath -cne 'SYNTHETIC_FINISH') { Stop-Fixture }
        return $script:pollIndex -ge $script:modes.Count
    }
    function Start-Sleep($Milliseconds) { if ($Milliseconds -ne 100) { Stop-Fixture } }
    function Get-Snapshot {
        $script:mode = $script:modes[$script:pollIndex]; $script:pollIndex += 1
        $rows = @(
            [pscustomobject]@{ ProcessId = 1; CreationDate = $script:created; Name = 'SYNTHETIC_DRIVER'; ExecutablePath = (Join-Path $ToolsDirectory 'tauri/bin/tauri-driver.exe').Replace('/', '\') },
            [pscustomobject]@{ ProcessId = 2; CreationDate = $script:created; Name = 'SYNTHETIC_EDGE'; ExecutablePath = (Join-Path $ToolsDirectory 'edge/msedgedriver.exe').Replace('/', '\') },
            [pscustomobject]@{ ProcessId = 3; CreationDate = $script:created; Name = 'SYNTHETIC_APP'; ExecutablePath = $ApplicationPath }
        )
        if ($script:mode -cne 'zero') {
            $view = [pscustomobject]@{ ProcessId = 4; CreationDate = $script:created; Name = 'msedgewebview2.exe'; ExecutablePath = 'SYNTHETIC_VIEW' }
            $view | Add-Member ScriptProperty CommandLine {
                $script:commandReads += 1
                if ($script:mode -ceq 'getter') { throw [Exception]::new('SYNTHETIC_GETTER') }
                if ($script:mode -ceq 'null') { return $null }
                if ($script:mode -ceq 'empty') { return '' }
                return 'SYNTHETIC_COMMAND'
            }
            $rows += $view
        }
        return $rows
    }
    function Invoke-PollCase($Modes) {
        $script:modes = $Modes; $script:pollIndex = 0; $script:commandReads = 0
        $script:created = [DateTime]::new(2000, 1, 1, 0, 0, 0, [DateTimeKind]::Utc)
        $ToolsDirectory = 'SYNTHETIC_TOOLS'; $ApplicationPath = 'SYNTHETIC_APP'; $FinishPath = 'SYNTHETIC_FINISH'
        $lineageSource = 'SYNTHETIC_SOURCE'; $lineageSourceTrusted = $false; $before = @()
        $timer = [pscustomobject]@{ ElapsedMilliseconds = 0 }
        $facts = @{ lineageConfirmed = $false; queryComplete = $false; omittedRows = 0 }
        $tracked = @{ ('4:' + $script:created.Ticks) = 'SYNTHETIC_ALREADY_TRACKED' }
        $queryFailed = $false; $observerFailures = New-ObserverFailures
        . $poll
        return @{ complete = $facts.queryComplete; failed = $queryFailed; ledger = $observerFailures; reads = $script:commandReads; polls = $script:pollIndex }
    }
    $startup = Invoke-PollCase @('zero', 'valid')
    $teardown = Invoke-PollCase @('valid', 'zero')
    foreach ($result in @($startup, $teardown)) {
        if ($result.complete -or -not $result.failed -or $result.polls -ne 2 -or $result.reads -ne 1 -or $result.ledger.failureCount -ne 1 -or $result.ledger.sites[0] -cne 'lineage-query' -or $result.ledger.details[0].code -cne 'unclassified-detail') { Stop-Fixture }
    }
    # Same bounded ledger cannot reconstruct which poll failed; stack stays unknown here.
    if (($startup.ledger | ConvertTo-Json -Depth 5) -cne ($teardown.ledger | ConvertTo-Json -Depth 5)) { Stop-Fixture }
    foreach ($mode in @('null', 'empty', 'getter')) {
        $result = Invoke-PollCase @($mode, 'valid')
        if ($result.complete -or -not $result.failed -or $result.polls -ne 2 -or $result.reads -ne 2 -or $result.ledger.failureCount -ne 1 -or $result.ledger.details[0].code -cne 'metadata-command-line-guard') { Stop-Fixture }
    }
    $valid = Invoke-PollCase @('valid', 'valid')
    if (-not $valid.complete -or $valid.failed -or $valid.polls -ne 2 -or $valid.reads -ne 2 -or $valid.ledger.failureCount -ne 0) { Stop-Fixture }
    [Console]::Out.WriteLine('PROFILE_OBSERVER_POLL_SYNTHETIC_PASS cases=6')
} catch { Stop-Fixture }
