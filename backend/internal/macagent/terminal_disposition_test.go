package macagent

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type terminalFake struct {
	*observedFakeOS
	waits   atomic.Int32
	waitErr error
	pending bool
}

func (f *terminalFake) reapExitedOnce() (bool, exitInfo, error) {
	f.waits.Add(1)
	if f.waitErr != nil {
		return false, exitInfo{}, f.waitErr
	}
	if f.pending {
		return false, exitInfo{}, nil
	}
	return f.tryReap()
}
func terminalFixture(t *testing.T) (*process, *terminalFake, *TerminalDisposition) {
	p, base := observedProcess(t, groupPermission, nil, nil)
	// Quiesce fake I/O so TryLock refusal is tested only by explicit busy cases.
	select {
	case <-p.ioDone:
	case <-time.After(time.Second):
		t.Fatal("fake I/O did not finish")
	}
	f := &terminalFake{observedFakeOS: base}
	p.os = f
	d := &TerminalDisposition{}
	p.disposition = d
	d.retain(p)
	return p, f, d
}
func TestTerminalDispositionReapsOnlyKnownOwnedChildOnce(t *testing.T) {
	p, f, d := terminalFixture(t)
	observeNatural(t, p)
	if !errors.Is(p.Terminate(finite(t)), errGroupObservation) {
		t.Fatal("fixture")
	}
	for i := 0; i < 2; i++ {
		if !errors.Is(d.RelinquishUnknown(true), ErrTerminalCleanupUnknown) {
			t.Fatal("not unknown")
		}
	}
	if f.waits.Load() != 1 || !p.reaped || p.groupErr == nil || p.groupState != groupPermission {
		t.Fatal("lost ownership/evidence")
	}
	if p.Terminate(finite(t)) == nil || p.Release(finite(t)) == nil {
		t.Fatal("disposed unknown became clean")
	}
	if _, e := p.Wait(finite(t)); !errors.Is(e, ErrTerminalCleanupUnknown) {
		t.Fatal(e)
	}
	if _, _, e := p.Next(finite(t)); !errors.Is(e, ErrTerminalCleanupUnknown) {
		t.Fatal(e)
	}
	if f.observations.Load() != 1 {
		t.Fatal("query retried")
	}
	assertNoSignals(t, f.observedFakeOS)
}
func TestTerminalDispositionNeverWaitsForLiveUnknownOrUnsealed(t *testing.T) {
	for _, kind := range []string{"live", "unknown-status", "lost", "unsealed", "busy", "already-reaped"} {
		t.Run(kind, func(t *testing.T) {
			p, f, d := terminalFixture(t)
			if kind != "live" {
				observeNatural(t, p)
			}
			allow := true
			switch kind {
			case "unknown-status":
				p.mu.Lock()
				p.natKnown = false
				p.mu.Unlock()
			case "lost":
				p.mu.Lock()
				p.lost = errFake
				p.mu.Unlock()
			case "unsealed":
				allow = false
			case "busy":
				p.mu.Lock()
			case "already-reaped":
				p.mu.Lock()
				p.reaped = true
				p.mu.Unlock()
			}
			done := make(chan struct{})
			go func() { _ = d.RelinquishUnknown(allow); close(done) }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("blocking disposition")
			}
			if kind == "busy" {
				p.mu.Unlock()
			}
			if f.waits.Load() != 0 {
				t.Fatal("unexpected child wait")
			}
			if p.Release(finite(t)) == nil {
				t.Fatal("false success")
			}
			assertNoSignals(t, f.observedFakeOS)
		})
	}
}
func TestTerminalReapFailureAndPendingAreNeverRetried(t *testing.T) {
	for _, err := range []error{errFake, nil} {
		p, f, d := terminalFixture(t)
		observeNatural(t, p)
		f.waitErr = err
		f.pending = err == nil
		_ = d.RelinquishUnknown(true)
		_ = d.RelinquishUnknown(true)
		if f.waits.Load() != 1 || p.reaped || p.Release(finite(t)) == nil {
			t.Fatal("unknown rescued")
		}
		assertNoSignals(t, f.observedFakeOS)
	}
}
func TestTerminalDispositionStopsSubsequentSignalAndReap(t *testing.T) {
	p, f, d := terminalFixture(t)
	observeNatural(t, p)
	_ = d.RelinquishUnknown(false)
	p.signal(sigTerm)
	p.signal(sigKill)
	p.mu.Lock()
	_, err := p.reapLocked()
	p.mu.Unlock()
	if !errors.Is(err, ErrTerminalCleanupUnknown) || f.waits.Load() != 0 {
		t.Fatal("normal reap admitted")
	}
	assertNoSignals(t, f.observedFakeOS)
}
func TestTerminalDispositionConcurrentReleaseCannotRescueUnknown(t *testing.T) {
	for i := 0; i < 50; i++ {
		p, f, d := terminalFixture(t)
		observeNatural(t, p)
		// A query failure makes normal cleanup permanently unknown before the race.
		_ = p.Terminate(finite(t))
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); _ = d.RelinquishUnknown(true) }()
		go func() { defer wg.Done(); _ = p.Release(context.Background()) }()
		go func() { defer wg.Done(); _ = p.Terminate(context.Background()) }()
		wg.Wait()
		if p.released || p.Release(finite(t)) == nil || f.waits.Load() > 1 {
			t.Fatal("unknown recovered")
		}
		assertNoSignals(t, f.observedFakeOS)
	}
}
func TestTerminalDispositionKeepsConfirmedResultsAndOriginalPipes(t *testing.T) {
	p, f, d := terminalFixture(t)
	f.observation = groupAnchorOnly
	observeNatural(t, p)
	if e := p.Terminate(finite(t)); e != nil {
		t.Fatal(e)
	}
	if e := p.Release(finite(t)); e != nil {
		t.Fatal(e)
	}
	_ = d.RelinquishUnknown(true)
	if e := p.Release(finite(t)); e != nil || !p.released {
		t.Fatal("prior confirmation changed")
	}
	if f.waits.Load() != 0 {
		t.Fatal("released child reaped again")
	}
	p2, _, d2 := terminalFixture(t)
	old := &scriptedCloser{Reader: &eofReader{}, results: []error{errFake}}
	p2.partialPipes, _ = retainPartialPipes(spawned{stdout: old})
	_ = d2.RelinquishUnknown(false)
	if p2.partialPipes[1] == nil || old.attempts() != 1 {
		t.Fatal("original pipe evidence lost/retried")
	}
}
func TestTerminalDispositionRefusesNewAndLateAcquisition(t *testing.T) {
	s := newFakeSpawner()
	d := &TerminalDisposition{}
	cfg := testTiming
	cfg.TerminalDisposition = d
	r, e := newRuntime(cfg, s.spawn, s.checks(), time.Millisecond)
	if e != nil {
		t.Fatal(e)
	}
	s.gate = make(chan struct{})
	s.entered = make(chan struct{})
	done := make(chan agentexec.GuestProcess, 1)
	go func() { p, _ := r.StartShell(context.Background(), supervisor.Binding{Generation: 1}, nil); done <- p }()
	<-s.entered
	_ = d.RelinquishUnknown(false)
	close(s.gate)
	p := <-done
	if p == nil || p.Terminate(finite(t)) == nil || p.Release(finite(t)) == nil {
		t.Fatal("late ownership resumed")
	}
	if p2, e := r.StartShell(context.Background(), supervisor.Binding{Generation: 1}, nil); p2 != nil || !errors.Is(e, agentexec.ErrNotStarted) {
		t.Fatal("new start admitted")
	}
	c := s.child(t)
	c.closeOutputs()
	_ = c.stdin.Close()
	if s.count() != 1 {
		t.Fatal("new process spawned")
	}
}

var _ io.Reader = &eofReader{}

// An already admitted operation is not retroactively undone; no next signal or
// reap may follow terminalization, even when the first call returns permission.
type heldSignalOS struct {
	*terminalFake
	entered chan struct{}
	resume  chan struct{}
	calls   atomic.Int32
}

func (f *heldSignalOS) signalGroup(signal) error {
	f.calls.Add(1)
	close(f.entered)
	<-f.resume
	return os.ErrPermission
}
func TestTerminalDispositionDuringAdmittedSignal(t *testing.T) {
	p, f, d := terminalFixture(t)
	h := &heldSignalOS{terminalFake: f, entered: make(chan struct{}), resume: make(chan struct{})}
	p.os = h
	done := make(chan struct{})
	go func() { p.signal(sigTerm); close(done) }()
	<-h.entered
	_ = d.RelinquishUnknown(true)
	close(h.resume)
	<-done
	p.signal(sigKill)
	if h.calls.Load() != 1 || f.waits.Load() != 0 || p.sigErr == nil {
		t.Fatal("admitted signal evidence lost or subsequent operation admitted")
	}
	if p.Terminate(finite(t)) == nil || p.Release(finite(t)) == nil {
		t.Fatal("terminal became successful")
	}
}
