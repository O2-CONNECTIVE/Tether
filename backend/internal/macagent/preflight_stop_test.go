package macagent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// The public Session and actual Mac runtime share the original command scope.
// Only the read-only host check and OS spawn edges are replaced with memory
// fixtures. A stop observed during preflight must prevent a new spawn, rather
// than deliver stdin and rely on later cleanup to undo possible side effects.
func TestSessionStopDuringMacPreflightDoesNotSpawn(t *testing.T) {
	for _, check := range []string{"child-reaping", "descriptors"} {
		for _, stop := range []string{"cancel", "revoke", "transport-lost", "close"} {
			t.Run(check+"/"+stop, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					h := newHarness(t, time.Hour, agentexec.Limits{})
					defer h.issuer.Close()
					entered, release := make(chan struct{}), make(chan struct{})
					var once sync.Once
					unblock := func() { once.Do(func() { close(release) }) }
					defer unblock()
					held := func() error { close(entered); <-release; return nil }
					if check == "child-reaping" {
						h.runtime.checks.childReaping = held
					} else {
						h.runtime.checks.inheritableFDs = held
					}
					diag := &StartDiagnostics{}
					h.runtime.diagnostics = diag
					st := h.start("preflight-stop", "memory-only")
					<-entered
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
						defer cancel()
						if err := h.session.Close(ctx); !errors.Is(err, agentexec.ErrCleanupUnknown) {
							t.Fatal("held preflight was not retained", err)
						}
					}
					pending, err := h.session.Status(h.ref(st.CommandID))
					if err != nil || pending.Terminal || pending.Cleanup != agentexec.CleanupPending || pending.Stop != want {
						t.Fatal("preflight stop lost original command", pending, err)
					}
					state, _ := h.authority.Status()
					if (state == supervisor.Granted) != (stop == "cancel") {
						t.Fatal("wrong grant lifetime", state)
					}
					unblock()
					final := h.await(st.CommandID)
					if h.spawner.count() != 0 {
						t.Fatalf("stopped preflight still spawned %d process(es)", h.spawner.count())
					}
					if !final.Terminal || final.Cleanup != agentexec.CleanupNotRequired || final.Stop != want || final.Exit.Known || final.OutputEnd != 0 {
						t.Fatal("refused spawn invented work or cleanup", final)
					}
					if diag.Failure() != StartFailureContext {
						t.Fatal("missing fixed context refusal", diag.Failure())
					}
					if err := h.session.Close(finite(t)); err != nil {
						t.Fatal("same owner did not join refused start", err)
					}
				})
			})
		}
	}
}
