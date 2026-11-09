$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $IsWindows -or -not [Environment]::Is64BitProcess) { throw 'OWNER_PROBE_WINDOWS_X64_REQUIRED' }
# Use the installed Windows .NET Framework compiler, never download/install a toolchain.
$compiler = Join-Path ([Environment]::GetFolderPath('Windows')) 'Microsoft.NET/Framework64/v4.0.30319/csc.exe'
if (-not (Test-Path -LiteralPath $compiler -PathType Leaf)) { throw 'OWNER_PROBE_COMPILER_UNAVAILABLE' }
$directory = Join-Path ([IO.Path]::GetTempPath()) ('tether-owner-probe-' + [Guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($directory)
try {
    $image = Join-Path $directory 'DescendantProbe.exe'
    $sources = @('Native.cs', 'DescendantNative.cs', 'DescendantProbe.cs') | ForEach-Object { Join-Path $PSScriptRoot $_ }
    # Raw diagnostics can contain runner paths. Publish only fixed failure classifications.
    $diagnostics = & $compiler /nologo /target:exe /platform:x64 /optimize+ "/out:$image" @sources 2>&1
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $image -PathType Leaf)) {
        throw 'OWNER_PROBE_FIXTURE_COMPILE_FAILED'
    }
    $results = @(& $image)
    if ($LASTEXITCODE -ne 0) { throw 'OWNER_PROBE_DESCENDANT_FAILED' }
    $expected = @('descendant-normal-exit:passed', 'descendant-cancel-before-resume:passed',
        'OWNER_PROBE_DESCENDANT_CASES_PASSED_2')
    if ($results.Count -ne $expected.Count) { throw 'OWNER_PROBE_DESCENDANT_RESULTS_FAILED' }
    for ($index = 0; $index -lt $expected.Count; $index++) {
        if ($results[$index] -cne $expected[$index]) { throw 'OWNER_PROBE_DESCENDANT_RESULTS_FAILED' }
    }
    $expected | Write-Output
} finally {
    Remove-Item -LiteralPath $directory -Recurse -Force
}
