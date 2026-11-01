package agentchannel

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestQuietChannelStaysOpenWhileHeartbeatsAnswer(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	b := Budgets{Handshake: 5 * time.Second, HeartbeatInterval: 20 * time.Millisecond, HeartbeatTimeout: 150 * time.Millisecond, ReadStall: 400 * time.Millisecond, WriteStall: 2 * time.Second, Cleanup: 2 * time.Second}
	p := connect(t, env, context.Background(), b, b)
	// Three read-stall budgets without one agent byte, like a quiet command.
	select {
	case <-p.server.Done():
		t.Fatalf("server ended while quiet: %v", p.server.Reason())
	case <-p.client.Done():
		t.Fatalf("client ended while quiet: %v", p.client.Reason())
	case <-time.After(3 * b.ReadStall):
	}
	if p.server.answeredBeats() < 5 || p.client.answeredBeats() < 5 {
		t.Fatalf("heartbeats answered: server %d client %d", p.server.answeredBeats(), p.client.answeredBeats())
	}
	if env.revoked() {
		t.Fatal("quiet channel revoked the grant")
	}
	roundTrip(t, p.client, p.server, "after a long quiet period")
}

func TestHalfOpenRelayRevokesBeforeClose(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	b := Budgets{Handshake: 5 * time.Second, HeartbeatInterval: 20 * time.Millisecond, HeartbeatTimeout: 200 * time.Millisecond, ReadStall: 2 * time.Second, WriteStall: 2 * time.Second, Cleanup: 2 * time.Second}
	p := connect(t, env, context.Background(), b, b)
	early := env.closedBeforeRevoke(p.sConn)
	p.sConn.link.setBlackhole() // bytes vanish and neither end sees a close
	waitDone(t, p.server.Done(), 2*time.Second, "server teardown on a missing heartbeat")
	if p.server.Reason() != ReasonHeartbeat || !env.revoked() {
		t.Fatalf("reason %v revoked %v", p.server.Reason(), env.revoked())
	}
	// The blocked heartbeat sender is unblocked by closing the same conn and joined.
	if err := p.server.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if early.Load() || !p.sConn.isClosed() {
		t.Fatal("not revoked before closing the owned conn")
	}
	waitDone(t, p.client.Done(), 2*time.Second, "client teardown on its own missing heartbeat")
	if p.client.Reason() != ReasonHeartbeat {
		t.Fatalf("client reason %v", p.client.Reason())
	}
}

func TestStalledPeerReaderBoundsWrite(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	b := Budgets{Handshake: 5 * time.Second, HeartbeatInterval: 20 * time.Millisecond, HeartbeatTimeout: time.Second, ReadStall: 2 * time.Second, WriteStall: 150 * time.Millisecond, Cleanup: 2 * time.Second}
	p := connect(t, env, context.Background(), b, quickBudgets())
	early := env.closedBeforeRevoke(p.sConn)
	p.cConn.rd.setFrozen(true) // the relay stops delivering to the client
	errs := make(chan error, 1)
	go func() {
		_, err := p.server.Write(make([]byte, 256<<10))
		errs <- err
	}()
	select {
	case err := <-errs:
		if reasonOf(err) != ReasonWriteStall {
			t.Fatalf("write = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stalled write was not bounded")
	}
	if p.server.Reason() != ReasonWriteStall || !env.revoked() {
		t.Fatalf("reason %v revoked %v", p.server.Reason(), env.revoked())
	}
	if err := p.server.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if early.Load() {
		t.Fatal("transport closed before the grant was revoked")
	}
}

func TestHardDeadlineIsNotExtendedByPeerHeartbeats(t *testing.T) {
	env := newEnv(t, 600*time.Millisecond)
	peer := quickBudgets()
	peer.HeartbeatInterval = 15 * time.Millisecond
	p := connect(t, env, context.Background(), quickBudgets(), peer)
	early := env.closedBeforeRevoke(p.sConn)
	waitDone(t, p.server.Done(), 3*time.Second, "teardown at the hard deadline")
	if time.Now().Before(env.deadline) || p.server.Reason() != ReasonHardDeadline || !env.revoked() {
		t.Fatalf("reason %v at %v before %v", p.server.Reason(), time.Now(), env.deadline)
	}
	if p.client.answeredBeats() == 0 {
		t.Fatal("peer heartbeats never ran")
	}
	if err := p.server.Close(); err != nil || early.Load() {
		t.Fatalf("close %v early %v", err, early.Load())
	}
}

func TestExternalRevokeClosesChannel(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	p := connect(t, env, context.Background(), quickBudgets(), quickBudgets())
	early := env.closedBeforeRevoke(p.sConn)
	if err := env.issuer.Revoke(env.binding.Grant); err != nil {
		t.Fatal(err)
	}
	waitDone(t, p.server.Done(), 3*time.Second, "teardown after revoke")
	if p.server.Reason() != ReasonRevoked {
		t.Fatalf("reason %v", p.server.Reason())
	}
	if _, err := p.server.Write([]byte("late")); reasonOf(err) != ReasonRevoked || !errors.Is(err, ErrClosed) {
		t.Fatalf("write after revoke = %v", err)
	}
	if err := p.server.Close(); err != nil || early.Load() {
		t.Fatalf("close %v early %v", err, early.Load())
	}
	waitDone(t, p.client.Done(), 3*time.Second, "client teardown after the host closed")
	if p.client.Reason() != ReasonTransport {
		t.Fatalf("client reason %v", p.client.Reason())
	}
}

func TestContextCancelClosesChannel(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	p := connect(t, env, ctx, quickBudgets(), quickBudgets())
	early := env.closedBeforeRevoke(p.sConn)
	cancel()
	waitDone(t, p.server.Done(), 3*time.Second, "teardown after cancel")
	if p.server.Reason() != ReasonCanceled || !env.revoked() {
		t.Fatalf("reason %v revoked %v", p.server.Reason(), env.revoked())
	}
	if err := p.server.Close(); err != nil || early.Load() {
		t.Fatalf("close %v early %v", err, early.Load())
	}
}

func TestRefusedHeartbeatClosesChannel(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	b := quietBudgets()
	b.HeartbeatInterval = 20 * time.Millisecond
	p := rawAdmitted(t, env, b, false) // answers like a stock server: false
	endsWith(t, env, p, ReasonHeartbeat)
}

func TestPeerHeartbeatFloodIsViolation(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	p := rawAdmitted(t, env, quietBudgets(), true)
	for range 3 * MaxPeerHeartbeats {
		if ok, _, err := p.raw.SendRequest(HeartbeatRequest, true, nil); err != nil || !ok {
			break
		}
	}
	endsWith(t, env, p, ReasonViolation)
}

func TestBlockedTransportCloseIsUnknownThenJoined(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	b := quickBudgets()
	b.Cleanup = 200 * time.Millisecond
	p := connect(t, env, context.Background(), b, quickBudgets())
	gate := make(chan struct{})
	p.sConn.setGate(gate)
	if err := p.server.Close(); !errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("close with a hung conn = %v", err)
	}
	if p.server.Reason() != ReasonClosed || !env.revoked() {
		t.Fatalf("reason %v revoked %v", p.server.Reason(), env.revoked())
	}
	close(gate)
	if err := p.server.Close(); err != nil {
		t.Fatalf("close after release = %v", err)
	}
	if !p.sConn.isClosed() {
		t.Fatal("owner did not close its own conn")
	}
}

func TestFailedTransportCloseIsRetriedOnSameConn(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	p := connect(t, env, context.Background(), quickBudgets(), quickBudgets())
	p.sConn.setFailures(1)
	err := p.server.Close()
	if err != nil {
		if !errors.Is(err, ErrCleanupUnknown) {
			t.Fatalf("close = %v", err)
		}
		err = p.server.Close()
	}
	if err != nil {
		t.Fatalf("retried close = %v", err)
	}
	// One injected failure, then one success on the same conn; nothing else.
	if p.sConn.closeCalls() != 2 || !p.sConn.isClosed() {
		t.Fatalf("owned conn close calls %d", p.sConn.closeCalls())
	}
	if !env.revoked() {
		t.Fatal("grant live after close")
	}
}

func TestTransportTeardownClassifiesAlreadyReachedDeadline(t *testing.T) {
	for _, tc := range []struct {
		name        string
		delta       time.Duration
		first, want Reason
	}{
		{"before deadline", time.Hour, ReasonTransport, ReasonTransport},
		{"after deadline", -time.Hour, ReasonTransport, ReasonHardDeadline},
		{"explicit cancel stays first", -time.Hour, ReasonCanceled, ReasonCanceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := memPair()
			defer b.Close()
			c := newChannel(a, quickBudgets(), time.Now().Add(tc.delta), Binding{}, nil)
			c.raw.admit()
			c.teardown(tc.first)
			c.teardown(ReasonTransport)
			c.release()
			if got := c.Reason(); got != tc.want {
				t.Errorf("reason=%v want=%v", got, tc.want)
			}
			if e := c.Close(); e != nil {
				t.Fatal(e)
			}
		})
	}
}
