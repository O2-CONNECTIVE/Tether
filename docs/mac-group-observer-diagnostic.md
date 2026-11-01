# Owned process-group diagnostic candidate

The integrated runtime fast path has SOURCE/FAKE validation only. A separate
standalone literal-child probe observed anchor-only and reaped its own child;
that is not an integrated runtime test. The runtime has not been deployed or run
with this integration. No earlier cleanup unknown is reclassified.

The only positive observation is `owned_group_quiescent_snapshot`. It is not
persistent quiescence and must never map directly to cleanup confirmed.

The local lifecycle owner must provide the original unreaped
leader, sole-waiter ownership and non-reaping exit evidence while holding its
lifecycle lock. The internal anchor is not a remote argument or authority token.
The native owner method supplies the PID; no caller accepts it from remote input.

The query is limited to that PGID. The list is capped at 256; full/oversize,
duplicates, invalid PIDs, missing anchor, errno, short metadata, PID/PGID change
or query race fail unknown. Multiple members are always unknown, even if a later
sequence of status queries might call them zombies: a live member could fork
then exit during that sequence. Only a complete list containing precisely the
held leader proceeds to its zombie status check. No other member is inspected.

The libc list wrapper can turn syscall failure into zero. errno is cleared and
captured in the same C call; zero is not absence proof. The metadata call uses
PROC_PIDT_SHORTBSDINFO with arg=1 to include zombies; exact returned size is
required. Name bytes incidental to this ABI are discarded inside C and never
returned, logged or stored. There are no path, argv or environment queries.
The null-buffer size probe is not used (XNU reports a global process estimate).
Native adapter methods have no initialization side effects and no test calls them.

An anchor-only snapshot still cannot rule out a previously escaped same-session
descendant returning via setpgid after the snapshot. Repeated identical snapshots
do not close this race. The existing escaped-descendant boundary is not expanded.
Never reap first then signal a recycled numeric PGID. A signal success may mean
only partial delivery; permission errors must not be ignored. Prior live
permission diagnostic combines EPERM/EACCES; exact errno was not established.

## Integrated runtime execution remains separate

One observation of the current locally owned unreaped PGID, bounded PID list,
then metadata for the anchor only (PID/PGID/zombie status). No signals, global
process dump, other-group metadata, keys, paths, changes to OS security, or
queries about a prior trial. The integrated runtime requires its own authorization/review before execution.

## Primary source basis

- https://github.com/apple-oss-distributions/xnu/blob/main/libsyscall/wrappers/libproc/libproc.c
  proc_listpgrppids and proc_pidinfo error-to-zero wrappers.
- https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/proc_info.c
  proc_listpids locks the list and includes allproc/zombproc filtered by PGID;
  proc_pidinfo sets findzomb for SHORTBSDINFO when arg is nonzero.
- https://github.com/apple-oss-distributions/xnu/blob/main/bsd/sys/proc_info.h
  proc_bsdshortinfo PID/PGID/status ABI.
- https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_prot.c
  setpgid permits same-session group changes.
These are upstream main source findings, not proof of this Mac's exact XNU tag.

Existing native owner evidence is kqueue NOTE_EXIT/NOTE_EXITSTATUS, which is not a reap. The caller must hold the same process lifecycle mutex and ensure its sole Wait4 path has not reaped/lost the leader before and throughout observation. These obligations are not proven by the plain internal anchor struct itself; the native owner method and process lifecycle guards enforce them. The leader numeric identity remains anchored only while unreaped.

Reported target: macOS 26.6.2 / Darwin 25.6.0. Matching published XNU tag is not yet established.

## Natural-exit integration

Only a fresh natural known exit, before any signal, with no signal error or
reap loss, may observe. process.sweep and process.mu serialize snapshot and the
same-generation reap. The native owner also checks its done/exitSeen/exitKnow
state. A positive snapshot skips unnecessary TERM/KILL; it does not convert a
permission error into success. Query errors, partial lists, extra members or
identity loss become sticky unknown and are never queried again. The unreaped
anchor remains owned on those failures; no alternate signal/API rescue is added.

Cleanup confirmation additionally requires the same leader reap, normal output
EOF, joined I/O, acceptable input completion, and successful owned-descriptor
Release. Expected stdin closure caused by Terminate is recorded separately from
normal input delivery. Abnormal reads/writes remain sticky errors; Next does not
convert a read error into EOF. Closed resources do not prove complete output.
The pre-existing non-natural stop path is unchanged.
