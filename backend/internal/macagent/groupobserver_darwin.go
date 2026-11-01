//go:build darwin && cgo

package macagent

/*
#include <libproc.h>
#include <sys/proc_info.h>
#include <sys/proc.h>
#include <errno.h>
#include <string.h>

struct tether_group_list { int count; int error; int ids[256]; };
static struct tether_group_list tether_list_group(int pgid) {
 struct tether_group_list r; memset(&r, 0, sizeof(r));
 errno = 0;
 r.count = proc_listpgrppids(pgid, r.ids, sizeof(r.ids));
 r.error = errno;
 return r;
}
struct tether_group_member { int bytes; int error; int pid; int pgid; int zombie; };
static struct tether_group_member tether_group_member_info(int pid) {
 struct tether_group_member r; memset(&r, 0, sizeof(r));
 struct proc_bsdshortinfo info; memset(&info, 0, sizeof(info));
 errno = 0;
 // arg=1 includes zombies: XNU bsd/kern/proc_info.c proc_pidinfo.
 r.bytes = proc_pidinfo(pid, PROC_PIDT_SHORTBSDINFO, 1, &info, sizeof(info));
 r.error = errno;
 if (r.bytes == sizeof(info)) {
  r.pid = info.pbsi_pid; r.pgid = info.pbsi_pgid;
  r.zombie = info.pbsi_status == SZOMB;
 }
 // No comm/name/path data crosses the C boundary.
 memset(&info, 0, sizeof(info));
 return r;
}
static int tether_shortinfo_size(void) { return sizeof(struct proc_bsdshortinfo); }
*/
import "C"

// Local native adapter. Repository tests use fakes and never call this adapter.
// This is not a fallback after a signal or query permission denial.
type nativeGroupQuery struct{}

func groupErrno(e C.int) groupQueryError {
	if e == 0 {
		return groupQueryOK
	}
	if e == C.EPERM || e == C.EACCES {
		return groupQueryPermission
	}
	if e == C.ESRCH {
		return groupQueryMissing
	}
	return groupQueryOther
}
func (nativeGroupQuery) listGroup(pgid int) groupListing {
	if pgid <= 1 || int(int32(pgid)) != pgid {
		return groupListing{err: groupQueryOther}
	}
	r := C.tether_list_group(C.int(pgid))
	if e := groupErrno(r.error); e != groupQueryOK {
		return groupListing{err: e}
	}
	if r.count <= 0 || r.count > groupObservationLimit {
		return groupListing{err: groupQueryOther}
	}
	out := make([]int, int(r.count))
	for i := range out {
		out[i] = int(r.ids[i])
	}
	return groupListing{pids: out}
}
func (nativeGroupQuery) member(pid int) (groupMember, groupQueryError) {
	if pid <= 1 || int(int32(pid)) != pid {
		return groupMember{}, groupQueryOther
	}
	r := C.tether_group_member_info(C.int(pid))
	if e := groupErrno(r.error); e != groupQueryOK {
		return groupMember{}, e
	}
	if r.bytes != C.tether_shortinfo_size() {
		return groupMember{}, groupQueryOther
	}
	return groupMember{pid: int(r.pid), pgid: int(r.pgid), zombie: r.zombie != 0}, groupQueryOK
}

// observeOwnedGroup uses only this locally retained process identity. The caller
// holds process.mu and sweep; no reap has occurred. The lifecycle owner gates
// natural-before-signal or fresh TERM-success-before-KILL use.
func (d *darwinProcess) observeOwnedGroup() groupObservation {
	if d.done || d.pid <= 1 || !d.exitSeen || !d.exitKnow || d.watchErr != nil {
		return groupInvalidAnchor
	}
	return observeOwnedGroup(groupAnchor{pid: d.pid, observedExit: true, unreaped: true}, nativeGroupQuery{})
}
