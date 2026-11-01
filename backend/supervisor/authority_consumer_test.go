package supervisor_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestDoneClosesOnExplicitRevokeOnly(t *testing.T) {
	g, now := approvedGrant()
	a, err := supervisor.New(g, now)
	if err != nil {
		t.Fatal(err)
	}
	done := a.Done()
	if closed(done) {
		t.Fatal("live authority reported done")
	}
	other := g.Binding
	other.Generation++
	if err := a.Revoke(other); !errors.Is(err, supervisor.ErrDenied) || closed(done) {
		t.Fatalf("other binding closed done: %v", err)
	}
	if err := a.Revoke(g.Binding); err != nil || !closed(done) || a.Done() != done {
		t.Fatalf("revoke did not close the same done channel: %v", err)
	}
	if err := a.Revoke(g.Binding); err != nil {
		t.Fatalf("repeated revoke: %v", err)
	}
}

func TestDoneClosesWhenDeadlineIsObserved(t *testing.T) {
	g, now := approvedGrant()
	a, err := supervisor.New(g, now)
	if err != nil {
		t.Fatal(err)
	}
	done := a.Done()
	a.Snapshot(g.HardDeadline.Add(-time.Second))
	if closed(done) {
		t.Fatal("done closed before the deadline")
	}
	if got := a.Snapshot(g.HardDeadline); got.Reason != supervisor.HardDeadlineReached || !closed(done) {
		t.Fatalf("observed deadline did not close done: %+v", got)
	}
}

func TestGrantReadDoesNotObserveTime(t *testing.T) {
	g, now := approvedGrant()
	a, err := supervisor.New(g, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Grant(); got != g || closed(a.Done()) {
		t.Fatalf("Grant changed state or value: %+v", got)
	}
	if got := a.Snapshot(now); got.State != supervisor.Granted {
		t.Fatalf("Grant read revoked the authority: %+v", got)
	}
}

func TestZeroAuthorityDoneIsClosed(t *testing.T) {
	var a supervisor.Authority
	if !closed(a.Done()) {
		t.Fatal("zero authority done is open")
	}
	if err := a.ClaimConsumer(supervisor.Binding{}); !errors.Is(err, supervisor.ErrDenied) {
		t.Fatalf("zero authority claimed: %v", err)
	}
}

func TestClaimConsumerIsSingleUseAcrossConcurrentCallers(t *testing.T) {
	g, now := approvedGrant()
	a, err := supervisor.New(g, now)
	if err != nil {
		t.Fatal(err)
	}
	other := g.Binding
	other.Identity.Epoch = "other"
	if err := a.ClaimConsumer(other); !errors.Is(err, supervisor.ErrDenied) {
		t.Fatalf("other binding claimed: %v", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- a.ClaimConsumer(g.Binding)
		}()
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, supervisor.ErrClaimed):
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("claims won = %d", won)
	}
	if err := a.Revoke(g.Binding); err != nil {
		t.Fatal(err)
	}
	if err := a.ClaimConsumer(g.Binding); !errors.Is(err, supervisor.ErrDenied) {
		t.Fatalf("revoked authority claimed again: %v", err)
	}
}

func TestClaimConsumerRejectsObservedExpiry(t *testing.T) {
	g, now := approvedGrant()
	a, err := supervisor.New(g, now)
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshot(g.HardDeadline)
	if err := a.ClaimConsumer(g.Binding); !errors.Is(err, supervisor.ErrDenied) || !closed(a.Done()) {
		t.Fatalf("expired grant claimed: %v", err)
	}
}
