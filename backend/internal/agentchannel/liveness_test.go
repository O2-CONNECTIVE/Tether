package agentchannel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
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

func TestHalfOpenRelayPausesSessionForReconnect(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	b := Budgets{Handshake: 5 * time.Second, HeartbeatInterval: 20 * time.Millisecond, HeartbeatTimeout: 200 * time.Millisecond, ReadStall: 2 * time.Second, WriteStall: 2 * time.Second, Cleanup: 2 * time.Second}
	p := connect(t, env, context.Background(), b, b)
	early := env.closedBeforeRevoke(p.sConn)
	p.sConn.link.setBlackhole() // bytes vanish and neither end sees a close
	waitDone(t, p.server.Done(), 2*time.Second, "server teardown on a missing heartbeat")
	if p.server.Reason() != ReasonHeartbeat || env.revoked() {
		t.Fatalf("reason %v revoked %v", p.server.Reason(), env.revoked())
	}
	// The blocked heartbeat sender is unblocked by closing the same conn and joined.
	if err := p.server.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !early.Load() || !p.sConn.isClosed() {
		t.Fatal("transport did not close while the grant remained live")
	}
	waitDone(t, p.client.Done(), 2*time.Second, "client teardown on its own missing heartbeat")
	if p.client.Reason() != ReasonHeartbeat {
		t.Fatalf("client reason %v", p.client.Reason())
	}
}

func TestChannelTeardownPreservesGrantOnlyForGraceReasonsAfterAdmission(t *testing.T) {
	cases := []struct {
		name   string
		reason Reason
		grace  bool
	}{
		{"transport", ReasonTransport, true},
		{"relay close code 4000 transport", ReasonTransport, true},
		{"heartbeat", ReasonHeartbeat, true},
		{"read stall", ReasonReadStall, true},
		{"write stall", ReasonWriteStall, true},
		{"closed", ReasonClosed, false},
		{"canceled", ReasonCanceled, false},
		{"hard deadline", ReasonHardDeadline, false},
		{"revoked", ReasonRevoked, false},
		{"violation", ReasonViolation, false},
		{"handshake", ReasonHandshake, false},
		{"rejected", ReasonRejected, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnv(t, time.Minute)
			b := quickBudgets()
			p := connect(t, env, context.Background(), b, b)
			p.server.teardown(tc.reason)
			waitDone(t, p.server.Done(), 2*time.Second, "channel teardown")
			if got := env.revoked(); got == tc.grace {
				t.Fatalf("grant revoked = %v, grace reason = %v", got, tc.grace)
			}
			if !tc.grace {
				cfg := env.serverConfig(b)
				cfg.ConnectionGeneration = 2
				if _, err := NewServer(cfg); err == nil {
					t.Fatal("a valid-shaped next-generation server was accepted after immediate revocation")
				}
			}
		})
	}
}

func TestTransportBeforeChannelAdmissionStillRevokes(t *testing.T) {
	env := newEnv(t, time.Minute)
	sConn, _ := memPair()
	srv := env.newServer(t, quickBudgets())
	c := newChannel(sConn, quickBudgets(), env.deadline, srv.Dispatch().Binding, env.session)
	_, err := c.fail(ReasonTransport, errors.New("transport ended before admission"))
	if err == nil {
		t.Fatal("pre-admission transport loss had no channel error")
	}
	if !env.revoked() {
		t.Fatal("pre-admission transport loss preserved the grant")
	}
}

func TestNextConnectionGenerationReusesSessionAndHardDeadline(t *testing.T) {
	env := newEnv(t, time.Minute)
	b := quickBudgets()
	first := connect(t, env, context.Background(), b, b)
	firstDeadline := first.srv.Dispatch().Deadline
	first.server.teardown(ReasonTransport)
	waitDone(t, first.server.Done(), 2*time.Second, "first generation teardown")
	if env.revoked() {
		t.Fatal("transport loss revoked the grant instead of entering grace")
	}
	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := env.session.AwaitCleanup(cleanup); err != nil {
		cancel()
		t.Fatalf("clean first generation work: %v", err)
	}
	cancel()

	cfg := env.serverConfig(b)
	cfg.ConnectionGeneration = 2
	second, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer for generation 2 = %v", err)
	}
	dispatch := second.Dispatch()
	if dispatch.Binding.ConnectionGeneration != 2 {
		t.Fatalf("connection generation = %d, want 2", dispatch.Binding.ConnectionGeneration)
	}
	if !dispatch.Deadline.Equal(firstDeadline) {
		t.Fatalf("generation 2 deadline = %v, generation 1 deadline = %v", dispatch.Deadline, firstDeadline)
	}
	sConn, cConn := memPair()
	accepted := acceptAsync(context.Background(), second, sConn)
	clientCfg := env.clientConfig(b)
	clientCfg.Binding = dispatch.Binding
	client, err := Open(context.Background(), clientCfg, cConn)
	if err != nil {
		t.Fatalf("Open generation 2 = %v", err)
	}
	result := await(t, accepted, 5*time.Second)
	if result.err != nil {
		t.Fatalf("Accept generation 2 = %v", result.err)
	}
	defer func() {
		_ = client.Close()
		_ = result.ch.Close()
	}()
	roundTrip(t, client, result.ch, "generation two")
}

func TestNextConnectionGenerationStillRejectsAnotherClientKey(t *testing.T) {
	env := newEnv(t, time.Minute)
	b := quickBudgets()
	first := connect(t, env, context.Background(), b, b)
	first.server.teardown(ReasonTransport)
	waitDone(t, first.server.Done(), 2*time.Second, "first generation teardown")
	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := env.session.AwaitCleanup(cleanup); err != nil {
		cancel()
		t.Fatalf("clean first generation work: %v", err)
	}
	cancel()
	cfg := env.serverConfig(b)
	cfg.ConnectionGeneration = 2
	second, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer for generation 2 = %v", err)
	}
	sConn, cConn := memPair()
	accepted := acceptAsync(context.Background(), second, sConn)
	clientCfg := env.clientConfig(b)
	clientCfg.Binding = second.Dispatch().Binding
	clientCfg.Signer = fixtureSigner(t, 0x33)
	client, openErr := Open(context.Background(), clientCfg, cConn)
	if openErr == nil {
		_ = client.Close()
		t.Fatal("generation 2 accepted a client key outside the grant")
	}
	result := await(t, accepted, 5*time.Second)
	if reasonOf(result.err) != ReasonHandshake || !env.revoked() {
		t.Fatalf("wrong key result = %v, reason = %v, grant revoked = %v", result.err, reasonOf(result.err), env.revoked())
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
	if p.server.Reason() != ReasonWriteStall || env.revoked() {
		t.Fatalf("reason %v revoked %v", p.server.Reason(), env.revoked())
	}
	if err := p.server.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !early.Load() {
		t.Fatal("write-stall transport closed without preserving the grant")
	}
}

func TestHardDeadlineIsNotExtendedByPeerHeartbeats(t *testing.T) {
	env := newEnv(t, 600*time.Millisecond)
	peer := quickBudgets()
	peer.HeartbeatInterval = 15 * time.Millisecond
	p := connect(t, env, context.Background(), quickBudgets(), peer)
	early := env.closedBeforeRevoke(p.sConn)
	waitDone(t, p.server.Done(), 3*time.Second, "teardown at the hard deadline")
	// The deadline owners end the grant when the wall clock reaches the deadline
	// (Authority Lookup, the channel's hard timer) or the monotonic budget runs
	// out, whichever comes first. The two clocks can drift either way, so the
	// teardown is early only if it came before the deadline on both clocks.
	now := time.Now()
	if now.Before(env.notBefore) && now.Round(0).Before(env.deadline) {
		t.Fatalf("teardown at %v before the deadline %v on both clocks", now, env.deadline)
	}
	if p.server.Reason() != ReasonHardDeadline || !env.revoked() {
		t.Fatalf("reason %v revoked %v", p.server.Reason(), env.revoked())
	}
	if p.client.answeredBeats() == 0 {
		t.Fatal("peer heartbeats never ran")
	}
	if err := p.server.Close(); err != nil || early.Load() {
		t.Fatalf("close %v early %v", err, early.Load())
	}
}

// The Session also ends the grant on its elapsed (monotonic) budget, which can
// run out while the wall clock is still before the hard deadline: maclaunch's
// ElapsedDeadline, or a wall clock that runs slow. That is the hard deadline,
// not an outside revoke, and the channel must report it the way the Session
// recorded it.
func TestSessionElapsedDeadlineIsReportedAsHardDeadline(t *testing.T) {
	env := newEnvElapsed(t, 30*time.Second, 300*time.Millisecond)
	p := connect(t, env, context.Background(), quickBudgets(), quickBudgets())
	early := env.closedBeforeRevoke(p.sConn)
	waitDone(t, p.server.Done(), 3*time.Second, "teardown at the Session's elapsed deadline")
	if !time.Now().Before(env.deadline) {
		t.Fatal("the wall deadline passed; this test needs the elapsed deadline to come first")
	}
	if p.server.Reason() != ReasonHardDeadline || env.session.StopReason() != agentexec.StopHardDeadline || !env.revoked() {
		t.Fatalf("reason %v session %v revoked %v", p.server.Reason(), env.session.StopReason(), env.revoked())
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
