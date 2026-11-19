package residentmac

import "time"

// RunEndingNotice is how long before its end the allowance warns the Mac's UI
// through the status, so the person can extend it or wrap up (#399).
const RunEndingNotice = 10 * time.Minute

// listedRunAllowance reports whether d is one of the lengths the person can
// pick (#399). The GUI defaults to the shortest. There are no other lengths.
func listedRunAllowance(d time.Duration) bool {
	switch d {
	case 2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 12 * time.Hour, 24 * time.Hour:
		return true
	}
	return false
}

// AllowRun records a local decision to let command-run requests through for d,
// one of the listed lengths. Standby ON alone never does. The allowance lives
// in the current standby cycle only: it is not persisted, a control reconnect
// keeps it, and standby OFF, DenyRun or process exit end it. A later AllowRun
// replaces it; that is the only way to extend it. When it runs out, standby
// turns off as if the person had turned it off, and the status records why.
// It is an input to admission, not an execution grant for any one command.
//
// The end is kept twice. The monotonic clock ignores wall-clock changes but may
// stop while the Mac sleeps; the wall clock keeps counting through sleep but can
// be set back. The allowance has ended as soon as either says so.
func (s *Standby) AllowRun(id, revision uint64, d time.Duration) StandbyReceipt {
	if s == nil {
		return StandbyReceipt{RequestID: id, Intent: "allow-run", Disposition: "rejected"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.admitLocked(id, revision, "allow-run")
	if !ok || !s.admittingLocked() || !listedRunAllowance(d) {
		return r
	}
	mono, wall := s.now()
	s.cycle.runUntil = mono.Add(d)
	s.cycle.runUntilWall = wall.Round(0).Add(d)
	s.observation.RunUntil = s.cycle.runUntilWall
	s.observation.RunEndingSoon = false
	s.changedLocked()
	r.Disposition = "accepted"
	r.Revision = s.observation.Revision
	return r
}

// DenyRun withdraws the allowance. It does not stop work that already started,
// and a command run admitted before it may still start until that intent's own
// deadline: the allowance is checked once, at admission. An allowance that has
// already run out is not withdrawn: it ends standby instead.
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
	if left, live := s.runLeftLocked(); live && left > 0 {
		s.cycle.runUntil, s.cycle.runUntilWall = time.Time{}, time.Time{}
		s.observation.RunUntil, s.observation.RunEndingSoon = time.Time{}, false
		s.changedLocked()
	}
	r.Disposition = "accepted"
	r.Revision = s.observation.Revision
	return r
}

// RunAllowed reports whether a command-run request may be admitted now.
func (s *Standby) RunAllowed() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runAllowedLocked()
}

func (s *Standby) runAllowedLocked() bool {
	left, live := s.runLeftLocked()
	return live && left > 0 && s.admittingLocked()
}

// now reads the two clocks the allowance is kept on. Tests replace them.
func (s *Standby) now() (mono, wall time.Time) {
	if s.clock != nil {
		return s.clock()
	}
	t := time.Now()
	return t, t
}

// runLeftLocked is how long the allowance has left by the stricter of its two
// clocks. live is false while there is none.
func (s *Standby) runLeftLocked() (left time.Duration, live bool) {
	if s.cycle == nil || s.cycle.runUntil.IsZero() {
		return 0, false
	}
	mono, wall := s.now()
	return min(s.cycle.runUntil.Sub(mono), s.cycle.runUntilWall.Sub(wall.Round(0))), true
}

// admittingLocked reports whether the current cycle admits new work. Every state
// other than standby "on" keeps admission sealed, and so does an allowance that
// has run out: from its end, the cycle is ending. Unlike admissionLocked, it
// does not compare against one particular cycle.
func (s *Standby) admittingLocked() bool {
	if s.cycle == nil || s.observation.AdmissionSealed {
		return false
	}
	left, live := s.runLeftLocked()
	return !live || left > 0
}

// expireRunLocked turns standby off once the allowance has run out, and keeps
// the ten-minute warning current otherwise. It reports whether it turned
// standby off. The monitor calls it; admission does not wait for it.
func (s *Standby) expireRunLocked() bool {
	if s.observation.Standby != "on" {
		return false
	}
	left, live := s.runLeftLocked()
	if live && left <= 0 {
		s.observation.RunExpired = true
		s.stopLocked()
		return true
	}
	s.observation.RunEndingSoon = live && left <= RunEndingNotice
	return false
}

// admission is what consume does with one received intent.
type admission int

const (
	admitNothing admission = iota // sealed or another cycle: refuse and stop serving
	admitNoRun                    // a command run without a live allowance: refuse, keep serving
	admitFixed                    // fixed work: admit
	admitRun                      // a command run under a live allowance: admit
)

// admissionLocked decides, once per received intent, whether cycle c admits it.
// Standby ON admits fixed work. A command run also needs the local allowance.
func (s *Standby) admissionLocked(c *standbyCycle, run bool) admission {
	switch {
	case s.cycle != c || !s.admittingLocked():
		return admitNothing
	case !run:
		return admitFixed
	case s.runAllowedLocked():
		return admitRun
	}
	return admitNoRun
}
