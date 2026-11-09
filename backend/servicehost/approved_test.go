package servicehost_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

type approvedPolicyFunc func(context.Context) (ephemeral.ApprovedPolicy, error)

func (f approvedPolicyFunc) ReadApproved(ctx context.Context) (ephemeral.ApprovedPolicy, error) {
	return f(ctx)
}

// processAPI は所有processのOS seam。Session/coreは差し替えない。
type processAPI struct {
	wait  func(context.Context) error
	close func(context.Context) error
}

func (p *processAPI) Wait(ctx context.Context) error  { return p.wait(ctx) }
func (p *processAPI) Close(ctx context.Context) error { return p.close(ctx) }

type launcherFunc func(context.Context, *supervisor.Authority) (servicehost.Process, error)

func (f launcherFunc) Start(ctx context.Context, a *supervisor.Authority) (servicehost.Process, error) {
	return f(ctx, a)
}

func TestApprovedHostRetainsTheIssuerAuthorityAndRevokesBeforeCleanup(t *testing.T) {
	p := ephemeral.ApprovedPolicy{PolicyDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", KeySetDigest: [32]byte{1}, GuestBoundary: supervisor.SeparatedNonAdminGuest, HardDeadline: time.Now().Add(time.Minute), BackendLeaseCeiling: time.Second}
	entered := make(chan *supervisor.Authority, 1)
	waiting := make(chan struct{})
	closed := false
	var borrowed *supervisor.Authority
	h, err := servicehost.WithRuntime(approvedPolicyFunc(func(context.Context) (ephemeral.ApprovedPolicy, error) { return p, nil }), launcherFunc(func(ctx context.Context, a *supervisor.Authority) (servicehost.Process, error) {
		if _, finite := ctx.Deadline(); !finite {
			t.Fatal("startup has no budget")
		}
		borrowed = a
		entered <- a
		return &processAPI{wait: func(ctx context.Context) error { close(waiting); <-ctx.Done(); return ctx.Err() }, close: func(ctx context.Context) error {
			if _, finite := ctx.Deadline(); !finite || ctx.Err() != nil {
				t.Error("cleanup has no independent budget")
			}
			if borrowed.Snapshot(time.Now()).State != supervisor.Revoked {
				t.Error("cleanup preceded core revoke")
			}
			closed = true
			return nil
		}}, nil
	}), servicehost.Budgets{Startup: time.Second, Cleanup: time.Second, Observe: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	var a *supervisor.Authority
	select {
	case a = <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime not entered")
	}
	select {
	case <-waiting:
	case <-time.After(2 * time.Second):
		t.Fatal("owned process not awaited")
	}
	binding := a.Snapshot(time.Now()).Grant.Binding
	if binding.Generation != 1 || binding.Identity.LaunchID == "" {
		t.Fatal("issuer did not mint binding")
	}
	if err := a.Revoke(binding); err != nil {
		t.Fatal(err)
	}
	if snapshot, ok := h.Snapshot(); !ok || snapshot.State != supervisor.Revoked || snapshot.Grant.Binding != binding {
		t.Fatal("Host recreated the issuer authority")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation result: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("host did not clean up")
	}
	if !closed {
		t.Fatal("process ownership was dropped")
	}
}

func TestFailedLaunchCanRetryOnlyOwnedCleanupAndNeverApproval(t *testing.T) {
	p := provisionedPolicy()
	reads, closes := 0, 0
	h, err := servicehost.WithRuntime(approvedPolicyFunc(func(context.Context) (ephemeral.ApprovedPolicy, error) {
		reads++
		return p, nil
	}), launcherFunc(func(_ context.Context, core *supervisor.Authority) (servicehost.Process, error) {
		return &processAPI{wait: func(context.Context) error { t.Error("failed startup was awaited as ready"); return nil }, close: func(context.Context) error {
			closes++
			if core.Snapshot(time.Now()).State != supervisor.Revoked {
				t.Error("partial child cleanup preceded revoke")
			}
			if closes == 1 {
				return errors.New("synthetic unconfirmed cleanup")
			}
			return nil
		}}, errors.New("synthetic partial startup")
	}), servicehost.Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Run(context.Background()); !errors.Is(err, servicehost.ErrRuntimeUnconfirmed) {
		t.Fatalf("partial startup became success: %v", err)
	}
	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.Close(cleanup); err != nil {
		t.Fatalf("owned recovery reference lost: %v", err)
	}
	if err := h.Run(context.Background()); !errors.Is(err, servicehost.ErrAlreadyUsed) {
		t.Fatalf("cleanup revived approval: %v", err)
	}
	if reads != 1 || closes != 2 {
		t.Fatalf("ownership counts: reads=%d closes=%d", reads, closes)
	}
}

func TestCanceledCloseBudgetStillRevokesTheGrant(t *testing.T) {
	waiting := make(chan struct{})
	h, err := servicehost.WithRuntime(approvedPolicyFunc(func(context.Context) (ephemeral.ApprovedPolicy, error) { return provisionedPolicy(), nil }), launcherFunc(func(context.Context, *supervisor.Authority) (servicehost.Process, error) {
		return &processAPI{wait: func(ctx context.Context) error { close(waiting); <-ctx.Done(); return ctx.Err() }, close: func(context.Context) error { return nil }}, nil
	}), servicehost.Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("host did not finish")
		}
	})
	select {
	case <-waiting:
	case <-time.After(2 * time.Second):
		t.Fatal("host did not await runtime")
	}
	budget, end := context.WithCancel(context.Background())
	end()
	if err := h.Close(budget); !errors.Is(err, servicehost.ErrRuntimeUnconfirmed) {
		t.Fatalf("canceled budget claimed cleanup: %v", err)
	}
	if s, ok := h.Snapshot(); !ok || s.State != supervisor.Revoked {
		t.Fatal("canceled cleanup budget retained authorization")
	}
}

// hostDeadlineObserved keeps the timer and authority clock evidence separate.
// This helper is a test oracle only, not a product authorization decision.
func hostDeadlineObserved(runErr error, reason supervisor.Reason, monotonicRemaining, wallRemaining time.Duration) bool {
	switch runErr {
	case context.DeadlineExceeded:
		return monotonicRemaining <= 0
	case context.Canceled:
		return reason == supervisor.HardDeadlineReached && wallRemaining <= 0
	default:
		return false
	}
}

// Synthetic observations fix the test-oracle contract; they do not reproduce
// clock skew on this machine or change either production clock.
func TestHostDeadlineObservationKeepsClockPathsSeparate(t *testing.T) {
	for _, tc := range []struct {
		name            string
		err             error
		reason          supervisor.Reason
		monotonic, wall time.Duration
		want            bool
	}{
		{"wall-first-recorded-CI", context.Canceled, supervisor.HardDeadlineReached, 70048 * time.Nanosecond, -180000 * time.Nanosecond, true},
		{"wall-boundary", context.Canceled, supervisor.HardDeadlineReached, time.Nanosecond, 0, true},
		{"timer-first", context.DeadlineExceeded, supervisor.ExplicitStop, 0, time.Nanosecond, true},
		{"both-elapsed", context.DeadlineExceeded, supervisor.HardDeadlineReached, -time.Nanosecond, -time.Nanosecond, true},
		{"wall-not-reached", context.Canceled, supervisor.HardDeadlineReached, -time.Nanosecond, time.Nanosecond, false},
		{"timer-not-reached", context.DeadlineExceeded, supervisor.HardDeadlineReached, time.Nanosecond, -time.Nanosecond, false},
		{"explicit-cancel", context.Canceled, supervisor.ExplicitStop, -time.Nanosecond, -time.Nanosecond, false},
		{"clock-regression", context.Canceled, supervisor.ClockRegressed, -time.Nanosecond, -time.Nanosecond, false},
		{"normal-exit", nil, supervisor.HardDeadlineReached, -time.Nanosecond, -time.Nanosecond, false},
		{"mixed-context-errors", errors.Join(context.Canceled, context.DeadlineExceeded), supervisor.HardDeadlineReached, -time.Nanosecond, -time.Nanosecond, false},
		{"deadline-plus-cleanup-failure", errors.Join(context.DeadlineExceeded, servicehost.ErrRuntimeUnconfirmed), supervisor.HardDeadlineReached, -time.Nanosecond, -time.Nanosecond, false},
		{"unrelated-error", servicehost.ErrRuntimeUnconfirmed, supervisor.HardDeadlineReached, -time.Nanosecond, -time.Nanosecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostDeadlineObserved(tc.err, tc.reason, tc.monotonic, tc.wall); got != tc.want {
				t.Fatalf("deadline observation = %t, want %t", got, tc.want)
			}
		})
	}
}

// coreの期限判定自体のTDDとは別に、Hostの所有process待機へ届く経路を回帰確認する。
func TestHostHardDeadlineCancelsWaitAndRevokesBeforeProcessCleanup(t *testing.T) {
	p := provisionedPolicy()
	p.HardDeadline = time.Now().Add(500 * time.Millisecond)
	p.BackendLeaseCeiling = 20 * time.Millisecond
	closes := 0
	var cleanupSnapshot supervisor.Snapshot
	h, err := servicehost.WithRuntime(approvedPolicyFunc(func(context.Context) (ephemeral.ApprovedPolicy, error) { return p, nil }), launcherFunc(func(_ context.Context, a *supervisor.Authority) (servicehost.Process, error) {
		return &processAPI{wait: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, close: func(ctx context.Context) error {
			closes++
			cleanupSnapshot = a.Snapshot(time.Now())
			if cleanupSnapshot.State != supervisor.Revoked {
				t.Error("deadline cleanup preceded revoke")
			}
			if ctx.Err() != nil {
				t.Error("expired execution budget reused for cleanup")
			}
			return nil
		}}, nil
	}), servicehost.Budgets{Startup: time.Second, Cleanup: time.Second, Observe: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	runErr := h.Run(context.Background())
	if runErr == nil {
		t.Fatal("deadline became normal runtime exit")
	}
	observed := time.Now()
	monotonicRemaining := p.HardDeadline.Sub(observed)
	wallRemaining := p.HardDeadline.Round(0).Sub(observed.Round(0))
	if !hostDeadlineObserved(runErr, cleanupSnapshot.Reason, monotonicRemaining, wallRemaining) || closes != 1 {
		t.Fatalf("deadline/ownership mismatch: closes=%d revoke_reason=%s execution_deadline=%t execution_canceled=%t monotonic_remaining_ns=%d wall_remaining_ns=%d",
			closes, cleanupSnapshot.Reason, errors.Is(runErr, context.DeadlineExceeded), errors.Is(runErr, context.Canceled),
			monotonicRemaining.Nanoseconds(), wallRemaining.Nanoseconds())
	}
	if s, ok := h.Snapshot(); !ok || s.State != supervisor.Revoked || !s.Grant.HardDeadline.Equal(p.HardDeadline.Round(0)) {
		t.Fatal("hard deadline changed or grant retained")
	}
}

func TestUnconstructedHostCannotResumeOrStart(t *testing.T) {
	var h servicehost.Host
	if err := h.Run(context.Background()); !errors.Is(err, servicehost.ErrProvisioningRejected) {
		t.Fatalf("zero host accepted: %v", err)
	}
	if _, present := h.Snapshot(); present {
		t.Fatal("zero host retained authority")
	}
}

// OS thread の退役が必要な分類は公開境界を跨いで保持し、内部詳細は隠す。
func TestHostPreservesFatalRetirementWithoutForwardingDiagnostics(t *testing.T) {
	for _, fatal := range []error{bootstrapauth.ErrFatal, protectedkey.ErrFatal} {
		for _, phase := range []string{"start", "start-no-process", "start-canceled", "cleanup"} {
			t.Run(fatal.Error()+"/"+phase, func(t *testing.T) {
				private := errors.New("private-runtime-diagnostic")
				var authority *supervisor.Authority
				h, err := servicehost.WithRuntime(approvedPolicyFunc(func(context.Context) (ephemeral.ApprovedPolicy, error) {
					return provisionedPolicy(), nil
				}), launcherFunc(func(ctx context.Context, a *supervisor.Authority) (servicehost.Process, error) {
					authority = a
					p := &processAPI{wait: func(context.Context) error { return nil }, close: func(context.Context) error {
						if phase == "cleanup" {
							return errors.Join(fatal, private)
						}
						return private
					}}
					if phase == "cleanup" {
						return p, nil
					}
					if phase == "start-no-process" {
						return nil, errors.Join(fatal, private)
					}
					if phase == "start-canceled" {
						<-ctx.Done()
					}
					return p, errors.Join(fatal, private)
				}), servicehost.Budgets{Startup: time.Millisecond, Cleanup: time.Second, Observe: time.Millisecond})
				if err != nil {
					t.Fatal(err)
				}
				err = h.Run(context.Background())
				if !errors.Is(err, servicehost.ErrRuntimeUnconfirmed) || !errors.Is(err, fatal) {
					t.Fatalf("fatal retirement classification lost: %v", err)
				}
				if errors.Is(err, private) || strings.Contains(err.Error(), private.Error()) {
					t.Fatalf("private runtime diagnostic leaked: %v", err)
				}
				if authority == nil || authority.Snapshot(time.Now()).State != supervisor.Revoked {
					t.Fatal("fatal runtime retained a grant")
				}
				cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if phase == "cleanup" && !errors.Is(h.Close(cleanup), fatal) {
					t.Fatal("explicit cleanup lost fatal retirement classification")
				}
			})
		}
	}
}
