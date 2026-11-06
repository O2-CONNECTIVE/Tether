package workerowner

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The source is already owned by the approval adapter. This context carries
// lifetime only; neither a context nor a Request is authentication proof.
func TestSourceCancellationRetainsLateAcquisitionAndNeverRuns(t *testing.T) {
	o := New(10 * time.Millisecond)
	source, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseOnce := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { releaseOnce(); ctx, stop := stoppedContext(); defer stop(); _ = o.Stop(ctx) })
	var runs, closes atomic.Int32
	r := &Request{Deadline: time.Now().Add(time.Minute), SourceContext: source, Approve: func(ctx context.Context) (*Worker, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return &Worker{Session: &fixtureSession{
			run:   func(context.Context) error { runs.Add(1); return nil },
			close: func(context.Context) error { closes.Add(1); return nil },
		}}, nil
	}}
	done := make(chan error, 1)
	go func() { done <- o.RunOnce(context.Background(), r) }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnknown) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("source cancellation did not reach acquisition")
	}
	if s := o.Snapshot(); s.Owned != 1 || !s.Pending || !s.Unknown {
		t.Fatalf("late acquisition lost: %+v", s)
	}
	releaseOnce()
	awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
	if runs.Load() != 0 || closes.Load() != 1 {
		t.Fatal("late source worker ran or cleanup repeated")
	}
}

func TestSourceLifetimeUsesEarliestDeadlineWithoutRenewal(t *testing.T) {
	for _, earliest := range []string{"source", "caller", "request"} {
		t.Run(earliest, func(t *testing.T) {
			now := time.Now()
			sourceEnd, callerEnd, requestEnd := now.Add(20*time.Second), now.Add(20*time.Second), now.Add(20*time.Second)
			want := now.Add(5 * time.Second)
			switch earliest {
			case "source":
				sourceEnd = want
			case "caller":
				callerEnd = want
			case "request":
				requestEnd = want
			}
			source, cancelSource := context.WithDeadline(context.Background(), sourceEnd)
			defer cancelSource()
			caller, cancelCaller := context.WithDeadline(context.Background(), callerEnd)
			defer cancelCaller()
			o := New(time.Second)
			observed := make(chan time.Time, 2)
			r := &Request{Deadline: requestEnd, SourceContext: source, Approve: func(ctx context.Context) (*Worker, error) {
				deadline, _ := ctx.Deadline()
				observed <- deadline
				return &Worker{Session: &fixtureSession{
					run: func(ctx context.Context) error {
						deadline, _ := ctx.Deadline()
						observed <- deadline
						<-ctx.Done()
						return ctx.Err()
					},
					close: func(context.Context) error { return nil },
				}}, nil
			}}
			done := make(chan error, 1)
			go func() { done <- o.RunOnce(caller, r) }()
			for i := 0; i < 2; i++ {
				select {
				case got := <-observed:
					if !got.Equal(want) {
						t.Fatalf("deadline renewed: %v != %v", got, want)
					}
				case <-time.After(time.Second):
					t.Fatal("context not observed")
				}
			}
			cancelSource()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("source cancellation lost: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("source cancellation not joined")
			}
			if o.Snapshot().Owned != 0 {
				t.Fatal("confirmed cleanup retained")
			}
		})
	}
}

func TestSourceRequestRejectsInvalidOriginBeforeApproval(t *testing.T) {
	for _, mode := range []string{"unbounded", "expired", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			var source context.Context
			var cancel context.CancelFunc
			switch mode {
			case "unbounded":
				source, cancel = context.WithCancel(context.Background())
			case "expired":
				source, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			case "canceled":
				source, cancel = context.WithTimeout(context.Background(), time.Minute)
				cancel()
			}
			defer cancel()
			for _, entry := range []string{"request", "run-once"} {
				o := New(time.Second)
				r := &Request{Deadline: time.Now().Add(time.Minute), SourceContext: source, Approve: func(context.Context) (*Worker, error) { t.Fatal("invalid source reached approval"); return nil, nil }}
				var err error
				if entry == "request" {
					err = o.Request(context.Background(), r)
				} else {
					err = o.RunOnce(context.Background(), r)
				}
				if !errors.Is(err, ErrRejected) || o.Snapshot().Owned != 0 {
					t.Fatalf("invalid source accepted via %s: %v", entry, err)
				}
			}
		})
	}
}

func TestWaitSourceCancellationReturnsToStandbyWithoutClosingHost(t *testing.T) {
	o := New(time.Second)
	host, cancelHost := context.WithCancel(context.Background())
	defer cancelHost()
	source, cancelSource := context.WithTimeout(context.Background(), time.Minute)
	defer cancelSource()
	requests := make(chan *Request, 1)
	done := make(chan error, 1)
	go func() { done <- o.Wait(host, requests, nil) }()
	started := make(chan struct{})
	var closes atomic.Int32
	r := &Request{Deadline: time.Now().Add(time.Minute), SourceContext: source, Approve: func(context.Context) (*Worker, error) {
		return &Worker{Session: &fixtureSession{
			run:   func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() },
			close: func(context.Context) error { closes.Add(1); return nil },
		}}, nil
	}}
	requests <- r
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("queued request did not start")
	}
	cancelSource()
	awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
	if o.Snapshot().Closed || closes.Load() != 1 {
		t.Fatal("source cancellation stopped host or retried cleanup")
	}
	cancelHost()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("host stop did not join")
	}
}

// Model legitimate asynchronous AfterFunc scheduling: the original Context
// reports cancellation immediately, while propagation callbacks are held at
// a test barrier. Production code must also recheck the original scope.
type deferredSourceContext struct {
	context.Context
	release <-chan struct{}
}

func (s deferredSourceContext) Value(key interface{}) interface{} { return nil }
func (s deferredSourceContext) AfterFunc(f func()) func() bool {
	return context.AfterFunc(s.Context, func() { <-s.release; f() })
}
func TestSourceCanceledInsideApprovalDoesNotRunBeforePropagation(t *testing.T) {
	raw, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	source := deferredSourceContext{Context: raw, release: release}
	o := New(time.Second)
	var runs, closes atomic.Int32
	r := &Request{Deadline: time.Now().Add(time.Minute), SourceContext: source, Approve: func(context.Context) (*Worker, error) {
		cancel()
		return &Worker{Session: &fixtureSession{
			run:   func(context.Context) error { runs.Add(1); return nil },
			close: func(context.Context) error { closes.Add(1); return nil },
		}}, nil
	}}
	err := o.RunOnce(context.Background(), r)
	if runs.Load() != 0 {
		t.Fatal("source canceled during approval still ran before propagation")
	}
	if !errors.Is(err, context.Canceled) || closes.Load() != 1 || o.Snapshot().Owned != 0 {
		t.Fatalf("source cancellation normalized: %v closes=%d", err, closes.Load())
	}
}

func TestRunOnceObservesOriginalSourceWhilePropagationIsDelayed(t *testing.T) {
	raw, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	release := make(chan struct{})
	acquireRelease := make(chan struct{})
	var once sync.Once
	releaseOnce := func() { once.Do(func() { close(release); close(acquireRelease) }) }
	o := New(10 * time.Millisecond)
	t.Cleanup(func() { releaseOnce(); ctx, stop := stoppedContext(); defer stop(); _ = o.Stop(ctx) })
	source := deferredSourceContext{Context: raw, release: release}
	entered := make(chan struct{})
	var runs, closes atomic.Int32
	r := &Request{Deadline: time.Now().Add(time.Minute), SourceContext: source, Approve: func(ctx context.Context) (*Worker, error) {
		close(entered)
		<-ctx.Done()
		// RunOnce also cancels its own scope when returning unknown. Keep the
		// actual acquisition independently blocked until ownership is observed.
		<-acquireRelease
		return &Worker{Session: &fixtureSession{
			run:   func(context.Context) error { runs.Add(1); return nil },
			close: func(context.Context) error { closes.Add(1); return nil },
		}}, nil
	}}
	done := make(chan error, 1)
	go func() { done <- o.RunOnce(context.Background(), r) }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnknown) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunOnce waited for deferred propagation instead of source cancellation")
	}
	if s := o.Snapshot(); s.Owned != 1 || !s.Pending || !s.Unknown {
		t.Fatalf("pending acquisition lost: %+v", s)
	}
	releaseOnce()
	awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
	if runs.Load() != 0 || closes.Load() != 1 {
		t.Fatal("late acquisition ran or cleanup repeated")
	}
}

// Hold the original scope's final error observation after cleanup has completed,
// so another public Request can be accepted before the old waiter returns.
type resultObservationBarrier struct {
	context.Context
	enabled atomic.Bool
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (c *resultObservationBarrier) Err() error {
	if c.enabled.Load() {
		c.once.Do(func() { close(c.entered) })
		<-c.release
	}
	return c.Context.Err()
}
func TestOldRunOnceUnknownResultCannotMarkReplacementUnknown(t *testing.T) {
	o := New(time.Second)
	scope, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	source := &resultObservationBarrier{Context: scope, entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	first := &Request{Deadline: time.Now().Add(time.Minute), SourceContext: source, Approve: func(context.Context) (*Worker, error) {
		return &Worker{Session: &fixtureSession{
			run:   func(context.Context) error { return ErrUnknown },
			close: func(context.Context) error { source.enabled.Store(true); return nil },
		}}, nil
	}}
	go func() { done <- o.RunOnce(context.Background(), first) }()
	select {
	case <-source.entered:
	case <-time.After(time.Second):
		t.Fatal("old result observation not reached")
	}
	awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
	started := make(chan struct{})
	second := freshRequest(&Worker{Session: &fixtureSession{
		run:   func(ctx context.Context) error { close(started); <-ctx.Done(); return nil },
		close: func(context.Context) error { return nil },
	}})
	if err := o.Request(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	<-started
	close(source.release)
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnknown) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("old waiter did not return")
	}
	if s := o.Snapshot(); s.Owned != 1 || s.Unknown || s.Closed {
		t.Fatalf("replacement contaminated: %+v", s)
	}
	ctx, stop := stoppedContext()
	defer stop()
	if err := o.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRunOnceUnknownNotifiesStandbyObserverBeforeAcquisitionReturns(t *testing.T) {
	o := New(10 * time.Millisecond)
	source, cancelSource := context.WithTimeout(context.Background(), time.Minute)
	defer cancelSource()
	watch, cancelWatch := context.WithCancel(context.Background())
	observed := make(chan string, 8)
	watchDone := make(chan error, 1)
	go func() { watchDone <- o.Wait(watch, nil, func(phase string) { observed <- phase }) }()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseOnce := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		releaseOnce()
		cancelWatch()
		select {
		case <-watchDone:
		case <-time.After(time.Second):
			t.Error("observer did not join")
		}
	})
	r := &Request{Deadline: time.Now().Add(time.Minute), SourceContext: source, Approve: func(context.Context) (*Worker, error) {
		close(entered)
		<-release
		return &Worker{Session: &fixtureSession{run: func(context.Context) error { t.Error("late worker ran"); return nil }, close: func(context.Context) error { return nil }}}, nil
	}}
	done := make(chan error, 1)
	go func() { done <- o.RunOnce(context.Background(), r) }()
	<-entered
	cancelSource()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnknown) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunOnce did not return")
	}
	select {
	case phase := <-observed:
		if phase != "cleanup_unknown" {
			t.Fatalf("wrong observation: %s", phase)
		}
	case <-time.After(time.Second):
		t.Fatal("unknown state was not notified")
	}
	if s := o.Snapshot(); s.Owned != 1 || !s.Pending || !s.Unknown {
		t.Fatalf("unknown acquisition not retained: %+v", s)
	}
	releaseOnce()
	awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
}
