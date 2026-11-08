package macagent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// StartShell has already acquired a shell when spawn returns a process. A
// failed or stopped acquisition must retain that process without starting a
// new command-input delivery. The public boundary is exercised before the
// caller requests termination, so a racing close cannot hide an unsafe write.
func TestStartShellSuppressesInputAfterFailedOrStoppedSpawn(t *testing.T) {
	for _, outcome := range []string{"live", "spawn-error", "observer-error", "canceled", "terminal-unknown"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				spawner := newFakeSpawner()
				r := newTestRuntime(t, spawner)
				disposition := &TerminalDisposition{}
				r.terminalDisposition = disposition
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var spawnErr error
				spawner.result = func(sp spawned) (spawned, error) {
					switch outcome {
					case "spawn-error":
						spawnErr = errFake
					case "observer-error":
						spawnErr = errors.Join(errObserver, errFake)
					case "canceled":
						cancel()
					case "terminal-unknown":
						_ = disposition.RelinquishUnknown(false)
					}
					return sp, spawnErr
				}
				owned, err := r.StartShell(ctx, supervisor.Binding{Generation: 1}, []byte("memory-only-command"))
				if owned == nil || err != spawnErr || errors.Is(err, agentexec.ErrNotStarted) {
					t.Fatal("lost acquired process or changed start evidence", owned != nil, err)
				}
				p := owned.(*process)
				child := spawner.child(t)
				defer child.closeOutputs() // Only memory fixture producers, never production cleanup.
				if p.os != child.os || spawner.count() != 1 {
					t.Fatal("acquired owner was replaced")
				}
				var delivered []byte
				select {
				case delivered = <-child.stdinData:
				case <-time.After(time.Second):
					t.Fatal("owned input writer did not close")
				}
				want := ""
				if outcome == "live" {
					want = "memory-only-command"
				}
				if string(delivered) != want {
					t.Errorf("%s spawn delivered %q, want %q", outcome, delivered, want)
				}
				if outcome == "terminal-unknown" {
					if !errors.Is(p.Terminate(finite(t)), ErrTerminalCleanupUnknown) || !errors.Is(p.Release(finite(t)), ErrTerminalCleanupUnknown) {
						t.Fatal("terminal unknown became confirmed")
					}
					if p.terminalState.Load() != 1 || len(disposition.owned) != 1 {
						t.Fatal("late terminal process lost ownership")
					}
					if log := child.os.entries(); len(log) != 1 || log[0] != "stdin-closed" {
						t.Fatal("terminal acquisition performed a native operation", log)
					}
					child.closeOutputs()
					synctest.Wait()
					select {
					case <-p.ioDone:
					default:
						t.Fatal("memory fixture I/O was not joined")
					}
					return
				}
				if err := p.Release(finite(t)); !errors.Is(err, errNotTerminated) {
					t.Fatal("empty input bypassed termination", err)
				}
				if err := p.Terminate(finite(t)); err != nil {
					t.Fatal("same process did not terminate", err)
				}
				if err := p.Release(finite(t)); err != nil || len(disposition.owned) != 0 {
					t.Fatal("same process did not release", err)
				}
				if log := child.os.entries(); !before(log, "stdin-closed", "TERM") || !before(log, "KILL", "reap") || !before(log, "reap", "observer-released") {
					t.Fatal("changed cleanup ownership or order", log)
				}
				noViolation(t, child.os)
			})
		})
	}
}

func TestSessionLateSpawnSuppressesInputAndKeepsStopClassification(t *testing.T) {
	for _, stop := range []string{"cancel", "revoke", "transport-lost", "close"} {
		t.Run(stop, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t, time.Hour, agentexec.Limits{})
				defer h.issuer.Close()
				h.spawner.gate, h.spawner.entered = make(chan struct{}), make(chan struct{})
				st := h.start("late-input", "memory-only-command")
				<-h.spawner.entered
				want := agentexec.StopRevoked
				switch stop {
				case "cancel":
					want = agentexec.StopCanceled
					if _, err := h.session.Cancel(h.ref(st.CommandID)); err != nil {
						t.Fatal(err)
					}
				case "revoke":
					h.session.Revoke()
				case "transport-lost":
					want = agentexec.StopTransportLost
					h.session.TransportLost()
				case "close":
					ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
					if err := h.session.Close(ctx); !errors.Is(err, agentexec.ErrCleanupUnknown) {
						t.Fatal("pending spawn was not retained", err)
					}
					cancel()
				}
				close(h.spawner.gate)
				child := h.spawner.child(t)
				final := h.await(st.CommandID)
				if delivered := <-child.stdinData; len(delivered) != 0 {
					t.Fatalf("stopped spawn delivered %q", delivered)
				}
				if final.Phase != agentexec.PhaseCanceled || final.Stop != want || final.Exit.Known || !final.Terminal || final.Cleanup != agentexec.CleanupConfirmed {
					t.Fatal("empty input changed command outcome or cleanup", final)
				}
				if state, _ := h.authority.Status(); (state == supervisor.Granted) != (stop == "cancel") {
					t.Fatal("changed grant lifetime", state)
				}
				if log := child.os.entries(); !before(log, "KILL", "reap") || !before(log, "reap", "observer-released") {
					t.Fatal("late process was not reclaimed by original owner", log)
				}
				noViolation(t, child.os)
			})
		})
	}
}

func TestSessionFailedSpawnSuppressesInputAndRetainsCleanupFailure(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "confirmed"
		if unknown {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t, time.Hour, agentexec.Limits{})
				defer h.issuer.Close()
				h.spawner.configure = func(f *fakeOS) {
					f.observerErr = errFake
					if unknown {
						f.observerCloseErr = errFake
					}
				}
				h.spawner.result = func(sp spawned) (spawned, error) { return sp, errors.Join(errObserver, errFake) }
				st := h.start("failed-input", "memory-only-command")
				child := h.spawner.child(t)
				final := h.await(st.CommandID)
				if delivered := <-child.stdinData; len(delivered) != 0 {
					t.Fatalf("failed spawn delivered %q", delivered)
				}
				wantCleanup := agentexec.CleanupConfirmed
				if unknown {
					wantCleanup = agentexec.CleanupUnknown
				}
				if final.Phase != agentexec.PhaseFailed || final.Stop != agentexec.StopRuntimeFault || final.Exit.Known || !final.Terminal || final.Cleanup != wantCleanup {
					t.Fatal("empty input changed failed start evidence", final)
				}
				if unknown {
					if _, err := h.session.Start(h.startReq("replacement", "memory-only")); !errors.Is(err, agentexec.ErrCleanupUnknown) {
						t.Fatal("unknown start admitted replacement", err)
					}
					if retried, err := h.session.Cleanup(finite(t), h.ref(st.CommandID)); !errors.Is(err, agentexec.ErrCleanupUnknown) || retried.Cleanup != agentexec.CleanupUnknown {
						t.Fatal("same owner forgot cleanup failure", retried, err)
					}
				}
				noViolation(t, child.os)
			})
		})
	}
}
