package agentexec

import (
	"testing"
	"time"
)

// The fixed wire namespace must not replace the Windows command's existing
// context budget or introduce a second timer/cancellation owner.
func TestFixedNamespacePreservesRuntimeCommandContext(t *testing.T) {
	fixed := &FixedStartConstraint{RequestID: "fixed-budget", Command: "memory-only", TimeoutMillis: 15000}
	e := newEnv(t, Limits{}, fixed)
	if err := e.session.ClaimWithRequestPrefix("0123456789ab."); err != nil {
		t.Fatal(err)
	}
	req := e.startReq("0123456789ab.fixed-budget", "memory-only")
	req.TimeoutMillis = 15000
	status, err := e.session.Start(req)
	if err != nil {
		t.Fatal(err)
	}
	e.track(status)
	p := e.proc()
	e.session.mu.Lock()
	ctx := e.session.byID[status.CommandID].ctx
	e.session.mu.Unlock()
	deadline, bounded := ctx.Deadline()
	if !bounded || !deadline.Equal(e.clock.Now().Add(15*time.Second)) || e.timers.timerCount() != 2 {
		t.Fatal("fixed namespace changed the original context/timer budget")
	}
	if _, err := e.session.Cancel(e.ref(status.CommandID)); err != nil {
		t.Fatal(err)
	}
	if final := e.await(status.CommandID); final.Cleanup != CleanupConfirmed {
		t.Fatal(final)
	}
	// Cancel may send the existing early idempotent Terminate before the worker's
	// final cleanup; actual resource Release still occurs once.
	if terms, releases := p.counts(); terms < 1 || releases != 1 {
		t.Fatalf("owned cleanup terms=%d releases=%d", terms, releases)
	}
}
