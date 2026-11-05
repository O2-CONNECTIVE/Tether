# Fixed SYSTEM observation setup contract — NOTRUN

This is a source-only candidate. No service, OS setting, account, credential,
public workflow or main modification is authorized by this file. Before any
Prepare/Run/Cleanup invocation, the human must authorize the concrete packet
SHA256 + source/workflow commit + one named fresh GitHub windows-2022 job and
all effects below. ExpectedPacketSha256 is byte binding, never proof of approval.
Missing/invalid digest or Phase=None refuses before native compilation; a wrong
well-formed digest is checked from <=4096 held manifest bytes before compilation.

## Fixed objects and privileges

- One new `RescueSessionIOPrereq-20261104`, OWN_PROCESS, DEMAND_START,
  LocalSystem (CreateService null account/password), UNRESTRICTED service SID.
  No arbitrary account/name/path inputs, no existing-service reuse or fallback.
- Command exactly `"C:\ProgramData\RescueSessionIOPrereq-20261104\scm-token-observer.exe" --service`.
  Source is independent from product/backend/auth/helper/NativeFactory.
- New root `C:\ProgramData\RescueSessionIOPrereq-20261104`, reports directory,
  EXE, manifest/owner/consumed/exited receipts and precreated subject.json.
  Exclusive creation and no reparse/collision/unknown-entry adoption.
- Private evidence root `C:\ProgramData\RescueSessionIOPrereq-20261104-evidence`;
  fixed four files manifest.json/owner.json/subject.json/exit.json only. No raw
  upload/public artifact, new account/password/key or persistent host credential.
- Already elevated controller, WindowsPowerShell5.1 Desktop, local NTFS C:.
  No automatic elevation, ExecutionPolicy bypass, UAC/AV/firewall changes, global
  ACL/privilege assignment, network or service account logon configuration.
  Add-Type compiler temp files may exist outside owned root; do not delete shared
  Temp. Observe only self/current locked worker/own primary using query rights.

New owned file/directory owner/group is Administrators; protected DACL with
unflagged allow SYSTEM+Administrators0x1f01ff, plus dedicated service SID
RX0x1200a9 for root/reports/EXE; report has service0x120116 (FILE_GENERIC_WRITE).
Manifest/receipts/evidence are SYSTEM+Administrators only. Each new object is
created with explicit SD, not created permissively then repaired. The new stopped
SCM service object is protected to SYSTEM+Administrators0xf01ff via its own held
handle; service-object inheritance flags are not confused with file/token policy.
These new-object changes require approval and do not change **observed** existing
process/thread/token security. There is no token/DACL/privilege setter.

Actual service SID is resolved from fixed qualified name and reverse lookup by
the collector. Existing System primary, enabled exact service group, non-deny
attributes and no worker impersonation are observed, never manufactured. Missing
SID after first start is failed/condition-unmet: no reboot/restart/account switch.
The documented SID activation boundary is not assumed satisfied by configuration.
No SACL, foreign PID inventory, CIM/Hyper-V query, SYSTEM shell or guest is used.

## Packet and one-shot phases

Runtime manifest has exact fields schema1/sourceTreeSha256/executableSha256/
toolchain/goos/goarch/signed/windowsExecuted/files. Source tree and EXE/controller
are hash-bound; sourceTreeSha256 is64hex snapshot-manifest hash, not a fabricated
40hex commit. Go1.27.3/windows/amd64, unsigned and windowsExecuted=false are fixed
build provenance, not runtime verdicts. Max manifest4096B; executable16MiB;
report256KiB. Go/x-sys/project licenses accompany the immutable bundle.

Prepare verifies hashes/absence, creates only the fixed new stopped registration
and protected files/report/owner receipt, then stops without start. Any partial
failure retains exact objects; no automatic recovery or broad deletion.

Run verifies original config/object security/hash/owner receipt, STOPPED/PID0,
empty report/no consumed or exit marker. It creates and flushes CREATE_NEW consumed
before StartService, starts once, retains the SCM-reported original process handle
and proves image/creation/run generation. No start retry even on failure. It waits
for held-process signal AND SCM stopped/PID0, then writes immutable exit receipt.
Running control readiness lasts5s so a held process can be acquired; it does not
mean capture success. Cooperative capture15s/self-exit20s; controller25s then
own-service STOP and5s more observation. OS/RPC/scheduler hangs are not guaranteed
bounded by timers. Watchdog exit124 is failure, never checked cleanup.

Cleanup requires original receipts/config/hashes/expected entries and proven
never-run empty state or the same consumed+exit proof. Preserves/readbacks private
evidence, deletes only original stopped service, verifies absence, releases owned
pins and deletes enumerated files/empty directories nonrecursively. Unknown or
missing identity, partial setup, pending deletion or malformed evidence retains
failure and owned objects. No unbound PID reopen/kill, wildcard/recursive delete,
security repair or retry. Provider VM disposal is containment, not cleanup proof.

## Public summary and workflow boundary

--summary reads <=256KiB stdin with exact canonical Go report shape/no trailing/unknown fields and
outputs <=64KiB of fixed enums/counts/digest only. Exact token-object policy is
SYSTEM owner/protected/non-null DACL/two unflagged allow ACEs SYSTEM+actual service
SID each0xf01ff, **not TokenDefaultDacl**. Unknown ACEs/missing queries/failed
collection are UNKNOWN; wrong observed policy is MISMATCH_OBSERVED. Matching is
MATCH_OBSERVED only, never initial protection/authentication/admission. Native
acceptance NOT_EVALUATED and product gate CLOSED on every summary. Persistence always requires original exit/
controller proof; the workflow checks packet-bound zero process/service exit codes
separately before emitting observations. Raw report
includes private SID/SDDL/LUID/PID, so it remains on VM and is not logged/uploaded.

The separate .yml.candidate is not a registered workflow. Its future location is
.github/workflows/scm-sessionio-prerequisite.yml, manual-only/main-only on exactly
windows-2022. It uses reviewed pinned actions, source-manifest build and exact
operator-approved packet hash; a nonmatching cross/native rebuild refuses setup.
No PR trigger/secrets/write token/paid runner/cache/artifact upload. It parses
PowerShell and compiles declarations without invoking controller before setup.
Controller logs stay private. Known cleanup precedes anonymous summary; unknown
states stay failed and the one provider VM is retired without green inference.
Manual dispatch requires prior reviewed default-branch integration approval;
new job registration/one action-time SYSTEM service run are separately authorized.

## Evidence limits

Portable observation-flow/identity/summary tests and Windows cross-compile are
source/model checks. Real Windows SID/descriptor/layout/query, SCM transition,
flush/close/watchdog/exit/cleanup and noninvoking PowerShell5.1/.NETFramework
parse/compile are NOTRUN for this candidate. No fake collector becomes authority.
Two later snapshots do not prove initial-object protection, all runtime threads,
absence of earlier dangerous handles or guest/network authentication. This source
cannot resolve protected native Session physical stop/both-close/Revoke-Resume or
GUI/0205 acceptance. Existing verified native fixture and gates are unchanged.

Canonical summary accepts exactly the typed Go report serialization (all encoded
fields, exact spelling/order and JSON representation), plus outer transport
whitespace only. Required scalar omission/null, duplicate/case-alias keys and
noncanonical inner formatting refuse. Legitimate native nil slices may be null;
this is not blanket null rejection. Digest is canonical native-writer bytes and
workflow compares it with the controller-preserved original report hash. Module
.gitattributes and binary metadata writes preserve LF bytes across hosts.
