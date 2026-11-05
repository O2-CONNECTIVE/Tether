package workerowner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunOnceWaitsForCleanupAndReturnsRunError(t *testing.T) {
	o := New(time.Second)
	failure := errors.New("run failed")
	entered, release := make(chan struct{}), make(chan struct{})
	r := freshRequest(&Worker{Session: &fixtureSession{
		run:   func(context.Context) error { return failure },
		close: func(context.Context) error { close(entered); <-release; return nil },
	}})
	done := make(chan error, 1)
	go func() { done <- o.RunOnce(context.Background(), r) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup missing")
	}
	select {
	case <-done:
		t.Fatal("returned before cleanup")
	default:
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("completion missing")
	}
	if o.Snapshot().Owned != 0 {
		t.Fatal("confirmed owner retained")
	}
}

func TestRunOnceCancellationRetainsLatePartialAcquisition(t *testing.T) {
	o := New(15 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	entered, release := make(chan struct{}), make(chan struct{})
	var runs, closes atomic.Int32
	r := &Request{Deadline: time.Now().Add(20 * time.Minute), Approve: func(context.Context) (*Worker, error) {
		close(entered)
		<-release
		return &Worker{Session: &fixtureSession{run: func(context.Context) error { runs.Add(1); return nil }, close: func(context.Context) error { closes.Add(1); return nil }}}, errors.New("partial")
	}}
	done := make(chan error, 1)
	go func() { done <- o.RunOnce(ctx, r) }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnknown) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unbounded acquire wait")
	}
	if s := o.Snapshot(); s.Owned != 1 || !s.Pending {
		t.Fatalf("lost acquisition: %+v", s)
	}
	close(release)
	stop, cleanup := stoppedContext()
	defer cleanup()
	if err := o.Stop(stop); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 0 || closes.Load() != 1 {
		t.Fatalf("runs=%d closes=%d", runs.Load(), closes.Load())
	}
}

func TestRunOnceCancellationRetainsUnjoinedRunAndClose(t *testing.T) {
	for _, blocked := range []string{"run", "close"} {
		t.Run(blocked, func(t *testing.T) {
			o := New(15 * time.Millisecond)
			ctx, cancel := context.WithCancel(context.Background())
			entered, release := make(chan struct{}), make(chan struct{})
			var closes atomic.Int32
			w := &Worker{Session: &fixtureSession{
				run: func(ctx context.Context) error {
					close(entered)
					if blocked == "run" {
						<-release
					} else {
						<-ctx.Done()
					}
					return nil
				},
				close: func(context.Context) error {
					closes.Add(1)
					if blocked == "close" {
						<-release
					}
					return nil
				},
			}}
			done := make(chan error, 1)
			go func() { done <- o.RunOnce(ctx, freshRequest(w)) }()
			<-entered
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, ErrUnknown) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("unbounded join")
			}
			if s := o.Snapshot(); s.Owned != 1 || !s.Unknown {
				t.Fatalf("unconfirmed run/close hidden: %+v", s)
			}
			if !errors.Is(o.Request(context.Background(), freshRequest(w)), ErrBusy) {
				t.Fatal("admitted parallel worker")
			}
			close(release)
			awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
			if closes.Load() != 1 {
				t.Fatal("cleanup retried")
			}
		})
	}
}

func TestRunOnceReturnsCancellationAfterConfirmedCleanup(t *testing.T) {
	o := New(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- o.RunOnce(ctx, freshRequest(&Worker{Session: &fixtureSession{
			run:   func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() },
			close: func(context.Context) error { return nil },
		}}))
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("completion missing")
	}
	if o.Snapshot().Owned != 0 {
		t.Fatal("cleanup not confirmed")
	}
}

func TestRunOnceRejectsInvalidBudgetAndRetainsUnknownAcquisition(t *testing.T) {
	for _, budget := range []time.Duration{0, -1, time.Minute + 1} {
		o := New(budget)
		if o.ValidBudget() {
			t.Fatal("invalid budget")
		}
		if !errors.Is(o.RunOnce(context.Background(), freshRequest(nil)), ErrRejected) {
			t.Fatal("accepted invalid budget")
		}
	}
	o := New(time.Second)
	r := &Request{Deadline: time.Now().Add(time.Minute), Approve: func(context.Context) (*Worker, error) { return nil, ErrUnknown }}
	if !errors.Is(o.RunOnce(context.Background(), r), ErrUnknown) || o.Snapshot().Owned != 1 {
		t.Fatal("unknown discarded")
	}
}

func TestRunOnceBoundsNaturallyStartedClose(t *testing.T) {
	o := New(10 * time.Millisecond)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release); awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 }) })
	done := make(chan error, 1)
	go func() {
		done <- o.RunOnce(context.Background(), freshRequest(&Worker{Session: &fixtureSession{
			run:   func(context.Context) error { return nil },
			close: func(context.Context) error { close(entered); <-release; return nil },
		}}))
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("cleanup not started")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnknown) {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Close wait used request lifetime instead of cleanup deadline")
	}
	if s := o.Snapshot(); s.Owned != 1 || !s.Unknown {
		t.Fatalf("in-flight Close hidden: %+v", s)
	}
}
