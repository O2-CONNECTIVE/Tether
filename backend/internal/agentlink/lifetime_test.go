package agentlink_test

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentlink"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"golang.org/x/crypto/ssh"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type gatedClose struct {
	net.Conn
	allowed atomic.Bool
}

func (c *gatedClose) Close() error {
	if !c.allowed.Load() {
		return errors.New("injected owned close failure")
	}
	return c.Conn.Close()
}

func TestHostRetainsUnknownCloseForSameOwnerRetry(t *testing.T) {
	signer, peer := fixtureSigner(t, 51), fixtureSigner(t, 52)
	runtime := &macRuntime{procs: make(chan *fakeProc, 1)}
	_, authority, session := grant(t, 5*time.Second, []ssh.PublicKey{peer.PublicKey()}, runtime)
	budget := budgets()
	budget.Cleanup = 20 * time.Millisecond
	host, err := agentlink.NewHost(agentlink.HostConfig{Session: session, AllowedKeys: []ssh.PublicKey{peer.PublicKey()}, HostKey: signer, Budgets: budget})
	if err != nil {
		t.Fatal(err)
	}
	raw, b := memPair()
	a := &gatedClose{Conn: raw}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx, a) }()
	ch, err := agentchannel.Open(ctx, agentchannel.ClientConfig{Binding: agentchannel.Binding{Grant: session.Binding(), KeySetDigest: session.KeySetDigest(), Profile: agentexec.ProfileMacCurrentUser}, HostKey: signer.PublicKey(), Signer: peer, Deadline: authority.Grant().HardDeadline, Budgets: budgets()}, b)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	authority.Revoke(session.Binding())
	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatal("host did not return unknown")
	}
	if !errors.Is(err, agentchannel.ErrCleanupUnknown) {
		t.Fatal(err)
	}
	a.allowed.Store(true)
	if err = host.Close(ctx); err != nil {
		t.Fatal("same owner could not rejoin", err)
	}
}

func TestSecondHostServeDoesNotRevokeFirst(t *testing.T) {
	signer, peer := fixtureSigner(t, 53), fixtureSigner(t, 54)
	_, authority, session := grant(t, 5*time.Second, []ssh.PublicKey{peer.PublicKey()}, &macRuntime{procs: make(chan *fakeProc, 1)})
	host, err := agentlink.NewHost(agentlink.HostConfig{Session: session, AllowedKeys: []ssh.PublicKey{peer.PublicKey()}, HostKey: signer, Budgets: budgets()})
	if err != nil {
		t.Fatal(err)
	}
	a, b := memPair()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx, a) }()
	ch, err := agentchannel.Open(ctx, agentchannel.ClientConfig{Binding: agentchannel.Binding{Grant: session.Binding(), KeySetDigest: session.KeySetDigest(), Profile: agentexec.ProfileMacCurrentUser}, HostKey: signer.PublicKey(), Signer: peer, Deadline: authority.Grant().HardDeadline, Budgets: budgets()}, b)
	if err != nil {
		t.Fatal(err)
	}
	second, unused := memPair()
	defer unused.Close()
	if err = host.Serve(ctx, second); !errors.Is(err, agentchannel.ErrUsed) {
		t.Fatal(err)
	}
	select {
	case <-session.Done():
		t.Fatal("second invocation revoked first")
	default:
	}
	_ = ch.Close()
	select {
	case err = <-done:
		if !errors.Is(err, agentwire.ErrPeerLost) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("host did not finish")
	}
}

func TestConnectorReturnsUnconfirmedChannelInsteadOfDroppingIt(t *testing.T) {
	signer, peer := fixtureSigner(t, 55), fixtureSigner(t, 56)
	_, authority, session := grant(t, 5*time.Second, []ssh.PublicKey{peer.PublicKey()}, &macRuntime{procs: make(chan *fakeProc, 1)})
	a, b := memPair()
	defer b.Close()
	owned := &gatedClose{Conn: a}
	budget := budgets()
	budget.Handshake = 10 * time.Millisecond
	budget.Cleanup = 10 * time.Millisecond
	connect := agentlink.Connector(agentchannel.ClientConfig{Binding: agentchannel.Binding{Grant: session.Binding(), KeySetDigest: session.KeySetDigest(), Profile: session.Profile()}, HostKey: signer.PublicKey(), Signer: peer, Deadline: authority.Grant().HardDeadline, Budgets: budget}, owned)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch, err := connect(ctx)
	if ch == nil || !errors.Is(err, agentchannel.ErrCleanupUnknown) {
		t.Fatal("lost cleanup owner", ch, err)
	}
	owned.allowed.Store(true)
	if err := ch.Close(); err != nil {
		t.Fatal(err)
	}
	if next, err := connect(ctx); next != nil || err == nil {
		t.Fatal("reused connector", next, err)
	}
}
