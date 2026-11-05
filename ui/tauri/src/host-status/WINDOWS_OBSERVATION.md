# Windows shell observation → presentation (local contract candidate)

The ordinary `App.tsx` status region now consumes this read-only adapter. No IPC command,
capability, service, native runtime, consent, grant, execution or stop action is
added. It does not accept or translate the Mac JSONL or native control protocol.

## Existing authority boundary is reused, not duplicated

`src/controller.ts` already owns the strict `get_shell_status` response validator.
It is private; this candidate does not export, copy or relax it. Raw backend
responses still go through `createShellController`. The adapter consumes only the
controller's normalized `onChange(ShellState)` observation. Tests feed the checked-in
`contracts/shell-status-v1.json` response and rejected responses through that real
controller before calling the adapter. No Windows process is launched by tests.

`src-tauri/src/lib.rs` and `shell_runtime.rs` currently expose only the unwired
five-field success response, or fixed diagnostics. `control_coordinator/observations.rs`
has no established-authority success variant. Fresh observations, peer claims,
stop receipts, child exit and reader join cannot manufacture missing authority.

## Exact local envelope (not a wire protocol)

`{ scope: 'windows-shell-observation', version: 1, feed, state }`

- `state`: the current controller `onChange` value, not a raw backend response.
- `feed`: fresh / stale / eof / invalid. These are caller-supplied observation
  metadata, **not authenticated facts**. This adapter reads no clock or stream.
  The current polling controller does not emit an EOF event; a future owner must
  establish its actual source/end/freshness semantics before using these values.
- Unknown scope/version/extra fields/nonmatching state fail closed to unavailable.
- Only fresh + exact not_connected/SHELL_UNWIRED displays backend-unwired.
- stale/EOF/invalid never convert reports to current connected values or cleanup
  success. TERMINATION_UNCONFIRMED retains a warning across these feed states.
- Malformed raw payloads and incompatible protocols are rejected by the existing
  controller. Unknown exceptions remain its fixed IPC_ERROR, never raw output.

Output scope is `windows-host-presentation`, version 1; state is only unwired or
unavailable. Authority is always not_established, canStart/canStop always false,
workers/remaining always unavailable, cleanup never confirmed. Fixed text and
icons' tone are display data only. There is deliberately no connected/approved/
execution-ready variant. A caller forging the local envelope cannot gain an
operation through this adapter. It is not an authentication or security boundary.

The adapter is stateless: it does not retain fault history, enforce sequence,
join a reader or prove cleanup. Do not use repeated calls as a stream reducer or
replace an unresolved cleanup fault with a new fresh observation. That lifecycle
belongs to the future verified owner. Tests only establish per-observation mapping.

## Ordinary UI caller (display wiring, not host runtime wiring)

`main.tsx` still provides the sole `get_shell_status` invoke adapter. The existing
controller validates its five-field response, and `App.tsx` passes the normalized
state to this function. The normal status heading/detail consume its presentation;
checking and browser-without-bridge guidance retain their existing wording.
`feed: fresh` means the current controller observation only: no authenticated
transport, clock freshness, stream EOF event or established authority is implied.
No listener, timer, invoke, IPC command or capability is added. Synthetic fixture
panels remain separate; real observations are never relabeled synthetic.

Start/stop remain native disabled, permission fields remain unknown, and the
controller still owns request deduplication, timeout and disposal. The display
adapter does not retain fault history. A future live stream owner must establish
lifecycle/freshness and unresolved-fault retention before any runtime connection.
This UI wiring does not supply bootstrap authentication, native pipe connection,
consent/grant, execution or OS cleanup evidence.

## Verification boundary

The Node tests reuse the existing controller with synthetic resolved/rejected
promises. Existing browser acceptance tests target the ordinary built App through
its bootstrap and synthetic invoke boundary, including the adapter-specific unwired
copy and cleanup warning, invalid response, checking, missing bridge, disabled
operations, focus and narrow/enlarged display. These tests are not Windows native
or live backend verification. Record actual execution separately; adding a test
or compiling its source does not establish that its browser acceptance passed.
