# Minimal SYSTEM SCM self-observation candidate

A separate source-only experiment derived from PR170 LocalService collector at
893cebdc5fea972ad18749973bab581c8a0531ba. It observes one new fixed LocalSystem
service, not the product, LocalService helper, native Session/Job or guest approval.
No live Windows/setup action was performed. `--service` selects only the fixed
SCM entry; no args/config/account fallback. `--summary` is bounded stdin→anonymous
JSON only and invokes no SCM/native collector. Other/default CLI modes refuse.

Fixed service `RescueSessionIOPrereq-20261104`, image
`C:\ProgramData\RescueSessionIOPrereq-20261104\scm-token-observer.exe --service`
(quoted image path by controller), one consumed demand-start own-process run.
The controller's default phase/missing exact packet digest refuse before native
compilation. A digest binds files, not user approval. Every Prepare/Run/Cleanup
phase needs separate prior action-time authorization for the exact packet/scope.
No generic SYSTEM shell, LogonUser, token/privilege/DACL repair or other PID lookup.

Original observation worker stays locked across its two snapshots and handle
closes. Native SID is looked up by fixed qualified service name and reverse name.
Measured SYSTEM TokenUser, primary type and exact enabled/non-deny service group
are checked. Group membership is recomputed; the report bool never authenticates.
Owner/full DACL and raw SDDL are captured privately, distinct from TokenDefaultDacl.
Simple allow/deny ACE facts are bounded and preserved; unsupported ACEs remain
unknown. No token-object descriptor is rewritten. Failures/unknowns never pass
collection or become an authentication/provider receipt.

Summary exposes only fixed schema/status, counts, full-report digest and enums:
MATCH_OBSERVED/MISMATCH_OBSERVED/UNKNOWN for SYSTEM-owner/protected/exact2ACE token
policy. Unknown ACEs or failed collection do not match. MATCH_OBSERVED is only a
snapshot result; initial protection and native acceptance are NOT_EVALUATED,
product gate CLOSED. Raw SID/SDDL/LUID/PID, failure text and private paths are not
copied to the summary. Report caps256KiB, summary64KiB, queries64KiB,128groups/ACEs.

The adapted controller preserves original generation-bound exit receipts,
CREATE_NEW protected files, owner/hash/entry checks, single consumption, no
recursive cleanup and no retry. Known exit and SCM stop are distinct. Unknown
acquisition/partial setup/missing exit keeps owned objects; disposable VM disposal
is containment, never successful cleanup or physical Job stop. `SETUP-CONTRACT.md`
lists exact new-object security and action-time scope. New service-object/file
ACL setup does not imply matching existing process/token DACL.

Proposed host is one standard GitHub windows-2022 x64 VM, not a shared host or0205.
Server2022 build20348 and no-reboot SID activation are actual preconditions; absent
SID means condition-unmet, no reboot/account/type fallback. The workflow candidate
is outside `.github`, manual-only/main-only, pinned actions, no secrets/write token,
no artifacts/cache upload and private controller logs. It is not registered/run.
A manual workflow must first be reviewed/integrated to default branch with
separate publication approval. No runtime authorization is supplied by this file.

Offline: existing Go1.27.3/x-sys0.49.0, `go test -count=1 ./...`, race, vet and Windows
amd64/arm64 cross builds. Pure fake collectors test observation flow only. New
identity and summary tests had actual assertion-red/green. Native SID/ACL decoder,
SCM/controller/report I/O/watchdog/cleanup need real Windows; cross compile and
static source review are not that proof. Mac lacks WindowsPowerShell5.1/.NET
Framework parser/compiler; the candidate workflow includes non-invoking syntax/
C# declaration checks before any setup, but they have NOTRUN for this snapshot.

`source-manifest.json` pins this standalone source snapshot (not an invented git
commit). `build-bundle.py` requires that exact tree, Go version, immutable compiler
flags and an absent artifacts directory; builds only and never runs Windows.
Runtime manifest hashes source tree/EXE/controller, says unsigned/windowsExecuted
false, and includes Go/x-sys/project licenses. Windows job rebuild must reproduce
the separately approved runtime-manifest digest exactly or refuse all setup.
Any future source/workflow change requires new hashes and affected validation.

Canonical summary accepts exactly the typed Go report serialization (all encoded
fields, exact spelling/order and JSON representation), plus outer transport
whitespace only. Required scalar omission/null, duplicate/case-alias keys and
noncanonical inner formatting refuse. Legitimate native nil slices may be null;
this is not blanket null rejection. Digest is canonical native-writer bytes and
workflow compares it with the controller-preserved original report hash. Module
.gitattributes and binary metadata writes preserve LF bytes across hosts.
