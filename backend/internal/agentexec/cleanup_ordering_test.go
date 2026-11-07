package agentexec

import (
	"context"
	"errors"
	"testing"
)

func TestCleanupRetryAfterCancelBeforeStartShellReturns(t *testing.T) {
	e := newEnv(t, Limits{})
	// Publish the process exactly as fakeRuntime normally does, but hold the
	// return until Cancel. This forces the valid ordering in which run has
	// not installed c.proc and Cancel cannot send an early Terminate.
	e.runtime.start = func(ctx context.Context) (GuestProcess, error) {
		p := newFakeProc()
		p.terminateErrs = []error{errors.New("terminate failed"), errors.New("terminate failed")}
		e.runtime.procs <- p
		<-ctx.Done()
		return p, nil
	}
	st := e.mustStart("req-1", "sleep\n")
	p := e.proc()
	if st, err := e.session.Status(e.ref(st.CommandID)); err != nil || st.Phase != PhaseStarting {
		t.Fatalf("before Cancel = %+v, %v", st, err)
	}
	if st, err := e.session.Cancel(e.ref(st.CommandID)); err != nil || st.Stop != StopCanceled {
		t.Fatalf("Cancel = %+v, %v", st, err)
	}
	final := e.await(st.CommandID)
	if final.Cleanup != CleanupUnknown || final.Phase != PhaseCanceled {
		t.Fatalf("final = %+v", final)
	}
	if terminates, releases := p.counts(); terminates != 1 || releases != 0 {
		t.Fatalf("before retry: terminate=%d release=%d", terminates, releases)
	}
	// Only the worker consumed a failure. The first explicit retry must still
	// fail closed, retaining the same workload and blocking another Start.
	if st, err := e.session.Cleanup(finite(t), e.ref(st.CommandID)); !errors.Is(err, ErrCleanupUnknown) || st.Cleanup != CleanupUnknown {
		t.Fatalf("first Cleanup = %+v, %v", st, err)
	}
	if terminates, releases := p.counts(); terminates != 2 || releases != 0 {
		t.Fatalf("failed retry: terminate=%d release=%d", terminates, releases)
	}
	if _, err := e.session.Start(e.startReq("req-2", "whoami\n")); !errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("Start with unknown cleanup = %v", err)
	}
	if st, err := e.session.Cleanup(finite(t), e.ref(st.CommandID)); err != nil || st.Cleanup != CleanupConfirmed {
		t.Fatalf("second Cleanup = %+v, %v", st, err)
	}
	if terminates, releases := p.counts(); terminates != 3 || releases != 1 {
		t.Fatalf("successful retry: terminate=%d release=%d", terminates, releases)
	}
}
