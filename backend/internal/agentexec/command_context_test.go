package agentexec

import (
	"context"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// The command already has a finite Session-owned timer. Expose that bound to
// runtimes without introducing a second cancellation owner that could beat
// canonical revocation. Fake clock/timers here do not run a native shell.
func TestCommandContextDeadlineMetadataPreservesExistingTimerOwner(t *testing.T) {
	for _, capGrant := range []bool{false, true} {
		t.Run(map[bool]string{false: "command-timeout", true: "grant-cap"}[capGrant], func(t *testing.T) {
			e := newEnv(t, Limits{MaxTimeout: uint32((2 * time.Hour).Milliseconds())})
			request := e.startReq("deadline-metadata", "synthetic")
			if capGrant {
				request.TimeoutMillis = uint32((2 * time.Hour).Milliseconds())
			}
			status, err := e.session.Start(request)
			if err != nil {
				t.Fatal(err)
			}
			e.track(status)
			p := e.proc()
			e.session.mu.Lock()
			ctx := e.session.byID[status.CommandID].ctx
			e.session.mu.Unlock()
			deadline, bounded := ctx.Deadline()
			want := e.clock.Now().Add(time.Duration(request.TimeoutMillis) * time.Millisecond)
			if capGrant {
				want = e.session.deadline
			}
			if !bounded || !deadline.Equal(want) {
				t.Fatalf("START_CONTEXT_UNBOUNDED: bounded=%t deadline=%v want=%v", bounded, deadline, want)
			}
			if e.timers.timerCount() != 2 {
				t.Fatal("metadata introduced a new deadline timer")
			}
			elapsed := time.Duration(request.TimeoutMillis) * time.Millisecond
			if capGrant {
				elapsed = time.Hour
			}
			e.clock.Advance(elapsed)
			if ctx.Err() != nil {
				t.Fatal("metadata canceled command before existing timer owner")
			}
			observed := make(chan supervisor.State, 1)
			unlink := context.AfterFunc(ctx, func() { state, _ := e.authority.Status(); observed <- state })
			defer unlink()
			e.timers.fire(t, 1)
			p.awaitKilled(t)
			select {
			case state := <-observed:
				if capGrant && state != supervisor.Revoked {
					t.Fatal("command canceled before canonical revocation")
				}
				if !capGrant && state != supervisor.Granted {
					t.Fatal("command timeout revoked whole grant")
				}
			case <-time.After(time.Second):
				t.Fatal("existing timer did not cancel command")
			}
			final := e.await(status.CommandID)
			if final.Cleanup != CleanupConfirmed {
				t.Fatal("cleanup unknown")
			}
		})
	}
}

func TestCommandContextDeadlineMetadataKeepsImmutableElapsedCap(t *testing.T) {
	e := &env{t: t, clock: &testClock{now: time.Now()}, timers: &fakeTimers{}, runtime: newFakeRuntime()}
	e.issuer, e.authority = newGrant(t, e.clock)
	var err error
	e.session, err = newSession(Config{Issuer: e.issuer, Authority: e.authority, Runtime: e.runtime, Clock: e.clock.Now, CleanupTimeout: time.Second}, e.timers.after)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.closeAndCheck() })
	// Simulate an almost-spent original monotonic budget, without changing the
	// authority's wall deadline or installing another cancellation/timer owner.
	e.session.elapsed = func() time.Duration { return e.session.budget - 3*time.Second }
	before := time.Now()
	status, err := e.session.Start(e.startReq("elapsed-cap-metadata", "memory-only"))
	after := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	e.track(status)
	e.proc()
	e.session.mu.Lock()
	ctx := e.session.byID[status.CommandID].ctx
	e.session.mu.Unlock()
	deadline, finite := ctx.Deadline()
	if !finite || deadline.Before(before.Add(3*time.Second)) || deadline.After(after.Add(3*time.Second)) || e.timers.timerCount() != 2 {
		t.Fatal("elapsed cap lost or new timer introduced", finite, deadline)
	}
	if _, err := e.session.Cancel(e.ref(status.CommandID)); err != nil {
		t.Fatal(err)
	}
	if final := e.await(status.CommandID); final.Cleanup != CleanupConfirmed {
		t.Fatal(final)
	}
}
