package agentexec

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// This memory-only process honors its cleanup context. Its first Terminate
// waits longer than the caller's Close budget, without blocking observation.
type delayedStopProcess struct {
	ready, entered, release, nextReturn chan struct{}
	once                                sync.Once
	terminates, active                  atomic.Int32
	releases, overlaps                  atomic.Int32
	panicEarly, failFinal               bool
}

func (p *delayedStopProcess) Next(ctx context.Context) (Stream, []byte, error) {
	p.once.Do(func() { close(p.ready) })
	<-ctx.Done()
	// Hold the read's return until the test observes the early attempt. This
	// fixes the ordering without racing the worker's final cleanup for ownership.
	<-p.nextReturn
	return 0, nil, ctx.Err()
}
func (p *delayedStopProcess) Wait(context.Context) (ExitStatus, error) {
	return ExitStatus{}, errors.New("no natural exit")
}
func (p *delayedStopProcess) Terminate(ctx context.Context) error {
	if p.active.Add(1) != 1 {
		p.overlaps.Add(1)
	}
	defer p.active.Add(-1)
	if p.terminates.Add(1) == 1 {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if p.panicEarly {
			panic("synthetic early termination fault")
		}
	} else if p.failFinal {
		return errors.New("synthetic final termination failure")
	}
	return nil
}
func (p *delayedStopProcess) Release(context.Context) error {
	p.releases.Add(1)
	if p.active.Load() != 0 {
		p.overlaps.Add(1)
	}
	return nil
}

type delayedStopRuntime struct {
	profile Profile
	process *delayedStopProcess
}

func (r delayedStopRuntime) AgentProfile() Profile { return r.profile }
func (r delayedStopRuntime) StartShell(context.Context, supervisor.Binding, []byte) (GuestProcess, error) {
	return r.process, nil
}

func TestEarlyTerminationDoesNotBorrowStopCallerLifetime(t *testing.T) {
	for _, profile := range []Profile{ProfileWindowsGuest, ProfileMacCurrentUser} {
		for _, stop := range []string{"cancel", "revoke", "transport-lost", "close"} {
			t.Run(map[Profile]string{ProfileWindowsGuest: "windows-guest", ProfileMacCurrentUser: "mac-current-user"}[profile]+"/"+stop, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) { checkEarlyTermination(t, profile, stop, "release") })
			})
		}
	}
}

func TestEarlyTerminationCompletionKeepsOriginalOwner(t *testing.T) {
	for _, profile := range []Profile{ProfileWindowsGuest, ProfileMacCurrentUser} {
		for _, outcome := range []string{"deadline", "panic", "retry"} {
			t.Run(map[Profile]string{ProfileWindowsGuest: "windows-guest", ProfileMacCurrentUser: "mac-current-user"}[profile]+"/"+outcome, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) { checkEarlyTermination(t, profile, "close", outcome) })
			})
		}
	}
}

func checkEarlyTermination(t *testing.T, profile Profile, stop, outcome string) {
	t.Helper()
	policy := FullPolicyDigest()
	if profile == ProfileMacCurrentUser {
		policy = MacCurrentUserPolicyDigest()
	}
	issuer, authority := newGrantWith(t, time.Now, policy, time.Minute, time.Second)
	defer issuer.Close()
	p := &delayedStopProcess{ready: make(chan struct{}), entered: make(chan struct{}), release: make(chan struct{}), nextReturn: make(chan struct{})}
	p.panicEarly, p.failFinal = outcome == "panic", outcome == "retry"
	var release sync.Once
	unblock := func() { release.Do(func() { close(p.release) }) }
	defer unblock()
	s, err := New(Config{Issuer: issuer, Authority: authority, Runtime: delayedStopRuntime{profile, p}, Clock: time.Now, CleanupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error("same owner did not finish cleanup", err)
		}
	}()
	request := StartRequest{Version: Version, Binding: s.Binding(), RequestID: "finite-stop", Mode: ModeFull, Command: []byte("memory-only"), TimeoutMillis: 15000}
	st, err := s.Start(request)
	if err != nil {
		t.Fatal(err)
	}
	ref := StatusRequest{Version: Version, Binding: s.Binding(), CommandID: st.CommandID}
	<-p.ready
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	stopped := make(chan error, 1)
	go func() {
		switch stop {
		case "cancel":
			_, err := s.Cancel(ref)
			stopped <- err
		case "revoke":
			s.Revoke()
			stopped <- nil
		case "transport-lost":
			s.TransportLost()
			stopped <- nil
		case "close":
			stopped <- s.Close(ctx)
		}
	}()
	<-p.entered
	close(p.nextReturn)
	synctest.Wait()
	if state, _ := authority.Status(); (state == supervisor.Granted) != (stop == "cancel") {
		t.Fatal("stop changed the wrong grant lifetime", state)
	}
	if stop == "close" {
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
	}
	select {
	case err := <-stopped:
		if (stop == "close" && !errors.Is(err, ErrCleanupUnknown)) || (stop != "close" && err != nil) {
			t.Fatal("unexpected stop result", err)
		}
	default:
		t.Fatal("stop caller is blocked by the longer early Terminate budget")
	}
	status, err := s.Status(ref)
	if err != nil || status.Terminal || status.Cleanup != CleanupPending || p.terminates.Load() != 1 || p.releases.Load() != 0 {
		t.Fatal("pending early termination lost ownership", status, err)
	}
	request.RequestID = "replacement"
	if _, err := s.Start(request); err == nil {
		t.Fatal("unjoined early termination admitted replacement")
	}
	if _, err := s.Cancel(ref); err != nil {
		t.Fatal(err)
	}
	synctest.Wait()
	if p.terminates.Load() != 1 || p.releases.Load() != 0 {
		t.Fatal("repeated stop started another cleanup attempt")
	}
	retry, stopRetry := context.WithTimeout(context.Background(), time.Second)
	defer stopRetry()
	if status, err := s.Cleanup(retry, ref); !errors.Is(err, ErrBusy) || status.Cleanup != CleanupPending {
		t.Fatal("cleanup retry raced the retained early attempt", status, err)
	}
	if outcome == "deadline" {
		time.Sleep(900 * time.Millisecond)
	} else {
		unblock()
	}
	synctest.Wait()
	status, err = s.Status(ref)
	wantTerminates := int32(2)
	if outcome == "retry" {
		if err != nil || !status.Terminal || status.Cleanup != CleanupUnknown || p.terminates.Load() != 2 || p.releases.Load() != 0 {
			t.Fatal("failed final termination released ownership", status, err)
		}
		p.failFinal = false
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		status, err = s.Cleanup(cleanup, ref)
		wantTerminates++
	}
	if err != nil || !status.Terminal || status.Cleanup != CleanupConfirmed || p.terminates.Load() != wantTerminates || p.releases.Load() != 1 || p.overlaps.Load() != 0 {
		t.Fatalf("same process not joined in order: %+v err=%v terminate=%d release=%d overlap=%d", status, err, p.terminates.Load(), p.releases.Load(), p.overlaps.Load())
	}
}

func TestNaturalExitOwnsTerminationBeforeLaterStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, Limits{})
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		defer unblock()
		e.runtime.configure = func(p *fakeProc) {
			p.code = 7
			p.onTerminate = func() { close(entered); <-release }
		}
		st := e.mustStart("natural-stop", "memory-only")
		p := e.proc()
		close(p.events)
		close(p.exited)
		<-entered // The worker observed natural exit and owns final termination.
		if _, err := e.session.Cancel(e.ref(st.CommandID)); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if err := e.session.Close(ctx); !errors.Is(err, ErrCleanupUnknown) {
			t.Fatal("unjoined final termination reported success", err)
		}
		if terms, releases := p.counts(); terms != 1 || releases != 0 {
			t.Fatal("later stop competed with natural cleanup", terms, releases)
		}
		unblock()
		synctest.Wait()
		final, err := e.session.Status(e.ref(st.CommandID))
		if err != nil || !final.Terminal || final.Phase != PhaseExited || final.Stop != StopNone || !final.Exit.Known || final.Exit.Code != 7 || final.Cleanup != CleanupConfirmed {
			t.Fatal("natural exit relabeled by later stop", final, err)
		}
		if terms, releases := p.counts(); terms != 1 || releases != 1 {
			t.Fatal("natural cleanup repeated", terms, releases)
		}
	})
}
