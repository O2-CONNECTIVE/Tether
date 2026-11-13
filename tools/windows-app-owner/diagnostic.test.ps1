param([Parameter(Mandatory)][string]$OutputPath)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$report = [ordered]@{
    schema = 1; kind = 'app-owner-prerequisite-diagnostic'; source = (& git rev-parse HEAD).Trim()
    firstFailureRun = 42967354064; firstFailureHead = '3b0ec99a367b71d4edfb31ddfd479e7740b30e6c'
    phase = 'environment'; passed = $false; actualAppExecuted = $false; actualAppGate = 'closed'
    primary = $null; cleanup = $null; completedCases = 0; directoryCleanup = 'unconfirmed'
}
$directory = $null
try {
    if (-not $IsWindows -or -not [Environment]::Is64BitProcess) { throw 'unsupported' }
    $report.phase = 'compiler-present'
    $compiler = Join-Path ([Environment]::GetFolderPath('Windows')) 'Microsoft.NET/Framework64/v4.0.30319/csc.exe'
    if (-not (Test-Path -LiteralPath $compiler -PathType Leaf)) { throw 'missing' }
    $directory = Join-Path ([IO.Path]::GetTempPath()) ('tether-app-owner-diagnostic-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $directory -ErrorAction Stop | Out-Null
    $image = Join-Path $directory 'AppOwnerFixture.exe'
    $sources = @('FixtureNative.cs','AppOwner.cs','Program.cs','Diagnostic.cs') | ForEach-Object { Join-Path $PSScriptRoot $_ }
    $sources += @('Native.cs','DescendantNative.cs') | ForEach-Object { Join-Path "$PSScriptRoot/../windows-owner-probe" $_ }
    $report.sourceHashes = @{}
    foreach ($source in $sources) { $report.sourceHashes[(Split-Path $source -Leaf)] = (Get-FileHash $source -Algorithm SHA256).Hash.ToLowerInvariant() }
    $report.phase = 'compile'
    $diagnostics = @(& $compiler /nologo /target:exe /platform:x64 /optimize+ "/out:$image" @sources 2>&1)
    $compileExit = $LASTEXITCODE
    $report.compilerCodes = @($diagnostics | ForEach-Object { if ($_ -cmatch '\berror (CS[0-9]{4}):') { $Matches[1] } } | Select-Object -Unique)
    if ($compileExit -ne 0 -or -not (Test-Path -LiteralPath $image -PathType Leaf)) { throw 'compile' }
    $report.binarySha256 = (Get-FileHash $image -Algorithm SHA256).Hash.ToLowerInvariant()
    $report.phase = 'execute'
    $results = @(& $image 2>&1)
    $executeExit = $LASTEXITCODE
    if ($results.Count -ne 1 -or $results[0].Length -gt 4096) { throw 'result-count' }
    $value = $results[0] | ConvertFrom-Json
    $report.phase = 'validate-result'
    if ($value.schema -ne 1 -or $value.kind -cne 'app-owner-prerequisite-diagnostic' -or $value.passed -isnot [bool] -or $value.completedCases -notin @(0,1,2,3,4)) { throw 'result-schema' }
    if ($value.scenario -cnotin @('not-started','LateChild','ForcedCleanup','CancelBeforeResume','AssignmentDenied') -or $value.phase -cnotin @('entry','prepare','observe','cleanup','outcome')) { throw 'result-classification' }
    function Assert-Facts($item) {
        if ($item.rootExit -cnotin @('unobserved','zero','fixture-failure','forced','other') -or $item.active -cnotin @('unobserved','zero','one','more-than-one') -or $item.wait -cnotin @('unobserved','signaled','timeout','failed','unexpected')) { throw 'result-facts' }
    }
    Assert-Facts $value
    foreach ($name in @('primary','cleanup')) {
        $failure = $value.$name
        if ($null -ne $failure) {
            if ($failure.phase -cnotin @('entry','prepare','observe','cleanup','outcome') -or $failure.category -cnotin @('assertion','managed-exception','access-denied','invalid-handle','invalid-parameter','other')) { throw 'result-failure' }
            $api = @('SetInformationJobObject','QueryInformationJobObject','IsProcessInJob','ResumeThread','SetEvent','DuplicateHandle','CreateProcessW','UpdateProcThreadAttribute','TerminateProcess','TerminateJobObject','CloseHandle','CreateEventW','CreateJobObjectW','GetExitCodeProcess','AssignProcessToJobObject','WaitForSingleObject','managed-operation')
            if ($failure.operation -cnotin $api -and $failure.operation -cnotmatch '^(Create|Prepare|RootExited|WaitEmpty|Run|Observe|Cleanup|Helper|Main|Member|Active)-[1-9][0-9]{0,3}$') { throw 'result-operation' }
            if ($failure.assertion -cne 'managed-operation' -and $failure.assertion -cnotmatch '^(Create|Prepare|RootExited|WaitEmpty|Run|Observe|Cleanup|Helper|Main|Member|Active)-[1-9][0-9]{0,3}$') { throw 'result-assertion' }
            Assert-Facts $failure
            $report[$name] = [ordered]@{ phase = $failure.phase; operation = $failure.operation; category = $failure.category; assertion = $failure.assertion; rootExit = $failure.rootExit; active = $failure.active; wait = $failure.wait }
        }
    }
    $report.scenario = $value.scenario; $report.observationPhase = $value.phase
    $report.completedCases = $value.completedCases
    $report.rootExit = $value.rootExit; $report.active = $value.active; $report.wait = $value.wait
    $report.passed = $executeExit -eq 0 -and $value.passed -and $value.completedCases -eq 4 -and $null -eq $value.primary -and $null -eq $value.cleanup
    $report.phase = 'diagnostic-complete'
} catch {
    $report.passed = $false
} finally {
    if ($null -ne $directory) {
        try { Remove-Item -LiteralPath $directory -Recurse -Force -ErrorAction Stop; $report.directoryCleanup = 'verified' }
        catch { $report.passed = $false }
    }
    $report | ConvertTo-Json -Depth 10 | Set-Content -LiteralPath $OutputPath -Encoding utf8
}
# This script has no actual-app setup/attach continuation, even after diagnostic green.
if (-not $report.passed) { throw 'APP_OWNER_DIAGNOSTIC_FAILED' }
Write-Output 'APP_OWNER_DIAGNOSTIC_COMPLETED_ACTUAL_APP_GATE_CLOSED'
