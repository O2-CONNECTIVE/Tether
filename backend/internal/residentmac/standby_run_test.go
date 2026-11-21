package residentmac

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/workerowner"
)

type terminalStandbyConnector struct {
	done   chan struct{}
	once   sync.Once
	result error
}

func (c *terminalStandbyConnector) Run(context.Context) error {
	c.once.Do(func() { close(c.done) })
	return c.result
}
func (c *terminalStandbyConnector) Accept(ctx context.Context) (*agenttransport.ResidentSourceLease, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, agenttransport.ErrClosed
	}
}
func (c *terminalStandbyConnector) Stop()                       { c.once.Do(func() { close(c.done) }) }
func (c *terminalStandbyConnector) Close(context.Context) error { return nil }
func (c *terminalStandbyConnector) Snapshot() agenttransport.ResidentControlSnapshot {
	return agenttransport.ResidentControlSnapshot{}
}

func terminalRefusalConnector() *terminalStandbyConnector {
	return &terminalStandbyConnector{done: make(chan struct{}), result: &agenttransport.ResidentNeedsActionError{Reason: agenttransport.ResidentFailureIdentityRefused, Err: agenttransport.ErrRefused}}
}

type statusStandbyConnector struct {
	mu       sync.Mutex
	snapshot agenttransport.ResidentControlSnapshot
}

func (c *statusStandbyConnector) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
func (c *statusStandbyConnector) Accept(ctx context.Context) (*agenttransport.ResidentSourceLease, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*statusStandbyConnector) Stop()                       {}
func (*statusStandbyConnector) Close(context.Context) error { return nil }
func (c *statusStandbyConnector) Snapshot() agenttransport.ResidentControlSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot
}
func (c *statusStandbyConnector) setSnapshot(snapshot agenttransport.ResidentControlSnapshot) {
	c.mu.Lock()
	c.snapshot = snapshot
	c.mu.Unlock()
}

func TestReconnectWakeDetectorUsesInjectedClocksAndNetworkFingerprint(t *testing.T) {
	base := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	oldNetwork := reconnectNetworkFingerprint([]string{"en0/192.0.2.10", "en1/198.51.100.4"})
	if oldNetwork != reconnectNetworkFingerprint([]string{"en1/198.51.100.4", "en0/192.0.2.10"}) {
		t.Fatal("interface enumeration order changed the network fingerprint")
	}
	newNetwork := reconnectNetworkFingerprint([]string{"en0/203.0.113.8"})
	var detector reconnectWakeDetector
	if detector.observe(base, base, oldNetwork, true) {
		t.Fatal("baseline produced a wake")
	}
	if detector.observe(base.Add(3*time.Second), base.Add(time.Second), oldNetwork, true) {
		t.Fatal("clock drift at the exact two-second threshold produced a wake")
	}
	if !detector.observe(base.Add(7*time.Second), base.Add(2*time.Second), oldNetwork, true) {
		t.Fatal("three-second sleep-aware clock gap did not produce a wake")
	}
	if !detector.observe(base.Add(8*time.Second), base.Add(3*time.Second), newNetwork, true) {
		t.Fatal("changed address fingerprint did not produce a wake")
	}
}

func TestRegistrationStatusMovesFromConnectingThroughReconnectToAuthenticated(t *testing.T) {
	for _, tc := range []struct {
		name          string
		control       agenttransport.ResidentControlSnapshot
		authenticated bool
		want          string
	}{
		{"first attempt", agenttransport.ResidentControlSnapshot{}, false, "connecting"},
		{"temporary failure waiting", agenttransport.ResidentControlSnapshot{RetryWaiting: true, Reconnecting: true}, false, "reconnecting"},
		{"temporary failure dialing", agenttransport.ResidentControlSnapshot{Reconnecting: true}, false, "reconnecting"},
		{"known session reconnecting", agenttransport.ResidentControlSnapshot{}, true, "reconnecting"},
		{"authenticated", agenttransport.ResidentControlSnapshot{Connected: true}, true, "authenticated"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := registrationStatus(tc.control, tc.authenticated); got != tc.want {
				t.Fatalf("registrationStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStandbyMonitorPublishesReconnectThenAuthentication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newStandby()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		connector := new(statusStandbyConnector)
		cycle := &standbyCycle{ctx: ctx, cancel: cancel, owner: workerowner.New(time.Second), done: make(chan struct{}), initialUntil: time.Now().Add(initialAuthentication), connector: connector}
		s.mu.Lock()
		s.cycle = cycle
		s.observation.Standby = "on"
		s.observation.Registration = "connecting"
		s.observation.AdmissionSealed = false
		s.observation.Transition = &StandbyTransition{RequestID: 1, Intent: "on", Outcome: "pending"}
		s.endStandbyInLocked(2 * time.Hour)
		s.changedLocked()
		s.mu.Unlock()
		go s.monitor(cycle, make(chan standbyReport, 1))

		settle()
		if o := s.Snapshot(); o.Registration != "connecting" {
			t.Fatal("first connection did not remain connecting", o)
		}
		connector.setSnapshot(agenttransport.ResidentControlSnapshot{RetryWaiting: true, Reconnecting: true})
		settle()
		if o := s.Snapshot(); o.Registration != "reconnecting" {
			t.Fatal("temporary failure was not published as reconnecting", o)
		}
		connector.setSnapshot(agenttransport.ResidentControlSnapshot{Connected: true, Generation: "fresh-generation"})
		settle()
		if o := s.Snapshot(); o.Registration != "authenticated" || o.Transition == nil || o.Transition.Outcome != "applied" {
			t.Fatal("new authenticated connection was not published", o)
		}
		cancel()
		synctest.Wait()
	})
}

func TestStandbyKeepsNeedsActionAfterCleanConnectorExit(t *testing.T) {
	s, _, calls := idleStandby(t)
	connector := terminalRefusalConnector()
	s.makeConnector = func(context.Context) (residentConnector, error) {
		calls.Add(1)
		return connector, nil
	}
	if r := s.RequestOn(1, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	eventually(t, func() bool { return s.Snapshot().Registration == "needs_action" })
	o := s.Snapshot()
	if o.Standby != "on" || o.RegistrationReason != RegistrationReasonIdentityRefused || !o.AdmissionSealed || o.Cleanup != "confirmed" || !o.CanRequestOff || o.CanRequestOn {
		t.Fatal("clean terminal refusal did not retain an ON needs-action state", o)
	}
	if calls.Load() != 1 {
		t.Fatal("terminal refusal created another connector", calls.Load())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal("closing the process-local standby failed", err)
	}
	if o := s.Snapshot(); o.Standby != "off" || o.Registration != "disconnected" || o.RegistrationReason != "" {
		t.Fatal("closing retained a process-local needs-action state", o)
	}
}

func TestNeedsActionExpiresUsingInjectedClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, _ := idleStandby(t)
		clock := &stepClock{base: time.Now()}
		s.clock = clock.now
		refusal := terminalRefusalConnector()
		s.makeConnector = func(context.Context) (residentConnector, error) { return refusal, nil }
		if r := s.RequestOn(1, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "accepted" {
			t.Fatal(r)
		}
		settle()
		if o := s.Snapshot(); o.Standby != "on" || o.Registration != "needs_action" {
			t.Fatal("clean terminal refusal was not retained before the end", o)
		}
		clock.awake(2 * time.Hour)
		settle()
		if o := s.Snapshot(); o.Standby != "off" || o.Registration != "disconnected" || o.RegistrationReason != "" || !o.StandbyExpired || !o.CanRequestOn {
			t.Fatal("needs-action standby did not turn off at its deadline", o)
		}
	})
}

func TestNeedsActionRequiresOffBeforeOnRetries(t *testing.T) {
	s, _, calls := idleStandby(t)
	first := terminalRefusalConnector()
	second := &idleConnector{done: make(chan struct{}), gate: make(chan struct{})}
	t.Cleanup(func() { close(second.gate) })
	s.makeConnector = func(context.Context) (residentConnector, error) {
		if calls.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	}
	if r := s.RequestOn(1, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	eventually(t, func() bool { return s.Snapshot().Registration == "needs_action" })
	if r := s.RequestOn(2, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "rejected" || calls.Load() != 1 {
		t.Fatal("ON alone retried a needs-action cycle", r, calls.Load())
	}
	if r := s.RequestOff(3, s.Snapshot().Revision); r.Disposition != "accepted" {
		t.Fatal("OFF did not clear a needs-action cycle", r)
	}
	o := s.Snapshot()
	if o.Standby != "off" || o.Registration != "disconnected" || o.RegistrationReason != "" || !o.CanRequestOn {
		t.Fatal("OFF did not clear the reason and release ON", o)
	}
	if r := s.RequestOn(4, o.Revision, 2*time.Hour); r.Disposition != "accepted" {
		t.Fatal("ON after OFF did not start a new cycle", r)
	}
	eventually(t, func() bool { return calls.Load() == 2 })
}

// The standby lengths a person can pick (#399). Nothing else.
var listedStandbyLengths = []time.Duration{2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 12 * time.Hour, 24 * time.Hour}

// Lengths nobody can pick in the GUI, including the old fifteen-minute limit.
var unlistedStandbyLengths = []time.Duration{0, -2 * time.Hour, time.Nanosecond, 15 * time.Minute, time.Hour, 2*time.Hour - 1, 2*time.Hour + 1, 3 * time.Hour, 23 * time.Hour, 24*time.Hour + 1, 25 * time.Hour, 48 * time.Hour}

func standbyTurnedOn(t *testing.T) (*Standby, *idleConnector) {
	t.Helper()
	s, c, _ := idleStandby(t)
	if r := s.RequestOn(1, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	return s, c
}
func allowRun(t *testing.T, s *Standby, id uint64) {
	t.Helper()
	if r := s.AllowRun(id, s.Snapshot().Revision); r.Disposition != "accepted" || r.Intent != "allow-run" || r.RequestID != id {
		t.Fatal(r)
	}
}
func extend(t *testing.T, s *Standby, id uint64, d time.Duration) {
	t.Helper()
	if r := s.ExtendStandby(id, s.Snapshot().Revision, d); r.Disposition != "accepted" || r.Intent != "extend" || r.RequestID != id {
		t.Fatal(r)
	}
}

// stepClock stands in for the two clocks standby's end is kept on. awake moves
// both. asleep moves only the wall clock: the worst case, an elapsed-time clock
// that stops while the Mac sleeps, as Go's own does. setBack moves only the
// wall clock, backwards.
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

// clockedStandbyOn turns standby on for d. It must run inside a synctest
// bubble: the monitor's ticker is then virtual, and settle lets it look at the
// clock deterministically.
func clockedStandbyOn(t *testing.T, d time.Duration) (*Standby, *idleConnector, *stepClock) {
	t.Helper()
	s, c, _ := idleStandby(t)
	clock := &stepClock{base: time.Now()}
	s.clock = clock.now
	if r := s.RequestOn(1, s.Snapshot().Revision, d); r.Disposition != "accepted" {
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
// remote party run commands. Only a separate local action does.
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
	allowRun(t, s, 2)
	if !s.RunAllowed() {
		t.Fatal("accepted allowance did not allow run")
	}
}

// Admission is sealed before standby leaves "on" (for example when the first
// authentication times out). From that moment nothing new may start.
func TestSealedAdmissionEndsRunAllowanceBeforeStandbyLeavesOn(t *testing.T) {
	s, _ := standbyTurnedOn(t)
	allowRun(t, s, 2)
	s.mu.Lock()
	s.observation.AdmissionSealed = true
	on := s.observation.Standby == "on"
	s.mu.Unlock()
	if !on || s.RunAllowed() {
		t.Fatal("run allowed after admission was sealed")
	}
	if r := s.AllowRun(3, s.Snapshot().Revision); r.Disposition != "rejected" || s.RunAllowed() {
		t.Fatal("allowance accepted after admission was sealed", r)
	}
	if r := s.ExtendStandby(4, s.Snapshot().Revision, 24*time.Hour); r.Disposition != "rejected" {
		t.Fatal("standby extended after admission was sealed", r)
	}
}

// The person picks how long standby stays on from a short list (#399). A
// length nobody can pick in the GUI is not a local decision: ON with it does
// not start standby, and extending with it leaves the end as it was.
func TestStandbyTakesOnlyTheListedLengths(t *testing.T) {
	for _, d := range unlistedStandbyLengths {
		s, _, _ := idleStandby(t)
		if r := s.RequestOn(1, s.Snapshot().Revision, d); r.Disposition != "rejected" {
			t.Fatal("ON with an unlisted length accepted", d, r)
		}
		if o := s.Snapshot(); o.Standby != "off" || !o.CanRequestOn || !o.StandbyUntil.IsZero() {
			t.Fatal("refused ON changed standby", d, o)
		}
	}
	// Every accepted ON has an end, d from now: there is no standby without one.
	for _, d := range listedStandbyLengths {
		s, _, _ := idleStandby(t)
		if r := s.RequestOn(1, s.Snapshot().Revision, d); r.Disposition != "accepted" || s.Snapshot().Standby != "on" {
			t.Fatal("ON with a listed length refused", d, r)
		}
		if left := time.Until(s.Snapshot().StandbyUntil); left <= d-time.Minute || left > d {
			t.Fatal("ON did not end after its length", d, left)
		}
	}
	s, _ := standbyTurnedOn(t)
	for _, d := range listedStandbyLengths {
		extend(t, s, s.lastRequest+1, d)
	}
	end := s.Snapshot().StandbyUntil
	for _, d := range unlistedStandbyLengths {
		if r := s.ExtendStandby(s.lastRequest+1, s.Snapshot().Revision, d); r.Disposition != "rejected" {
			t.Fatal("unlisted extension accepted", d, r)
		}
		if o := s.Snapshot(); o.Standby != "on" || !o.StandbyUntil.Equal(end) {
			t.Fatal("refused extension changed the end", d, o.StandbyUntil, end)
		}
	}
}

// The end of standby is the last safety net for a person who forgot to turn it
// off (#399). At the end, the Mac does what standby OFF does: no new work,
// owned work stopped and collected, and the command-run allowance gone with
// it. That holds for every length, exactly at its end and not one moment before.
func TestStandbyEndTurnsStandbyOffForEveryLength(t *testing.T) {
	for _, d := range listedStandbyLengths {
		t.Run(d.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, c, clock := clockedStandbyOn(t, d)
				allowRun(t, s, 2)
				clock.awake(d - time.Nanosecond)
				settle()
				if o := s.Snapshot(); !s.RunAllowed() || o.Standby != "on" || o.AdmissionSealed || o.StandbyExpired {
					t.Fatal("standby ended before its length", o)
				}
				clock.awake(time.Nanosecond)
				// Before the monitor has acted, nothing new is admitted already.
				s.mu.Lock()
				fixed, run := s.admissionLocked(s.cycle, false), s.admissionLocked(s.cycle, true)
				s.mu.Unlock()
				if fixed != admitNothing || run != admitNothing || s.RunAllowed() {
					t.Fatal("ended standby still admitted work", fixed, run)
				}
				// The host cannot stretch standby that has already ended.
				if r := s.ExtendStandby(3, s.Snapshot().Revision, 24*time.Hour); r.Disposition != "rejected" {
					t.Fatal("ended standby extended", r)
				}
				settle()
				if o := s.Snapshot(); o.Standby != "stopping" || !o.AdmissionSealed || !o.StandbyExpired || o.CanRequestOff || !o.StandbyUntil.IsZero() {
					t.Fatal("the end did not turn standby off", o)
				}
				close(c.gate)
				eventually(t, func() bool { return s.Snapshot().Standby == "off" })
				select {
				case <-c.done:
				default:
					t.Fatal("the end left the control open")
				}
				if o := s.Snapshot(); o.Cleanup != "confirmed" || o.Work != "idle" || !o.StandbyExpired || !o.CanRequestOn {
					t.Fatal("the end did not finish in a collected standby OFF that records why", o)
				}
			})
		})
	}
}

// Standby turned on before a night of sleep must not still be on in the
// morning. Should elapsed time stop while the Mac sleeps, the wall clock ends
// standby too, and the Mac stops as soon as it wakes.
func TestStandbyEndsByWallClockAcrossSleep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, c, clock := clockedStandbyOn(t, 2*time.Hour)
		clock.awake(time.Hour)
		clock.asleep(time.Hour - time.Nanosecond)
		settle()
		if o := s.Snapshot(); o.Standby != "on" || o.StandbyExpired {
			t.Fatal("standby ended before its wall-clock end", o)
		}
		clock.asleep(8 * time.Hour)
		settle()
		if o := s.Snapshot(); o.Standby != "stopping" || !o.StandbyExpired {
			t.Fatal("waking past the wall-clock end did not turn standby off", o)
		}
		close(c.gate)
		eventually(t, func() bool { return s.Snapshot().Standby == "off" })
	})
}

// Setting the wall clock back must not stretch standby: elapsed time ends it
// too. The status then shows that earlier end on the wall clock as it now
// reads, since the person plans around the end they are shown.
func TestStandbyEndsByElapsedTimeWhenWallClockIsSetBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, c, clock := clockedStandbyOn(t, 2*time.Hour)
		clock.setBack(24 * time.Hour)
		settle()
		if _, wall := clock.now(); !s.Snapshot().StandbyUntil.Equal(wall.Add(2 * time.Hour)) {
			t.Fatal("status kept the wall-clock end the set-back clock no longer reaches first", s.Snapshot().StandbyUntil, wall)
		}
		clock.awake(2*time.Hour - time.Nanosecond)
		settle()
		if s.Snapshot().Standby != "on" {
			t.Fatal("standby ended before its elapsed length")
		}
		clock.awake(time.Nanosecond)
		settle()
		if o := s.Snapshot(); o.Standby != "stopping" || !o.StandbyExpired {
			t.Fatal("elapsed time did not turn standby off", o)
		}
		close(c.gate)
		eventually(t, func() bool { return s.Snapshot().Standby == "off" })
	})
}

// Once standby has been seen to run out it stays run out, whichever clock saw
// it: setting the wall clock back before the monitor's next look must not
// reopen the allowance or extension, and an OFF in that moment still records
// the end as why standby stopped.
func TestStandbyStaysRunOutOnceSeenRunOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, c, clock := clockedStandbyOn(t, 2*time.Hour)
		clock.asleep(2 * time.Hour)
		if s.RunAllowed() {
			t.Fatal("standby past its wall-clock end admitted command runs")
		}
		clock.setBack(time.Hour)
		if r := s.AllowRun(2, s.Snapshot().Revision); r.Disposition != "rejected" {
			t.Fatal("setting the wall clock back reopened the allowance", r)
		}
		if r := s.ExtendStandby(3, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "rejected" {
			t.Fatal("setting the wall clock back reopened extension", r)
		}
		if r := s.RequestOff(4, s.Snapshot().Revision); r.Disposition != "accepted" {
			t.Fatal(r)
		}
		if o := s.Snapshot(); !o.StandbyExpired {
			t.Fatal("an OFF after the end lost the end as why standby stopped", o)
		}
		close(c.gate)
		eventually(t, func() bool { o := s.Snapshot(); return o.Standby == "off" && o.StandbyExpired })
	})
}

// The Mac's UI tells the person ten minutes ahead, so they can extend standby
// or wrap up. The status carries the wall-clock end the UI shows ("until
// hh:mm"), and extending moves the end and withdraws the warning.
func TestStandbyWarnsTenMinutesBeforeItsEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, clock := clockedStandbyOn(t, 2*time.Hour)
		_, wall := clock.now()
		if o := s.Snapshot(); !o.StandbyUntil.Equal(wall.Add(2*time.Hour)) || o.StandbyEndingSoon {
			t.Fatal("standby did not publish its wall-clock end", o.StandbyUntil, wall)
		}
		clock.awake(2*time.Hour - StandbyEndingNotice - time.Nanosecond)
		settle()
		before := s.Snapshot()
		if before.StandbyEndingSoon {
			t.Fatal("warned earlier than ten minutes before the end")
		}
		clock.awake(time.Nanosecond)
		settle()
		o := s.Snapshot()
		if !o.StandbyEndingSoon || o.Revision <= before.Revision || o.Standby != "on" {
			t.Fatal("no fresh status warned ten minutes before the end", o)
		}
		extend(t, s, 2, 4*time.Hour)
		_, wall = clock.now()
		if o := s.Snapshot(); o.StandbyEndingSoon || !o.StandbyUntil.Equal(wall.Add(4*time.Hour)) {
			t.Fatal("extension kept the warning or the old end", o)
		}
		clock.awake(4*time.Hour - time.Nanosecond)
		settle()
		if s.Snapshot().Standby != "on" {
			t.Fatal("extended standby ended at its old end")
		}
	})
}

// "The end is standby OFF" (#399): owned work is stopped and collected on the
// same path. Only the recorded reason differs.
func TestStandbyEndStopsOwnedWorkLikeStandbyOff(t *testing.T) {
	final := map[string]StandbyObservation{}
	for _, end := range []string{"off", "expiry"} {
		t.Run(end, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, c, clock := clockedStandbyOn(t, 2*time.Hour)
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
				if o.Cleanup != "confirmed" || o.Owned != 0 || w.closes.Load() != 1 || o.StandbyExpired != (end == "expiry") {
					t.Fatal("owned work not collected", o, w.closes.Load())
				}
				o.Revision, o.StandbyExpired, o.Transition = 0, false, nil
				final[end] = o
			})
		})
	}
	if final["off"] != final["expiry"] {
		t.Fatal("the end finished differently from standby OFF", final["off"], final["expiry"])
	}
}

// The newest local action decides the end: it can extend standby or cut it
// short. The allowance is separate: allow and deny do not move the end, and
// the end does not depend on them.
func TestStandbyEndIsReplacedByTheNewestExtension(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, c, clock := clockedStandbyOn(t, 24*time.Hour)
		extend(t, s, 2, 2*time.Hour)
		_, wall := clock.now()
		if !s.Snapshot().StandbyUntil.Equal(wall.Add(2 * time.Hour)) {
			t.Fatal("newer shorter extension did not replace the older end")
		}
		extend(t, s, 3, 12*time.Hour)
		end := s.Snapshot().StandbyUntil
		if !end.Equal(wall.Add(12 * time.Hour)) {
			t.Fatal("newer longer extension did not replace the older end")
		}
		allowRun(t, s, 4)
		if r := s.DenyRun(5, s.Snapshot().Revision); r.Disposition != "accepted" || r.Intent != "deny-run" {
			t.Fatal(r)
		}
		if o := s.Snapshot(); s.RunAllowed() || o.Standby != "on" || !o.StandbyUntil.Equal(end) {
			t.Fatal("deny did not withdraw the allowance, or it moved or ended standby", o)
		}
		clock.awake(12 * time.Hour)
		settle()
		if o := s.Snapshot(); o.Standby != "stopping" || !o.StandbyExpired {
			t.Fatal("standby without an allowance did not end", o)
		}
		close(c.gate)
		eventually(t, func() bool { return s.Snapshot().Standby == "off" })
	})
}

// The relay replaces the control connection every few minutes. That is not a
// local decision, so it neither ends nor renews standby or the allowance.
func TestStandbyEndAndAllowanceSurviveControlReconnect(t *testing.T) {
	s, c := standbyTurnedOn(t)
	c.connected.Store(true)
	eventually(t, func() bool { return s.Snapshot().Registration == "authenticated" })
	allowRun(t, s, 2)
	s.mu.Lock()
	until := s.cycle.until
	s.mu.Unlock()
	c.connected.Store(false)
	eventually(t, func() bool { return s.Snapshot().Registration != "authenticated" })
	c.connected.Store(true)
	eventually(t, func() bool { return s.Snapshot().Registration == "authenticated" })
	s.mu.Lock()
	same := s.cycle.until.Equal(until)
	s.mu.Unlock()
	if !s.RunAllowed() || !same {
		t.Fatal("reconnect changed the end or the allowance")
	}
}

// Turning standby off withdraws everything the person allowed; turning it on
// again must start from "not allowed", with a fresh end and without the old
// expiry record.
func TestStandbyOffEndsRunAllowanceAndOnDoesNotRestoreIt(t *testing.T) {
	s, c := standbyTurnedOn(t)
	allowRun(t, s, 2)
	if r := s.RequestOff(3, s.Snapshot().Revision); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	if o := s.Snapshot(); s.RunAllowed() || !o.StandbyUntil.IsZero() || o.StandbyExpired {
		t.Fatal("run allowed while standby is stopping, or OFF recorded as the end", o)
	}
	if r := s.AllowRun(4, s.Snapshot().Revision); r.Disposition != "rejected" || s.RunAllowed() {
		t.Fatal("allowance accepted while standby is stopping", r)
	}
	close(c.gate)
	eventually(t, func() bool { return s.Snapshot().Standby == "off" })
	if r := s.AllowRun(5, s.Snapshot().Revision); r.Disposition != "rejected" || s.RunAllowed() {
		t.Fatal("allowance accepted while standby is off", r)
	}
	if r := s.ExtendStandby(6, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "rejected" || s.Snapshot().Standby != "off" {
		t.Fatal("extension accepted while standby is off", r)
	}
	next := &idleConnector{done: make(chan struct{}), gate: make(chan struct{})}
	t.Cleanup(func() { close(next.gate) })
	s.mu.Lock()
	s.makeConnector = func(context.Context) (residentConnector, error) { return next, nil }
	s.observation.StandbyExpired = true // as if the last cycle had run out
	s.mu.Unlock()
	if r := s.RequestOn(7, s.Snapshot().Revision, 4*time.Hour); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	if o := s.Snapshot(); o.Standby != "on" || s.RunAllowed() || o.StandbyExpired || time.Until(o.StandbyUntil) <= 2*time.Hour {
		t.Fatal("standby on restored an earlier allowance or the old end or expiry", o)
	}
}

func TestLocalStandbyActionsRefuseStaleAndClosedRequests(t *testing.T) {
	s, _ := standbyTurnedOn(t)
	allowRun(t, s, 10)
	if r := s.DenyRun(11, s.Snapshot().Revision); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	end := s.Snapshot().StandbyUntil
	// A replayed or out-of-date request must not turn the allowance back on or
	// move the end.
	for name, r := range map[string]StandbyReceipt{
		"reused-id":             s.AllowRun(10, s.Snapshot().Revision),
		"zero-id":               s.AllowRun(0, s.Snapshot().Revision),
		"stale-revision":        s.AllowRun(12, s.Snapshot().Revision-1),
		"extend-reused-id":      s.ExtendStandby(11, s.Snapshot().Revision, 24*time.Hour),
		"extend-stale-revision": s.ExtendStandby(13, s.Snapshot().Revision-1, 24*time.Hour),
	} {
		if r.Disposition != "stale" || s.RunAllowed() || !s.Snapshot().StandbyUntil.Equal(end) {
			t.Fatal(name, r)
		}
	}
	allowRun(t, s, 14)
	// A stale deny is not a local decision made against the current state.
	if r := s.DenyRun(14, s.Snapshot().Revision); r.Disposition != "stale" || !s.RunAllowed() {
		t.Fatal("stale deny applied", r)
	}
	if r := s.DenyRun(15, s.Snapshot().Revision-1); r.Disposition != "stale" || !s.RunAllowed() {
		t.Fatal("deny against an old revision applied", r)
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	_ = s.Close(ctx)
	if s.RunAllowed() {
		t.Fatal("run allowed after close")
	}
	if r := s.AllowRun(16, s.Snapshot().Revision); r.Disposition == "accepted" || s.RunAllowed() {
		t.Fatal("allowance accepted after close", r)
	}
	if r := s.ExtendStandby(17, s.Snapshot().Revision, 2*time.Hour); r.Disposition == "accepted" {
		t.Fatal("extension accepted after close", r)
	}
	var none *Standby
	if none.RunAllowed() || none.AllowRun(1, 1).Disposition != "rejected" || none.DenyRun(1, 1).Disposition != "rejected" || none.ExtendStandby(1, 1, 2*time.Hour).Disposition != "rejected" {
		t.Fatal("nil standby accepted a local action")
	}
}

// consume asks this once for every intent the gateway sends. A signed
// command-run intent names a command nobody at the Mac has seen, so it gets
// through only under the local allowance; without it the intent is refused and
// standby keeps serving fixed work.
func TestAdmissionLetsCommandRunThroughOnlyUnderTheAllowance(t *testing.T) {
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
	allowRun(t, s, 2)
	if admit(c, true) != admitRun || admit(c, false) != admitFixed {
		t.Fatal("the allowance did not admit a command run")
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
	allowRun(t, s, 4)
	s.mu.Lock()
	s.observation.AdmissionSealed = true
	s.mu.Unlock()
	if admit(c, true) != admitNothing || admit(c, false) != admitNothing {
		t.Fatal("sealed admission still admitted work")
	}
}
