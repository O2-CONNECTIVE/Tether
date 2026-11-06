package residentmac

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
)

// Only idle control lifecycle is faked. No receiver/native grant is forged.
type idleConnector struct {
	done      chan struct{}
	gate      chan struct{}
	once      sync.Once
	closes    atomic.Int32
	closeErr  error
	connected atomic.Bool
	hadAuth   atomic.Bool
}

func (c *idleConnector) Run(ctx context.Context) error {
	<-ctx.Done()
	c.once.Do(func() { close(c.done) })
	return ctx.Err()
}
func (c *idleConnector) Accept(ctx context.Context) (*agenttransport.ResidentSourceLease, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (c *idleConnector) Stop() { c.once.Do(func() { close(c.done) }) }
func (c *idleConnector) Close(ctx context.Context) error {
	c.closes.Add(1)
	select {
	case <-c.gate:
		return c.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *idleConnector) Snapshot() agenttransport.ResidentControlSnapshot {
	v := agenttransport.ResidentControlSnapshot{Connected: c.connected.Load()}
	if v.Connected || c.hadAuth.Load() {
		v.Generation = "fake-authenticated-history-display-only"
	}
	return v
}
func idleStandby(t *testing.T) (*Standby, *idleConnector, *atomic.Int32) {
	t.Helper()
	s := newStandby()
	c := &idleConnector{done: make(chan struct{}), gate: make(chan struct{})}
	calls := &atomic.Int32{}
	s.makeConnector = func(context.Context) (residentConnector, error) { calls.Add(1); return c, nil }
	t.Cleanup(func() {
		select {
		case <-c.gate:
		default:
			close(c.gate)
		}
		ctx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = s.Close(ctx)
	})
	return s, c, calls
}
func eventually(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not complete")
}
func TestStandbyOnRequiresObservedAuthenticationAndOffOriginalJoin(t *testing.T) {
	s, c, calls := idleStandby(t)
	on := s.RequestOn(1, s.Snapshot().Revision)
	if on.Disposition != "accepted" {
		t.Fatal(on)
	}
	if o := s.Snapshot(); o.Transition.Outcome != "pending" || o.Registration != "connecting" {
		t.Fatal(o)
	}
	c.connected.Store(true)
	eventually(t, func() bool {
		o := s.Snapshot()
		return o.Registration == "authenticated" && o.Transition.Outcome == "applied"
	})
	old := s.cycle
	off := s.RequestOff(2, s.Snapshot().Revision)
	if off.Disposition != "accepted" {
		t.Fatal(off)
	}
	eventually(t, func() bool { return c.closes.Load() == 1 })
	o := s.Snapshot()
	if o.Standby != "stopping" || o.Transition.Outcome != "pending" || !o.AdmissionSealed || o.CanRequestOn {
		t.Fatal(o)
	}
	if r := s.RequestOn(3, o.Revision); r.Disposition != "rejected" {
		t.Fatal("replaced before original join", r)
	}
	close(c.gate)
	eventually(t, func() bool { return s.Snapshot().Standby == "off" })
	select {
	case <-old.done:
	default:
		t.Fatal("OFF published before original join")
	}
	if o = s.Snapshot(); o.Transition.RequestID != 2 || o.Transition.Outcome != "applied" || o.Cleanup != "confirmed" || !o.CanRequestOn {
		t.Fatal(o)
	}
	if r := s.RequestOn(4, o.Revision); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	if s.cycle.owner == old.owner {
		t.Fatal("reused sealed common owner")
	}
	eventually(t, func() bool { return calls.Load() == 2 })
}
func TestStandbyUnknownRetainsOriginalAndNeverRetries(t *testing.T) {
	s, c, calls := idleStandby(t)
	c.closeErr = errors.New("synthetic original cleanup failure")
	close(c.gate)
	s.RequestOn(1, s.Snapshot().Revision)
	s.RequestOff(2, s.Snapshot().Revision)
	eventually(t, func() bool { return s.Snapshot().Standby == "unknown" })
	original := s.cycle
	if o := s.Snapshot(); o.CanRequestOn || o.Cleanup != "unknown" || o.Transition.Outcome != "unknown" {
		t.Fatal(o)
	}
	if r := s.RequestOn(3, s.Snapshot().Revision); r.Disposition != "unknown" {
		t.Fatal(r)
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if e := s.Close(ctx); !errors.Is(e, c.closeErr) {
		t.Fatal(e)
	}
	if s.cycle != original || calls.Load() != 1 || c.closes.Load() != 1 {
		t.Fatal("unknown retried/replaced originals")
	}
}
func TestStandbyInitialAuthenticationEndsPendingOnWithoutServiceLifetime(t *testing.T) {
	s, c, _ := idleStandby(t)
	close(c.gate)
	s.RequestOn(1, s.Snapshot().Revision)
	s.mu.Lock()
	s.cycle.initialUntil = time.Now().Add(-time.Second)
	s.mu.Unlock()
	eventually(t, func() bool { return s.Snapshot().Standby == "off" })
	o := s.Snapshot()
	if o.Transition.Intent != "on" || o.Transition.Outcome != "rejected" || o.Registration != "disconnected" || !o.CanRequestOn {
		t.Fatal(o)
	}
}
func TestStandbyCloseDeadlineDoesNotPublishOffOrRetry(t *testing.T) {
	s, c, _ := idleStandby(t)
	s.RequestOn(1, s.Snapshot().Revision)
	eventually(t, func() bool { s.cycle.mu.Lock(); defer s.cycle.mu.Unlock(); return s.cycle.connector != nil })
	ctx, stop := context.WithTimeout(context.Background(), time.Millisecond)
	defer stop()
	if e := s.Close(ctx); !errors.Is(e, ErrStandbyUnknown) {
		t.Fatal(e)
	}
	if o := s.Snapshot(); o.Standby != "stopping" || o.CanRequestOn || !o.AdmissionSealed {
		t.Fatal(o)
	}
	close(c.gate)
	ctx2, stop2 := context.WithTimeout(context.Background(), time.Second)
	defer stop2()
	if e := s.Close(ctx2); e != nil {
		t.Fatal(e)
	}
	if c.closes.Load() != 1 || s.Snapshot().CanRequestOn {
		t.Fatal("terminal Close retried/reopened")
	}
}
func TestStandbyIntentBoundsAndSnapshotCopies(t *testing.T) {
	s, _, _ := idleStandby(t)
	for _, id := range []uint64{0, maxStandbyInteger + 1} {
		if r := s.RequestOn(id, s.Snapshot().Revision); r.Disposition != "stale" {
			t.Fatal(r)
		}
	}
	if r := s.RequestOn(1, 0); r.Disposition != "stale" {
		t.Fatal(r)
	}
	s.RequestOn(1, s.Snapshot().Revision)
	o := s.Snapshot()
	o.Transition.Outcome = "applied"
	if s.Snapshot().Transition.Outcome != "pending" {
		t.Fatal("snapshot mutated authoritative state")
	}
	if r := s.RequestOff(1, s.Snapshot().Revision); r.Disposition != "stale" {
		t.Fatal(r)
	}
}

func TestStandbyShortLivedAuthenticationHistoryDoesNotExpireResidency(t *testing.T) {
	s, c, _ := idleStandby(t)
	// Fake lifecycle snapshot models a completed authentication that disconnected
	// before the first monitor poll; this does not mint a receiver or grant.
	c.hadAuth.Store(true)
	s.RequestOn(1, s.Snapshot().Revision)
	s.mu.Lock()
	s.cycle.initialUntil = time.Now().Add(-time.Second)
	s.mu.Unlock()
	eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.cycle.authenticated })
	if o := s.Snapshot(); o.Standby != "on" || o.AdmissionSealed || o.Transition.Outcome != "applied" {
		t.Fatal("initial ON timer became resident lifetime", o)
	}
}
