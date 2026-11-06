package agentlink_test

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentlink"
	"golang.org/x/crypto/ssh"
)

type cleanupObservation interface{ CleanupResult() (error, bool) }

func cleanupOf(t *testing.T, host *agentlink.Host) cleanupObservation {
	t.Helper()
	observation, ok := interface{}(host).(cleanupObservation)
	if !ok {
		t.Fatal("canonical Host cleanup observation is unavailable; Close would retry")
	}
	return observation
}
func TestHostCleanupObservationIsPureAndNotInferredFromStandaloneClose(t *testing.T) {
	signer, peer := fixtureSigner(t, 71), fixtureSigner(t, 72)
	_, _, session := grant(t, 5*time.Second, []ssh.PublicKey{peer.PublicKey()}, &macRuntime{procs: make(chan *fakeProc, 1)})
	host, err := agentlink.NewHost(agentlink.HostConfig{Session: session, AllowedKeys: []ssh.PublicKey{peer.PublicKey()}, HostKey: signer, Budgets: budgets()})
	if err != nil {
		t.Fatal(err)
	}
	observation := cleanupOf(t, host)
	if _, ok := observation.CleanupResult(); ok {
		t.Fatal("unattempted cleanup reported")
	}
	select {
	case <-session.Done():
		t.Fatal("observation revoked canonical Session")
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := host.Close(ctx)
	if result != nil {
		t.Fatal(result)
	}
	// Standalone Close returns its result to its caller. It does not assert
	// that a Serve invocation and its original cleanup have completed.
	if _, ok := observation.CleanupResult(); ok {
		t.Fatal("standalone Close fabricated Serve completion")
	}
}

type cleanupCountConn struct {
	net.Conn
	closes atomic.Int32
}

func (c *cleanupCountConn) Close() error { c.closes.Add(1); return c.Conn.Close() }

func TestHostCleanupObservationRequiresServeJoinAndNeverRetriesUnknown(t *testing.T) {
	signer, peer := fixtureSigner(t, 73), fixtureSigner(t, 74)
	runtime := &macRuntime{procs: make(chan *fakeProc, 1)}
	_, authority, session := grant(t, 5*time.Second, []ssh.PublicKey{peer.PublicKey()}, runtime)
	budget := budgets()
	budget.Cleanup = 20 * time.Millisecond
	host, err := agentlink.NewHost(agentlink.HostConfig{Session: session, AllowedKeys: []ssh.PublicKey{peer.PublicKey()}, HostKey: signer, Budgets: budget})
	if err != nil {
		t.Fatal(err)
	}
	observation := cleanupOf(t, host)
	raw, client := memPair()
	gate := &gatedClose{Conn: raw}
	counted := &cleanupCountConn{Conn: gate}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	done := make(chan struct{})
	var serveErr error
	go func() { serveErr = host.Serve(ctx, counted); close(done) }()
	t.Cleanup(func() {
		gate.allowed.Store(true)
		_ = client.Close()
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = host.Close(cleanup)
		select {
		case <-done:
		case <-cleanup.Done():
			t.Error("same Host Serve did not join")
		}
	})
	channel, err := agentchannel.Open(ctx, agentchannel.ClientConfig{
		Binding: agentchannel.Binding{Grant: session.Binding(), KeySetDigest: session.KeySetDigest(), Profile: agentexec.ProfileMacCurrentUser},
		HostKey: signer.PublicKey(), Signer: peer, Deadline: authority.Grant().HardDeadline, Budgets: budgets(),
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	if _, ok := observation.CleanupResult(); ok {
		t.Fatal("active Serve reported completed cleanup")
	}
	authority.Revoke(session.Binding())
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("Serve did not return unknown")
	}
	if !errors.Is(serveErr, agentchannel.ErrCleanupUnknown) {
		t.Fatalf("expected actual transport cleanup failure: %v", serveErr)
	}
	original, ok := observation.CleanupResult()
	if !ok || !errors.Is(original, agentchannel.ErrCleanupUnknown) {
		t.Fatal("original cleanup not observed", original, ok)
	}
	closes := counted.closes.Load()
	for i := 0; i < 4; i++ {
		if result, known := observation.CleanupResult(); !known || result != original {
			t.Fatal("original result changed", result, known)
		}
	}
	if counted.closes.Load() != closes {
		t.Fatal("observation retried native Close")
	}
	// Explicit same-owner recovery is an existing Host API, never performed by
	// the observer. It must not erase the original Serve's unknown result.
	gate.allowed.Store(true)
	if err := host.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if result, known := observation.CleanupResult(); !known || result != original {
		t.Fatal("later Close overwrote original Serve history")
	}
	if runtime.startCount() != 0 {
		t.Fatal("fixture launched a command")
	}
}

func TestHostCleanupObservationRejectsNilAndZero(t *testing.T) {
	for _, host := range []*agentlink.Host{nil, {}} {
		if _, ok := cleanupOf(t, host).CleanupResult(); ok {
			t.Fatal("empty Host reported cleanup")
		}
	}
}
