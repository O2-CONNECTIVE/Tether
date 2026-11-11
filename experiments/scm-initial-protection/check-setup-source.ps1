$ErrorActionPreference = 'Stop'
$sourcePath = Join-Path $PSScriptRoot 'Invoke-ProtectionProbe.ps1'
$tokens = $null
$parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($sourcePath, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -ne 0) { throw 'Setup PowerShell syntax failed.' }
$commands = @($ast.FindAll({ param($node)
    $node -is [System.Management.Automation.Language.CommandAst] -and $node.GetCommandName() -ceq 'Add-Type'
}, $true))
if ($commands.Count -ne 1) { throw 'Expected one literal native wrapper definition.' }
$literal = @($commands[0].CommandElements | Where-Object {
    $_ -is [System.Management.Automation.Language.StringConstantExpressionAst] -and $_.StringConstantType -eq 'SingleQuotedHereString'
})
if ($literal.Count -ne 1) { throw 'Native wrapper source must be a literal here-string.' }
# Compile declarations only. Never invoke or dot-source the setup script, native
# wrapper methods, service executable, service registration or runtime queries.
Add-Type -TypeDefinition $literal[0].Value -Language CSharp -ErrorAction Stop
Write-Output 'PowerShell parsed; native wrapper compiled; no probe/setup execution.'
