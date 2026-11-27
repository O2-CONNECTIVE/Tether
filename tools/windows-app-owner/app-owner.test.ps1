$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$failure = $null
$cleanupFailed = $false
$directory = $null
$phase = 'APP_OWNER_WINDOWS_X64_REQUIRED'
try {
    if (-not $IsWindows -or -not [Environment]::Is64BitProcess) { throw 'unsupported' }
    $phase = 'APP_OWNER_COMPILER_UNAVAILABLE'
    $compiler = Join-Path ([Environment]::GetFolderPath('Windows')) 'Microsoft.NET/Framework64/v4.0.30319/csc.exe'
    if (-not (Test-Path -LiteralPath $compiler -PathType Leaf)) { throw 'missing' }
    $phase = 'APP_OWNER_FIXTURE_DIRECTORY_FAILED'
    $directory = Join-Path ([IO.Path]::GetTempPath()) ('tether-app-owner-' + [Guid]::NewGuid().ToString('N'))
    [void][IO.Directory]::CreateDirectory($directory)
    $phase = 'APP_OWNER_FIXTURE_COMPILE_FAILED'
    $image = Join-Path $directory 'AppOwnerFixture.exe'
    $sources = @('FixtureNative.cs', 'AppOwner.cs', 'Program.cs') | ForEach-Object { Join-Path $PSScriptRoot $_ }
    $probe = Join-Path $PSScriptRoot '../windows-owner-probe'
    $sources += @('Native.cs', 'DescendantNative.cs') | ForEach-Object { Join-Path $probe $_ }
    $diagnostics = & $compiler /nologo /target:exe /platform:x64 /optimize+ "/out:$image" @sources 2>&1
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $image -PathType Leaf)) { throw 'compile' }
    $phase = 'APP_OWNER_FIXTURE_FAILED'
    $results = @(& $image 2>&1)
    if ($LASTEXITCODE -ne 0) { throw 'execute' }
    $phase = 'APP_OWNER_FIXTURE_RESULTS_FAILED'
    if ($results.Count -ne 1 -or $results[0] -cne 'APP_OWNER_FIXED_CASES_PASSED_4') { throw 'result' }
} catch {
    $failure = $phase
} finally {
    if ($null -ne $directory -and (Test-Path -LiteralPath $directory)) {
        try { Remove-Item -LiteralPath $directory -Recurse -Force -ErrorAction Stop }
        catch { $cleanupFailed = $true }
    }
}
if ($null -ne $failure -or $cleanupFailed) {
    if ($null -ne $failure) { Write-Output $failure }
    if ($cleanupFailed) { Write-Output 'APP_OWNER_FIXTURE_CLEANUP_FAILED' }
    throw 'APP_OWNER_FIXTURE_FAILED'
}
Write-Output 'APP_OWNER_FIXED_CASES_PASSED_4'
