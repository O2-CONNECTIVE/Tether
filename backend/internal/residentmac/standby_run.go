package residentmac

import "time"

// DefaultStandbyFor is the standby length for an ON that names none: the
// product's JSON entry cannot name one until #410 (#399).
const DefaultStandbyFor = 2 * time.Hour

// StandbyEndingNotice is how long before its end standby warns the Mac's UI
// through the status, so the person can extend it or wrap up (#399).
const StandbyEndingNotice = 10 * time.Minute

// listedStandbyFor reports whether d is one of the standby lengths the person
// can pick (#399). There are no other lengths.
func listedStandbyFor(d time.Duration) bool {
	switch d {
	case 2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 12 * time.Hour, 24 * time.Hour:
		return true
	}
	return false
}

// ExtendStandby moves the end of the current standby to d from now, d one of
// the listed lengths. It is the only way to extend standby, and only the person
// at the Mac reaches it (#399). Standby that is not on, or has already run out,
// is not extended: from its end the cycle is ending.
func (s *Standby) ExtendStandby(id, revision uint64, d time.Duration) StandbyReceipt {
	if s == nil {
		return StandbyReceipt{RequestID: id, Intent: "extend", Disposition: "rejected"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.admitLocked(id, revision, "extend")
	if !ok || s.observation.Standby != "on" || !s.admittingLocked() || !listedStandbyFor(d) {
		return r
	}
	s.endStandbyInLocked(d)
	s.changedLocked()
	r.Disposition = "accepted"
	r.Revision = s.observation.Revision
	return r
}

// endStandbyInLocked sets the current cycle to end d from now.
//
// The end is kept twice. The monotonic clock ignores wall-clock changes but may
// stop while the Mac sleeps; the wall clock keeps counting through sleep but can
// be set back. Standby has ended as soon as either says so. The wall-clock end
// is an integer: a time.Time from time.Now carries a monotonic reading that Sub
// would prefer, which would quietly turn the wall-clock check into a second
// monotonic one.
func (s *Standby) endStandbyInLocked(d time.Duration) {
	mono, wall := s.now()
	s.cycle.until = mono.Add(d)
	s.cycle.untilWall = wall.UnixNano() + int64(d)
	s.observation.StandbyUntil = time.Unix(0, s.cycle.untilWall)
	s.observation.StandbyEndingSoon = false
}

// AllowRun records a local decision to let command-run requests through.
// Standby ON alone never does. The allowance lives in the current standby
// cycle only: it is not persisted, a control reconnect keeps it, and standby
// OFF (asked for, or at the end of standby), DenyRun or process exit end it, so
// it never outlives standby. It is an input to admission, not an execution
// grant for any one command.
func (s *Standby) AllowRun(id, revision uint64) StandbyReceipt {
	if s == nil {
		return StandbyReceipt{RequestID: id, Intent: "allow-run", Disposition: "rejected"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.admitLocked(id, revision, "allow-run")
	if !ok || !s.admittingLocked() {
		return r
	}
	s.cycle.runAllowed = true
	r.Disposition = "accepted"
	return r
}

// DenyRun withdraws the allowance. It does not stop work that already started,
// and a command run admitted before it may still start until that intent's own
// deadline: the allowance is checked once, at admission.
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
		s.cycle.runAllowed = false
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
	return s.runAllowedLocked()
}

func (s *Standby) runAllowedLocked() bool {
	return s.admittingLocked() && s.cycle.runAllowed
}

// now reads the two clocks standby's end is kept on. Tests replace them.
func (s *Standby) now() (mono, wall time.Time) {
	if s.clock != nil {
		return s.clock()
	}
	t := time.Now()
	return t, t
}

// standbyLeftLocked is how long the current cycle has left by the stricter of
// its two clocks, and not positive without a cycle.
func (s *Standby) standbyLeftLocked() time.Duration {
	if s.cycle == nil {
		return 0
	}
	mono, wall := s.now()
	return min(s.cycle.until.Sub(mono), time.Duration(s.cycle.untilWall-wall.UnixNano()))
}

// admittingLocked reports whether the current cycle admits new work. Every state
// other than standby "on" keeps admission sealed, and so does standby that has
// run out: from its end, the cycle is ending. Unlike admissionLocked, it does
// not compare against one particular cycle.
func (s *Standby) admittingLocked() bool {
	return s.cycle != nil && !s.observation.AdmissionSealed && s.standbyLeftLocked() > 0
}

// expireStandbyLocked turns standby off once it has run out, and keeps the
// ten-minute warning current otherwise. It reports whether it turned standby
// off. The monitor calls it; admission does not wait for it. Sealed standby is
// already stopping for another reason and publishes no end.
func (s *Standby) expireStandbyLocked() bool {
	if s.observation.Standby != "on" || s.observation.AdmissionSealed {
		return false
	}
	left := s.standbyLeftLocked()
	if left <= 0 {
		s.observation.StandbyExpired = true
		s.stopLocked()
		return true
	}
	s.observation.StandbyEndingSoon = left <= StandbyEndingNotice
	return false
}

// admission is what consume does with one received intent.
type admission int

const (
	admitNothing admission = iota // sealed or another cycle: refuse and stop serving
	admitNoRun                    // a command run without the allowance: refuse, keep serving
	admitFixed                    // fixed work: admit
	admitRun                      // a command run under the allowance: admit
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
