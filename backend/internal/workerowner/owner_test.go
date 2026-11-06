package workerowner

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerOwnerHoldsUnknownUntilSameWorkerConfirmed(t *testing.T) {
	owner := New(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	worker := &Worker{Session: &fixtureSession{run: func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() }, close: func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}}
	request := &Request{Deadline: time.Now().Add(time.Minute), Approve: func(context.Context) (*Worker, error) { return worker, nil }}
	if err := owner.Request(ctx, request); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker not started")
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if owner.Stop(stopCtx) == nil {
		t.Fatal("unknown cleanup reported confirmed")
	}
	cancel()
	if owner.Snapshot().Owned != 1 {
		t.Fatal("unknown worker ownership lost")
	}
	close(release)
	stopCtx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := owner.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if owner.Snapshot().Owned != 0 {
		t.Fatal("confirmed worker retained")
	}
}

type fixtureSession struct {
	run   func(context.Context) error
	close func(context.Context) error
}

func (s *fixtureSession) Run(ctx context.Context) error   { return s.run(ctx) }
func (s *fixtureSession) Close(ctx context.Context) error { return s.close(ctx) }

func awaitOwner(t *testing.T, o *Owner, want func(Snapshot) bool) {
	t.Helper()
	timeoutTimer := time.NewTimer(time.Second)
	defer timeoutTimer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if want(o.Snapshot()) {
			return
		}
		select {
		case <-tick.C:
		case <-timeoutTimer.C:
			t.Fatalf("owner state not reached: %+v", o.Snapshot())
		}
	}
}
func freshRequest(w *Worker) *Request {
	return &Request{Deadline: time.Now().Add(time.Minute), Approve: func(context.Context) (*Worker, error) { return w, nil }}
}
func stoppedContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Second)
}

func TestWorkerOwnerLatePartialApprovalIsClosedNotRun(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "late", true: "partial"}[partial], func(t *testing.T) {
			o := New(time.Second)
			entered := make(chan struct{})
			release := make(chan struct{})
			var runs, closes atomic.Int32
			w := &Worker{Session: &fixtureSession{run: func(context.Context) error { runs.Add(1); return nil }, close: func(context.Context) error { closes.Add(1); return nil }}}
			r := &Request{Deadline: time.Now().Add(time.Minute), Approve: func(context.Context) (*Worker, error) {
				close(entered)
				<-release
				if partial {
					return w, errors.New("private approval detail")
				}
				return w, nil
			}}
			if err := o.Request(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("approval not entered")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			if o.Stop(ctx) == nil {
				t.Fatal("pending approval released")
			}
			cancel()
			if s := o.Snapshot(); s.Owned != 1 || !s.Pending {
				t.Fatalf("lost pending owner: %+v", s)
			}
			close(release)
			ctx, cancel = stoppedContext()
			defer cancel()
			if err := o.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			if runs.Load() != 0 || closes.Load() != 1 {
				t.Fatalf("runs=%d closes=%d", runs.Load(), closes.Load())
			}
		})
	}
}

func TestWorkerOwnerRejectsConcurrentRequestsAndCannotReuseWorker(t *testing.T) {
	o := New(time.Second)
	var runs, closes atomic.Int32
	w := &Worker{Session: &fixtureSession{run: func(ctx context.Context) error { runs.Add(1); <-ctx.Done(); return nil }, close: func(context.Context) error { closes.Add(1); return nil }}}
	shared := freshRequest(w)
	var accepted atomic.Int32
	var group sync.WaitGroup
	for i := 0; i < 10; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if o.Request(context.Background(), shared) == nil {
				accepted.Add(1)
			}
		}()
	}
	group.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("admitted %d", accepted.Load())
	}
	awaitOwner(t, o, func(s Snapshot) bool { return !s.Pending && runs.Load() == 1 })
	if !errors.Is(o.Request(context.Background(), freshRequest(w)), ErrBusy) {
		t.Fatal("parallel attempt accepted")
	}
	o.cancelCurrent()
	awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
	if err := o.Request(context.Background(), freshRequest(w)); err != nil {
		t.Fatal(err)
	}
	awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
	if runs.Load() != 1 || closes.Load() != 1 {
		t.Fatal("consumed worker resumed or reclosed")
	}
	fresh := &Worker{Session: &fixtureSession{run: func(context.Context) error { runs.Add(1); return nil }, close: func(context.Context) error { closes.Add(1); return nil }}}
	if err := o.Request(context.Background(), freshRequest(fresh)); err != nil {
		t.Fatal(err)
	}
	awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
	if runs.Load() != 2 || closes.Load() != 2 {
		t.Fatal("fresh Request did not get independent worker")
	}
}

func TestWorkerOwnerCloseErrorRequiresExplicitRetry(t *testing.T) {
	o := New(time.Second)
	var closes atomic.Int32
	w := &Worker{Session: &fixtureSession{run: func(context.Context) error { return nil }, close: func(context.Context) error {
		if closes.Add(1) == 1 {
			return errors.New("private close failure")
		}
		return nil
	}}}
	if err := o.Request(context.Background(), freshRequest(w)); err != nil {
		t.Fatal(err)
	}
	awaitOwner(t, o, func(s Snapshot) bool { return s.Unknown })
	if !errors.Is(o.Request(context.Background(), freshRequest(w)), ErrBusy) {
		t.Fatal("unknown owner slot released")
	}
	for i := 0; i < 3; i++ {
		ctx, cancel := stoppedContext()
		if o.Stop(ctx) == nil {
			t.Fatal("unknown reported confirmed")
		}
		cancel()
	}
	if closes.Load() != 1 || o.Snapshot().Owned != 1 {
		t.Fatal("Stop silently retried or discarded worker")
	}
	ctx, cancel := stoppedContext()
	defer cancel()
	if err := o.retryCleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 2 || o.Snapshot().Owned != 0 {
		t.Fatal("same-owner explicit retry did not confirm")
	}
	if o.Request(context.Background(), freshRequest(w)) == nil {
		t.Fatal("shutdown reopened admission")
	}
}

func TestWorkerOwnerRunMustJoinEvenAfterCloseReturnsNil(t *testing.T) {
	o := New(10 * time.Millisecond)
	release := make(chan struct{})
	started := make(chan struct{})
	var closes atomic.Int32
	w := &Worker{Session: &fixtureSession{run: func(context.Context) error { close(started); <-release; return nil }, close: func(context.Context) error { closes.Add(1); return nil }}}
	if err := o.Request(context.Background(), freshRequest(w)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("run not started")
	}
	o.cancelCurrent()
	awaitOwner(t, o, func(s Snapshot) bool { return s.Unknown })
	if o.Snapshot().Owned != 1 {
		t.Fatal("live Run goroutine abandoned")
	}
	ctx, cancel := stoppedContext()
	if o.retryCleanup(ctx) == nil {
		t.Fatal("recheck claimed join or retried confirmed close")
	}
	cancel()
	close(release)
	awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
	if closes.Load() != 1 {
		t.Fatal("close repeated instead of observing Run join")
	}
}

func TestWorkerOwnerDeadlineCancelsAttemptWithoutRenewal(t *testing.T) {
	o := New(time.Second)
	started := make(chan time.Time, 1)
	r := freshRequest(&Worker{Session: &fixtureSession{run: func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		started <- deadline
		<-ctx.Done()
		return nil
	}, close: func(context.Context) error { return nil }}})
	r.Deadline = time.Now().Add(30 * time.Millisecond)
	if err := o.Request(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	select {
	case deadline := <-started:
		if !deadline.Equal(r.Deadline) {
			t.Fatal("Deadline renewed")
		}
	case <-time.After(time.Second):
		t.Fatal("run missing")
	}
	awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
	if o.Request(context.Background(), r) == nil {
		t.Fatal("expired Request reused")
	}
}

func TestWorkerOwnerAcquisitionPanicRetainsUnknownRecord(t *testing.T) {
	o := New(time.Second)
	r := &Request{Deadline: time.Now().Add(time.Minute), Approve: func(context.Context) (*Worker, error) { panic("private detail") }}
	if err := o.Request(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	awaitOwner(t, o, func(s Snapshot) bool { return s.Unknown })
	ctx, cancel := stoppedContext()
	defer cancel()
	if o.Stop(ctx) == nil || o.retryCleanup(ctx) == nil || o.Snapshot().Owned != 1 {
		t.Fatal("unknown acquisition discarded")
	}
}

func TestWorkerOwnerInFlightCloseNeverSpawnsRetry(t *testing.T) {
	o := New(10 * time.Millisecond)
	entered := make(chan struct{})
	release := make(chan struct{})
	var closes atomic.Int32
	w := &Worker{Session: &fixtureSession{run: func(context.Context) error { return nil }, close: func(context.Context) error { closes.Add(1); close(entered); <-release; return nil }}}
	if err := o.Request(context.Background(), freshRequest(w)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("close not entered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	if o.Stop(ctx) == nil {
		t.Fatal("in-flight close declared joined")
	}
	cancel()
	ctx, cancel = stoppedContext()
	if o.retryCleanup(ctx) == nil {
		t.Fatal("parallel retry accepted")
	}
	cancel()
	if o.Snapshot().Owned != 1 || closes.Load() != 1 {
		t.Fatal("live cleanup ownership lost")
	}
	close(release)
	ctx, cancel = stoppedContext()
	defer cancel()
	if err := o.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatal("cleanup restarted")
	}
}

// retryCleanup is a distinct explicit local operation on the SAME retained
// worker after a returned Close error. It never restarts approval or Run. An
// acquisition or Close still in flight prevents another cleanup goroutine.
// This entry is fixture-only today, not a native retry policy. A retained owner
// or an explicit call does NOT make permission denial/identity loss retryable.
// Native wiring requires Session-specific retry eligibility and authority checks;
// it must not forward arbitrary returned errors into this operation.
func (o *Owner) retryCleanup(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrUnknown
	}
	if _, ok := ctx.Deadline(); !ok {
		return ErrUnknown
	}
	o.mu.Lock()
	o.refreshLocked()
	a := o.held
	if a == nil {
		o.mu.Unlock()
		return nil
	}
	if a.pending || !a.complete || a.worker == nil || a.closeConfirmed {
		o.mu.Unlock()
		return ErrUnknown
	}
	a.complete = false
	a.done = make(chan struct{})
	a.cancel()
	done := a.done
	o.mu.Unlock()
	go o.cleanup(ctx, a)
	select {
	case <-done:
	case <-ctx.Done():
		return ErrUnknown
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.refreshLocked()
	if o.held != nil {
		return ErrUnknown
	}
	return nil
}

// cancelCurrent is cancellation of the current local attempt, not permission to
// mint a replacement grant. The slot remains until the same resources confirm.
func (o *Owner) cancelCurrent() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.held != nil {
		o.held.cancel()
	}
}

// A GUI snapshot must preserve an unconfirmed stop even while Close is still
// in flight, not only after Close returns an error.
func TestStopTimeoutPublishesUnknownWhileRetainingOwner(t *testing.T) {
	o := New(time.Second)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() { close(release); awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 }) }()
	if err := o.Request(context.Background(), freshRequest(&Worker{Session: &fixtureSession{
		run:   func(ctx context.Context) error { <-ctx.Done(); return nil },
		close: func(context.Context) error { close(entered); <-release; return nil },
	}})); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if !errors.Is(o.Stop(ctx), ErrUnknown) {
		t.Fatal("unconfirmed stop accepted")
	}
	<-entered
	if s := o.Snapshot(); s.Owned != 1 || !s.Closed || !s.Unknown {
		t.Fatalf("unconfirmed stop hidden from snapshot: %+v", s)
	}
}

// Request identity is backend-owned; this is not a UI-provided grant or ID.
func TestStopRequestReturnsToStandbyAndRejectsStaleStop(t *testing.T) {
	o := New(time.Second)
	stopper, ok := interface{}(o).(interface {
		StopRequest(context.Context, *Request) error
	})
	if !ok {
		t.Fatal("request-bound stop entry missing")
	}
	var runs, closes atomic.Int32
	makeRequest := func(started chan struct{}) *Request {
		return freshRequest(&Worker{Session: &fixtureSession{
			run:   func(ctx context.Context) error { runs.Add(1); close(started); <-ctx.Done(); return nil },
			close: func(context.Context) error { closes.Add(1); return nil },
		}})
	}
	started := make(chan struct{})
	first := makeRequest(started)
	if err := o.Request(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := stoppedContext()
	defer cancel()
	if err := stopper.StopRequest(ctx, first); err != nil {
		t.Fatal(err)
	}
	if s := o.Snapshot(); s.Owned != 0 || s.Closed || s.Unknown {
		t.Fatalf("not standby: %+v", s)
	}
	started = make(chan struct{})
	second := makeRequest(started)
	if err := o.Request(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	<-started
	defer func() {
		if err := o.Stop(ctx); err != nil {
			t.Fatal(err)
		}
	}()
	if !errors.Is(stopper.StopRequest(ctx, first), ErrRejected) {
		t.Fatal("stale stop accepted")
	}
	if s := o.Snapshot(); s.Owned != 1 || s.Closed {
		t.Fatalf("next worker affected: %+v", s)
	}
	if err := stopper.StopRequest(ctx, second); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 2 || closes.Load() != 2 {
		t.Fatal("worker reused or cleanup repeated")
	}
}

func TestStopRequestUnknownRetainsExactAttemptAndBlocksReplacement(t *testing.T) {
	for _, blocked := range []string{"approve", "run", "close", "close-error"} {
		t.Run(blocked, func(t *testing.T) {
			o := New(10 * time.Millisecond)
			entered, release := make(chan struct{}), make(chan struct{})
			var runs, closes atomic.Int32
			w := &Worker{Session: &fixtureSession{
				run: func(ctx context.Context) error {
					runs.Add(1)
					if blocked == "run" {
						close(entered)
						<-release
					} else {
						<-ctx.Done()
					}
					return nil
				},
				close: func(context.Context) error {
					closes.Add(1)
					if blocked == "close" {
						close(entered)
						<-release
					}
					if blocked == "close-error" {
						return errors.New("private failure sentinel")
					}
					return nil
				},
			}}
			r := freshRequest(w)
			if blocked == "approve" {
				r.Approve = func(context.Context) (*Worker, error) { close(entered); <-release; return w, nil }
			}
			if err := o.Request(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if blocked == "approve" || blocked == "run" {
				<-entered
			} else {
				awaitOwner(t, o, func(s Snapshot) bool { return !s.Pending && runs.Load() == 1 })
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			if !errors.Is(o.StopRequest(ctx, r), ErrUnknown) {
				t.Fatal("unknown stop accepted")
			}
			s := o.Snapshot()
			if s.Owned != 1 || !s.Unknown || s.Closed {
				t.Fatalf("unknown ownership lost: %+v", s)
			}
			replacement := freshRequest(&Worker{Session: w.Session})
			if !errors.Is(o.Request(context.Background(), replacement), ErrBusy) {
				t.Fatal("replacement admitted")
			}
			copyRequest := freshRequest(w)
			if !errors.Is(o.StopRequest(ctx, copyRequest), ErrRejected) {
				t.Fatal("unrelated request accepted")
			}
			close(release)
			if blocked == "close-error" {
				stop, done := stoppedContext()
				defer done()
				if !errors.Is(o.Stop(stop), ErrUnknown) || o.Snapshot().Owned != 1 || closes.Load() != 1 {
					t.Fatal("failed Close retried or owner dropped")
				}
			} else {
				awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
				if closes.Load() != 1 {
					t.Fatal("cleanup repeated")
				}
				if blocked == "approve" && runs.Load() != 0 {
					t.Fatal("late acquisition started")
				}
			}
		})
	}
}

func TestStopRequestRejectsInvalidWaitAndDoesNotSealIdleOwner(t *testing.T) {
	o := New(time.Second)
	r := freshRequest(nil)
	ctx, cancel := stoppedContext()
	defer cancel()
	if !errors.Is(o.StopRequest(ctx, nil), ErrRejected) || !errors.Is(o.StopRequest(ctx, r), ErrRejected) {
		t.Fatal("nonowned request accepted")
	}
	if !errors.Is(o.StopRequest(context.Background(), r), ErrUnknown) || !errors.Is(o.StopRequest(nil, r), ErrUnknown) {
		t.Fatal("unbounded wait accepted")
	}
	if s := o.Snapshot(); s.Closed || s.Owned != 0 {
		t.Fatalf("idle owner changed: %+v", s)
	}
}
