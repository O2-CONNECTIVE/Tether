# Synthetic location/ledger fixtures only. No CIM, native argv, process or supervisor calls.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. "$PSScriptRoot/observer-detail.ps1"
$source = 'PRIVATE_SOURCE'
foreach ($case in @(
    @('Stop-UnconfirmedLineage', 73, 'Get-Descendants', 77, 'lineage-parent-row-guard-site'),
    @('Stop-UnconfirmedLineage', 73, 'Get-Descendants', 78, 'lineage-parent-creation-guard-site'),
    @('Stop-UnconfirmedLineage', 73, 'Get-Descendants', 88, 'lineage-child-row-guard-site'),
    @('Stop-UnconfirmedLineage', 73, 'Get-Descendants', 90, 'lineage-child-creation-guard-site'),
    @('Stop-UnconfirmedLineage', 73, 'Get-Descendants', 92, 'lineage-child-order-guard-site'),
    @('Require-Live', 7, 'Get-OwnedApp', 113, 'lineage-driver-live-check-site'),
    @('Require-Live', 7, 'Get-OwnedApp', 114, 'lineage-edge-live-check-site')
)) {
    $stack = "at $($case[0]), ${source}: line $($case[1])`nat $($case[2]), ${source}: line $($case[3])"
    if ((Resolve-LineageFailure $stack $source $true) -cne $case[4]) { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=SYNTHETIC_LOCATION_FAILED') } catch { }; throw 'SYNTHETIC_LOCATION_FAILED' }
    if ((Resolve-LineageFailure $stack $source $false) -cne 'unclassified-detail') { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=UNTRUSTED_SOURCE_ACCEPTED') } catch { }; throw 'UNTRUSTED_SOURCE_ACCEPTED' }
}
foreach ($stack in @('PRIVATE_EXCEPTION', "at Get-OwnedApp, ${source}: line 999", "at Get-OwnedApp, OTHER_SOURCE: line 118", ('x' * 16385))) {
    if ((Resolve-LineageFailure $stack $source $true) -cne 'unclassified-detail') { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=UNKNOWN_LOCATION_ACCEPTED') } catch { }; throw 'UNKNOWN_LOCATION_ACCEPTED' }
}
$tokens = $null; $errors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot 'observe-profile.ps1'), [ref]$tokens, [ref]$errors)
if ($errors.Count -ne 0) { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=OBSERVER_SOURCE_SYNTAX_FAILED') } catch { }; throw 'OBSERVER_SOURCE_SYNTAX_FAILED' }
$functions = @($ast.FindAll({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -in @('New-ObserverFailures', 'Add-ObserverFailure') }, $true))
if ($functions.Count -ne 2) { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=LEDGER_SEAM_MISSING') } catch { }; throw 'LEDGER_SEAM_MISSING' }
foreach ($definition in $functions) { . ([scriptblock]::Create($definition.Extent.Text)) }
$ledger = New-ObserverFailures
if (@(Add-ObserverFailure $ledger 'webview-metadata' 'metadata-creation-guard').Count -ne 0) { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=DETAIL_UNEXPECTED_OUTPUT') } catch { }; throw 'DETAIL_UNEXPECTED_OUTPUT' }
Add-ObserverFailure $ledger 'webview-metadata' 'metadata-creation-guard'
Add-ObserverFailure $ledger 'webview-metadata' 'metadata-command-line-guard'
if ($ledger.failureCount -ne 3 -or $ledger.details.Count -ne 2) { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=DETAIL_DEDUP_FAILED') } catch { }; throw 'DETAIL_DEDUP_FAILED' }
Add-ObserverFailure $ledger 'lineage-query' 'PRIVATE_EXCEPTION'
if ($ledger.details[-1].code -cne 'unclassified-detail' -or ($ledger | ConvertTo-Json -Depth 5).Contains('PRIVATE')) { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=DETAIL_REDACTION_FAILED') } catch { }; throw 'DETAIL_REDACTION_FAILED' }
$ledger = New-ObserverFailures
foreach ($code in @('lineage-parent-row-guard-site', 'lineage-parent-creation-guard-site', 'lineage-child-row-guard-site',
    'lineage-child-creation-guard-site', 'lineage-child-order-guard-site', 'lineage-driver-live-check-site',
    'lineage-edge-live-check-site', 'lineage-reference-check-site', 'lineage-driver-descendants-call-site',
    'lineage-app-selection-site', 'lineage-app-count-check-site', 'lineage-app-descendants-call-site',
    'lineage-webview-selection-site', 'lineage-webview-count-check-site', 'lineage-root-selection-site',
    'lineage-root-count-check-site', 'lineage-root-creation-check-site')) { Add-ObserverFailure $ledger 'lineage-query' $code }
if ($ledger.details.Count -ne 16 -or -not $ledger.truncated -or $ledger.failureCount -ne 17) { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=DETAIL_BOUND_FAILED') } catch { }; throw 'DETAIL_BOUND_FAILED' }
# Execute the actual metadata guard text against synthetic getters. This tests
# short-circuit/read/error behavior without loading processes.ps1 or any OS API.
$text = [IO.File]::ReadAllText((Join-Path $PSScriptRoot 'observe-profile.ps1'))
$start = $text.IndexOf("                    `$detail = 'metadata-creation-check-exception'", [StringComparison]::Ordinal)
$end = $text.IndexOf("                    `$detail = 'metadata-identity-key-exception'", [StringComparison]::Ordinal)
if ($start -lt 0 -or $end -le $start) { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=METADATA_GUARD_SEAM_MISSING') } catch { }; throw 'METADATA_GUARD_SEAM_MISSING' }
$after = [scriptblock]::Create($text.Substring($start, $end - $start))
$before = { if (-not (Test-LineageCreation $view) -or -not $view.CommandLine) { throw 'PROFILE_IDENTITY_UNKNOWN' } }
function Test-LineageCreation($Entry) {
    $script:creationCalls += 1
    if ($script:creationMode -eq 'error') { throw $script:sentinel }
    return $script:creationMode -eq 'true'
}
function Invoke-SyntheticGuard($Block, $Creation, $Command, $CommandError) {
    $script:creationMode = $Creation; $script:commandValue = $Command; $script:commandError = $CommandError
    $script:creationCalls = 0; $script:reads = 0
    $view = [pscustomobject]@{}
    $view | Add-Member ScriptProperty CommandLine {
        $script:reads += 1
        if ($script:commandError) { throw $script:sentinel }
        return $script:commandValue
    }
    $detail = 'not-observed'; $caught = $null
    try { . $Block } catch { $caught = $_ }
    return @{ caught = $caught; detail = $detail; reads = $script:reads; calls = $script:creationCalls }
}
# BEGIN_MEMBER_BOUNDARY_FUNCTION
# Dot-member access normalizes this ScriptProperty failure at the v7.6.9 binder.
# Assert each guard's absolute contract as well as the existing old/new equality.
function Test-MemberBoundaryOutcome($Old, $New, [string]$CaseId) {
    if ($CaseId -cnotin @('creation-false', 'creation-exception', 'empty-command', 'getter-exception', 'valid-command')) { return 'METADATA_VERDICT_CHANGED' }
    $shouldReject = $CaseId -cne 'valid-command'
    if ((($null -ne $Old.caught) -ne $shouldReject) -or (($null -ne $New.caught) -ne $shouldReject)) { return 'METADATA_VERDICT_CHANGED' }
    if ($shouldReject -and ($CaseId -cne 'creation-exception') -and (
        $Old.caught.Exception.Message -cne 'PROFILE_IDENTITY_UNKNOWN' -or $New.caught.Exception.Message -cne 'PROFILE_IDENTITY_UNKNOWN')) { return 'METADATA_ERROR_CHANGED' }
    return 'pass'
}
# END_MEMBER_BOUNDARY_FUNCTION
$script:sentinel = [Exception]::new('SYNTHETIC_ERROR_NOT_PUBLISHED')
foreach ($case in @(
    @('false', 'value', $true, 'metadata-creation-guard', 0, 'creation-false'),
    @('error', 'value', $true, 'metadata-creation-check-exception', 0, 'creation-exception'),
    @('true', '', $false, 'metadata-command-line-guard', 1, 'empty-command'),
    @('true', 'value', $true, 'metadata-command-line-guard', 1, 'getter-exception'),
    @('true', 'value', $false, 'metadata-command-line-read-exception', 1, 'valid-command')
)) {
    $old = Invoke-SyntheticGuard $before $case[0] $case[1] $case[2]
    $new = Invoke-SyntheticGuard $after $case[0] $case[1] $case[2]
    if ((($metadataComparison = 'old-calls') -and ($old.calls -ne 1)) -or (($metadataComparison = 'new-calls') -and ($new.calls -ne 1)) -or (($metadataComparison = 'old-reads') -and ($old.reads -ne $case[4])) -or (($metadataComparison = 'new-reads') -and ($new.reads -ne $case[4])) -or (($metadataComparison = 'new-detail') -and (($metadataActualDetail = $new.detail) -cne $case[3]))) { try {
        $metadataActualDetailCode = 'not-evaluated'
        if ($metadataComparison -ceq 'new-detail') {
            $metadataActualDetailCode = 'unclassified-detail'
            if ($metadataActualDetail -is [string] -and $metadataActualDetail -cin @('not-observed', 'metadata-creation-check-exception', 'metadata-creation-guard', 'metadata-command-line-read-exception', 'metadata-command-line-guard')) {
                $metadataActualDetailCode = $metadataActualDetail
            }
        }
        [Console]::Out.WriteLine(('OBSERVER_DETAIL_ASSERTION_FAILED code=METADATA_SHORT_CIRCUIT_CHANGED case={0} comparison={1} actualDetail={2}' -f $case[5], $metadataComparison, $metadataActualDetailCode))
    } catch { }; throw 'METADATA_SHORT_CIRCUIT_CHANGED' }
    if (($null -eq $old.caught) -ne ($null -eq $new.caught)) { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=METADATA_VERDICT_CHANGED') } catch { }; throw 'METADATA_VERDICT_CHANGED' }
    if ($null -ne $old.caught -and $old.caught.Exception.Message -cne $new.caught.Exception.Message) { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=METADATA_ERROR_CHANGED') } catch { }; throw 'METADATA_ERROR_CHANGED' }
    # Creation-error only: preserve the directly thrown sentinel reference.
    # This does not assert ScriptProperty getter exception identity.
    if ($case[0] -eq 'error' -and (-not [object]::ReferenceEquals($old.caught.Exception, $script:sentinel) -or -not [object]::ReferenceEquals($new.caught.Exception, $script:sentinel))) { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=METADATA_ERROR_IDENTITY_CHANGED') } catch { }; throw 'METADATA_ERROR_IDENTITY_CHANGED' }
# BEGIN_MEMBER_BOUNDARY_CHECK
    $boundaryFailure = Test-MemberBoundaryOutcome $old $new $case[5]
    if ($boundaryFailure -ceq 'METADATA_VERDICT_CHANGED') { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=METADATA_VERDICT_CHANGED') } catch { }; throw 'METADATA_VERDICT_CHANGED' }
    if ($boundaryFailure -ceq 'METADATA_ERROR_CHANGED') { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=METADATA_ERROR_CHANGED') } catch { }; throw 'METADATA_ERROR_CHANGED' }
    if ($boundaryFailure -cne 'pass') { try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=METADATA_BOUNDARY_CONTROL_FAILED') } catch { }; throw 'METADATA_BOUNDARY_CONTROL_FAILED' }
# END_MEMBER_BOUNDARY_CHECK
}
# BEGIN_MEMBER_BOUNDARY_CONTROLS
# Harmless synthetic observations: reject missing rejection, rejected valid input,
# and equal-but-wrong guard errors. Positive valid input must still be accepted.
$accepted = @{ caught = $null }
$rejected = @{ caught = [pscustomobject]@{ Exception = [Exception]::new('PROFILE_IDENTITY_UNKNOWN') } }
$wrongError = @{ caught = [pscustomobject]@{ Exception = [Exception]::new('SYNTHETIC_WRONG_GUARD') } }
if ((Test-MemberBoundaryOutcome $accepted $accepted 'empty-command') -cne 'METADATA_VERDICT_CHANGED' -or
    (Test-MemberBoundaryOutcome $rejected $rejected 'valid-command') -cne 'METADATA_VERDICT_CHANGED' -or
    (Test-MemberBoundaryOutcome $wrongError $wrongError 'getter-exception') -cne 'METADATA_ERROR_CHANGED' -or
    (Test-MemberBoundaryOutcome $accepted $accepted 'valid-command') -cne 'pass') {
    try { [Console]::Out.WriteLine('OBSERVER_DETAIL_ASSERTION_FAILED code=METADATA_BOUNDARY_CONTROL_FAILED') } catch { }; throw 'METADATA_BOUNDARY_CONTROL_FAILED'
}
# END_MEMBER_BOUNDARY_CONTROLS
Write-Output 'OBSERVER_DETAIL_SYNTHETIC_PASS'
