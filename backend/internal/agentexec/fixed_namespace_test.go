package agentexec

import (
	"errors"
	"strings"
	"testing"
)

func TestFixedStartWireAndDirectConcurrentStartKeepOneOwner(t *testing.T) {
	fixed := &FixedStartConstraint{RequestID: "fixed-race", Command: "memory-only", TimeoutMillis: 15000}
	e := newEnv(t, Limits{}, fixed)
	if err := e.session.ClaimWithRequestPrefix("0123456789ab."); err != nil {
		t.Fatal(err)
	}
	requests := []StartRequest{e.startReq("fixed-race", "memory-only"), e.startReq("0123456789ab.fixed-race", "memory-only")}
	for i := range requests {
		requests[i].TimeoutMillis = 15000
	}
	type result struct {
		index  int
		status Status
		err    error
	}
	ready, gate, results := make(chan struct{}, 2), make(chan struct{}), make(chan result, 2)
	for i, req := range requests {
		go func(i int, req StartRequest) {
			ready <- struct{}{}
			<-gate
			status, err := e.session.Start(req)
			results <- result{i, status, err}
		}(i, req)
	}
	<-ready
	<-ready
	close(gate)
	winner, loser := -1, -1
	var status Status
	for range requests {
		r := <-results
		if r.err == nil {
			if winner != -1 {
				t.Fatal("different namespaces both obtained execution")
			}
			winner, status = r.index, r.status
		} else if errors.Is(r.err, ErrBusy) {
			loser = r.index
		} else {
			t.Fatal(r.err)
		}
	}
	if winner == -1 || loser == -1 || winner == loser {
		t.Fatal("missing distinct winner/loser")
	}
	e.track(status)
	e.proc()
	if e.runtime.startCount() != 1 {
		t.Fatal("concurrent namespaces started twice")
	}
	if _, err := e.session.Cancel(e.ref(status.CommandID)); err != nil {
		t.Fatal(err)
	}
	if final := e.await(status.CommandID); final.Cleanup != CleanupConfirmed {
		t.Fatal(final)
	}
	if replay, err := e.session.Start(requests[winner]); err != nil || replay.CommandID != status.CommandID || !replay.Terminal {
		t.Fatal("winner lost replay", replay, err)
	}
	if _, err := e.session.Start(requests[loser]); !errors.Is(err, ErrExhausted) {
		t.Fatal("loser adopted finished execution", err)
	}
}

func TestFixedStartAdapterPrefixIsExactSingleClaim(t *testing.T) {
	fixed := &FixedStartConstraint{RequestID: "fixed-v1", Command: "fixed", TimeoutMillis: 15000}
	e := newEnv(t, Limits{}, fixed)
	for _, prefix := range []string{"", "bad prefix.", strings.Repeat("p", 64)} {
		if err := e.session.ClaimWithRequestPrefix(prefix); !errors.Is(err, ErrUnconfigured) {
			t.Fatalf("invalid prefix claimed Session: %q %v", prefix, err)
		}
	}
	const prefix = "0123456789ab."
	if err := e.session.ClaimWithRequestPrefix(prefix); err != nil {
		t.Fatal(err)
	}
	if err := e.session.Claim(); !errors.Is(err, ErrSessionClaimed) {
		t.Fatal("ordinary claim bypassed adapter claim", err)
	}
	if err := e.session.ClaimWithRequestPrefix("other."); !errors.Is(err, ErrSessionClaimed) {
		t.Fatal("adapter prefix was replaced", err)
	}
	req := e.startReq("other."+fixed.RequestID, fixed.Command)
	req.TimeoutMillis = fixed.TimeoutMillis
	if _, err := e.session.Start(req); !errors.Is(err, ErrPolicy) {
		t.Fatal("foreign namespace admitted", err)
	}
	req.RequestID = prefix + fixed.RequestID
	st, err := e.session.Start(req)
	if err != nil {
		t.Fatal(err)
	}
	e.track(st)
	e.proc()
	if _, err := e.session.Cancel(e.ref(st.CommandID)); err != nil {
		t.Fatal(err)
	}
	if final := e.await(st.CommandID); final.Cleanup != CleanupConfirmed {
		t.Fatal(final)
	}
}

func TestFixedStartAdapterPrefixCannotAdoptDirectOwnerCommand(t *testing.T) {
	fixed := &FixedStartConstraint{RequestID: "fixed-v1", Command: "fixed", TimeoutMillis: 15000}
	e := newEnv(t, Limits{}, fixed)
	owner := e.startReq(fixed.RequestID, fixed.Command)
	owner.TimeoutMillis = fixed.TimeoutMillis
	st, err := e.session.Start(owner)
	if err != nil {
		t.Fatal(err)
	}
	e.track(st)
	e.proc()
	const prefix = "0123456789ab."
	if err := e.session.ClaimWithRequestPrefix(prefix); err != nil {
		t.Fatal(err)
	}
	wire := owner
	wire.RequestID = prefix + owner.RequestID
	if _, err := e.session.Start(wire); !errors.Is(err, ErrBusy) {
		t.Fatal("wire adopted live direct-owner command", err)
	}
	if _, err := e.session.Cancel(e.ref(st.CommandID)); err != nil {
		t.Fatal(err)
	}
	if final := e.await(st.CommandID); final.Cleanup != CleanupConfirmed {
		t.Fatal(final)
	}
	if _, err := e.session.Start(wire); !errors.Is(err, ErrExhausted) {
		t.Fatal("wire adopted finished direct-owner command", err)
	}
	if replay, err := e.session.Start(owner); err != nil || replay.CommandID != st.CommandID {
		t.Fatal("direct owner lost its own replay", replay, err)
	}
	e.runtime.mu.Lock()
	defer e.runtime.mu.Unlock()
	if len(e.runtime.stdin) != 1 {
		t.Fatal("namespace change restarted command")
	}
}
