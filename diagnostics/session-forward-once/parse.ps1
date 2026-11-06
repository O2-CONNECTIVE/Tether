param([Parameter(Mandatory)][string]$Harness)
$ErrorActionPreference = 'Stop'
try {
    $files = @(Get-ChildItem -LiteralPath $Harness -Filter '*.ps1' -File)
    if ($files.Count -lt 1 -or $files.Count -gt 80) { throw 'PARSE_SET_REJECTED' }
    foreach ($file in $files) {
        $tokens = $null; $errors = $null
        $null = [Management.Automation.Language.Parser]::ParseFile($file.FullName, [ref]$tokens, [ref]$errors)
        if ($errors.Count -ne 0) { throw 'PARSE_REJECTED' }
    }
    Write-Output "FORWARD_PARSE_PASS count=$($files.Count)"
} catch { Write-Output 'FORWARD_PARSE_REJECTED'; exit 1 }
