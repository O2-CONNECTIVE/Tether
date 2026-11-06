# Same real capture function, synthetic owned rows/handles and injected OS boundaries.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$case = 'load'
$knownCases = @('load','root','child','all-child-slots','outside','nested-child','sibling','null-argv','empty-argv','duplicate-argv','split-argv','quoted-suffix','invalid-path','shuffled-slots','duplicate-slots','missing-handle','duplicate-identity','duplicate-handle','row-limit','pid-reuse','creation-mismatch','creation-unknown','exited-before','exited-during','rights-unknown','release-uncertain','reparse','alias','directory-changed','directory-partial','directory-dispose','new-child','set-unknown','overrun','partial','throw-contained','close-all','close-failure','close-throw','argv-native','native-layout','missing-child-name','missing-child-parent','null-child','missing-entry-slot','missing-entry-identity','null-entry','time-unknown-sticky','set-overrun','final-overrun','borrow-ok','borrow-query-throw','borrow-addref-fail','borrow-release-fail','no-webview','hook-throw-preserves-close','hook-save-failure-preserves-close','hook-success-isolated','hook-overrun-memory-only')
function Fail-OwnedFixture {
    $safe = if ($case -cin $knownCases) { $case } else { 'unclassified-case' }
    [Console]::Out.WriteLine('OWNED_CAPTURE_FIXTURE_FAILED case=' + $safe); exit 1
}
try {
    . "$PSScriptRoot/owned-capture.ps1"
    Initialize-OwnedNative # Compile only; real process/directory APIs are not called.
    function Synthetic-Owned($Scenario) {
        $date = [DateTime]::new(2000,1,1,0,0,0,[DateTimeKind]::Utc)
        $app = [pscustomobject]@{ ProcessId = 100; CreationDate = $date; ExecutablePath = 'SYNTHETIC_APP' }
        $children = @(
            [pscustomobject]@{ ProcessId = 101; ParentProcessId = 100; CreationDate = $date; ExecutablePath = 'SYNTHETIC_WEBVIEW'; Name = 'msedgewebview2.exe' },
            [pscustomobject]@{ ProcessId = 102; ParentProcessId = 101; CreationDate = $date; ExecutablePath = 'SYNTHETIC_CONSOLE'; Name = 'conhost.exe' }
        )
        $script:reads = 0; $script:states = 0; $script:clockCalls = 0; $script:lateOverrun = $false; $script:scenario = $Scenario
        foreach ($child in $children) { $child | Add-Member ScriptProperty CommandLine { $script:reads += 1; if ($script:scenario -ceq 'null-argv') { return $null }; if ($script:scenario -ceq 'empty-argv') { return '' }; return 'SYNTHETIC_COMMAND' } }
        $handles = @([pscustomobject]@{ Slot = 1; Identity = 'verified'; Process = [object]::new() }, [pscustomobject]@{ Slot = 2; Identity = 'verified'; Process = [object]::new() })
        switch ($Scenario) {
            'shuffled-slots' { $handles = @($handles[1], $handles[0]) }
            'duplicate-slots' { $handles[1].Slot = 1 }
            'missing-handle' { $handles = @($handles[0]) }
            'duplicate-identity' { $children[1].ProcessId = 101 }
            'duplicate-handle' { $handles[1].Process = $handles[0].Process }
            'no-webview' { $children[0].Name = 'SYNTHETIC_OTHER' }
            'row-limit' { $children = @($children * 17) }
            'missing-child-name' { $children[0].PSObject.Properties.Remove('Name') }
            'missing-child-parent' { $children[0].PSObject.Properties.Remove('ParentProcessId') }
            'null-child' { $children[0] = $null }
            'missing-entry-slot' { $handles[0].PSObject.Properties.Remove('Slot') }
            'missing-entry-identity' { $handles[0].PSObject.Properties.Remove('Identity') }
            'null-entry' { $handles[0] = $null }
        }
        $ops = @{
            State = { param($c,$h) $script:states += 1; switch ($script:scenario) {
                'pid-reuse' { return 'identity-mismatch' }; 'creation-mismatch' { return 'creation-mismatch' }; 'creation-unknown' { return 'creation-unknown' }
                'exited-before' { return 'exited' }; 'exited-during' { if ($script:states -gt 2) { return 'exited' } }
                'rights-unknown' { return 'unavailable' }; 'release-uncertain' { return 'handle-release-uncertain' }
                'final-overrun' { if ($script:states -eq 3) { $script:lateOverrun = $true } }
                'partial' { if ($c.ProcessId -eq 102) { return 'unavailable' } }; 'throw-contained' { throw 'SYNTHETIC_NOT_PUBLISHED' }
            }; return 'live' }
            Argv = { param($command) switch ($script:scenario) {
                'duplicate-argv' { return @('view','--user-data-dir=C:\synthetic\profile','--user-data-dir=C:\synthetic\profile') }
                'split-argv' { return @('view','--user-data-dir','C:\synthetic\profile') }
                'quoted-suffix' { return @('view','--user-data-dir="C:\synthetic\profile"suffix') }
                'invalid-path' { return @('view','--user-data-dir=relative') }
                'outside' { return @('view','--user-data-dir=C:\other') }; 'nested-child' { return @('view','--user-data-dir=C:\synthetic\profile\EBWebView\nested') }
                'sibling' { return @('view','--user-data-dir=C:\synthetic\profilesuffix') }
                'child' { return @('view','--user-data-dir=C:\synthetic\profile\EBWebView') }
            }; return @('view','--user-data-dir=C:\synthetic\profile') }
            Directories = { param($r,$a) switch ($script:scenario) {
                'reparse' { return 'directory-reparse' }; 'alias' { return 'directory-alias' }; 'directory-changed' { return 'directory-changed' }
                'directory-partial' { return 'directory-unavailable' }; 'directory-dispose' { return 'directory-dispose-uncertain' }
            }; return 'ok' }
            Set = { param($c,$a) if ($script:scenario -ceq 'set-overrun') { $script:lateOverrun = $true }; if ($script:scenario -ceq 'new-child') { return 'changed' }; if ($script:scenario -ceq 'set-unknown') { return 'unknown' }; return 'unchanged' }
            Elapsed = { param($timer) $script:clockCalls += 1; if ($script:scenario -ceq 'time-unknown-sticky' -and $script:clockCalls -eq 1) { throw 'SYNTHETIC_TIME' }; if ($script:scenario -ceq 'overrun' -or $script:lateOverrun) { return 2001 }; return 0 }
        }
        $record = Invoke-OwnedCollectionCapture $children $app $handles 'C:\synthetic\profile' $ops
        return @{ record = $record; reads = $script:reads; states = $script:states; handles = $handles }
    }
    foreach ($case in $knownCases | Where-Object { $_ -notin @('load','close-all','close-failure','close-throw','argv-native','native-layout','borrow-ok','borrow-query-throw','borrow-addref-fail','borrow-release-fail','hook-throw-preserves-close','hook-save-failure-preserves-close','hook-success-isolated','hook-overrun-memory-only') }) {
        $out = Synthetic-Owned $case; $record = $out.record
        $accepted = $case -cin @('root','child','all-child-slots')
        if ($record.complete -ne $accepted -or $record.originalGatesChanged -or $record.atomicNamespace) { Fail-OwnedFixture }
        if ($record.rows.Count -ne [Math]::Min(32,$record.childrenExpected)) { Fail-OwnedFixture }
        for ($i = 0; $i -lt $record.rows.Count; $i += 1) { if ($record.rows[$i].slot -ne $i + 1) { Fail-OwnedFixture } }
        if ($accepted -and ($out.reads -ne 1 -or $out.states -ne 4 -or $record.rows[1].role -cne 'console-host' -or $record.rows[1].parentSlot -ne 1)) { Fail-OwnedFixture }
        if ($case -ceq 'child' -and $record.rows[0].mapping -cne 'exact-runtime-ebwebview-child') { Fail-OwnedFixture }
        if ($case -ceq 'exited-before' -and ($record.rows[0].identityBound -ne $true -or $record.rows[0].before -cne 'exited')) { Fail-OwnedFixture }
        if ($case -ceq 'exited-during' -and $record.rows[0].after -cne 'exited') { Fail-OwnedFixture }
        if ($case -ceq 'row-limit' -and $record.omittedRows -ne 2) { Fail-OwnedFixture }
        if ($case -cin @('overrun','time-unknown-sticky','set-overrun','final-overrun') -and -not $record.overrun) { Fail-OwnedFixture }
        if ($case -ceq 'set-overrun' -and $out.states -ne 2) { Fail-OwnedFixture }
        if ($case -ceq 'final-overrun' -and $out.states -ne 3) { Fail-OwnedFixture }
        # The record must not retain any synthetic identity/command/path or arbitrary exception.
        $json = $record | ConvertTo-Json -Depth 6
        if ($json.Contains('SYNTHETIC') -or $json.Contains('C:\') -or $json.Contains('ProcessId')) { Fail-OwnedFixture }
    }
    foreach ($case in @('close-all','close-failure','close-throw')) {
        $script:closed = 0; $list = [Collections.Generic.List[IntPtr]]::new()
        foreach ($value in 1..3) { $list.Add([IntPtr]::new($value)) }
        $close = [Func[IntPtr,bool]]{ param($h) $script:closed += 1; if ($case -ceq 'close-throw' -and $h.ToInt64() -eq 2) { throw 'SYNTHETIC_CLOSE' }; return -not ($case -ceq 'close-failure' -and $h.ToInt64() -eq 2) }
        $ok = [OwnedCaptureNative]::CloseHandles($list,$close)
        if ($script:closed -ne 3 -or $ok -ne ($case -ceq 'close-all')) { Fail-OwnedFixture }
    }
    foreach ($case in @('borrow-ok','borrow-query-throw','borrow-addref-fail','borrow-release-fail')) {
        $script:borrowAdds = 0; $script:borrowReleases = 0; $script:originalDisposes = 0
        $fake = [pscustomobject]@{}
        $fake | Add-Member ScriptMethod DangerousAddRef { param([ref]$added) $script:borrowAdds += 1; if ($case -ceq 'borrow-addref-fail') { throw 'SYNTHETIC_PIN' }; $added.Value = $true }
        $fake | Add-Member ScriptMethod DangerousGetHandle { return [IntPtr]::new(1) }
        $fake | Add-Member ScriptMethod DangerousRelease { $script:borrowReleases += 1; if ($case -ceq 'borrow-release-fail') { throw 'SYNTHETIC_RELEASE' } }
        $fake | Add-Member ScriptMethod Dispose { $script:originalDisposes += 1 }
        $date = [DateTime]::new(2000,1,1,0,0,0,[DateTimeKind]::Utc)
        $child = [pscustomobject]@{ ProcessId = 1; CreationDate = $date; ExecutablePath = 'SYNTHETIC_IMAGE' }
        $entry = [pscustomobject]@{ Process = [pscustomobject]@{ StartTime = $date; SafeHandle = $fake } }
        $native = { param($raw,$expectedProcessId,$created,$image) if ($case -ceq 'borrow-query-throw') { throw 'SYNTHETIC_QUERY' }; return 'live' }
        $state = Read-OwnedHandle $child $entry $native
        $expected = switch ($case) { 'borrow-ok' { 'live' }; 'borrow-release-fail' { 'handle-release-uncertain' } default { 'unavailable' } }
        if ($state -cne $expected -or $script:borrowAdds -ne 1 -or $script:borrowReleases -ne $(if ($case -ceq 'borrow-addref-fail') { 0 } else { 1 }) -or $script:originalDisposes -ne 0) { Fail-OwnedFixture }
    }
    $case = 'argv-native'
    $argv = [OwnedCaptureNative]::Argv('view --user-data-dir="C:\synthetic profile"')
    if ($argv.Count -ne 2 -or $argv[1] -cne '--user-data-dir=C:\synthetic profile') { Fail-OwnedFixture }
    $case = 'native-layout'
    $info = [OwnedCaptureNative].GetNestedType('Info',[Reflection.BindingFlags]::NonPublic)
    if ([Runtime.InteropServices.Marshal]::SizeOf([Type]$info) -ne 52 -or [Runtime.InteropServices.Marshal]::OffsetOf($info,'Creation').ToInt32() -ne 4) { Fail-OwnedFixture }
    # Exercise the actual generated hook statement with failures; never run the supervisor.
    $tokens = $null; $errors = $null
    $overlayAst = [System.Management.Automation.Language.Parser]::ParseFile((Join-Path $env:OWNED_OVERLAY_DIRECTORY 'supervise.ps1'), [ref]$tokens, [ref]$errors)
    if ($errors.Count -ne 0) { Fail-OwnedFixture }
    $nodes = @($overlayAst.FindAll({ param($n) $n -is [System.Management.Automation.Language.IfStatementAst] -and $n.Extent.Text.Contains('param($OwnedChildren, $OwnedApp, $OwnedHandles, $OwnedRoot, $OwnedOutput)') }, $true))
    if ($nodes.Count -ne 1) { Fail-OwnedFixture }
    $hook = [scriptblock]::Create($nodes[0].Extent.Text)
    $persistNodes = @($overlayAst.FindAll({ param($n) $n -is [System.Management.Automation.Language.TryStatementAst] -and $null -ne $n.Finally -and $n.Finally.Extent.Text.Contains('Save-OwnedCaptureRecord $ownedCaptureState $OutputDirectory') }, $true))
    if ($persistNodes.Count -ne 1) { Fail-OwnedFixture }
    $persistText = $persistNodes[0].Finally.Extent.Text
    $persist = [scriptblock]::Create($persistText.Substring(1, $persistText.Length - 2))
    function Invoke-OwnedCollectionCapture {
        if ($case -ceq 'hook-throw-preserves-close') { throw 'SYNTHETIC_HOOK' }
        $value = New-OwnedCaptureRecord 0
        if ($case -ceq 'hook-overrun-memory-only') { $value.complete = $true; $value.overrun = $true; $value.code = 'overrun' }
        return $value
    }
    function Save-OwnedCaptureRecord { param($record,$directory) $script:saves += 1; if ($case -ceq 'hook-save-failure-preserves-close') { throw 'SYNTHETIC_SAVE' } }
    function Write-OwnedPersistStatus { param($status) $script:persistStatus = $status }
    $previousOptIn = $env:RESCUE_OWNED_CAPTURE
    try {
        $env:RESCUE_OWNED_CAPTURE = '1'
        foreach ($case in @('hook-throw-preserves-close','hook-save-failure-preserves-close','hook-success-isolated','hook-overrun-memory-only')) {
            $children = @(); $app = $null; $childHandles = @(); $OutputDirectory = 'SYNTHETIC_OUTPUT'; $env:NATIVE_PROFILE = 'SYNTHETIC_PROFILE'
            $report = @{ failureCode = 'ORIGINAL_FAILURE'; continue = $false }; $script:saves = 0; $script:persistStatus = 'not-observed'; $closeContinued = 0; $cleanupContinued = 0
            $ownedCaptureState = $null
            . $hook
            if ($script:saves -ne 0) { Fail-OwnedFixture }
            if ($case -ceq 'hook-overrun-memory-only' -and ($ownedCaptureState.complete -or -not $ownedCaptureState.overrun)) { Fail-OwnedFixture }
            $closeContinued += 1; $cleanupContinued += 1
            . $persist
            if ($closeContinued -ne 1 -or $cleanupContinued -ne 1 -or $report.failureCode -cne 'ORIGINAL_FAILURE' -or $report.continue -or $children.Count -ne 0 -or $childHandles.Count -ne 0) { Fail-OwnedFixture }
            if ($script:saves -ne 1 -or $script:persistStatus -cne $(if ($case -ceq 'hook-save-failure-preserves-close') { 'uncertain' } else { 'ok' })) { Fail-OwnedFixture }
        }
    } finally { $env:RESCUE_OWNED_CAPTURE = $previousOptIn }
    [Console]::Out.WriteLine('OWNED_CAPTURE_FIXTURE_PASS cases=58')
} catch { Fail-OwnedFixture }
