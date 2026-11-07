# Actual producer connection CI fixture

This is an opt-in, instrumented-source test, not production signed launcher,
GUI, dot account connection, deployment or unmodified application acceptance.
It starts a separate Go producer process with real OS pipes and executes the
production resident owner and native current-user runtime through real TLS,
signed resident control and explicit MCP. Synthetic auth is fixture-only.

## Layout and compile-only preparation

Publish the reviewed script, `fixtures/` and this README under `ci-producer/`,
next to the repository's `backend/`. Generated binaries, overlays, private
notes, manifests from a developer computer and raw logs are not source inputs.

Run `python3 ci-producer/prepare_overlay.py` to generate compiler overlays.
The script verifies three pinned production sources. It changes one resident
loader expression, adds cached-state capture defers/fields to native cleanup,
and adds one optional final-cleanup observation hook in Session.run. The producer lifecycle, unknown-retention branch and
capturePipes implementation remain unchanged. Any source pin update needs
independent review, not an automatic replacement of the expected hash.

From backend, compile with Go 1.27.3, cgo enabled, the `ci_actual_producer` tag,
`-overlay=../ci-producer/OVERLAY.json`, `-mod=readonly` and `-p=1`:

- `go build ... -o ../ci-producer/generated/producer-helper ./cmd/ci-producer-helper`
- `go test -c ... -o ../ci-producer/generated/connection.test ./internal/residentpipe`
- Repeat BOTH commands with `-race` and distinct outputs. A race test parent
  with a normal helper does not cover producer races.
- Compile separate agenttransport trust regression tests similarly.

No helper or tests run during these commands. Default production builds have
no fixture exports or trust configuration. Generated cmd directory contains
no real source file; only the explicit overlay introduces the helper.

## Execution gate

Do not run on a user's desktop. Execution needs independent final source and
ownership review, then an explicit GitHub-hosted macos-26 ARM64 job. Use no real
credentials, account policy, endpoint, browser, GUI, OS trust or settings. Do
not weaken the strict protected reader if runner ancestors are refused.

For each normal/race lane, use the corresponding separately built helper and
test binary; calculate and set its exact CI_PRODUCER_SHA256. Set absolute paths
for CI_PRODUCER_BINARY and a fresh CI_PRODUCER_RESULT, plus
CI_ACTUAL_PRODUCER=1. Generate a fresh cryptographically random 32-character
lowercase hex CI_PRODUCER_INVOCATION for EACH test/lane and pass that same
value as --invocation to both keeper and observer. Markers bind the invocation
and exact test name; never reuse result paths or invocation IDs. Run supervise_ci.py keeper in the background with the
exact test binary, result and log paths, then run its observer synchronously.
The workflow must retain the original keeper process until test exit or
runner teardown. Do not use kill, CommandContext or test timeout.

The test requires real first output before cancel; it checks top-level
finished plus command_phase canceled/exited, exact decoded stdout, terminal,
empty error, and all three cleanup results confirmed. A real authoritative OFF
must precede closed-confirmed/EOF/child Wait0. Handler and Gateway close
separately, followed by HTTP shutdown/Serve and owned I/O joins. Fixture files
are removed only afterward. This proves neither escaped descendant cleanup
nor unrelated process state.

## Failed/unknown is not a pass

Any failed assertion writes and fsyncs failed-unknown JSON before retaining
original owners indefinitely. The original test owns the producer/pipe/gateway
handles; keeper owns the original test process and log. Observer fails CI on
that marker or its observation deadline without signalling anything. Its
exclusive invocation-bound .verdict.json is immutable: a late keeper pass
cannot override a timeout/failure. Workflow success must require observer
exit0 plus its confirmed verdict. Budgets are 900s for producer lifecycle
and 180s for four negative TLS cases. Eventual
runner destruction is unconfirmed disposal, never cleanup evidence. Do not
rerun in that runner or substitute a different owner to obtain green.

Success requires the test's confirmed marker AND keeper's original test Wait0
marker. Require exactly TestCIActualProducerConnection, no skips, no missing
result. A guard failure or zero-test execution must fail. Publish only the
allowlisted outcome, source revision, helper hash and CI job link. Do not
publish fixture keys/CA/bootstrap/policy, raw child logs, local absolute paths,
or private historical candidates.

TLS trust-negative tests use the same keeper/observer with
--test-name TestCIProducerTrustTLSNegative and their own fresh result/identity.
They are separate actual socket tests, not producer
execution evidence. They must reject wrong CA/hostname for both transport
bindings and preserve normal TLS validation and native owned dial tracking.

## Compiler-only native cleanup diagnostics

The generated process.go overlay preserves original return expressions and all
OS operations. Named-return capture defers copy fixed cached fields while the
original term lock is held, briefly taking mu. A panic is rethrown unchanged
without capture, preserving the original guard behavior. First failure and last
call are separate immutable value records; no raw error is stored. I/O completion
may advance before this snapshot, so it is an original failed-call snapshot,
not a fresh observation or proof of the exact earlier OS instant.

The generated agentexec/run.go adds one optional interface call after the
original final Terminate and conditional Release, before terminal publication.
It targets that exact process and passes that original command ID privately.
The hook copies the record outside session locks, releases term, and performs
one raw nonblocking write to fixture-owned FD4. The 64-byte versioned binary
record is below the POSIX minimum PIPE_BUF; no retries, callbacks or native
queries are added. Reserved bytes, enums and record consistency are validated.
The sink is disabled by default in every tagged test binary. Only the helper
enables it once after adopting its pipe; disabling joins any in-flight bounded
export before descriptor close. FD4 is verified as a pipe, made nonblocking and CLOEXEC before native launch.
The unmodified native descriptor scanner still runs. Native commands cannot
inherit FD4. The parent closes its inherited write duplicate before bootstrap.

On failure, the parent reads at most the two serial work records for at most
two seconds and accepts only the current MCP result's exact command ID. It
publishes fixed first/final cause, group, I/O and diagnostic-category fields,
never the ID, errors, PIDs, paths or payload. Optional cleanup-recorder
categories are unavailable when that production recorder was not configured,
not reported as an observed absence. A missing, dropped or invalid
record remains unavailable/invalid; it cannot turn failed cleanup into success.
On confirmed original producer closure and Wait, the parent additionally joins
FD4 EOF and closes its read owner. On unknown it retains that owner, like all
other fixture resources. Instrumentation can perturb scheduling; a later pass
is not evidence that the original failure was fixed.

Pure regressions (no process/socket/native operations):
TestCINativeCleanupCapture, TestCINativeCleanupPanicPreserved (macagent), and
TestCINativeEvidenceValidation (residentpipe). Original connection success
assertions, fixed shell command, native ownership rules and unknown retention
remain unchanged. All generated overlays and binaries remain excluded.
