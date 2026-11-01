package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
)

type fakeHTTPShutdown struct {
	shutdownErr error
	closed      bool
}

func (s *fakeHTTPShutdown) Shutdown(context.Context) error { return s.shutdownErr }
func (s *fakeHTTPShutdown) Close() error                   { s.closed = true; return nil }

// ownedFixture records the close order of the registration handler and the
// gateway; both must see a bounded context.
func ownedFixture(t *testing.T, served <-chan struct{}, registrationErr error) (*owned, *fakeHTTPShutdown, func() string) {
	var mu sync.Mutex
	var order []string
	srv := &fakeHTTPShutdown{}
	step := func(name string, err error) func(context.Context) error {
		return func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); !ok {
				t.Errorf("unbounded %s close", name)
			}
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return err
		}
	}
	trace := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(order, " ")
	}
	return &owned{srv: srv, served: served, closeRegistration: step("registration", registrationErr), closeGateway: step("gateway", nil)}, srv, trace
}

func TestFinishOwnedClosesRegistrationBeforeGatewayAndJoinsServe(t *testing.T) {
	served := make(chan struct{})
	close(served)
	o, srv, trace := ownedFixture(t, served, nil)
	if err := finishOwned(*o, time.Second); err != nil || srv.closed || trace() != "registration gateway" {
		t.Fatalf("err=%v forced=%v order=%q", err, srv.closed, trace())
	}
}

func TestFinishOwnedRetainsUnconfirmedRegistrationAndStillClosesGateway(t *testing.T) {
	served := make(chan struct{})
	close(served)
	o, _, trace := ownedFixture(t, served, errors.Join(agenttransport.ErrCleanupUnknown, context.DeadlineExceeded))
	if err := finishOwned(*o, time.Second); !errors.Is(err, agenttransport.ErrCleanupUnknown) || trace() != "registration gateway" {
		t.Fatalf("err=%v order=%q", err, trace())
	}
}

func TestFinishOwnedReportsServeStillRunning(t *testing.T) {
	pending := make(chan struct{})
	defer close(pending)
	o, _, trace := ownedFixture(t, pending, nil)
	if err := finishOwned(*o, 10*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) || trace() != "registration gateway" {
		t.Fatalf("err=%v order=%q", err, trace())
	}
}

func TestFinishOwnedForcesHTTPClosureAndReportsFailure(t *testing.T) {
	served := make(chan struct{})
	close(served)
	o, srv, _ := ownedFixture(t, served, nil)
	srv.shutdownErr = context.DeadlineExceeded
	if err := finishOwned(*o, time.Second); !errors.Is(err, context.DeadlineExceeded) || !srv.closed {
		t.Fatalf("err=%v forced=%v", err, srv.closed)
	}
}

func TestCloseUnservedReportsUnconfirmedOwner(t *testing.T) {
	var diagnostic strings.Builder
	calls := 0
	unknown := func(context.Context) error { calls++; return agenttransport.ErrCleanupUnknown }
	clean := func(context.Context) error { calls++; return nil }
	if code := closeUnserved(unknown, clean, &diagnostic, 2); code != 1 || calls != 2 || diagnostic.Len() == 0 {
		t.Fatalf("exit=%d calls=%d", code, calls)
	}
	if code := closeUnserved(nil, clean, &diagnostic, 2); code != 2 {
		t.Fatalf("clean exit=%d", code)
	}
}
