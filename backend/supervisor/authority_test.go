package supervisor_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

func approvedGrant() (supervisor.ProvisionedGrant, time.Time) {
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	return supervisor.ProvisionedGrant{
		Binding: supervisor.Binding{
			Generation: 7,
			Identity: supervisor.Identity{
				LaunchID: "approved-launch", Epoch: "approved-epoch",
				PolicyDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			},
		},
		KeySetDigest: [32]byte{1}, GuestBoundary: supervisor.SeparatedNonAdminGuest,
		HardDeadline: now.Add(20 * time.Second), BackendLeaseCeiling: 3 * time.Second,
	}, now
}

// The current-user boundary has its own constructor. The default New keeps
// accepting only the separated guest, and each constructor refuses the other.
func TestCurrentUserBoundaryNeedsItsExplicitConstructor(t *testing.T) {
	g, now := approvedGrant()
	if a, err := supervisor.NewCurrentUser(g, now); err == nil || a != nil {
		t.Fatal("NewCurrentUser accepted a guest-boundary grant")
	}
	g.GuestBoundary = supervisor.CurrentUserNoIsolation
	if a, err := supervisor.New(g, now); err == nil || a != nil {
		t.Fatal("the default New accepted a current-user grant")
	}
	a, err := supervisor.NewCurrentUser(g, now)
	if err != nil {
		t.Fatal(err)
	}
	if s := a.Snapshot(now); s.State != supervisor.Granted || s.Grant.GuestBoundary != supervisor.CurrentUserNoIsolation {
		t.Fatalf("snapshot = %+v", s)
	}
	// Deadline and revoke semantics are unchanged.
	if s := a.Snapshot(g.HardDeadline); s.State != supervisor.Revoked || s.Reason != supervisor.HardDeadlineReached {
		t.Fatalf("expired snapshot = %+v", s)
	}
	b, err := supervisor.NewCurrentUser(g, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Revoke(g.Binding); err != nil || b.Snapshot(now).State != supervisor.Revoked {
		t.Fatalf("revoke = %v", err)
	}
	g.HardDeadline = now
	if a, err := supervisor.NewCurrentUser(g, now); err == nil || a != nil {
		t.Fatal("NewCurrentUser skipped the deadline checks")
	}
}

func TestGrantIsImmutableAcrossInputAndSnapshotChanges(t *testing.T) {
	input, now := approvedGrant()
	original := input
	a, err := supervisor.New(input, now)
	if err != nil {
		t.Fatal(err)
	}
	input.Binding.Generation = 8
	input.KeySetDigest[0] = 2
	input.HardDeadline = now.Add(time.Hour)
	first := a.Snapshot(now)
	if first.State != supervisor.Granted || first.Grant != original {
		t.Fatalf("grant changed: %+v", first)
	}
	first.Grant.KeySetDigest[0] = 3
	first.Grant.BackendLeaseCeiling = time.Hour
	if got := a.Snapshot(now); got.Grant != original {
		t.Fatalf("snapshot mutated authority: %+v", got)
	}
}

func TestOnlyExplicitValidProvisionedGrantsAreAccepted(t *testing.T) {
	for _, name := range []string{"generation-zero", "launch-empty", "epoch-space", "digest-upper", "keys-empty", "guest-unspecified", "guest-unsupported", "now-zero", "deadline-zero", "deadline-past", "deadline-now", "lease-zero", "lease-negative", "lease-equals-grant", "lease-exceeds-grant"} {
		t.Run(name, func(t *testing.T) {
			g, now := approvedGrant()
			switch name {
			case "generation-zero":
				g.Binding.Generation = 0
			case "launch-empty":
				g.Binding.Identity.LaunchID = ""
			case "epoch-space":
				g.Binding.Identity.Epoch = "bad epoch"
			case "digest-upper":
				g.Binding.Identity.PolicyDigest = "ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
			case "keys-empty":
				g.KeySetDigest = [32]byte{}
			case "guest-unspecified":
				g.GuestBoundary = 0
			case "guest-unsupported":
				g.GuestBoundary = 2
			case "now-zero":
				now = time.Time{}
			case "deadline-zero":
				g.HardDeadline = time.Time{}
			case "deadline-past":
				g.HardDeadline = now.Add(-time.Nanosecond)
			case "deadline-now":
				g.HardDeadline = now
			case "lease-zero":
				g.BackendLeaseCeiling = 0
			case "lease-negative":
				g.BackendLeaseCeiling = -time.Nanosecond
			case "lease-equals-grant":
				g.BackendLeaseCeiling = 20 * time.Second
			case "lease-exceeds-grant":
				g.BackendLeaseCeiling = 21 * time.Second
			}
			if a, err := supervisor.New(g, now); err == nil || a != nil {
				t.Fatalf("invalid grant accepted: authority=%v error=%v", a, err)
			}
		})
	}
}

func TestHardDeadlineIsInclusiveAndCannotBeUndoneByPolling(t *testing.T) {
	g, now := approvedGrant()
	a, err := supervisor.New(g, now)
	if err != nil {
		t.Fatal(err)
	}
	for second := 1; second < 20; second++ {
		if got := a.Snapshot(now.Add(time.Duration(second) * time.Second)); got.State != supervisor.Granted || got.Grant != g {
			t.Fatalf("poll changed grant: %+v", got)
		}
	}
	expired := a.Snapshot(g.HardDeadline)
	if expired.State != supervisor.Revoked || expired.Reason != supervisor.HardDeadlineReached {
		t.Fatalf("deadline did not revoke: %+v", expired)
	}
	if got := a.Snapshot(now); got != expired {
		t.Fatalf("old time revived grant: %+v", got)
	}
}

func TestUnusableOrRegressingClockIrrevocablyRevokes(t *testing.T) {
	for _, name := range []string{"unavailable", "regression"} {
		t.Run(name, func(t *testing.T) {
			g, now := approvedGrant()
			a, err := supervisor.New(g, now)
			if err != nil {
				t.Fatal(err)
			}
			a.Snapshot(now.Add(2 * time.Second))
			badTime := time.Time{}
			want := supervisor.ClockUnavailable
			if name == "regression" {
				badTime, want = now.Add(time.Second), supervisor.ClockRegressed
			}
			got := a.Snapshot(badTime)
			if got.State != supervisor.Revoked || got.Reason != want {
				t.Fatalf("unsafe clock kept grant: %+v", got)
			}
			if later := a.Snapshot(now.Add(3 * time.Second)); later != got {
				t.Fatalf("clock recovery revived grant: %+v", later)
			}
		})
	}
}

func TestZeroAuthorityFailsClosed(t *testing.T) {
	_, now := approvedGrant()
	var a supervisor.Authority
	if err := a.Revoke(supervisor.Binding{}); !errors.Is(err, supervisor.ErrDenied) {
		t.Fatalf("zero binding accepted before observation: %v", err)
	}
	if got := a.Snapshot(now); got.State != supervisor.Revoked {
		t.Fatalf("zero authority not revoked: %+v", got)
	}
	if err := a.Revoke(supervisor.Binding{}); !errors.Is(err, supervisor.ErrDenied) {
		t.Fatalf("zero binding accepted after observation: %v", err)
	}
}

func TestRevokeIsBoundToGenerationAndCompleteIdentity(t *testing.T) {
	for _, name := range []string{"generation", "launch", "epoch", "policy"} {
		t.Run(name, func(t *testing.T) {
			g, now := approvedGrant()
			a, err := supervisor.New(g, now)
			if err != nil {
				t.Fatal(err)
			}
			wrong := g.Binding
			switch name {
			case "generation":
				wrong.Generation++
			case "launch":
				wrong.Identity.LaunchID = "another-launch"
			case "epoch":
				wrong.Identity.Epoch = "another-epoch"
			case "policy":
				wrong.Identity.PolicyDigest = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
			}
			if err := a.Revoke(wrong); !errors.Is(err, supervisor.ErrDenied) {
				t.Fatalf("wrong binding accepted: %v", err)
			}
			if got := a.Snapshot(now); got.State != supervisor.Granted {
				t.Fatalf("wrong binding stopped another grant: %+v", got)
			}
			if err := a.Revoke(g.Binding); err != nil {
				t.Fatal(err)
			}
			stopped := a.Snapshot(now)
			if stopped.State != supervisor.Revoked || stopped.Reason != supervisor.ExplicitStop {
				t.Fatalf("stop not terminal: %+v", stopped)
			}
			if err := a.Revoke(g.Binding); err != nil {
				t.Fatalf("duplicate revoke: %v", err)
			}
			if got := a.Snapshot(now.Add(time.Hour)); got != stopped {
				t.Fatalf("terminal reason changed: %+v", got)
			}
		})
	}
}

func TestBackendLeaseMustFitBothExplicitCeilings(t *testing.T) {
	for _, tc := range []struct {
		name           string
		elapsed, until time.Duration
		allowed        bool
	}{
		{"below-short-ceiling", 0, 2 * time.Second, true},
		{"at-short-ceiling", 0, 3 * time.Second, true},
		{"above-short-ceiling", 0, 3*time.Second + time.Nanosecond, false},
		{"not-future", time.Second, time.Second, false},
		{"stale", time.Second, 0, false},
		{"at-hard-ceiling", 19 * time.Second, 20 * time.Second, true},
		{"above-hard-ceiling", 19 * time.Second, 20*time.Second + time.Nanosecond, false},
		{"hard-deadline-already-reached", 20 * time.Second, 21 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, now := approvedGrant()
			a, err := supervisor.New(g, now)
			if err != nil {
				t.Fatal(err)
			}
			err = a.CheckBackendLease(g.Binding, now.Add(tc.until), now.Add(tc.elapsed))
			if tc.allowed && err != nil || !tc.allowed && !errors.Is(err, supervisor.ErrDenied) {
				t.Fatalf("lease decision: allowed=%v error=%v", tc.allowed, err)
			}
			if got := a.Snapshot(now.Add(tc.elapsed)); got.Grant != g {
				t.Fatalf("lease check changed immutable ceiling: %+v", got)
			}
		})
	}
}

func TestConcurrentObservationLeaseChecksAndRevokeRemainTerminal(t *testing.T) {
	g, now := approvedGrant()
	a, err := supervisor.New(g, now)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func(stop bool) {
			defer workers.Done()
			<-start
			for attempt := 0; attempt < 100; attempt++ {
				a.Snapshot(now)
				a.CheckBackendLease(g.Binding, now.Add(time.Second), now)
				if stop {
					if err := a.Revoke(g.Binding); err != nil {
						t.Error(err)
					}
				}
			}
		}(i%4 == 0)
	}
	close(start)
	workers.Wait()
	if got := a.Snapshot(now); got.State != supervisor.Revoked || got.Reason != supervisor.ExplicitStop {
		t.Fatalf("concurrent stop lost: %+v", got)
	}
	if err := a.CheckBackendLease(g.Binding, now.Add(time.Second), now); !errors.Is(err, supervisor.ErrDenied) {
		t.Fatalf("revoked grant permitted another lease: %v", err)
	}
}

func TestBackendLeaseChecksCannotReviveOrChangeBinding(t *testing.T) {
	for _, name := range []string{"generation", "launch", "epoch", "policy", "zero-deadline", "stopped", "expired", "clock-unavailable", "clock-regression", "zero-authority"} {
		t.Run(name, func(t *testing.T) {
			g, now := approvedGrant()
			a, err := supervisor.New(g, now)
			if err != nil {
				t.Fatal(err)
			}
			binding, deadline, checkedAt := g.Binding, now.Add(time.Second), now
			switch name {
			case "generation":
				binding.Generation++
			case "launch":
				binding.Identity.LaunchID = "stale-launch"
			case "epoch":
				binding.Identity.Epoch = "stale-epoch"
			case "policy":
				binding.Identity.PolicyDigest = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
			case "zero-deadline":
				deadline = time.Time{}
			case "stopped":
				if err := a.Revoke(binding); err != nil {
					t.Fatal(err)
				}
			case "expired":
				checkedAt, deadline = g.HardDeadline, now.Add(21*time.Second)
			case "clock-unavailable":
				checkedAt = time.Time{}
			case "clock-regression":
				a.Snapshot(now.Add(time.Second))
			case "zero-authority":
				a, binding = new(supervisor.Authority), supervisor.Binding{}
			}
			if err := a.CheckBackendLease(binding, deadline, checkedAt); !errors.Is(err, supervisor.ErrDenied) {
				t.Fatalf("invalid lease request accepted: %v", err)
			}
			if name == "stopped" || name == "expired" || name == "clock-unavailable" || name == "clock-regression" || name == "zero-authority" {
				if err := a.CheckBackendLease(g.Binding, now.Add(4*time.Second), now.Add(2*time.Second)); !errors.Is(err, supervisor.ErrDenied) {
					t.Fatalf("authority revived: %v", err)
				}
			} else if got := a.Snapshot(now); got.State != supervisor.Granted || got.Grant != g {
				t.Fatalf("invalid request changed another grant: %+v", got)
			}
		})
	}
}
