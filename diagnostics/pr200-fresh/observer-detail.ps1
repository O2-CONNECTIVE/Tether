# Pure classification seam. No OS command, process access, or exception text.
# Source location is evidence of a statement site, never an OS/exception cause.
function Resolve-LineageFailure([string]$Stack, [string]$SourcePath, [bool]$Trusted) {
    if (-not $Trusted -or -not $Stack -or $Stack.Length -gt 16384 -or -not $SourcePath) { return 'unclassified-detail' }
    $lines = @($Stack -split '\r?\n')
    if ($lines.Count -gt 32) { return 'unclassified-detail' }
    $pattern = '^at (Stop-UnconfirmedLineage|Require-Live|Get-Descendants|Get-OwnedApp), ' + [regex]::Escape($SourcePath) + ': line ([0-9]{1,3})$'
    $frames = @()
    for ($index = 0; $index -lt [Math]::Min(2, $lines.Count); $index += 1) {
        $line = $lines[$index]
        $match = [regex]::Match($line, $pattern, [Text.RegularExpressions.RegexOptions]::IgnoreCase)
        if (-not $match.Success) { break }
        $frames += @{ name = $match.Groups[1].Value; line = [int]$match.Groups[2].Value }
    }
    if ($frames.Count -eq 0) { return 'unclassified-detail' }
    $first = $frames[0]
    if ($first.name -eq 'Stop-UnconfirmedLineage' -and $first.line -eq 73 -and $frames.Count -eq 2 -and $frames[1].name -eq 'Get-Descendants') {
        switch ($frames[1].line) {
            77 { return 'lineage-parent-row-guard-site' }
            78 { return 'lineage-parent-creation-guard-site' }
            88 { return 'lineage-child-row-guard-site' }
            90 { return 'lineage-child-creation-guard-site' }
            92 { return 'lineage-child-order-guard-site' }
        }
    }
    if ($first.name -eq 'Require-Live' -and $first.line -eq 7 -and $frames.Count -eq 2 -and $frames[1].name -eq 'Get-OwnedApp') {
        switch ($frames[1].line) {
            113 { return 'lineage-driver-live-check-site' }
            114 { return 'lineage-edge-live-check-site' }
        }
    }
    if ($first.name -eq 'Get-OwnedApp') {
        switch ($first.line) {
            112 { return 'lineage-reference-check-site' }
            116 { return 'lineage-driver-descendants-call-site' }
            117 { return 'lineage-app-selection-site' }
            118 { return 'lineage-app-count-check-site' }
            119 { return 'lineage-app-descendants-call-site' }
            120 { return 'lineage-webview-selection-site' }
            121 { return 'lineage-webview-count-check-site' }
        }
    }
    if ($first.name -eq 'Get-Descendants') {
        switch ($first.line) {
            76 { return 'lineage-root-selection-site' }
            77 { return 'lineage-root-count-check-site' }
            78 { return 'lineage-root-creation-check-site' }
            85 { return 'lineage-child-selection-site' }
            87 { return 'lineage-child-identity-check-site' }
            90 { return 'lineage-child-creation-check-site' }
            92 { return 'lineage-child-order-check-site' }
        }
    }
    return 'unclassified-detail'
}
