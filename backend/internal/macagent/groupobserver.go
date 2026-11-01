package macagent

// An observation is neither persistent quiescence nor authority to signal/reap
// any PID. The lifecycle owner separately requires same-generation reap and I/O.
const groupObservationLimit = 256

type groupQueryError uint8

const (
	groupQueryOK groupQueryError = iota
	groupQueryPermission
	groupQueryMissing
	groupQueryOther
)

type groupMember struct {
	pid, pgid int
	zombie    bool
}
type groupListing struct {
	pids []int
	err  groupQueryError
}
type groupQuery interface {
	listGroup(pgid int) groupListing
	member(pid int) (groupMember, groupQueryError)
}

// Supplied only by a future local owner while holding its lifecycle lock.
// Never construct from remote input. observedExit must be non-reaping evidence;
// unreaped requires sole-waiter ownership, checked by the native owner method.
type groupAnchor struct {
	pid                    int
	observedExit, unreaped bool
}
type groupObservation string

const (
	groupAnchorOnly    groupObservation = "owned_group_quiescent_snapshot"
	groupInvalidAnchor groupObservation = "unknown_anchor"
	groupPermission    groupObservation = "unknown_permission"
	groupQueryFailed   groupObservation = "unknown_query"
	groupTruncated     groupObservation = "unknown_truncated"
	groupMissingAnchor groupObservation = "unknown_anchor_missing"
	groupInvalidList   groupObservation = "unknown_invalid_list"
	groupChanged       groupObservation = "unknown_pid_pgid_changed"
	groupRace          groupObservation = "unknown_query_race"
	groupLive          groupObservation = "observed_live_member_unknown"
	groupMultiple      groupObservation = "unknown_multiple_members"
)

func queryFailure(e groupQueryError) groupObservation {
	if e == groupQueryPermission {
		return groupPermission
	}
	if e == groupQueryMissing {
		return groupRace
	}
	return groupQueryFailed
}

// observeOwnedGroup cannot change cleanup state. In particular even anchor-only
// says nothing about descendants which left the group and may later rejoin.
func observeOwnedGroup(a groupAnchor, q groupQuery) groupObservation {
	if a.pid <= 1 || !a.observedExit || !a.unreaped || q == nil {
		return groupInvalidAnchor
	}
	l := q.listGroup(a.pid)
	if l.err != groupQueryOK {
		return queryFailure(l.err)
	}
	if len(l.pids) >= groupObservationLimit {
		return groupTruncated
	}
	seen := make(map[int]bool, len(l.pids))
	for _, pid := range l.pids {
		if pid <= 1 || seen[pid] {
			return groupInvalidList
		}
		seen[pid] = true
	}
	if !seen[a.pid] {
		return groupMissingAnchor
	}
	// Multiple-member lists are never accepted by a sequence of later status
	// queries: a listed live parent could fork then exit between those queries.
	if len(l.pids) != 1 {
		return groupMultiple
	}
	m, e := q.member(a.pid)
	if e != groupQueryOK {
		return queryFailure(e)
	}
	if m.pid != a.pid || m.pgid != a.pid {
		return groupChanged
	}
	if !m.zombie {
		return groupLive
	}
	return groupAnchorOnly
}
