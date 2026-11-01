package agentexec

import "time"

// LocalSummary is non-authoritative display data, not a grant or process inventory.
// Worker counts are mutually exclusive states, not proof of OS process absence.
type LocalSummary struct {
	Deadline                    time.Time
	Revoked                     bool
	Reason                      StopReason
	Starting, Running, Stopping int
	Cleanup                     Cleanup
}

// LocalSummary never observes the clock/issuer, revokes, retries or returns IDs.
// Contention returns no snapshot: consumers must become stale, not replay old data.
func (s *Session) LocalSummary() (LocalSummary, bool) {
	if s == nil || !s.mu.TryLock() {
		return LocalSummary{}, false
	}
	defer s.mu.Unlock()
	v := LocalSummary{Deadline: s.deadline, Revoked: s.revoked, Reason: s.reason, Cleanup: CleanupNotRequired}
	if s.authority != nil {
		select {
		case <-s.authority.Done():
			v.Revoked = true
		default:
		}
	}
	for _, c := range s.order {
		if c.cleanup == CleanupUnknown {
			v.Cleanup = CleanupUnknown
		} else if c.cleanup == CleanupPending && v.Cleanup != CleanupUnknown {
			v.Cleanup = CleanupPending
		} else if c.cleanup == CleanupConfirmed && v.Cleanup == CleanupNotRequired {
			v.Cleanup = CleanupConfirmed
		}

		if !c.terminal {
			switch {
			case c.stop != StopNone || c.releasing || c.exited:
				v.Stopping++
			case c.phase == PhaseRunning:
				v.Running++
			default:
				v.Starting++
			}
		}
	}
	if v.Reason == StopNone && len(s.order) > 0 {
		v.Reason = s.order[len(s.order)-1].stop
	}
	return v, true
}
