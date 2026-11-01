# Test-only local stdio schema fixture

This small harness reuses `mcpadapter.NewDiscoveryHTTPSession`: no backend can be
attached. It does not read a config, environment, file, credential or token;
create a host/authority; dial/listen; or spawn a command. stdout is MCP JSON only.
Valid tools/call parses against the real four-tool schemas, then always returns
`isError: true`, `schema_only_no_execution`, and `dispatch_count: 0`. It never
pretends a command ran. stderr emits fixed event enums, not raw arguments.
64 frames and the existing MaxLine bound cap input. EOF ends the fixture.

The independently inspectable evidence is the immutable unbound constructor,
shared real schema parser, fixed tool/argument match events and fake tests—not a
model's verbal claim. This demonstrates only Claude's schema loading, tool
selection and argument generation. It proves no Access/OAuth/Bearer
interoperability, no remote HTTP or 404 reinitialize behavior, no Mac command or
cleanup and no ChatGPT plugin registration. No token consumer or remote proxy is
implemented. `testdata` excludes this harness from normal product package builds.

## Build and local fake tests

From backend, with the already approved offline Go environment:

```
go test ./internal/mcpadapter/testdata/schemafixture
go test -race -count=100 ./internal/mcpadapter/testdata/schemafixture
go vet ./internal/mcpadapter/testdata/schemafixture
go build -o <owned-artifact-directory>/tether-schema-fixture ./internal/mcpadapter/testdata/schemafixture
```

No external Claude invocation is part of those tests.

## Coordinator-owned invocation candidate (not executed here)

Use a new isolated empty cwd and a unique temporary MCP JSON, not the normal
user/project configuration. The JSON has exactly one stdio server:

```json
{"mcpServers":{"tether_schema":{"type":"stdio","command":"<absolute-reviewed-binary>","args":[]}}}
```

A reviewed launcher may capture only the fixture's fixed stderr events in a new
owned artifact file; no shell tool permission is granted to the model. Suggested
CLI flags to verify against installed help and managed policy before use:

```
claude -p --model claude-opus-5-5 --effort high \
  --restricted --tools "" --strict-mcp-config --mcp-config <temporary-json> \
  --setting-sources '' --settings <temporary-restricted-settings> \
  --disable-slash-commands --no-chrome --no-session-persistence \
  --permission-mode dontAsk --permission-prompts none --max-turns 6 \
  --allowedTools 'mcp__tether_schema__rescue_start' 'mcp__tether_schema__rescue_status' \
    'mcp__tether_schema__rescue_output' 'mcp__tether_schema__rescue_cancel' \
  --output-format stream-json --verbose <fixed-prompt> < /dev/null
```

`--tools ""` disables built-ins, not MCP tools. `--safe-mode` disables MCP so
cannot serve this test; `--bare` excludes Team OAuth, so do not use it or change
billing. Keep managed policy; do not fall back to broader permissions if refused.
The temporary settings candidate is autoMemoryEnabled=false, disableAllHooks=true,
and pluginConfigs.cc-plugin-agents-md@builtin.options.instructionFiles set to
managed-only, subject to independent confirmation. This avoids loading personal
or project instructions; do not rewrite those files or normal settings.

Fixed prompt: “This is a local schema-only fixture, with no remote host and no
execution. Using only tether_schema MCP tools, submit rescue_start once with
request_id schema-check-1, command exactly `printf 'tether-schema-only\n'`,
timeout_ms 1000. Then submit rescue_status, rescue_output and rescue_cancel once
each using synthetic command_id 00000000000000000000000000000000; for output use
cursor 0 and max_bytes 1024. The expected result is schema_only_no_execution.
Do not retry or claim a real command ran. Report only the four schema outcomes.”

The evidence log should contain exactly one `valid_<tool>` event for each tool,
all with dispatch=0. Any argument mismatch, tool absence, model quota, managed
policy refusal or unexpected tool stops the trial; do not bypass it. Existing
Team authentication is used only by the coordinator-approved Claude process.

Official references:
- https://code.claude.com/docs/en/cli-reference
- https://code.claude.com/docs/en/mcp
- https://code.claude.com/docs/en/memory
