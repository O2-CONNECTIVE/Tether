package residentmac

import "time"

// MaxRunAllowance bounds one local allowance. It equals the lifetime of the
// limited development resident, so an allowance never outlives that process.
const MaxRunAllowance = 15 * time.Minute

// AllowRun records a local decision to let command-run requests through for d.
// Standby ON alone never does. The allowance lives in the current standby
// cycle only: it is not persisted, a control reconnect keeps it, and standby
// OFF, expiry, DenyRun or process exit end it. A later AllowRun replaces it.
// It is an input to admission, not an execution grant for any one command.
//
// The end is kept twice. The monotonic clock ignores wall-clock changes but may
// stop while the Mac sleeps; the wall clock keeps counting through sleep but can
// be set back. Run is allowed only while both say the allowance has not ended.
func (s *Standby) AllowRun(id, revision uint64, d time.Duration) StandbyReceipt {
	if s == nil {
		return StandbyReceipt{RequestID: id, Intent: "allow-run", Disposition: "rejected"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.admitLocked(id, revision, "allow-run")
	if !ok || !s.admittingLocked() || d <= 0 || d > MaxRunAllowance {
		return r
	}
	s.cycle.runUntil = time.Now().Add(d)
	s.cycle.runUntilWall = s.cycle.runUntil.Round(0)
	r.Disposition = "accepted"
	return r
}

// DenyRun withdraws the allowance. It does not stop work that already started.
func (s *Standby) DenyRun(id, revision uint64) StandbyReceipt {
	if s == nil {
		return StandbyReceipt{RequestID: id, Intent: "deny-run", Disposition: "rejected"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.admitLocked(id, revision, "deny-run")
	if !ok {
		return r
	}
	if s.cycle != nil {
		s.cycle.runUntil, s.cycle.runUntilWall = time.Time{}, time.Time{}
	}
	r.Disposition = "accepted"
	return r
}

// RunAllowed reports whether a command-run request may be admitted now.
func (s *Standby) RunAllowed() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	return s.admittingLocked() && now.Before(s.cycle.runUntil) && now.Round(0).Before(s.cycle.runUntilWall)
}

// admittingLocked reports whether the current cycle admits new work. Every state
// other than standby "on" keeps admission sealed. Unlike the fixed-work check in
// consume, it does not compare against one particular cycle.
func (s *Standby) admittingLocked() bool {
	return s.cycle != nil && !s.observation.AdmissionSealed
}
