# Pinned diagnostic wrapper: raw output stays only in disposable runner temp.
function Invoke-ProbeStep([string]$Name, [scriptblock]$Body) {
    $global:LASTEXITCODE = 0
    $code = 1
    try {
        & $Body *> (Join-Path $env:WRY_TEMP "raw-$Name.log")
        $code = $global:LASTEXITCODE
        if ($code -lt 0 -or $code -gt 65535) { $code = 1 }
    } catch { $code = 1 }
    & python -B "$env:WRY_SOURCE/scripts/wry_probe_ci.py" step --root $env:WRY_SOURCE --temp $env:WRY_TEMP --stage $Name --exit $code
    if ($LASTEXITCODE -ne 0) { Write-Output 'WRY_STEP_RECORD_REJECTED'; exit 1 }
    if ($code -ne 0) { Write-Output "WRY_STEP_FAILED:$Name"; exit 1 }
    Write-Output "WRY_STEP_PASSED:$Name"
}
