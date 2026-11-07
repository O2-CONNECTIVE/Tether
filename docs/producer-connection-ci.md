# Actual producer connection CI

The `Actual producer connection` workflow runs the reviewed `ci-producer/`
fixture on standard GitHub-hosted `macos-26` ARM64 VMs with Go 1.27.3. Normal
and race are independent matrix jobs with fail-fast disabled. Each compiles
its own helper, resident test binary and transport test binary. Actions are
commit-pinned, checkout identity is checked, modules must remain unchanged,
and the build and execution steps have module network access disabled.

Each job first runs the exact in-memory parser, trust validation, per-instance
TLS clone and certificate-byte ownership regressions. It then requires all four
real TLS rejection cases: wrong CA and wrong hostname through both worker and
resident-control transports. A strict JSON
gate rejects skipped, missing, duplicate or unexpected tests, including exact
subtest counts. Live phases also require the original test's confirmed result
and successful exit. Only then can
`TestCIActualProducerConnection` launch the hash-pinned producer helper and
exercise real pipes, resident control, verified TLS and MCP fixed work.

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
result or supersede the immutable observer verdict. Use the job's GitHub Actions
link when reporting results. No artifacts, raw runtime logs, fixture credentials or policy files
are uploaded. See [the fixture contract](../ci-producer/README.md) for exact
ownership and cleanup assertions. This is instrumented-source connection
coverage, not signed launcher, GUI, real dot-account or deployment acceptance.
