package ephemeral_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

func TestNewIssuerHasFreshEpochAndNoGrant(t *testing.T) {
	first, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	second, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	a, b := first.Snapshot(), second.Snapshot()
	if a.InstanceEpoch == ([32]byte{}) || b.InstanceEpoch == ([32]byte{}) || a.InstanceEpoch == b.InstanceEpoch {
		t.Fatal("issuer epoch is absent or reused")
	}
	if a.Generation != 0 || b.Generation != 0 || a.HasAuthority || b.HasAuthority {
		t.Fatal("startup restored authorization")
	}
}

func policy() ephemeral.ApprovedPolicy {
	return ephemeral.ApprovedPolicy{PolicyDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", KeySetDigest: [32]byte{1}, GuestBoundary: supervisor.SeparatedNonAdminGuest, HardDeadline: time.Date(2026, 10, 28, 12, 0, 20, 0, time.UTC), BackendLeaseCeiling: 3 * time.Second}
}

// The current-user issuer is a separate explicit path: it refuses a guest
// policy before consuming a generation, and the default issuer still cannot
// launch a current-user policy. Epoch, single use and revoke are unchanged.
func TestCurrentUserIssuerIsExplicitAndSeparate(t *testing.T) {
	p := policy()
	now := p.HardDeadline.Add(-20 * time.Second)
	clock := func() time.Time { return now }
	launch := func(context.Context, *supervisor.Authority) error { return nil }

	cu, err := ephemeral.NewCurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	defer cu.Close()
	def, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	defer def.Close()
	if cu.Boundary() != supervisor.CurrentUserNoIsolation || def.Boundary() != supervisor.SeparatedNonAdminGuest {
		t.Fatalf("boundaries = %d / %d", cu.Boundary(), def.Boundary())
	}
	if cu.Snapshot().InstanceEpoch == def.Snapshot().InstanceEpoch {
		t.Fatal("issuer epoch reused")
	}
	if _, err := cu.Prepare(p); !errors.Is(err, ephemeral.ErrDenied) || cu.Snapshot().Generation != 0 {
		t.Fatalf("current-user issuer accepted a guest policy: %v, generation %d", err, cu.Snapshot().Generation)
	}

	p.GuestBoundary = supervisor.CurrentUserNoIsolation
	c, err := def.Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	if a, err := def.Launch(context.Background(), c, clock, launch); a != nil || err == nil {
		t.Fatal("default issuer launched a current-user grant")
	}

	c, err = cu.Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	a, err := cu.Launch(context.Background(), c, clock, launch)
	if err != nil || a.Grant().GuestBoundary != supervisor.CurrentUserNoIsolation {
		t.Fatalf("current-user launch: %v", err)
	}
	if again, err := cu.Launch(context.Background(), c, clock, launch); again != nil || !errors.Is(err, ephemeral.ErrConsumed) {
		t.Fatalf("approval reused: %v", err)
	}
	if got, err := cu.Lookup(c.Binding(), clock); err != nil || got != a {
		t.Fatalf("lookup = %v", err)
	}
	if err := cu.Revoke(c.Binding()); err != nil {
		t.Fatal(err)
	}
	if _, err := cu.Lookup(c.Binding(), clock); !errors.Is(err, ephemeral.ErrDenied) || a.Snapshot(now).State != supervisor.Revoked {
		t.Fatalf("revoked grant still current: %v", err)
	}
}

func TestChallengeIsConsumedBeforeLaunchAndUsesTheRealCore(t *testing.T) {
	i, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	p := policy()
	now := p.HardDeadline.Add(-20 * time.Second)
	clock := func() time.Time { return now }
	c, err := i.Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Binding().Generation != 1 || !c.Binding().Identity.Valid() {
		t.Fatalf("bad owned binding: %+v", c.Binding())
	}
	launches := 0
	a, err := i.Launch(context.Background(), c, clock, func(ctx context.Context, a *supervisor.Authority) error {
		launches++
		if !i.Snapshot().Consumed {
			t.Error("launch ran before single-use burn")
		}
		if got := a.Snapshot(now); got.State != supervisor.Granted || got.Grant.Binding != c.Binding() {
			t.Errorf("core/binding mismatch: %+v", got)
		}
		if duplicate, err := i.Launch(ctx, c, clock, func(context.Context, *supervisor.Authority) error { launches++; return nil }); duplicate != nil || !errors.Is(err, ephemeral.ErrConsumed) {
			t.Errorf("reentrant launch reused approval: %v", err)
		}
		return nil
	})
	if err != nil || a == nil || launches != 1 {
		t.Fatalf("launch result: %v %v count=%d", a, err, launches)
	}
	if duplicate, err := i.Launch(context.Background(), c, clock, func(context.Context, *supervisor.Authority) error { launches++; return nil }); duplicate != nil || !errors.Is(err, ephemeral.ErrConsumed) {
		t.Fatalf("approval reused: %v", err)
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	if a.Snapshot(now).State != supervisor.Revoked {
		t.Fatal("lifetime close retained issued authority")
	}
}

func TestUncertainLaunchRemainsBurnedAfterRevoke(t *testing.T) {
	i, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	p := policy()
	c, err := i.Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return p.HardDeadline.Add(-20 * time.Second) }
	var issued *supervisor.Authority
	a, err := i.Launch(context.Background(), c, clock, func(_ context.Context, a *supervisor.Authority) error {
		issued = a
		return errors.New("synthetic unknown launch")
	})
	if a != nil || !errors.Is(err, ephemeral.ErrLaunchUnconfirmed) || issued == nil {
		t.Fatalf("uncertain launch accepted: %v %v", a, err)
	}
	if issued.Snapshot(clock()).State != supervisor.Revoked {
		t.Fatal("uncertain launch retained authority")
	}
	if err := i.Revoke(c.Binding()); err != nil {
		t.Fatal(err)
	}
	if next, err := i.Prepare(p); err == nil {
		t.Fatalf("unknown launch reset to reusable state: %+v", next.Binding())
	}
}

func launch(t *testing.T, i *ephemeral.Issuer, p ephemeral.ApprovedPolicy) (ephemeral.Challenge, *supervisor.Authority) {
	t.Helper()
	c, err := i.Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	a, err := i.Launch(context.Background(), c, func() time.Time { return p.HardDeadline.Add(-20 * time.Second) }, func(context.Context, *supervisor.Authority) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return c, a
}

func TestRestartRejectsOldApprovalAndCallbackWithoutChangingWireShape(t *testing.T) {
	old, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	p := policy()
	clock := func() time.Time { return p.HardDeadline.Add(-20 * time.Second) }
	c, a := launch(t, old, p)
	oldBinding := c.Binding()
	old.Close()
	if a.Snapshot(clock()).State != supervisor.Revoked {
		t.Fatal("old lifetime retained authority")
	}
	fresh, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if current, err := fresh.Lookup(oldBinding, clock); current != nil || err == nil {
		t.Fatal("startup accepted old callback")
	}
	if current, err := fresh.Launch(context.Background(), c, clock, func(context.Context, *supervisor.Authority) error { t.Error("old approval launched"); return nil }); current != nil || err == nil {
		t.Fatal("old challenge entered fresh lifetime")
	}
	next, _ := launch(t, fresh, p)
	binding := next.Binding()
	if binding.Generation != oldBinding.Generation || binding.Identity == oldBinding.Identity {
		t.Fatal("full binding did not isolate equal per-boot generation")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`).MatchString(binding.Identity.LaunchID) {
		t.Fatal("LaunchID exceeds existing wire shape")
	}
	instance := fresh.Snapshot().InstanceEpoch
	if !strings.HasPrefix(binding.Identity.LaunchID, hex.EncodeToString(instance[:])+"-") {
		t.Fatal("LaunchID lacks owning namespace")
	}
	epoch, err := strconv.ParseUint(binding.Identity.Epoch, 10, 64)
	if err != nil || strconv.FormatUint(epoch, 10) != binding.Identity.Epoch {
		t.Fatal("session Epoch is not existing canonical uint64")
	}
	if current, err := fresh.Lookup(oldBinding, clock); current != nil || err == nil {
		t.Fatal("old callback matched fresh generation")
	}
	if current, err := fresh.Lookup(binding, clock); current == nil || err != nil {
		t.Fatal("current known binding unavailable")
	}
}

func TestSavedUIStateAndZeroChallengeCannotRestorePermission(t *testing.T) {
	i, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	p := policy()
	c, _ := launch(t, i, p)
	data, err := json.Marshal(i.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var restored ephemeral.Issuer
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Prepare(p); err == nil {
		t.Fatal("saved state restored an issuer")
	}
	ticket, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var replay ephemeral.Challenge
	if err := json.Unmarshal(ticket, &replay); err != nil {
		t.Fatal(err)
	}
	if a, err := i.Launch(context.Background(), replay, func() time.Time { return p.HardDeadline.Add(-20 * time.Second) }, func(context.Context, *supervisor.Authority) error {
		t.Error("serialized challenge launched")
		return nil
	}); a != nil || err == nil {
		t.Fatal("serialized ticket restored permission")
	}
}

func TestRevokeKeepsOldCallbacksOutOfTheNextGeneration(t *testing.T) {
	i, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	p := policy()
	c, a := launch(t, i, p)
	clock := func() time.Time { return p.HardDeadline.Add(-20 * time.Second) }
	wrong := c.Binding()
	wrong.Identity.Epoch = "different"
	if err := i.Revoke(wrong); err == nil {
		t.Fatal("wrong identity revoked current grant")
	}
	if a.Snapshot(clock()).State != supervisor.Granted {
		t.Fatal("foreign revoke changed grant")
	}
	if err := i.Revoke(c.Binding()); err != nil {
		t.Fatal(err)
	}
	next, _ := launch(t, i, p)
	if next.Binding().Generation != 2 {
		t.Fatal("generation did not advance")
	}
	if got, err := i.Lookup(c.Binding(), clock); got != nil || err == nil {
		t.Fatal("old callback revived after new approval")
	}
}

func TestPolicyIsFrozenAndPollingDoesNotExtendHardDeadline(t *testing.T) {
	i, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	p := policy()
	original := p
	now := p.HardDeadline.Add(-20 * time.Second)
	clock := func() time.Time { return now }
	c, err := i.Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	p.HardDeadline = p.HardDeadline.Add(time.Hour)
	p.KeySetDigest[0]++
	a, err := i.Launch(context.Background(), c, clock, func(context.Context, *supervisor.Authority) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for second := 1; second < 20; second++ {
		now = original.HardDeadline.Add(time.Duration(second-20) * time.Second)
		if _, err := i.Lookup(c.Binding(), clock); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.Snapshot(now); got.Grant.HardDeadline != original.HardDeadline || got.Grant.KeySetDigest != original.KeySetDigest {
		t.Fatal("policy changed after prepare")
	}
	now = original.HardDeadline
	if got, err := i.Lookup(c.Binding(), clock); got != nil || err == nil {
		t.Fatal("poll extended hard deadline")
	}
	if a.Snapshot(now).State != supervisor.Revoked {
		t.Fatal("deadline did not irrevocably revoke")
	}
}

func TestConcurrentChallengeConsumptionHasOneSideEffect(t *testing.T) {
	i, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	p := policy()
	c, err := i.Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	var calls, success atomic.Int32
	for n := 0; n < 16; n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			a, err := i.Launch(context.Background(), c, func() time.Time { return p.HardDeadline.Add(-20 * time.Second) }, func(context.Context, *supervisor.Authority) error { calls.Add(1); return nil })
			if a != nil && err == nil {
				success.Add(1)
			} else if !errors.Is(err, ephemeral.ErrConsumed) {
				t.Errorf("unexpected duplicate result: %v", err)
			}
		}()
	}
	workers.Wait()
	if calls.Load() != 1 || success.Load() != 1 {
		t.Fatalf("calls=%d success=%d", calls.Load(), success.Load())
	}
}

func TestCloseCancelsInFlightLaunchWithoutClaimingItStopped(t *testing.T) {
	i, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	p := policy()
	c, err := i.Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	entered, canceled, release := make(chan *supervisor.Authority, 1), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, err := i.Launch(context.Background(), c, func() time.Time { return p.HardDeadline.Add(-20 * time.Second) }, func(ctx context.Context, a *supervisor.Authority) error {
			entered <- a
			<-ctx.Done()
			close(canceled)
			<-release
			return nil
		})
		done <- err
	}()
	var a *supervisor.Authority
	select {
	case a = <-entered:
	case <-time.After(2 * time.Second):
		i.Close()
		t.Fatal("launch not entered")
	}
	if err := i.Close(); !errors.Is(err, ephemeral.ErrLaunchUnconfirmed) {
		t.Fatalf("inflight close claimed completion: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("close did not cancel callback")
	}
	if a.Snapshot(p.HardDeadline.Add(-20*time.Second)).State != supervisor.Revoked {
		t.Fatal("close left grant valid")
	}
	select {
	case <-done:
		t.Fatal("callback completion was invented")
	default:
	}
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled launch succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("launch did not finish")
	}
}

func TestFailedLaunchesBurnApprovalAndNeverPublishAuthority(t *testing.T) {
	for _, name := range []string{"nil-context", "canceled", "nil-clock", "clock-panic", "expired", "unsupported-guest", "callback-error", "callback-panic", "expires-during-launch"} {
		t.Run(name, func(t *testing.T) {
			i, err := ephemeral.New()
			if err != nil {
				t.Fatal(err)
			}
			defer i.Close()
			p := policy()
			now := p.HardDeadline.Add(-20 * time.Second)
			if name == "unsupported-guest" {
				p.GuestBoundary = 2
			}
			c, err := i.Prepare(p)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			clock := func() time.Time { return now }
			switch name {
			case "nil-context":
				ctx = nil
			case "canceled":
				cctx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cctx
			case "nil-clock":
				clock = nil
			case "clock-panic":
				clock = func() time.Time { panic("synthetic clock") }
			case "expired":
				now = p.HardDeadline
			}
			var borrowed *supervisor.Authority
			a, err := i.Launch(ctx, c, clock, func(_ context.Context, a *supervisor.Authority) error {
				borrowed = a
				switch name {
				case "callback-error":
					return errors.New("synthetic unknown")
				case "callback-panic":
					panic("synthetic launch")
				case "expires-during-launch":
					now = p.HardDeadline
				}
				return nil
			})
			if a != nil || err == nil {
				t.Fatalf("failed launch authorized: %v %v", a, err)
			}
			if borrowed != nil && borrowed.Snapshot(p.HardDeadline.Add(-time.Second)).State != supervisor.Revoked {
				t.Fatal("failed callback retained authority")
			}
			if a, err := i.Launch(context.Background(), c, func() time.Time { return p.HardDeadline.Add(-20 * time.Second) }, func(context.Context, *supervisor.Authority) error { t.Error("burned challenge launched"); return nil }); a != nil || !errors.Is(err, ephemeral.ErrConsumed) {
				t.Fatalf("failed attempt reused: %v", err)
			}
		})
	}
}

func TestCancellationDuringClockReadDoesNotInvokeLaunch(t *testing.T) {
	i, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	p := policy()
	c, err := i.Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	a, err := i.Launch(ctx, c, func() time.Time { cancel(); return p.HardDeadline.Add(-20 * time.Second) }, func(context.Context, *supervisor.Authority) error { calls++; return nil })
	if a != nil || err == nil || calls != 0 {
		t.Fatalf("canceled context reached side effect: authority=%v error=%v calls=%d", a, err, calls)
	}
}

func TestCancellationDuringFinalClockReadRevokesAndRemainsFailed(t *testing.T) {
	i, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	p := policy()
	c, err := i.Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := p.HardDeadline.Add(-20 * time.Second)
	reads, launches := 0, 0
	var borrowed *supervisor.Authority
	a, err := i.Launch(ctx, c, func() time.Time {
		reads++
		if reads == 2 {
			cancel()
		}
		return now
	}, func(_ context.Context, core *supervisor.Authority) error {
		launches++
		borrowed = core
		return nil
	})
	if a != nil || !errors.Is(err, ephemeral.ErrLaunchUnconfirmed) || reads != 2 || launches != 1 {
		t.Fatalf("final clock cancellation accepted: authority=%v error=%v reads=%d launches=%d", a, err, reads, launches)
	}
	if borrowed == nil || borrowed.Snapshot(now).State != supervisor.Revoked || i.Snapshot().HasAuthority {
		t.Fatal("final clock cancellation retained authority")
	}
	if _, err := i.Lookup(c.Binding(), func() time.Time { return now }); !errors.Is(err, ephemeral.ErrDenied) {
		t.Fatalf("canceled binding remained available: %v", err)
	}
	if _, err := i.Launch(context.Background(), c, func() time.Time { return now }, func(context.Context, *supervisor.Authority) error {
		t.Fatal("consumed challenge launched again")
		return nil
	}); !errors.Is(err, ephemeral.ErrConsumed) {
		t.Fatalf("canceled challenge became reusable: %v", err)
	}
	if err := i.Revoke(c.Binding()); err != nil {
		t.Fatal(err)
	}
	if _, err := i.Prepare(p); !errors.Is(err, ephemeral.ErrBusy) {
		t.Fatalf("canceled launch became fresh after revoke: %v", err)
	}
}

func TestPostLaunchExpiryDoesNotBecomeFreshAfterRevoke(t *testing.T) {
	i, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	defer i.Close()
	p := policy()
	c, err := i.Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	now := p.HardDeadline.Add(-20 * time.Second)
	a, err := i.Launch(context.Background(), c, func() time.Time { return now }, func(context.Context, *supervisor.Authority) error { now = p.HardDeadline; return nil })
	if a != nil || err == nil {
		t.Fatal("expired post-launch result accepted")
	}
	if err := i.Revoke(c.Binding()); err != nil {
		t.Fatal(err)
	}
	if next, err := i.Prepare(p); err == nil {
		t.Fatalf("post-launch failure became fresh: %+v", next.Binding())
	}
}

func TestPerBootGenerationRefusesWrap(t *testing.T) {
	for _, tc := range []struct{ previous, want uint64 }{{0, 1}, {math.MaxUint64 - 1, math.MaxUint64}} {
		got, err := ephemeral.NextGeneration(tc.previous)
		if err != nil || got != tc.want {
			t.Fatalf("generation advance: %d %v", got, err)
		}
	}
	if got, err := ephemeral.NextGeneration(math.MaxUint64); got != 0 || !errors.Is(err, ephemeral.ErrExhausted) {
		t.Fatalf("generation wrapped: %d %v", got, err)
	}
}

func TestIssuerCloseDoesNotResetOrReuseItsEpoch(t *testing.T) {
	i, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	before := i.Snapshot()
	copy := before
	copy.InstanceEpoch[0] ^= 1
	if i.Snapshot().InstanceEpoch != before.InstanceEpoch {
		t.Fatal("snapshot mutated issuer epoch")
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	after := i.Snapshot()
	if !after.Closed || after.InstanceEpoch != before.InstanceEpoch || after.HasAuthority {
		t.Fatalf("close changed epoch or retained permission: %+v", after)
	}
}

func TestTrySnapshotRefusesBusyIssuerAndObservesActualLifecycle(t *testing.T) {
	var missing *ephemeral.Issuer
	if snapshot, ok := missing.TrySnapshot(); ok || snapshot != (ephemeral.Snapshot{}) {
		t.Fatal("nil issuer supplied observation")
	}
	issuer, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	initial, ok := issuer.TrySnapshot()
	if !ok || initial.InstanceEpoch == ([32]byte{}) || initial.Generation != 0 || initial.Closed || initial.HasAuthority || initial.Consumed {
		t.Fatal("new issuer observation changed", initial, ok)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p := policy()
	challenge, err := issuer.Prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var enter sync.Once
	joined := make(chan error, 1)
	go func() {
		_, err := issuer.Launch(ctx, challenge, func() time.Time {
			enter.Do(func() { close(entered) })
			select {
			case <-release:
			case <-ctx.Done():
			}
			return p.HardDeadline.Add(-20 * time.Second)
		}, func(context.Context, *supervisor.Authority) error { return nil })
		joined <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("issuer clock did not enter memory barrier")
	}
	type observation struct {
		snapshot ephemeral.Snapshot
		ok       bool
	}
	result := make(chan observation, 1)
	go func() { snapshot, ok := issuer.TrySnapshot(); result <- observation{snapshot, ok} }()
	bounded := true
	var busy observation
	select {
	case busy = <-result:
	case <-time.After(100 * time.Millisecond):
		bounded = false
	}
	close(release)
	if !bounded {
		busy = <-result
	}
	select {
	case err := <-joined:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("issuer memory worker did not join")
	}
	if !bounded || busy.ok || busy.snapshot != (ephemeral.Snapshot{}) {
		t.Fatal("busy issuer observation blocked or fabricated state", bounded, busy)
	}
	active, ok := issuer.TrySnapshot()
	if !ok || active.InstanceEpoch != initial.InstanceEpoch || active.Generation != 1 || !active.HasAuthority || !active.Consumed || active.Closed {
		t.Fatal("actual launch observation lost", active, ok)
	}
	if err := issuer.Close(); err != nil {
		t.Fatal(err)
	}
	closed, ok := issuer.TrySnapshot()
	if !ok || closed.InstanceEpoch != initial.InstanceEpoch || !closed.Closed || closed.HasAuthority || closed.Generation != 1 {
		t.Fatal("actual close observation lost", closed, ok)
	}
}
