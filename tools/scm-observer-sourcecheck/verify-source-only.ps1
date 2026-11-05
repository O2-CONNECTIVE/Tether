$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
if ($PSVersionTable.PSEdition -cne 'Desktop' -or $PSVersionTable.PSVersion.Major -ne 5) { throw 'WindowsPowerShell5.1 required.' }
$repo = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
$sourcePath = Join-Path $repo 'experiments\scm-system-observation\Invoke-ProtectionProbe.ps1'
function Digest([byte[]]$Bytes) {
    $h = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($h.ComputeHash($Bytes))).Replace('-', '').ToLowerInvariant() }
    finally { $h.Dispose() }
}
$sourceBytes = [IO.File]::ReadAllBytes($sourcePath)
if ($sourceBytes.Length -ne 34577 -or (Digest $sourceBytes) -cne '1f5d0d7b5b9911b74e3503806148192a6c008be0163da20e85747bab69ccf562') { throw 'Reviewed source digest mismatch.' }
$workflowBytes = [IO.File]::ReadAllBytes((Join-Path $PSScriptRoot 'runtime-workflow.yml.candidate'))
if ((Digest $workflowBytes) -cne 'bd0249cc95d3efe6825d0a2bfa4a481d80357e8b24bd1788a6a6c1ce308f23bd') { throw 'Reviewed workflow data changed.' }
$tokens=$null; $parseErrors=$null
$ast = [System.Management.Automation.Language.Parser]::ParseInput(([Text.UTF8Encoding]::new($false,$true).GetString($sourceBytes)), [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -ne 0) { throw 'Controller syntax check failed; no controller execution.' }
$commands = @($ast.FindAll({param($node) $node -is [System.Management.Automation.Language.CommandAst] -and $node.GetCommandName() -ceq 'Add-Type'}, $true))
if ($commands.Count -ne 1) { throw 'Expected one literal definition.' }
$literal = @($commands[0].CommandElements | Where-Object { $_ -is [System.Management.Automation.Language.StringConstantExpressionAst] -and $_.StringConstantType -eq 'SingleQuotedHereString' })
if ($literal.Count -ne 1) { throw 'Definition must remain literal.' }
# Type declarations are compiled, never instantiated or invoked. The controller
# script itself is parsed data; no dot-source/ScriptBlock/Phase invocation exists.
$types = @(Add-Type -TypeDefinition $literal[0].Value -Language CSharp -PassThru -ErrorAction Stop)
if (@($types | Where-Object { $_.FullName -ceq 'SystemObservation.Native' }).Count -ne 1) { throw 'Expected compiled type missing.' }
$blockBytes = [IO.File]::ReadAllBytes((Join-Path $PSScriptRoot 'runtime-run-blocks.json'))
if ((Digest $blockBytes) -cne 'd188cda0c53d6224799ebc2cb443bd83bb19934a3ee8bd116e27f1e7d5dd9a57') { throw 'Frozen workflow run-block data changed.' }
$blockData = ([Text.UTF8Encoding]::new($false,$true).GetString($blockBytes)) | ConvertFrom-Json
if ($blockData.runtime_workflow_sha256 -cne (Digest $workflowBytes)) { throw 'Run-blocks not bound to reviewed workflow.' }
$count=0
foreach ($block in $blockData.blocks) {
    $bt=$null; $be=$null
    $null = [System.Management.Automation.Language.Parser]::ParseInput($block.text,[ref]$bt,[ref]$be)
    if ($be.Count -ne 0) { throw 'Workflow run-block syntax failed; block was not invoked.' }
    $count++
}
[ordered]@{ scope='NONINVOKING_SOURCE_CHECK'; controller_sha256=(Digest $sourceBytes); workflow_sha256=(Digest $workflowBytes); ps_version=$PSVersionTable.PSVersion.ToString(); clr_version=$PSVersionTable.CLRVersion.ToString(); parse_errors=0; csharp_compile='PASS'; workflow_run_blocks_parsed=$count; controller_invoked=$false; native_type_instantiated=$false; service_operations=$false } | ConvertTo-Json -Compress
