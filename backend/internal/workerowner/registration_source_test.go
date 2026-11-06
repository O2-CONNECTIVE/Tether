package workerowner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type registrationWaiter interface {
	WaitSource(context.Context, context.Context, <-chan *Request) error
}

func sourceWaiter(t *testing.T, owner *Owner) registrationWaiter {
	t.Helper()
	w, ok := interface{}(owner).(registrationWaiter)
	if !ok {
		t.Fatal("owner cannot isolate registration lifetime from host lifetime")
	}
	return w
}
func sourceRequest(t *testing.T, started chan struct{}, closed *atomic.Int32, cleanup func(context.Context) error) *Request {
	t.Helper()
	scope, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return &Request{Deadline: time.Now().Add(time.Minute), SourceContext: scope, Reply: make(chan error, 1), Approve: func(context.Context) (*Worker, error) {
		return &Worker{Session: &fixtureSession{
			run: func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() },
			close: func(ctx context.Context) error {
				closed.Add(1)
				if cleanup != nil {
					return cleanup(ctx)
				}
				return nil
			},
		}}, nil
	}}
}
func sourceResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("source waiter did not finish")
		return nil
	}
}
func sourceStarted(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("source request did not run")
	}
}
func TestRegistrationSourceDisconnectKeepsHostForFreshWork(t *testing.T) {
	o := New(time.Second)
	w := sourceWaiter(t, o)
	host, stopHost := context.WithCancel(context.Background())
	defer stopHost()
	for attempt := 0; attempt < 2; attempt++ {
		source, disconnect := context.WithCancel(context.Background())
		requests := make(chan *Request, 1)
		done := make(chan error, 1)
		go func() { done <- w.WaitSource(host, source, requests) }()
		var closes atomic.Int32
		started := make(chan struct{})
		r := sourceRequest(t, started, &closes, nil)
		requests <- r
		sourceStarted(t, started)
		disconnect()
		if err := sourceResult(t, done); err != nil {
			t.Fatal(err)
		}
		if s := o.Snapshot(); s.Owned != 0 || s.Closed || s.Unknown || closes.Load() != 1 {
			t.Fatalf("host lost or cleanup repeated: %+v close=%d", s, closes.Load())
		}
	}
}
func TestRegistrationSourceClosedStreamStopsOnlyItsWork(t *testing.T) {
	o := New(time.Second)
	w := sourceWaiter(t, o)
	requests := make(chan *Request, 1)
	done := make(chan error, 1)
	go func() { done <- w.WaitSource(context.Background(), context.Background(), requests) }()
	var closes atomic.Int32
	started := make(chan struct{})
	requests <- sourceRequest(t, started, &closes, nil)
	sourceStarted(t, started)
	close(requests)
	if err := sourceResult(t, done); err != nil {
		t.Fatal(err)
	}
	if s := o.Snapshot(); s.Owned != 0 || s.Closed || closes.Load() != 1 {
		t.Fatal(s, closes.Load())
	}
}
func TestRegistrationSourceUnknownRetainsSlotWithoutRetry(t *testing.T) {
	o := New(20 * time.Millisecond)
	w := sourceWaiter(t, o)
	source, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	requests := make(chan *Request, 1)
	done := make(chan error, 1)
	go func() { done <- w.WaitSource(context.Background(), source, requests) }()
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var closes atomic.Int32
	started := make(chan struct{})
	requests <- sourceRequest(t, started, &closes, func(context.Context) error { close(entered); <-release; return nil })
	sourceStarted(t, started)
	disconnect()
	sourceStarted(t, entered)
	if err := sourceResult(t, done); !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
	if s := o.Snapshot(); s.Owned != 1 || !s.Unknown || s.Closed {
		t.Fatal(s)
	}
	var next atomic.Int32
	rejected := &Request{Deadline: time.Now().Add(time.Minute), SourceContext: context.Background(), Approve: func(context.Context) (*Worker, error) { next.Add(1); return nil, nil }}
	if err := o.Request(context.Background(), rejected); !errors.Is(err, ErrBusy) || next.Load() != 0 {
		t.Fatal(err, next.Load())
	}
	close(release)
	awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
	if closes.Load() != 1 {
		t.Fatal("cleanup retried", closes.Load())
	}
}

func TestRegistrationSourceHostOffSealsAdmission(t *testing.T) {
	o := New(time.Second)
	w := sourceWaiter(t, o)
	host, stopHost := context.WithCancel(context.Background())
	defer stopHost()
	source, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	requests, done := make(chan *Request, 1), make(chan error, 1)
	go func() { done <- w.WaitSource(host, source, requests) }()
	var closes atomic.Int32
	started := make(chan struct{})
	requests <- sourceRequest(t, started, &closes, nil)
	sourceStarted(t, started)
	stopHost()
	if err := sourceResult(t, done); err != nil {
		t.Fatal(err)
	}
	if s := o.Snapshot(); !s.Closed || s.Owned != 0 || s.Unknown || closes.Load() != 1 {
		t.Fatal(s, closes.Load())
	}
	if err := o.Request(context.Background(), freshRequest(nil)); !errors.Is(err, ErrRejected) {
		t.Fatal("OFF reopened admission", err)
	}
}

func TestRegistrationSourceRequiresOriginalFiniteRequestScope(t *testing.T) {
	o := New(time.Second)
	w := sourceWaiter(t, o)
	source, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	requests, done := make(chan *Request, 1), make(chan error, 1)
	go func() { done <- w.WaitSource(context.Background(), source, requests) }()
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for _, scope := range []context.Context{nil, context.Background(), expired} {
		var approvals atomic.Int32
		r := &Request{Deadline: time.Now().Add(time.Minute), SourceContext: scope, Reply: make(chan error, 1), Approve: func(context.Context) (*Worker, error) { approvals.Add(1); return nil, nil }}
		requests <- r
		select {
		case err := <-r.Reply:
			if !errors.Is(err, ErrRejected) {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("refusal not returned")
		}
		if approvals.Load() != 0 || o.Snapshot().Owned != 0 {
			t.Fatal("unscoped request reached local policy")
		}
	}
	disconnect()
	if err := sourceResult(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationSourceLateDisconnectCannotStopNewSource(t *testing.T) {
	o := New(time.Second)
	w := sourceWaiter(t, o)
	firstSource, disconnectFirst := context.WithCancel(context.Background())
	defer disconnectFirst()
	secondSource, disconnectSecond := context.WithCancel(context.Background())
	defer disconnectSecond()
	firstRequests, firstDone := make(chan *Request, 1), make(chan error, 1)
	secondRequests, secondDone := make(chan *Request, 1), make(chan error, 1)
	go func() { firstDone <- w.WaitSource(context.Background(), firstSource, firstRequests) }()
	go func() { secondDone <- w.WaitSource(context.Background(), secondSource, secondRequests) }()
	scope, cancelScope := context.WithTimeout(context.Background(), time.Minute)
	defer cancelScope()
	firstStarted, finishFirst := make(chan struct{}), make(chan struct{})
	firstRequests <- &Request{Deadline: time.Now().Add(time.Minute), SourceContext: scope, Approve: func(context.Context) (*Worker, error) {
		return &Worker{Session: &fixtureSession{run: func(ctx context.Context) error {
			close(firstStarted)
			select {
			case <-finishFirst:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, close: func(context.Context) error { return nil }}}, nil
	}}
	sourceStarted(t, firstStarted)
	close(finishFirst)
	awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
	observed := make(chan context.Context, 1)
	var closes atomic.Int32
	secondRequests <- &Request{Deadline: time.Now().Add(time.Minute), SourceContext: scope, Approve: func(context.Context) (*Worker, error) {
		return &Worker{Session: &fixtureSession{run: func(ctx context.Context) error { observed <- ctx; <-ctx.Done(); return ctx.Err() }, close: func(context.Context) error { closes.Add(1); return nil }}}, nil
	}}
	var held context.Context
	select {
	case held = <-observed:
	case <-time.After(time.Second):
		t.Fatal("new source did not run")
	}
	disconnectFirst()
	if err := sourceResult(t, firstDone); err != nil {
		t.Fatal(err)
	}
	if held.Err() != nil || closes.Load() != 0 || o.Snapshot().Unknown {
		t.Fatal("old source touched replacement")
	}
	disconnectSecond()
	if err := sourceResult(t, secondDone); err != nil {
		t.Fatal(err)
	}
	if s := o.Snapshot(); s.Closed || s.Owned != 0 || closes.Load() != 1 {
		t.Fatal(s, closes.Load())
	}
}

func TestRegistrationSourceDisconnectDuringAcquisitionRetainsLateWorker(t *testing.T) {
	o := New(20 * time.Millisecond)
	w := sourceWaiter(t, o)
	source, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	requests, done := make(chan *Request, 1), make(chan error, 1)
	scope, cancelScope := context.WithTimeout(context.Background(), time.Minute)
	defer cancelScope()
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	var runs, closes atomic.Int32
	go func() { done <- w.WaitSource(context.Background(), source, requests) }()
	requests <- &Request{Deadline: time.Now().Add(time.Minute), SourceContext: scope, Approve: func(context.Context) (*Worker, error) {
		close(entered)
		<-release
		return &Worker{Session: &fixtureSession{run: func(context.Context) error { runs.Add(1); return nil }, close: func(context.Context) error { closes.Add(1); return nil }}}, nil
	}}
	sourceStarted(t, entered)
	disconnect()
	if err := sourceResult(t, done); !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
	if s := o.Snapshot(); s.Owned != 1 || !s.Pending || !s.Unknown || s.Closed {
		t.Fatal(s)
	}
	close(release)
	awaitOwner(t, o, func(s Snapshot) bool { return s.Owned == 0 })
	if runs.Load() != 0 || closes.Load() != 1 {
		t.Fatal("late acquisition ran or lost cleanup", runs.Load(), closes.Load())
	}
}

func TestRegistrationSourceInvalidCompositionRefuses(t *testing.T) {
	for _, tc := range []struct {
		name         string
		owner        *Owner
		host, source context.Context
	}{
		{"nil-owner", nil, context.Background(), context.Background()},
		{"invalid-budget", New(0), context.Background(), context.Background()},
		{"nil-host", New(time.Second), nil, context.Background()},
		{"nil-source", New(time.Second), context.Background(), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.owner.WaitSource(tc.host, tc.source, nil); !errors.Is(err, ErrRejected) {
				t.Fatal(err)
			}
		})
	}
}
func TestRegistrationSourceAlreadyDisconnectedDoesNotAdmitQueuedWork(t *testing.T) {
	o := New(time.Second)
	source, disconnect := context.WithCancel(context.Background())
	disconnect()
	original, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var approvals atomic.Int32
	requests := make(chan *Request, 1)
	requests <- &Request{SourceContext: original, Deadline: time.Now().Add(time.Minute), Approve: func(context.Context) (*Worker, error) { approvals.Add(1); return nil, nil }}
	if err := o.WaitSource(context.Background(), source, requests); err != nil {
		t.Fatal(err)
	}
	if s := o.Snapshot(); s.Owned != 0 || s.Closed || approvals.Load() != 0 {
		t.Fatal(s, approvals.Load())
	}
}
