package macagent

import (
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
)

// Virtual time and the existing memory-pipe/fake-OS seams only. No native
// process, group query, signal, descriptor or socket is used by these tests.
func TestTermGraceEOFDoesNotAdvanceFirstSnapshot(t *testing.T) {
	const grace = 10 * time.Second
	for _, eofAt := range []time.Duration{0, grace / 4, 3 * grace / 4} {
		t.Run(eofAt.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				out, writer := io.Pipe()
				p, f := observedProcess(t, groupMultiple, out, nil)
				p.timing.termGrace = grace
				f.set(func(o *fakeOS) { o.exited = false; o.exitOnTerm = true })
				if eofAt == 0 {
					_ = writer.Close()
					<-p.ioDone // EOF is already known when TERM starts.
				} else {
					go func() { time.Sleep(eofAt); _ = writer.Close() }()
				}
				start := time.Now()
				var observedAt time.Duration
				f.onObserve = func() { observedAt = time.Since(start) }
				done := make(chan error, 1)
				go func() { done <- p.Terminate(context.Background()) }()
				synctest.Wait()
				if f.observations.Load() != 0 || countEntry(f, "TERM") != 1 {
					t.Fatal("EOF advanced the first query", f.entries())
				}
				if !p.mu.TryLock() {
					t.Fatal("owner mutex held during grace")
				}
				p.mu.Unlock()
				time.Sleep(grace / 2)
				// Model a changed listing before the sole observation. This does
				// not assert why a real extra member might disappear.
				p.mu.Lock()
				f.observation = groupAnchorOnly
				p.mu.Unlock()
				time.Sleep(grace/2 - time.Nanosecond)
				synctest.Wait()
				if f.observations.Load() != 0 || countEntry(f, "KILL") != 0 || countEntry(f, "reap") != 0 {
					t.Fatal("decision preceded grace", f.entries())
				}
				time.Sleep(time.Nanosecond)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if observedAt != grace || f.observations.Load() != 1 || countEntry(f, "reap") != 1 || countEntry(f, "KILL") != 0 {
					t.Fatalf("snapshot at %v, want one original grace %v; %v", observedAt, grace, f.entries())
				}
				if err := p.Release(context.Background()); err != nil {
					t.Fatal(err)
				}
				noViolation(t, f.fakeOS)
			})
		})
	}
}

func TestTermGraceOriginalContextBoundsWait(t *testing.T) {
	const grace = 10 * time.Second
	for _, tc := range []struct {
		name     string
		at       time.Duration
		deadline bool
	}{
		{"already-canceled", 0, false},
		{"already-expired", 0, true},
		{"canceled-before-grace", grace / 4, false},
		{"deadline-before-grace", grace / 4, true},
		{"cancel-at-grace", grace, false},
		{"deadline-at-grace", grace, true},
	} {
		for _, state := range []groupObservation{groupAnchorOnly, groupMultiple} {
			t.Run(tc.name+"/"+string(state), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					p, f := preKillFixture(t, state)
					p.timing.termGrace = grace
					start := time.Now()
					var ctx context.Context
					var cancel context.CancelFunc
					if tc.deadline {
						ctx, cancel = context.WithDeadline(context.Background(), start.Add(tc.at))
					} else {
						ctx, cancel = context.WithCancel(context.Background())
						if tc.at == 0 {
							cancel()
						} else {
							time.AfterFunc(tc.at, cancel)
						}
					}
					defer cancel()
					var observedAt time.Duration
					f.onObserve = func() { observedAt = time.Since(start) }
					err := p.Terminate(ctx)
					synctest.Wait() // Join a cancellation coincident with the grace timer.
					if ctx.Err() == nil || time.Since(start) != tc.at || observedAt != tc.at {
						t.Fatalf("original context not honored: elapsed=%v observed=%v ctx=%v", time.Since(start), observedAt, ctx.Err())
					}
					if state == groupMultiple {
						if !errors.Is(err, errGroupObservation) || p.reaped {
							t.Fatal("context/timer accepted multiple members", err)
						}
						f.observation = groupAnchorOnly
						if !errors.Is(p.Terminate(context.Background()), errGroupObservation) || p.Release(context.Background()) == nil {
							t.Fatal("truncated grace failure was rescued")
						}
					} else if err != nil || !p.reaped {
						t.Fatal("existing same-owner snapshot decision changed", err)
					}
					if f.observations.Load() != 1 || countEntry(f, "TERM") != 1 || countEntry(f, "KILL") != 0 {
						t.Fatal("new budget, signal or query", f.entries())
					}
					noViolation(t, f.fakeOS)
				})
			})
		}
	}
}

func TestTermGraceLiveLeaderKeepsKillFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, f := preKillFixture(t, groupAnchorOnly)
		p.timing.termGrace = 10 * time.Second
		f.set(func(o *fakeOS) { o.exitOnTerm = false })
		start := time.Now()
		done := make(chan error, 1)
		go func() { done <- p.Terminate(context.Background()) }()
		synctest.Wait()
		time.Sleep(p.timing.termGrace - time.Nanosecond)
		synctest.Wait()
		if countEntry(f, "TERM") != 1 || countEntry(f, "KILL") != 0 || f.observations.Load() != 0 {
			t.Fatal("EOF shortened live leader grace", f.entries())
		}
		time.Sleep(time.Nanosecond)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if time.Since(start) != p.timing.termGrace || countEntry(f, "KILL") != 1 || countEntry(f, "reap") != 1 || f.observations.Load() != 0 {
			t.Fatal("original live-child fallback changed", f.entries())
		}
		noViolation(t, f.fakeOS)
	})
}

func TestTermGraceNaturalSnapshotStillImmediate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, f := observedProcess(t, groupAnchorOnly, nil, nil)
		<-p.ioDone
		p.timing.termGrace = time.Hour
		observeNatural(t, p)
		start := time.Now()
		if err := p.Terminate(context.Background()); err != nil {
			t.Fatal(err)
		}
		if time.Since(start) != 0 || f.observations.Load() != 1 || countEntry(f, "reap") != 1 {
			t.Fatal("natural path acquired TERM grace", f.entries())
		}
		assertNoSignals(t, f)
	})
}

func TestTermGraceMultipleRetainsCanceledSessionOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, time.Hour, agentexec.Limits{})
		var original *process
		var observed *observedFakeOS
		h.spawner.configure = func(f *fakeOS) { f.exitOnTerm = true }
		h.spawner.result = func(sp spawned) (spawned, error) {
			observed = &observedFakeOS{fakeOS: sp.proc.(*fakeOS), observation: groupMultiple}
			sp.proc = observed
			return sp, nil
		}
		// Retain the original runtime owner locally through its existing registry.
		disposition := &TerminalDisposition{}
		h.runtime.terminalDisposition = disposition
		st := h.start("cancel-grace-multiple", "fixed")
		c := h.spawner.child(t)
		<-c.stdinData
		synctest.Wait()
		disposition.mu.Lock()
		for p := range disposition.owned {
			original = p
		}
		disposition.mu.Unlock()
		if original == nil {
			t.Fatal("original owner missing")
		}
		if _, err := h.session.Cancel(h.ref(st.CommandID)); err != nil {
			t.Fatal(err)
		}
		final := h.await(st.CommandID)
		if !final.Terminal || final.Phase != agentexec.PhaseCanceled || final.Stop != agentexec.StopCanceled || final.Cleanup != agentexec.CleanupUnknown {
			t.Fatalf("multiple snapshot was not terminal unknown: %+v", final)
		}
		observed.observation = groupAnchorOnly
		if _, err := h.session.Cleanup(finite(t), h.ref(st.CommandID)); !errors.Is(err, agentexec.ErrCleanupUnknown) {
			t.Fatal("later cleanup replaced negative evidence", err)
		}
		if _, err := h.session.Start(h.startReq("replacement", "fixed")); !errors.Is(err, agentexec.ErrCleanupUnknown) {
			t.Fatal("unknown owner allowed replacement", err)
		}
		disposition.mu.Lock()
		_, retained := disposition.owned[original]
		count := len(disposition.owned)
		disposition.mu.Unlock()
		if !retained || count != 1 || original.reaped || original.released || h.spawner.spawns != 1 || observed.observations.Load() != 1 || countEntry(observed, "TERM") != 1 || countEntry(observed, "KILL") != 0 || countEntry(observed, "reap") != 0 {
			t.Fatal("original unknown owner changed", observed.entries())
		}
		noViolation(t, observed.fakeOS)
	})
}
