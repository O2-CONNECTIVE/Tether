package maclaunch

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentlink"
)

func TestPartialApprovalRetainsFailedHostAcquisition(t *testing.T) {
	p, err := prepare(config(t), &neverRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	// The fake-terminal seam makes host validation fail after Session acquisition.
	p.budgets.Handshake = 0
	a, err := p.approve(context.Background(), &terminal{reply: p.phrase + "\n"})
	if a == nil || !errors.Is(err, agentlink.ErrConfig) {
		t.Fatalf("partial owner/error = %v, %v", a, err)
	}
	if a.session == nil || a.issuer == nil || a.host != nil {
		t.Fatal("lost partial references")
	}
	assertFailedApproval(t, p, a)
	if cleanup, observed := cleanupObservationFor(t, a).CleanupResult(); cleanup != nil || !observed {
		t.Fatalf("cleanup = %v, %v", cleanup, observed)
	}
}

type cancelAtSessionRuntime struct {
	neverRuntime
	cancel context.CancelFunc
}

func (r *cancelAtSessionRuntime) AgentProfile() agentexec.Profile {
	if r.cancel != nil {
		r.cancel()
	}
	return agentexec.ProfileMacCurrentUser
}

func TestPartialApprovalRetainsCanceledHost(t *testing.T) {
	r := &cancelAtSessionRuntime{}
	p, err := prepare(config(t), r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.cancel = cancel
	a, err := p.approve(ctx, &terminal{reply: p.phrase + "\n"})
	if a == nil || !errors.Is(err, ErrDenied) {
		t.Fatalf("partial owner/error = %v, %v", a, err)
	}
	if a.session == nil || a.issuer == nil || a.host == nil {
		t.Fatal("lost partial references")
	}
	assertFailedApproval(t, p, a)
	if cleanup, observed := cleanupObservationFor(t, a).CleanupResult(); cleanup != nil || !observed {
		t.Fatalf("cleanup = %v, %v", cleanup, observed)
	}
}

func assertFailedApproval(t *testing.T, p *Pending, a *Approved) {
	t.Helper()
	if d := a.Dispatch(); !d.Deadline.IsZero() || len(d.HostKey) != 0 || d.Binding.Grant.Generation != 0 {
		t.Fatal("failed owner exported dispatch")
	}
	if err := a.Serve(context.Background(), nil); !errors.Is(err, ErrConfig) {
		t.Fatal("failed owner accepted Serve", err)
	}
	select {
	case <-a.session.Done():
	default:
		t.Fatal("failed owner authority live")
	}
	if _, err := p.approve(context.Background(), &terminal{reply: p.phrase + "\n"}); !errors.Is(err, ErrConsumed) {
		t.Fatal("reapproved", err)
	}
	if err := a.RevokeTerminalUnknown(); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupResultObservationIsConcurrentAndDoesNotClose(t *testing.T) {
	p, err := prepare(config(t), &neverRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	a := approve(t, p)
	if err, observed := cleanupObservationFor(t, a).CleanupResult(); err != nil || observed {
		t.Fatal("invented cleanup result")
	}
	select {
	case <-a.session.Done():
		t.Fatal("observation revoked authority")
	default:
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				cleanupObservationFor(t, a).CleanupResult()
			}
		}()
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if err, observed := cleanupObservationFor(t, a).CleanupResult(); err != nil || !observed {
		t.Fatal("completed cleanup not observed")
	}
}

// The assertion uses the real approval path on both sides of the migration.
// A test-only assertion keeps the pre-observer baseline compilable; absence is
// a failure, never a skip or a fabricated completed cleanup.
type retainedCleanup interface{ CleanupResult() (error, bool) }

func cleanupObservationFor(t *testing.T, a *Approved) retainedCleanup {
	t.Helper()
	result, ok := any(a).(retainedCleanup)
	if !ok {
		t.Fatal("retained owner has no cleanup observation")
	}
	return result
}
func TestPartialCleanupRetainsExactCompletedFailureWithoutRetry(t *testing.T) {
	p, err := prepare(config(t), &neverRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	// An incomplete synthetic Session provides a deterministic cleanup failure;
	// this test does not create a command or rely on native cleanup timing.
	a := &Approved{issuer: p.issuer, session: &agentexec.Session{}, failed: true}
	if err, observed := a.CleanupResult(); err != nil || observed {
		t.Fatal("invented cleanup result")
	}
	first := a.Close(context.Background())
	if !errors.Is(first, agentexec.ErrUnconfigured) {
		t.Fatal(first)
	}
	if got, observed := a.CleanupResult(); got != first || !observed {
		t.Fatal("lost exact completed failure")
	}
	if got := a.Close(context.Background()); got != first {
		t.Fatal("retried completed partial cleanup")
	}
	if err := a.RevokeTerminalUnknown(); err != nil {
		t.Fatal(err)
	}
	if got, observed := a.CleanupResult(); got != first || !observed {
		t.Fatal("revoke rewrote cleanup evidence")
	}
}
