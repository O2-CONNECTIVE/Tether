package agentchannel

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestUnsupportedChannelsAreRejected(t *testing.T) {
	type target struct {
		Host       string
		Port       uint32
		Origin     string
		OriginPort uint32
	}
	forward := ssh.Marshal(target{"127.0.0.1", 22, "203.0.113.9", 1234})
	for _, tc := range []struct {
		name  string
		extra []byte
	}{
		{"session", nil},
		{"direct-tcpip", forward},
		{"forwarded-tcpip", forward},
		{"direct-streamlocal@openssh.com", nil},
		{"x11", nil},
		{"auth-agent@openssh.com", nil},
		{"rescue-agent-command-v2@rescue-host", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnv(t, 30*time.Second)
			sConn, cConn := memPair()
			early := env.closedBeforeRevoke(sConn)
			res := acceptAsync(context.Background(), env.newServer(t, quietBudgets()), sConn)
			raw := rawClient(t, env, cConn, true)
			extra := tc.extra
			if extra == nil {
				extra = env.extra()
			}
			var refused *ssh.OpenChannelError
			if _, _, err := raw.OpenChannel(tc.name, extra); !errors.As(err, &refused) {
				t.Fatalf("open %s: %v", tc.name, err)
			}
			r := await(t, res, 5*time.Second)
			if r.ch != nil || !errors.Is(r.err, ErrRejected) || reasonOf(r.err) != ReasonViolation {
				t.Fatalf("Accept = %v, %v", r.ch, r.err)
			}
			if !env.revoked() || early.Load() || !sConn.isClosed() {
				t.Fatal("grant not revoked before close")
			}
		})
	}
}

func TestSecondAgentChannelIsRejected(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	p := rawAdmitted(t, env, quietBudgets(), true)
	var refused *ssh.OpenChannelError
	if _, _, err := p.raw.OpenChannel(ChannelType, env.extra()); !errors.As(err, &refused) {
		t.Fatalf("second open: %v", err)
	}
	endsWith(t, env, p, ReasonViolation)
}

func TestUnsolicitedGlobalRequestsCloseConnection(t *testing.T) {
	forward := ssh.Marshal(struct {
		Address string
		Port    uint32
	}{"127.0.0.1", 22})
	for _, tc := range []struct {
		name    string
		want    bool
		payload []byte
	}{
		{"tcpip-forward", true, forward},
		{"cancel-tcpip-forward", true, forward},
		{"streamlocal-forward@openssh.com", true, nil},
		{"keepalive@openssh.com", true, nil},
		{"no-more-sessions@openssh.com", false, nil},
		{HeartbeatRequest, true, []byte{0}},
		{HeartbeatRequest, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnv(t, 30*time.Second)
			p := rawAdmitted(t, env, quietBudgets(), true)
			if ok, _, err := p.raw.SendRequest(tc.name, tc.want, tc.payload); ok && err == nil {
				t.Fatal("request was accepted")
			}
			endsWith(t, env, p, ReasonViolation)
		})
	}
}

func TestAgentChannelRequestsAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{"exec", ssh.Marshal(struct{ Command string }{"id"})},
		{"shell", nil},
		{"pty-req", nil},
		{"env", ssh.Marshal(struct{ Name, Value string }{"PATH", "/tmp"})},
		{"subsystem", ssh.Marshal(struct{ Name string }{"sftp"})},
		{"auth-agent-req@openssh.com", nil},
		{"signal", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnv(t, 30*time.Second)
			p := rawAdmitted(t, env, quietBudgets(), true)
			if ok, err := p.ch.SendRequest(tc.name, true, tc.payload); ok && err == nil {
				t.Fatal("channel request was accepted")
			}
			endsWith(t, env, p, ReasonViolation)
		})
	}
}

func TestExtendedDataIsViolation(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	p := rawAdmitted(t, env, quietBudgets(), true)
	if _, err := p.ch.Stderr().Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	endsWith(t, env, p, ReasonViolation)
}

func TestCanceledHandshakeRevokesThenCloses(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	sConn, peer := memPair() // the peer never speaks
	early := env.closedBeforeRevoke(sConn)
	ctx, cancel := context.WithCancel(context.Background())
	res := acceptAsync(ctx, env.newServer(t, quickBudgets()), sConn)
	time.Sleep(50 * time.Millisecond)
	cancel()
	r := await(t, res, 3*time.Second)
	if r.ch != nil || reasonOf(r.err) != ReasonCanceled {
		t.Fatalf("Accept = %v, %v", r.ch, r.err)
	}
	if !env.revoked() || early.Load() || !sConn.isClosed() || peer.closeCalls() != 0 {
		t.Fatal("cancel did not revoke before closing only the owned conn")
	}
}

func TestHandshakeBudgetBoundsSilentPeer(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	b := quickBudgets()
	b.Handshake = 150 * time.Millisecond
	sConn, _ := memPair()
	started := time.Now()
	r := await(t, acceptAsync(context.Background(), env.newServer(t, b), sConn), 3*time.Second)
	if r.ch != nil || reasonOf(r.err) != ReasonHandshake || time.Since(started) > 2*time.Second {
		t.Fatalf("Accept = %v, %v after %v", r.ch, r.err, time.Since(started))
	}
	if !env.revoked() || !sConn.isClosed() {
		t.Fatal("handshake timeout left the grant live")
	}
}

func TestAdmissionIsBoundedAfterAuthentication(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	b := quietBudgets()
	b.Handshake = 500 * time.Millisecond
	sConn, cConn := memPair()
	res := acceptAsync(context.Background(), env.newServer(t, b), sConn)
	rawClient(t, env, cConn, true) // authenticates, then never opens a channel
	r := await(t, res, 3*time.Second)
	if r.ch != nil || reasonOf(r.err) != ReasonHandshake {
		t.Fatalf("Accept = %v, %v", r.ch, r.err)
	}
	if !env.revoked() || !sConn.isClosed() {
		t.Fatal("admission timeout left the grant live")
	}
}

func TestCanceledHandshakeWithBlockedCloseKeepsOwner(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	b := quickBudgets()
	b.Handshake, b.Cleanup = 300*time.Millisecond, 200*time.Millisecond
	sConn, peer := memPair()
	gate := make(chan struct{})
	sConn.setGate(gate)
	ctx, cancel := context.WithCancel(context.Background())
	res := acceptAsync(ctx, env.newServer(t, b), sConn)
	time.Sleep(30 * time.Millisecond)
	cancel()
	r := await(t, res, 3*time.Second)
	if r.ch == nil || !errors.Is(r.err, ErrCleanupUnknown) || reasonOf(r.err) != ReasonCanceled {
		t.Fatalf("Accept = %v, %v; want the unconfirmed owner", r.ch, r.err)
	}
	if !env.revoked() {
		t.Fatal("grant live while close is pending")
	}
	if err := r.ch.Close(); !errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("close while blocked = %v", err)
	}
	close(gate)
	if err := r.ch.Close(); err != nil {
		t.Fatalf("close after release = %v", err)
	}
	if !sConn.isClosed() || peer.closeCalls() != 0 {
		t.Fatal("owner did not close exactly its own conn")
	}
}
