package agentgateway_test

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// blockedHandshake is an owned fake whose read stays pending even after
// Close. It models an unconfirmed native/transport operation; the test alone
// releases it, and no network or process is involved.
type blockedHandshake struct {
	net.Conn
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockedHandshake) Read([]byte) (int, error) {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return 0, net.ErrClosed
}
func (c *blockedHandshake) Write(p []byte) (int, error) { return len(p), nil }

func TestCloseReportsPendingConnectUntilOwnedHandshakeJoins(t *testing.T) {
	source := newRig(t, 0)
	m := source.approve(0, 30*time.Second)
	target := newRig(t, 0)
	_, base := memPair()
	c := &blockedHandshake{Conn: base, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(c.release) }) }
	defer release()
	done := make(chan error, 1)
	go func() { done <- target.gw.Connect(label(0), m.dispatch, c) }()
	select {
	case <-c.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handshake did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err := target.gw.Close(ctx)
	cancel()
	if !errors.Is(err, agentchannel.ErrCleanupUnknown) {
		release()
		<-done
		t.Fatalf("Close claimed completion with pending owned handshake: %v", err)
	}
	select {
	case <-done:
		t.Fatal("Connect ended before blocked read was released")
	default:
	}
	release()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("invalid handshake accepted")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Connect failed to join")
	}
	retry, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if err := target.gw.Close(retry); err != nil {
		t.Fatalf("same-owner retry: %v", err)
	}
}

type closeHeldHandshake struct {
	net.Conn
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *closeHeldHandshake) Read([]byte) (int, error)    { return 0, net.ErrClosed }
func (c *closeHeldHandshake) Write(p []byte) (int, error) { return len(p), nil }
func (c *closeHeldHandshake) Close() error {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return c.Conn.Close()
}

func TestConcurrentCloseRetainsUnknownAcquisitionAndSharedRetry(t *testing.T) {
	source := newRig(t, 0)
	m := source.approve(0, 30*time.Second)
	b := budgets()
	b.Cleanup = 100 * time.Millisecond
	gw, err := agentgateway.New(agentgateway.Config{
		Verifier: verifier(t), Principals: []agentgateway.Principal{{Label: label(0), Subject: subjects[0], Signer: fixtureSigner(t, 0x21)}}, Budgets: b,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, base := memPair()
	c := &closeHeldHandshake{Conn: base, entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(c.release) }) }
	defer release()
	if err := gw.Connect(label(0), m.dispatch, c); !errors.Is(err, agentchannel.ErrCleanupUnknown) {
		t.Fatalf("failed acquisition lost unknown owner: %v", err)
	}
	<-c.entered
	ready := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-ready
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			results <- gw.Close(ctx)
		}()
	}
	close(ready)
	for i := 0; i < 2; i++ {
		if err := <-results; !errors.Is(err, agentchannel.ErrCleanupUnknown) {
			t.Errorf("concurrent Close claimed pending owner cleaned: %v", err)
		}
	}
	release()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := gw.Close(ctx); err != nil {
		t.Fatalf("same-owner cleanup after release: %v", err)
	}
}
