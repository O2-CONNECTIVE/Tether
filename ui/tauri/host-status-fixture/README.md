# Windows Host status — isolated synthetic display seam

This is a **source candidate**, not a connected Windows Host GUI. The ordinary
React/Tauri app, strict five-field shell response, native commands/capabilities,
backend bootstrap, consent and runtime buttons are unchanged. This separate HTML
entry imports no native adapter and has no network/agent/service/control actions.
The visible badge always says 表示テスト・実接続なし. Selecting a sample changes
only fixed in-memory data; no query parameters, file input or live event source.

`src/host-status/presentation.ts` maps a small exact synthetic sample to fixed
presentation strings. Unknown enums/types/extra members and the old shell response
fail closed. Freshness, feed end, terminal history and cleanup are separate axes.
Stale/invalid/EOF do not expose current counts; cleanup unknown remains a warning.
Confirmed terminal is a **reported history**, never independent OS cleanup proof.
Zero time, reader EOF, GUI closure and agent exit are not interchangeable facts.

This sample shape is NOT a wire protocol or authentication/validation substitute.
The pure adapter does not ingest a stream, enforce sequence/sticky history, read a
clock, authenticate an owner, issue a grant or validate an OS cleanup claim.
`ageMs` is synthetic; Windows clock/sleep/producer binding needs separate design.
The Mac twelve-field status JSONL and Windows `rescue-host-control/1` are not mapped
into one another. Future live input needs a separately reviewed owner/decoder and
new versioned interface; do not loosen the existing `get_shell_status` validator.
No UI rewrite or shared framework choice is made by this candidate.

## Checks (from ui/tauri)

```sh
npm test
# Only when the existing locked dependencies are already available:
npm run typecheck
npm run lint
npm run knip
npm run knip:production
npm run build:host-status-fixture
```

The fixture build is separate from the ordinary app build and Tauri packaging.
It is a local development render candidate, not a licensed/signed distribution:
this extra build command does not create release notices or an installer.
Dependencies/lockfile are unchanged. No install/download is authorized here.

Current local evidence: Node v26.10.0, locked npm ci with lifecycle scripts disabled,
38 Node tests (0 failures/skips), typecheck, ESLint, both Knip modes, ordinary build,
fixture build and independent runtime notice check passed. Lockfile bytes stayed
unchanged. Vite built both HTML entries; this is not visual rendering evidence.
No browser/native GUI launch was performed locally. Visual fit, keyboard/screen-
reader behavior and Windows runtime remain unverified. Do not describe source
fixtures or successful asset builds as an operational Windows Host GUI.
