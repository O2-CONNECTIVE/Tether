$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$failure = $null
$cleanupFailed = $false
$created = $false
$directory = $null
$phase = 'OWNER_PROBE_WINDOWS_X64_REQUIRED'
try {
    if (-not $IsWindows -or -not [Environment]::Is64BitProcess) { throw 'unsupported' }
    # Use the installed Windows compiler, never download/install a toolchain.
    $phase = 'OWNER_PROBE_COMPILER_UNAVAILABLE'
    $compiler = Join-Path ([Environment]::GetFolderPath('Windows')) 'Microsoft.NET/Framework64/v4.0.30319/csc.exe'
    if (-not (Test-Path -LiteralPath $compiler -PathType Leaf)) { throw 'missing' }
    $phase = 'OWNER_PROBE_FIXTURE_DIRECTORY_FAILED'
    $directory = Join-Path ([IO.Path]::GetTempPath()) ('tether-owner-probe-' + [Guid]::NewGuid().ToString('N'))
    [void][IO.Directory]::CreateDirectory($directory)
    $created = $true
    $phase = 'OWNER_PROBE_FIXTURE_COMPILE_FAILED'
    $image = Join-Path $directory 'DescendantProbe.exe'
    $sources = @('Native.cs', 'DescendantNative.cs', 'DescendantProbe.cs') | ForEach-Object { Join-Path $PSScriptRoot $_ }
    # Capture both streams. Raw compiler/native errors and exceptions never reach output.
    $diagnostics = & $compiler /nologo /target:exe /platform:x64 /optimize+ "/out:$image" @sources 2>&1
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $image -PathType Leaf)) { throw 'compile' }
    $phase = 'OWNER_PROBE_DESCENDANT_FAILED'
    $results = @(& $image 2>&1)
    if ($LASTEXITCODE -ne 0) { throw 'execute' }
    $phase = 'OWNER_PROBE_DESCENDANT_RESULTS_FAILED'
    $expected = @('descendant-normal-exit:passed', 'descendant-cancel-before-resume:passed',
        'OWNER_PROBE_DESCENDANT_CASES_PASSED_2')
    if ($results.Count -ne $expected.Count) { throw 'count' }
    for ($index = 0; $index -lt $expected.Count; $index++) {
        if ($results[$index] -cne $expected[$index]) { throw 'result' }
    }
} catch {
    # Keep the original failure classification even when cleanup also fails.
    $failure = $phase
} finally {
    if ($created) {
        try { Remove-Item -LiteralPath $directory -Recurse -Force -ErrorAction Stop }
        catch { $cleanupFailed = $true }
    }
}
if ($null -ne $failure -or $cleanupFailed) {
    if ($null -ne $failure) { Write-Output $failure }
    if ($cleanupFailed) { Write-Output 'OWNER_PROBE_FIXTURE_CLEANUP_FAILED' }
    throw 'OWNER_PROBE_DESCENDANT_FAILED'
}
# Emit success only after all filesystem cleanup has succeeded.
$expected | Write-Output
