$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$previous = $env:WRY_TEMP
$folder = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid().ToString('N'))
try {
    New-Item -ItemType Directory $folder -ErrorAction Stop | Out-Null
    $env:WRY_TEMP = $folder
    $child = Join-Path $folder 'child.ps1'
    [IO.File]::WriteAllText($child, @'
. "$env:WRY_SOURCE/scripts/wry-probe-step.ps1"
Invoke-ProbeStep pins { python -c "print('PRIVATE_SENTINEL')" }
Invoke-ProbeStep rust { python -c "import sys; sys.exit(7)" }
throw 'UNREACHABLE'
'@)
    $output = (& pwsh -NoLogo -NoProfile -File $child 2>&1 | Out-String)
    $code = $LASTEXITCODE
    if ($code -ne 1 -or $output.Contains('PRIVATE_SENTINEL')) { throw 'FIXTURE' }
    $rows = @(Get-Content (Join-Path $folder 'steps.json') -Raw | ConvertFrom-Json)
    if ($rows.Count -ne 2 -or $rows[0].stage -cne 'pins' -or $rows[0].exitCode -ne 0 -or
        $rows[1].stage -cne 'rust' -or $rows[1].exitCode -ne 7) { throw 'FIXTURE' }
    if (-not (Get-Content (Join-Path $folder 'raw-pins.log') -Raw).Contains('PRIVATE_SENTINEL')) { throw 'FIXTURE' }
    Write-Output 'WRY_WRAPPER_FIXTURE_PASS'
} catch {
    Write-Output 'WRY_WRAPPER_FIXTURE_REJECTED'
    exit 1
} finally {
    $env:WRY_TEMP = $previous
    if (Test-Path -LiteralPath $folder) { Remove-Item -LiteralPath $folder -Recurse -Force }
}
