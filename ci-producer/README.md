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
The script verifies four pinned production sources. It changes one resident
loader expression, adds cached-state capture defers/fields to native cleanup,
adds one optional final-cleanup observation hook in Session.run, and replaces
only tether-host's unavailable composition leaf with fixture bootstrap. The
CLI main, argument contract and both input parsers remain unmodified. The shared producer lifecycle, unknown-retention branch and
capturePipes implementation remain unchanged. Any source pin update needs
independent review, not an automatic replacement of the expected hash.

From backend, compile with Go 1.27.3, cgo enabled, the `ci_actual_producer` tag,
`-overlay=../ci-producer/OVERLAY.json`, `-mod=readonly` and `-p=1`:

- `go build ... -o ../ci-producer/generated/producer-helper ./cmd/ci-producer-helper`
- For the CLI lane use the same flags with `./cmd/tether-host` instead. This
  builds its real foreground entry and only substitutes the pinned composition.
- `go test -c ... -o ../ci-producer/generated/connection.test ./internal/residentpipe`
- Repeat BOTH commands with `-race` and distinct outputs. A race test parent
  with a normal helper does not cover producer races.
- Compile separate agenttransport trust regression tests similarly.

No helper or tests run during these commands. Default production builds have
no fixture exports or trust configuration. Generated cmd directory contains
no real source file; only the explicit overlay introduces the helper.
The normal `tether-host foreground` command remains unavailable, not a way to
launch a synthetic or operational policy. See [the CLI contract](../docs/foreground-host-cli.md).

## Execution gate

Do not run on a user's desktop. Execution needs independent final source and
ownership review, then an explicit GitHub-hosted macos-26 ARM64 job. Use no real
credentials, account policy, endpoint, browser, GUI, OS trust or settings. Do
not weaken the strict protected reader if runner ancestors are refused.

For each normal/race lane, use the corresponding separately built helper and
test binary. JSON and CLI adapters also use separate VMs. Set
CI_PRODUCER_ADAPTER to exactly json or cli and calculate its exact
CI_PRODUCER_SHA256. Set absolute paths
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

The CLI lane starts the actual command with its `foreground` argument. It sends
status, ON/OFF lines with real observed revisions and the same increasing request
IDs. It ends with `close` while keeping stdin open until child Wait, so EOF cannot
substitute for close. Commands after close in the same chunk are ignored. The
original JSON lane continues to close its input as before. Execution, results and
cancel still use MCP; they are not new CLI verbs.

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

## Failure-only producer observation diagnostics

The failed marker also copies the current fixture request's receipt and the
latest cached producer snapshot under the existing reader lock. Only fixed
`producer_*` enums are published: request intent, receipt, snapshot age/status,
standby, registration, work, cleanup and latest transition. The request ID and
intent are compared privately for both receipt and transition; IDs, revisions,
source times and raw frames never enter public output. A new submission clears
the previous request correlation before any operation that can fail.

`missing`, `accepted`, `rejected`, `stale`, `unknown` and `mismatch` receipts
separate what was observed for that request. A matching accepted receipt with a
latest `pending`, `missing` or `other-request` transition does not prove that no
applied transition ever existed. `within-window` means only that the latest
receive age meets the existing three-second bound. `expired` and
`observation-unknown` still retain last observed fixed state enums; none are
fresh producer state or cleanup proof, even when cleanup says `confirmed`.
Missing producer/request/snapshot evidence stays unavailable as appropriate.
Old markers may omit the entire group; public projection then emits unavailable.
A partial or malformed group also makes the whole public group unavailable.
The synthetic validator accepts the group only when complete and allowlisted.

This adds no producer query, retry, timeout change, signal or cleanup action.
The original 45-second applied-transition wait and failed/unknown owner retention
remain unchanged. Pure in-memory cases run within the existing exact-selected
`TestCIProducerObservationValidation`. The workflow output boundary is tested
separately with synthetic markers; it never starts a producer or native work.

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

## Foreground work dictionary coverage

The CLI adapter also builds `tether-work` with only its pinned unavailable
composition leaf replaced. Its normal main, foreground parser, session client
and dictionary decoders remain unchanged. On the same actual work child it
checks `list`, `get_execution` and `read_output`, in addition to the existing
start/result/cancel/release operations:

- An empty list and explicit unknown-ID read failures before any work.
- Running metadata and a seven-byte output page after the real first output,
  before the existing cancellation request. The original five-second command
  is unchanged; failure to observe this state fails the lane.
- Finished natural and canceled metadata, finite explicit output pages, an
  empty final page, cursor-ahead refusal, identical repeated reads, and the
  unchanged retained full result before release.
- One-entry list pages across both executions, including released tombstones;
  released metadata remains readable while output returns `result_released`.
- One private HTTP session for every work request and its single DELETE.
  The observer does not consume request bodies or wrap responses. It rejects
  reinitialization, SID replacement and any request after DELETE. Commands
  queued after foreground close produce no response or additional request.

The success gate requires a separate dictionary marker for the CLI lane and
rejects that marker in the JSON-only lane. The marker is written only after
all existing producer OFF, original Wait/EOF, gateway close and fixture
removal checks. Pure sequence and synthetic workflow-marker tests validate
those evidence gates but are not native execution proof. Failure retains the
same original owners; no retries, signals, new cleanup path or source-pin
changes are added.

## Command run coverage

After the work CLI has closed, the CLI lane takes `tether_run_command` through
the same gateway, signed control and producer, over direct MCP HTTP in a new
session. The allowance exists only as the foreground `allow` verb, so the JSON
lane does not run this step.

- Without the allowance the work ends `admission_refused_or_uncertain` with
  owner and gateway cleanup confirmed, and the command's marker file is absent.
- After an accepted `allow`, one fixed command runs with `cwd` naming a
  directory below the work root: exit 0, all three cleanup results confirmed,
  stdout carrying that directory's resolved path, and the marker inside it.
  The producer-close evidence bound is 192 bytes in this lane, one 64-byte
  native record per command.
- `tether_fixed_read_output` returns the same bytes, `tether_fixed_release`
  releases the result, and a later read returns `result_released`.

It does not cover allowance expiry, a cwd outside the work root, signature
mismatch, cancellation of a command run or OFF during one. Source tests and
the recorded device acceptance cover those.

This is regression coverage for the existing dictionary implementation. It
uses one fixed command and synthetic CI credentials on an isolated hosted
Mac. It does not exercise dot's real account/session, production credentials,
GUI actions, signed distribution, output gaps/overflow or
live host-generation rotation. Existing source/unit coverage of those other
cases is separate, and product connection gates remain closed.
