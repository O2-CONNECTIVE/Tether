package workerowner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// This test-only Context is a barrier at select operand evaluation. Stop has
// already captured/canceled its exact attempt and released mu when Done is
// called. Holding this call lets the actual cleanup and next public Request
// complete before the waiter observes done; no production hook is installed.
type stopOperandBarrier struct {
	context.Context
	entered chan struct{}
	release chan struct{}
}

func (c *stopOperandBarrier) Done() <-chan struct{} {
	close(c.entered)
	<-c.release
	return c.Context.Done()
}
func newStopBarrier(t *testing.T) *stopOperandBarrier {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return &stopOperandBarrier{Context: ctx, entered: make(chan struct{}), release: make(chan struct{})}
}
func waitBarrier(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("barrier not entered")
	}
}
func stopResult(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(time.Second):
		t.Fatal("stop waiter did not finish")
		return ErrUnknown
	}
}

func TestStopRequestWaiterDoesNotObserveReplacementAsItsOwn(t *testing.T) {
	o := New(time.Second)
	closeEntered, closeRelease := make(chan struct{}), make(chan struct{})
	var firstCloses, secondCloses atomic.Int32
	first := freshRequest(&Worker{Session: &fixtureSession{
		run:   func(ctx context.Context) error { <-ctx.Done(); return nil },
		close: func(context.Context) error { firstCloses.Add(1); close(closeEntered); <-closeRelease; return nil },
	}})
	if err := o.Request(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	barrier := newStopBarrier(t)
	stopped := make(chan error, 1)
	go func() { stopped <- o.StopRequest(barrier, first) }()
	waitBarrier(t, barrier.entered)
	waitBarrier(t, closeEntered)
	close(closeRelease)
	awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
	started := make(chan struct{})
	second := freshRequest(&Worker{Session: &fixtureSession{
		run:   func(ctx context.Context) error { close(started); <-ctx.Done(); return nil },
		close: func(context.Context) error { secondCloses.Add(1); return nil },
	}})
	if err := o.Request(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	waitBarrier(t, started)
	close(barrier.release)
	if err := stopResult(t, stopped); err != nil {
		t.Fatalf("old stop observed new worker: %v", err)
	}
	if s := o.Snapshot(); s.Owned != 1 || s.Closed || s.Unknown || secondCloses.Load() != 0 {
		t.Fatalf("new worker affected: %+v", s)
	}
	ctx, cancel := stoppedContext()
	defer cancel()
	if !errors.Is(o.StopRequest(ctx, first), ErrRejected) {
		t.Fatal("stale request accepted")
	}
	if err := o.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if firstCloses.Load() != 1 || secondCloses.Load() != 1 {
		t.Fatal("cleanup repeated or skipped")
	}
}

func TestStopRequestAndHostStopObserveSameCleanupWithoutRetry(t *testing.T) {
	o := New(time.Second)
	entered, release := make(chan struct{}), make(chan struct{})
	var closes atomic.Int32
	request := freshRequest(&Worker{Session: &fixtureSession{
		run:   func(ctx context.Context) error { <-ctx.Done(); return nil },
		close: func(context.Context) error { closes.Add(1); close(entered); <-release; return nil },
	}})
	if err := o.Request(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	local, host := newStopBarrier(t), newStopBarrier(t)
	localDone, hostDone := make(chan error, 1), make(chan error, 1)
	go func() { localDone <- o.StopRequest(local, request) }()
	waitBarrier(t, local.entered)
	go func() { hostDone <- o.Stop(host) }()
	waitBarrier(t, host.entered)
	waitBarrier(t, entered)
	if s := o.Snapshot(); s.Owned != 1 || !s.Closed {
		t.Fatalf("host stop failed to seal: %+v", s)
	}
	if !errors.Is(o.Request(context.Background(), freshRequest(nil)), ErrRejected) {
		t.Fatal("host reopened admission")
	}
	close(release)
	awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 && s.Closed })
	close(host.release)
	close(local.release)
	if err := stopResult(t, hostDone); err != nil {
		t.Fatal(err)
	}
	if err := stopResult(t, localDone); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatal("parallel waiters repeated cleanup")
	}
}
