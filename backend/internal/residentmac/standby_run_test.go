package residentmac

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/workerowner"
)

// The lengths a person can pick for the allowance (#399). Nothing else.
var listedRunAllowances = []time.Duration{2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 12 * time.Hour, 24 * time.Hour}

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

// stepClock stands in for the two clocks the allowance is kept on. awake moves
// both. asleep moves only the wall clock, as on a Mac whose monotonic clock
// stops while it sleeps. setBack moves only the wall clock, backwards.
type stepClock struct {
	base       time.Time
	mono, wall atomic.Int64
}

func (c *stepClock) now() (time.Time, time.Time) {
	return c.base.Add(time.Duration(c.mono.Load())), c.base.Add(time.Duration(c.wall.Load()))
}
func (c *stepClock) awake(d time.Duration)   { c.mono.Add(int64(d)); c.wall.Add(int64(d)) }
func (c *stepClock) asleep(d time.Duration)  { c.wall.Add(int64(d)) }
func (c *stepClock) setBack(d time.Duration) { c.wall.Add(-int64(d)) }

// clockedStandbyOn must run inside a synctest bubble: the monitor's ticker is
// then virtual, and settle lets it look at the clock deterministically.
func clockedStandbyOn(t *testing.T) (*Standby, *idleConnector, *stepClock) {
	t.Helper()
	s, c, _ := idleStandby(t)
	clock := &stepClock{base: time.Now()}
	s.clock = clock.now
	if r := s.RequestOn(1, s.Snapshot().Revision); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	c.connected.Store(true)
	settle()
	if o := s.Snapshot(); o.Registration != "authenticated" {
		t.Fatal("standby did not authenticate", o)
	}
	return s, c, clock
}

// settle lets the standby monitor take a few looks at the clock.
func settle() {
	time.Sleep(100 * time.Millisecond)
	synctest.Wait()
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
	allowRun(t, s, 2, 2*time.Hour)
	if !s.RunAllowed() {
		t.Fatal("accepted allowance did not allow run")
	}
}

// Admission is sealed before standby leaves "on" (for example when the first
// authentication times out). From that moment nothing new may start.
func TestSealedAdmissionEndsRunAllowanceBeforeStandbyLeavesOn(t *testing.T) {
	s, _ := standbyTurnedOn(t)
	allowRun(t, s, 2, 2*time.Hour)
	s.mu.Lock()
	s.observation.AdmissionSealed = true
	on := s.observation.Standby == "on"
	s.mu.Unlock()
	if !on || s.RunAllowed() {
		t.Fatal("run allowed after admission was sealed")
	}
	if r := s.AllowRun(3, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "rejected" || s.RunAllowed() {
		t.Fatal("allowance accepted after admission was sealed", r)
	}
}

// The person picks the length from a short list (#399). A length nobody can
// pick in the GUI is not a local decision: it is refused and leaves the
// current allowance, and its end, as they were.
func TestRunAllowanceTakesOnlyTheListedLengths(t *testing.T) {
	s, _ := standbyTurnedOn(t)
	for _, d := range listedRunAllowances {
		allowRun(t, s, s.lastRequest+1, d)
		if !s.RunAllowed() {
			t.Fatal("listed length did not allow run", d)
		}
	}
	end := s.Snapshot().RunUntil
	for _, d := range []time.Duration{0, -2 * time.Hour, time.Nanosecond, 15 * time.Minute, time.Hour, 2*time.Hour - 1, 2*time.Hour + 1, 3 * time.Hour, 23 * time.Hour, 24*time.Hour + 1, 25 * time.Hour, 48 * time.Hour} {
		if r := s.AllowRun(s.lastRequest+1, s.Snapshot().Revision, d); r.Disposition != "rejected" {
			t.Fatal("unlisted length accepted", d, r)
		}
		if o := s.Snapshot(); !s.RunAllowed() || !o.RunUntil.Equal(end) {
			t.Fatal("refused length changed the allowance", d, o.RunUntil, end)
		}
	}
}

// The allowance is the last safety net for a person who forgot to turn standby
// off (#399). When it ends, the Mac does what standby OFF does: no new work,
// owned work stopped and collected. That holds for every length, exactly at
// its end and not one moment before.
func TestRunAllowanceExpiryTurnsStandbyOffForEveryLength(t *testing.T) {
	for _, d := range listedRunAllowances {
		t.Run(d.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, c, clock := clockedStandbyOn(t)
				allowRun(t, s, 2, d)
				clock.awake(d - time.Nanosecond)
				settle()
				if o := s.Snapshot(); !s.RunAllowed() || o.Standby != "on" || o.AdmissionSealed || o.RunExpired {
					t.Fatal("allowance ended before its length", o)
				}
				clock.awake(time.Nanosecond)
				if s.RunAllowed() {
					t.Fatal("run allowed at the allowance's end")
				}
				// Before the monitor has acted, fixed work is refused already.
				s.mu.Lock()
				admitted := s.admissionLocked(s.cycle, false)
				s.mu.Unlock()
				if admitted != admitNothing {
					t.Fatal("expired allowance still admitted fixed work", admitted)
				}
				// The host cannot stretch an allowance that has already ended.
				if r := s.AllowRun(3, s.Snapshot().Revision, 24*time.Hour); r.Disposition != "rejected" {
					t.Fatal("ended allowance extended", r)
				}
				settle()
				if o := s.Snapshot(); o.Standby != "stopping" || !o.AdmissionSealed || !o.RunExpired || o.CanRequestOff || !o.RunUntil.IsZero() {
					t.Fatal("expiry did not turn standby off", o)
				}
				close(c.gate)
				eventually(t, func() bool { return s.Snapshot().Standby == "off" })
				select {
				case <-c.done:
				default:
					t.Fatal("expiry left the control open")
				}
				if o := s.Snapshot(); o.Cleanup != "confirmed" || o.Work != "idle" || !o.RunExpired || !o.CanRequestOn {
					t.Fatal("expiry did not end in a collected standby OFF that records why", o)
				}
			})
		})
	}
}

// The monotonic clock may stop while the Mac sleeps. An allowance given before
// a night of sleep must not still be open in the morning, so the wall clock
// ends it too, and the Mac stops as soon as it wakes.
func TestRunAllowanceExpiresByWallClockAcrossSleep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, c, clock := clockedStandbyOn(t)
		allowRun(t, s, 2, 2*time.Hour)
		clock.awake(time.Hour)
		clock.asleep(time.Hour - time.Nanosecond)
		settle()
		if !s.RunAllowed() || s.Snapshot().Standby != "on" {
			t.Fatal("allowance ended before its wall-clock end")
		}
		clock.asleep(8 * time.Hour)
		if s.RunAllowed() {
			t.Fatal("allowance outlived its wall-clock end")
		}
		settle()
		if o := s.Snapshot(); o.Standby != "stopping" || !o.RunExpired {
			t.Fatal("waking past the wall-clock end did not turn standby off", o)
		}
		close(c.gate)
		eventually(t, func() bool { return s.Snapshot().Standby == "off" })
	})
}

// Setting the wall clock back must not stretch the allowance: elapsed time
// ends it too.
func TestRunAllowanceExpiresByElapsedTimeWhenWallClockIsSetBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, c, clock := clockedStandbyOn(t)
		allowRun(t, s, 2, 2*time.Hour)
		clock.setBack(24 * time.Hour)
		clock.awake(2 * time.Hour)
		if s.RunAllowed() {
			t.Fatal("allowance outlived its elapsed length")
		}
		settle()
		if o := s.Snapshot(); o.Standby != "stopping" || !o.RunExpired {
			t.Fatal("elapsed expiry did not turn standby off", o)
		}
		close(c.gate)
		eventually(t, func() bool { return s.Snapshot().Standby == "off" })
	})
}

// The Mac's UI tells the person ten minutes ahead, so they can extend it or
// wrap up. The status carries the wall-clock end the UI shows ("until hh:mm"),
// and a new local allowance moves the end and withdraws the warning.
func TestRunAllowanceWarnsTenMinutesBeforeItsEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, clock := clockedStandbyOn(t)
		if o := s.Snapshot(); !o.RunUntil.IsZero() || o.RunEndingSoon {
			t.Fatal("standby on alone published an allowance end", o)
		}
		allowRun(t, s, 2, 2*time.Hour)
		_, wall := clock.now()
		if o := s.Snapshot(); !o.RunUntil.Equal(wall.Add(2*time.Hour)) || o.RunEndingSoon {
			t.Fatal("allowance did not publish its wall-clock end", o.RunUntil, wall)
		}
		clock.awake(2*time.Hour - RunEndingNotice - time.Nanosecond)
		settle()
		before := s.Snapshot()
		if before.RunEndingSoon {
			t.Fatal("warned earlier than ten minutes before the end")
		}
		clock.awake(time.Nanosecond)
		settle()
		o := s.Snapshot()
		if !o.RunEndingSoon || o.Revision <= before.Revision || o.Standby != "on" {
			t.Fatal("no fresh status warned ten minutes before the end", o)
		}
		allowRun(t, s, 3, 4*time.Hour)
		_, wall = clock.now()
		if o := s.Snapshot(); o.RunEndingSoon || !o.RunUntil.Equal(wall.Add(4*time.Hour)) {
			t.Fatal("extension kept the warning or the old end", o)
		}
		if r := s.DenyRun(4, s.Snapshot().Revision); r.Disposition != "accepted" {
			t.Fatal(r)
		}
		if o := s.Snapshot(); !o.RunUntil.IsZero() || o.RunEndingSoon || o.Standby != "on" {
			t.Fatal("deny left an allowance end in the status", o)
		}
	})
}

// A deny that arrives after the end must not undo the expiry: standby still
// goes off.
func TestDenyCannotUndoAnEndedAllowance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, c, clock := clockedStandbyOn(t)
		allowRun(t, s, 2, 2*time.Hour)
		clock.awake(2 * time.Hour)
		_ = s.DenyRun(3, s.Snapshot().Revision)
		settle()
		if o := s.Snapshot(); o.Standby != "stopping" || !o.RunExpired {
			t.Fatal("deny after the end kept standby on", o)
		}
		close(c.gate)
		eventually(t, func() bool { return s.Snapshot().Standby == "off" })
	})
}

// "Expiry is standby OFF" (#399): owned work is stopped and collected on the
// same path. Only the recorded reason differs.
func TestRunAllowanceExpiryStopsOwnedWorkLikeStandbyOff(t *testing.T) {
	final := map[string]StandbyObservation{}
	for _, end := range []string{"off", "expiry"} {
		t.Run(end, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, c, clock := clockedStandbyOn(t)
				allowRun(t, s, 2, 2*time.Hour)
				w := &cleanupWorker{started: make(chan struct{}), runJoined: make(chan struct{}), entered: make(chan struct{}), release: make(chan struct{})}
				requests := make(chan *workerowner.Request, 1)
				waited := make(chan error, 1)
				go func() { waited <- s.cycle.waitSource(context.Background(), requests) }()
				finite, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				request := &workerowner.Request{SourceContext: finite, Deadline: time.Now().Add(time.Minute), Reply: make(chan error, 1), Approve: func(context.Context) (*workerowner.Worker, error) { return &workerowner.Worker{Session: w}, nil }}
				requests <- request
				if e := <-request.Reply; e != nil {
					t.Fatal(e)
				}
				<-w.started
				settle()
				if o := s.Snapshot(); o.Owned != 1 || o.Work == "idle" {
					t.Fatal("owned work not observed", o)
				}
				if end == "off" {
					if r := s.RequestOff(3, s.Snapshot().Revision); r.Disposition != "accepted" {
						t.Fatal(r)
					}
				} else {
					clock.awake(2 * time.Hour)
				}
				settle()
				<-w.entered // the owned worker is being stopped
				if o := s.Snapshot(); !o.AdmissionSealed || o.Standby != "stopping" {
					t.Fatal("standby not stopping while owned work is stopped", o)
				}
				close(w.release)
				close(c.gate)
				if e := <-waited; e != nil {
					t.Fatal(e)
				}
				<-w.runJoined
				eventually(t, func() bool { return s.Snapshot().Standby == "off" })
				o := s.Snapshot()
				if o.Cleanup != "confirmed" || o.Owned != 0 || w.closes.Load() != 1 || o.RunExpired != (end == "expiry") {
					t.Fatal("owned work not collected", o, w.closes.Load())
				}
				o.Revision, o.RunExpired, o.Transition = 0, false, nil
				final[end] = o
			})
		})
	}
	if final["off"] != final["expiry"] {
		t.Fatal("expiry ended differently from standby OFF", final["off"], final["expiry"])
	}
}

// The newest local action decides: it can extend the allowance or cut it short.
func TestRunAllowanceIsReplacedByTheNewestLocalAction(t *testing.T) {
	s, _ := standbyTurnedOn(t)
	allowRun(t, s, 2, 24*time.Hour)
	allowRun(t, s, 3, 2*time.Hour)
	s.mu.Lock()
	d := time.Until(s.cycle.runUntil)
	s.mu.Unlock()
	if d <= 0 || d > 2*time.Hour {
		t.Fatal("newer shorter allowance did not replace the older one", d)
	}
	allowRun(t, s, 4, 24*time.Hour)
	s.mu.Lock()
	d = time.Until(s.cycle.runUntil)
	s.mu.Unlock()
	if d <= 2*time.Hour || d > 24*time.Hour {
		t.Fatal("newer longer allowance did not extend the older one", d)
	}
	if r := s.DenyRun(s.lastRequest+1, s.Snapshot().Revision); r.Disposition != "accepted" || r.Intent != "deny-run" {
		t.Fatal(r)
	}
	if s.RunAllowed() || s.Snapshot().Standby != "on" {
		t.Fatal("deny did not withdraw the allowance, or it ended standby")
	}
}

// The relay replaces the control connection every few minutes. That is not a
// local decision, so it neither ends nor renews the allowance.
func TestRunAllowanceSurvivesControlReconnect(t *testing.T) {
	s, c := standbyTurnedOn(t)
	c.connected.Store(true)
	eventually(t, func() bool { return s.Snapshot().Registration == "authenticated" })
	allowRun(t, s, 2, 2*time.Hour)
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
// again must start from "not allowed", even inside the old allowance window,
// and without the old expiry record.
func TestStandbyOffEndsRunAllowanceAndOnDoesNotRestoreIt(t *testing.T) {
	s, c := standbyTurnedOn(t)
	allowRun(t, s, 2, 24*time.Hour)
	if r := s.RequestOff(3, s.Snapshot().Revision); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	if o := s.Snapshot(); s.RunAllowed() || !o.RunUntil.IsZero() || o.RunExpired {
		t.Fatal("run allowed while standby is stopping, or OFF recorded as expiry", o)
	}
	if r := s.AllowRun(4, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "rejected" || s.RunAllowed() {
		t.Fatal("allowance accepted while standby is stopping", r)
	}
	close(c.gate)
	eventually(t, func() bool { return s.Snapshot().Standby == "off" })
	if r := s.AllowRun(5, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "rejected" || s.RunAllowed() {
		t.Fatal("allowance accepted while standby is off", r)
	}
	next := &idleConnector{done: make(chan struct{}), gate: make(chan struct{})}
	t.Cleanup(func() { close(next.gate) })
	s.mu.Lock()
	s.makeConnector = func(context.Context) (residentConnector, error) { return next, nil }
	s.observation.RunExpired = true // as if the last cycle had expired
	s.mu.Unlock()
	if r := s.RequestOn(6, s.Snapshot().Revision); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	if o := s.Snapshot(); o.Standby != "on" || s.RunAllowed() || o.RunExpired || !o.RunUntil.IsZero() {
		t.Fatal("standby on restored an earlier allowance or its expiry", o)
	}
}

func TestRunAllowanceRefusesStaleAndClosedRequests(t *testing.T) {
	s, _ := standbyTurnedOn(t)
	allowRun(t, s, 10, 2*time.Hour)
	if r := s.DenyRun(11, s.Snapshot().Revision); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	// A replayed or out-of-date request must not turn the allowance back on.
	for name, r := range map[string]StandbyReceipt{
		"reused-id":      s.AllowRun(10, s.Snapshot().Revision, 2*time.Hour),
		"zero-id":        s.AllowRun(0, s.Snapshot().Revision, 2*time.Hour),
		"stale-revision": s.AllowRun(12, s.Snapshot().Revision-1, 2*time.Hour),
	} {
		if r.Disposition != "stale" || s.RunAllowed() {
			t.Fatal(name, r)
		}
	}
	allowRun(t, s, 13, 2*time.Hour)
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
	if r := s.AllowRun(15, s.Snapshot().Revision, 2*time.Hour); r.Disposition == "accepted" || s.RunAllowed() {
		t.Fatal("allowance accepted after close", r)
	}
	var none *Standby
	if none.RunAllowed() || none.AllowRun(1, 1, 2*time.Hour).Disposition != "rejected" || none.DenyRun(1, 1).Disposition != "rejected" {
		t.Fatal("nil standby allowed run")
	}
}

// consume asks this once for every intent the gateway sends. A signed
// command-run intent names a command nobody at the Mac has seen, so it gets
// through only while a local allowance is live; without one it is refused and
// standby keeps serving fixed work.
func TestAdmissionLetsCommandRunThroughOnlyUnderLiveAllowance(t *testing.T) {
	s, _ := standbyTurnedOn(t)
	s.mu.Lock()
	c := s.cycle
	s.mu.Unlock()
	admit := func(c *standbyCycle, run bool) admission {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.admissionLocked(c, run)
	}
	if admit(c, false) != admitFixed || admit(c, true) != admitNoRun {
		t.Fatal("standby on alone admitted a command run, or refused fixed work")
	}
	allowRun(t, s, 2, 2*time.Hour)
	if admit(c, true) != admitRun || admit(c, false) != admitFixed {
		t.Fatal("live allowance did not admit a command run")
	}
	if admit(&standbyCycle{}, true) != admitNothing || admit(&standbyCycle{}, false) != admitNothing {
		t.Fatal("an intent of another standby cycle was admitted")
	}
	if r := s.DenyRun(3, s.Snapshot().Revision); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	if admit(c, true) != admitNoRun || admit(c, false) != admitFixed {
		t.Fatal("withdrawn allowance still admitted a command run, or ended fixed work")
	}
	allowRun(t, s, 4, 2*time.Hour)
	s.mu.Lock()
	s.observation.AdmissionSealed = true
	s.mu.Unlock()
	if admit(c, true) != admitNothing || admit(c, false) != admitNothing {
		t.Fatal("sealed admission still admitted work")
	}
}
