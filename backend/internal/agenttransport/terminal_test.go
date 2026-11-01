package agenttransport

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStopTerminalUnknownSealsWithoutOwnerCleanup(t *testing.T) {
	p := newComposed(t, nil)
	if err := p.c.StopTerminalUnknown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.c.Run(context.Background()); !errors.Is(err, ErrUsed) {
		t.Fatal("reconnect allowed", err)
	}
	// No Run means no transport acquisition/serve; stopping does not perform owner Close.
	if p.owner.serves.Load() != 0 {
		t.Fatal("served")
	}
}
func TestStopTerminalUnknownCancellationIsBounded(t *testing.T) {
	called := false
	c := &Client{ran: true, finished: make(chan struct{}), cancel: func() { called = true }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if !errors.Is(c.StopTerminalUnknown(ctx), ErrCleanupUnknown) || !called {
		t.Fatal("not bounded/sealed")
	}
}
func TestStopTerminalUnknownBusyCloseRemainsUnknown(t *testing.T) {
	c := &Client{finished: make(chan struct{})}
	c.cleanMu.Lock()
	err := c.StopTerminalUnknown(context.Background())
	c.cleanMu.Unlock()
	if !errors.Is(err, ErrCleanupUnknown) {
		t.Fatal("busy close hidden", err)
	}
}
