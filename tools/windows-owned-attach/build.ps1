param([Parameter(Mandatory)][string]$ToolsDirectory, [Parameter(Mandatory)][string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
# Existing setup has verified the official pinned archive/Edge signature/runtime. This
# separate variant uses the pristine archive, not the established diagnostic postimage.
New-Item -ItemType Directory $OutputDirectory -ErrorAction Stop | Out-Null
$root = (Resolve-Path "$PSScriptRoot/../..").Path
function Get-Inputs {
    $inputs = [ordered]@{}
    $files = @(Get-ChildItem $PSScriptRoot -File | Where-Object Extension -in @('.rs','.cs','.ts','.py','.ps1','.patch'))
    $files += @(Get-ChildItem "$root/ui/tauri/native-smoke" -File | Where-Object Extension -in @('.ts','.json'))
    $files += @(Get-Item "$root/tools/windows-owner-probe/Native.cs", "$root/tools/windows-owner-probe/DescendantNative.cs")
    foreach ($file in ($files | Sort-Object FullName)) {
        $name = [IO.Path]::GetRelativePath($root, $file.FullName).Replace('\','/')
        $inputs[$name] = (Get-FileHash $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    return $inputs
}
$phase = 'preimage'
try {
$inputs = Get-Inputs
$source = Join-Path $OutputDirectory 'source'
$manifest = Join-Path $OutputDirectory 'provenance.json'
$archive = Join-Path $ToolsDirectory 'tauri-driver.crate'
& python "$PSScriptRoot/prepare.py" --archive $archive --source $source --manifest $manifest
if ($LASTEXITCODE -ne 0) { throw 'OWNED_ATTACH_PREIMAGE_FAILED' }
$phase = 'rust-compile'
& cargo +1.99.1 build --message-format=json --manifest-path "$source/Cargo.toml" --locked --release --target-dir "$OutputDirectory/target" *> "$OutputDirectory/build.private.log"
if ($LASTEXITCODE -ne 0) { throw 'OWNED_ATTACH_COMPILE_FAILED' }
$phase = 'postimage'
$binary = Join-Path $OutputDirectory 'target/release/tauri-driver.exe'
& python "$PSScriptRoot/prepare.py" --archive $archive --source $source --manifest $manifest --binary $binary
if ($LASTEXITCODE -ne 0) { throw 'OWNED_ATTACH_POSTIMAGE_FAILED' }
$phase = 'compiler-present'
$csc = Join-Path $env:WINDIR 'Microsoft.NET/Framework64/v4.0.30319/csc.exe'
if (-not (Test-Path $csc -PathType Leaf)) { throw 'OWNED_ATTACH_EXISTING_COMPILER_REQUIRED' }
$phase = 'launcher-compile'
& $csc /nologo /target:exe /platform:x64 /out:"$OutputDirectory/owned-attach.exe" "$PSScriptRoot/LauncherNative.cs" "$PSScriptRoot/Launcher.cs" "$PSScriptRoot/Program.cs" "$PSScriptRoot/../windows-owner-probe/Native.cs" "$PSScriptRoot/../windows-owner-probe/DescendantNative.cs" *> "$OutputDirectory/compiler.private.log"
if ($LASTEXITCODE -ne 0) { throw 'OWNED_ATTACH_LAUNCHER_COMPILE_FAILED' }

$phase = 'input-check'
if (($inputs | ConvertTo-Json -Compress) -cne ((Get-Inputs) | ConvertTo-Json -Compress)) { throw 'OWNED_ATTACH_INPUT_CHANGED_DURING_BUILD' }
[ordered]@{
    source = (& git -C $root rev-parse HEAD).Trim()
    inputs = $inputs
    launcherSha256 = (Get-FileHash "$OutputDirectory/owned-attach.exe" -Algorithm SHA256).Hash.ToLowerInvariant()
    compilerSha256 = (Get-FileHash $csc -Algorithm SHA256).Hash.ToLowerInvariant()
} | ConvertTo-Json -Depth 8 | Set-Content "$OutputDirectory/invocation-build.json" -Encoding utf8

$phase = 'complete'
} finally {
    & python "$PSScriptRoot/diagnostics.py" --directory $OutputDirectory --phase $phase
    if ($LASTEXITCODE -ne 0) { throw 'OWNED_ATTACH_BUILD_DIAGNOSTIC_FAILED' }
}
