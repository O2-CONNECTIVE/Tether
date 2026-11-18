$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. "$PSScriptRoot/runtime-prewarm.ps1"
$root = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid().ToString('n'))
New-Item -ItemType Directory (Join-Path $root 'nested') | Out-Null
try {
    [IO.File]::WriteAllBytes((Join-Path $root 'a.bin'), [byte[]]::new(3MB))
    [IO.File]::WriteAllBytes((Join-Path $root 'nested/b.bin'), [byte[]]::new(1MB))
    # 入れ子も含めて全fileを読み切ったときだけcompleteで、件数と量は実在のfileと一致する。
    $all = Read-FilesOnce $root 60000
    if (-not $all.complete -or $all.files -ne 2 -or $all.megabytes -ne 4) { throw 'PREWARM_CONTRACT_ALL_FAILED' }
    # 予算切れは読込を打ち切って未完了と記録する。読んだことにも、例外にもしない。
    $none = Read-FilesOnce $root 0
    if ($none.complete -or $none.files -ne 0 -or $none.megabytes -ne 0) { throw 'PREWARM_CONTRACT_BUDGET_FAILED' }
    # 読む対象が無いときは0件の成功にせず、失敗させる。
    $missing = $false
    try { Read-FilesOnce (Join-Path $root 'absent') 60000 | Out-Null } catch { $missing = $true }
    if (-not $missing) { throw 'PREWARM_CONTRACT_MISSING_ROOT_ACCEPTED' }
} finally { Remove-Item -LiteralPath $root -Recurse -Force }
Write-Output 'RUNTIME_PREWARM_CONTRACT_PASS checks=3 (fixtures only; not native evidence)'
