# Actual producer connection CI

The `Actual producer connection` workflow runs the reviewed `ci-producer/`
fixture on standard GitHub-hosted `macos-26` ARM64 VMs with Go 1.27.3. Normal
and race, for each JSON / foreground-CLI adapter, are independent matrix jobs
with fail-fast disabled. Each compiles
its own helper, resident test binary, transport test binary and native capture
test binary. Actions are
commit-pinned, checkout identity is checked, modules must remain unchanged,
and the build and execution steps have module network access disabled.

Each job first runs the exact in-memory parser, trust validation, per-instance
TLS clone and certificate-byte ownership regressions. Separate pure gates also
require native cached-state capture, preserved-panic and private-record validation
tests. It then requires all four
real TLS rejection cases: wrong CA and wrong hostname through both worker and
resident-control transports. A strict JSON
gate rejects skipped, missing, duplicate or unexpected tests, including exact
subtest counts. Live phases also require the original test's confirmed result
and successful exit. Only then can
`TestCIActualProducerConnection` launch the hash-pinned producer helper and
exercise real pipes, resident control, verified TLS and MCP fixed work.
The CLI lane compiles the actual `tether-host` command, with only its unavailable
composition leaf replaced by pinned fixture bootstrap. It drives the explicit
status / ON / OFF / close input contract while reusing the same owner and pipe
loop. The JSON lane preserves the existing producer input. See the
[foreground CLI contract](foreground-host-cli.md); normal startup stays refused.

The background keeper and foreground observer run in the same shell step.
Fresh per-phase invocation IDs bind the result, original-process join and
immutable observer verdict. Old results cannot be reused. Failure or an
observation deadline fails the job without signalling, retrying, replacing
owners or running the next phase. A late successful exit cannot overwrite a failed
observer verdict. The observer budgets are 180 seconds for TLS negatives and
900 seconds for the producer, within a 30-minute job limit. Runner destruction
after failed/unknown is disposal, not evidence of cleanup. A newer commit does not cancel a running job.

The job log and summary expose only fixed phase/lane labels, outcome, source
revision and helper SHA256. Failed runs may also show fixed stage/failure codes,
a validated original-test exit code and a fixed observer verdict/reason. These
fields come only from matching invocation/test markers. Failure summaries may
also show the last exact tool operation and fixed error, phase, stop and three
cleanup enums. These snapshots are not fresh observations or cleanup proof;
each new tool call clears the preceding snapshot. Missing, malformed or
unrecognized data becomes `unavailable` or `null`. They never change the failed
result or supersede the immutable observer verdict.

Failure summaries also carry fixed `producer_*` fields for the current fixture
request's same-ID/intent receipt and latest cached producer snapshot. These can
distinguish stale/rejected/missing/accepted receipts at OFF without changing the
original timeout. Snapshot `within-window` is only the existing receive-age
bound, not a new producer observation. The latest transition is not complete
history; an accepted receipt without a latest matching applied transition does
not establish why application was unobserved. Expired or unknown observations
retain only cached state enums, never cleanup proof. No request ID, revision,
raw frame, retry or additional producer/native operation is introduced.
Missing older-marker fields are unavailable. See the
[diagnostic contract](../ci-producer/DIAGNOSTIC_ENUMS.json).

Optional native first/final cleanup fields are fixed enums from the original
cleanup calls, correlated privately to the exact command. An unavailable or
invalid capture leaves all snapshot fields unavailable; diagnostic flags also
require an available original recorder. No private command ID or record bytes
are published. These cached snapshots authorize no additional observation,
signal, cleanup retry or claim of confirmed cleanup.

Use the job's GitHub Actions
link when reporting results. No artifacts, raw runtime logs, fixture credentials or policy files
are uploaded. See [the fixture contract](../ci-producer/README.md) for exact
ownership and cleanup assertions. This is instrumented-source connection
coverage, not signed launcher, GUI, real dot-account or deployment acceptance.
