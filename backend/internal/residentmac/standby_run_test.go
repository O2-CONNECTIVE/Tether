package residentmac

import (
	"context"
	"testing"
	"time"
)

func standbyTurnedOn(t *testing.T) (*Standby, *idleConnector) {
	t.Helper()
	s, c, _ := idleStandby(t)
	if r := s.RequestOn(1, s.Snapshot().Revision); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	return s, c
}
func allowRun(t *testing.T, s *Standby, id uint64, d time.Duration) {
	t.Helper()
	if r := s.AllowRun(id, s.Snapshot().Revision, d); r.Disposition != "accepted" || r.Intent != "allow-run" || r.RequestID != id {
		t.Fatal(r)
	}
}

// Standby ON lets the gateway reach this Mac; it must not, by itself, let a
// remote party run commands. Only a separate local action does, and only for
// a bounded time.
func TestStandbyOnAloneNeverAllowsRun(t *testing.T) {
	s, c := standbyTurnedOn(t)
	if s.RunAllowed() {
		t.Fatal("standby on allowed run without a local allowance")
	}
	c.connected.Store(true)
	eventually(t, func() bool { return s.Snapshot().Registration == "authenticated" })
	if s.RunAllowed() {
		t.Fatal("authenticated standby allowed run without a local allowance")
	}
	allowRun(t, s, 2, time.Minute)
	if !s.RunAllowed() {
		t.Fatal("accepted allowance did not allow run")
	}
}

// Admission is sealed before standby leaves "on" (for example when the first
// authentication times out). From that moment nothing new may start.
func TestSealedAdmissionEndsRunAllowanceBeforeStandbyLeavesOn(t *testing.T) {
	s, _ := standbyTurnedOn(t)
	allowRun(t, s, 2, time.Minute)
	s.mu.Lock()
	s.observation.AdmissionSealed = true
	on := s.observation.Standby == "on"
	s.mu.Unlock()
	if !on || s.RunAllowed() {
		t.Fatal("run allowed after admission was sealed")
	}
	if r := s.AllowRun(3, s.Snapshot().Revision, time.Minute); r.Disposition != "rejected" || s.RunAllowed() {
		t.Fatal("allowance accepted after admission was sealed", r)
	}
}

func TestRunAllowanceExpires(t *testing.T) {
	s, _ := standbyTurnedOn(t)
	allowRun(t, s, 2, time.Minute)
	s.mu.Lock()
	until := s.cycle.runUntil
	s.cycle.runUntil = time.Now().Add(-time.Millisecond)
	s.mu.Unlock()
	if d := time.Until(until); d <= 0 || d > time.Minute {
		t.Fatal("allowance does not end one requested duration from now", d)
	}
	if s.RunAllowed() {
		t.Fatal("expired allowance still allows run")
	}
}

// The monotonic clock may stop while the Mac sleeps. An allowance given before
// a night of sleep must not still be open in the morning, so the wall clock
// ends it too.
func TestRunAllowanceExpiresByWallClockAcrossSleep(t *testing.T) {
	s, _ := standbyTurnedOn(t)
	allowRun(t, s, 2, time.Minute)
	s.mu.Lock()
	monotonicOpen := time.Now().Before(s.cycle.runUntil)
	s.cycle.runUntilWall = time.Now().Round(0).Add(-time.Millisecond)
	s.mu.Unlock()
	if !monotonicOpen || s.RunAllowed() {
		t.Fatal("allowance outlived its wall-clock end")
	}
	// A fresh local action after waking gives a full new allowance.
	allowRun(t, s, 3, time.Minute)
	if !s.RunAllowed() {
		t.Fatal("new allowance after wall-clock expiry refused")
	}
}

// The newest local action decides: it can extend the allowance or cut it short.
func TestRunAllowanceIsReplacedByTheNewestLocalAction(t *testing.T) {
	s, _ := standbyTurnedOn(t)
	allowRun(t, s, 2, 10*time.Minute)
	allowRun(t, s, 3, time.Minute)
	s.mu.Lock()
	d := time.Until(s.cycle.runUntil)
	s.mu.Unlock()
	if d <= 0 || d > time.Minute {
		t.Fatal("newer shorter allowance did not replace the older one", d)
	}
	allowRun(t, s, 4, 10*time.Minute)
	s.mu.Lock()
	d = time.Until(s.cycle.runUntil)
	s.mu.Unlock()
	if d <= time.Minute || d > 10*time.Minute {
		t.Fatal("newer longer allowance did not extend the older one", d)
	}
	// A refused request is not a local decision and leaves the allowance as it is.
	for name, bad := range map[string]time.Duration{"zero": 0, "above-cap": MaxRunAllowance + time.Nanosecond} {
		if r := s.AllowRun(s.lastRequest+1, s.Snapshot().Revision, bad); r.Disposition != "rejected" {
			t.Fatal(name, r)
		}
		s.mu.Lock()
		after := time.Until(s.cycle.runUntil)
		s.mu.Unlock()
		if !s.RunAllowed() || after <= time.Minute {
			t.Fatal("refused request changed the allowance", name, after)
		}
	}
	if r := s.DenyRun(s.lastRequest+1, s.Snapshot().Revision); r.Disposition != "accepted" || r.Intent != "deny-run" {
		t.Fatal(r)
	}
	if s.RunAllowed() {
		t.Fatal("run still allowed after local deny")
	}
}

// The relay replaces the control connection every few minutes. That is not a
// local decision, so it neither ends nor renews the allowance.
func TestRunAllowanceSurvivesControlReconnect(t *testing.T) {
	s, c := standbyTurnedOn(t)
	c.connected.Store(true)
	eventually(t, func() bool { return s.Snapshot().Registration == "authenticated" })
	allowRun(t, s, 2, time.Minute)
	s.mu.Lock()
	until := s.cycle.runUntil
	s.mu.Unlock()
	c.connected.Store(false)
	eventually(t, func() bool { return s.Snapshot().Registration != "authenticated" })
	c.connected.Store(true)
	eventually(t, func() bool { return s.Snapshot().Registration == "authenticated" })
	s.mu.Lock()
	same := s.cycle.runUntil.Equal(until)
	s.mu.Unlock()
	if !s.RunAllowed() || !same {
		t.Fatal("reconnect changed the allowance")
	}
}

// Turning standby off withdraws everything the person allowed; turning it on
// again must start from "not allowed", even inside the old allowance window.
func TestStandbyOffEndsRunAllowanceAndOnDoesNotRestoreIt(t *testing.T) {
	s, c := standbyTurnedOn(t)
	allowRun(t, s, 2, 10*time.Minute)
	if r := s.RequestOff(3, s.Snapshot().Revision); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	if s.RunAllowed() {
		t.Fatal("run allowed while standby is stopping")
	}
	if r := s.AllowRun(4, s.Snapshot().Revision, time.Minute); r.Disposition != "rejected" || s.RunAllowed() {
		t.Fatal("allowance accepted while standby is stopping", r)
	}
	close(c.gate)
	eventually(t, func() bool { return s.Snapshot().Standby == "off" })
	if r := s.AllowRun(5, s.Snapshot().Revision, time.Minute); r.Disposition != "rejected" || s.RunAllowed() {
		t.Fatal("allowance accepted while standby is off", r)
	}
	next := &idleConnector{done: make(chan struct{}), gate: make(chan struct{})}
	t.Cleanup(func() { close(next.gate) })
	s.mu.Lock()
	s.makeConnector = func(context.Context) (residentConnector, error) { return next, nil }
	s.mu.Unlock()
	if r := s.RequestOn(6, s.Snapshot().Revision); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	if s.Snapshot().Standby != "on" || s.RunAllowed() {
		t.Fatal("standby on restored an earlier allowance")
	}
}

func TestRunAllowanceRefusesUnboundedStaleAndClosedRequests(t *testing.T) {
	s, _ := standbyTurnedOn(t)
	for name, d := range map[string]time.Duration{"zero": 0, "negative": -time.Second, "above-cap": MaxRunAllowance + time.Nanosecond} {
		id := s.lastRequest + 1
		if r := s.AllowRun(id, s.Snapshot().Revision, d); r.Disposition != "rejected" || s.RunAllowed() {
			t.Fatal(name, r)
		}
	}
	allowRun(t, s, 10, MaxRunAllowance)
	if r := s.DenyRun(11, s.Snapshot().Revision); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	// A replayed or out-of-date request must not turn the allowance back on.
	for name, r := range map[string]StandbyReceipt{
		"reused-id":      s.AllowRun(10, s.Snapshot().Revision, time.Minute),
		"zero-id":        s.AllowRun(0, s.Snapshot().Revision, time.Minute),
		"stale-revision": s.AllowRun(12, s.Snapshot().Revision-1, time.Minute),
	} {
		if r.Disposition != "stale" || s.RunAllowed() {
			t.Fatal(name, r)
		}
	}
	allowRun(t, s, 13, time.Minute)
	// A stale deny is not a local decision made against the current state.
	if r := s.DenyRun(13, s.Snapshot().Revision); r.Disposition != "stale" || !s.RunAllowed() {
		t.Fatal("stale deny applied", r)
	}
	if r := s.DenyRun(14, s.Snapshot().Revision-1); r.Disposition != "stale" || !s.RunAllowed() {
		t.Fatal("deny against an old revision applied", r)
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	_ = s.Close(ctx)
	if s.RunAllowed() {
		t.Fatal("run allowed after close")
	}
	if r := s.AllowRun(15, s.Snapshot().Revision, time.Minute); r.Disposition == "accepted" || s.RunAllowed() {
		t.Fatal("allowance accepted after close", r)
	}
	var none *Standby
	if none.RunAllowed() || none.AllowRun(1, 1, time.Minute).Disposition != "rejected" || none.DenyRun(1, 1).Disposition != "rejected" {
		t.Fatal("nil standby allowed run")
	}
}
