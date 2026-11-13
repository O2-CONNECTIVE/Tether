package grantstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/grantstore"
)

func TestDurableConsumptionPrecedesAuthorityAndRevocationPrecedesRelease(t *testing.T) {
	ctx := context.Background()
	store := newTempStore(t, grantstore.Ready)
	issuer, err := grantstore.Open(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	g := grant()
	now := g.HardDeadline.Add(-20 * time.Second)
	authority, err := issuer.Authorize(ctx, g, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.commits, []grantstore.Phase{grantstore.Intent, grantstore.Consumed}) {
		t.Fatalf("durable ordering: %v", store.commits)
	}
	if got := authority.Snapshot(now); got.State != supervisor.Granted {
		t.Fatalf("core not used: %+v", got)
	}
	store.beforeRelease = func() {
		if got := authority.Snapshot(now); got.State != supervisor.Revoked {
			t.Fatalf("claim released before revoke: %+v", got)
		}
	}
	if err := issuer.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.commits, []grantstore.Phase{grantstore.Intent, grantstore.Consumed, grantstore.Revoked}) {
		t.Fatalf("revoke not recorded: %v", store.commits)
	}
	if again, err := grantstore.Open(ctx, store); again != nil || !errors.Is(err, grantstore.ErrSpent) {
		t.Fatalf("old slot resumed: %v %v", again, err)
	}
}

func TestDeadlineIsCheckedAfterConsumptionCompletes(t *testing.T) {
	ctx := context.Background()
	store := newTempStore(t, grantstore.Ready)
	issuer, err := grantstore.Open(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	g := grant()
	now := g.HardDeadline.Add(-20 * time.Second)
	store.afterCommit = func(phase grantstore.Phase) {
		if phase == grantstore.Consumed {
			now = g.HardDeadline
		}
	}
	a, err := issuer.Authorize(ctx, g, func() time.Time { return now })
	if a != nil || err == nil {
		t.Fatalf("stale pre-commit clock authorized an expired grant: %v %v", a, err)
	}
}

func TestUnconfiguredAndZeroIssuerFailClosed(t *testing.T) {
	ctx := context.Background()
	if i, err := grantstore.OpenProtected(ctx); i != nil || !errors.Is(err, grantstore.ErrUnconfigured) {
		t.Fatalf("protected store enabled: %v %v", i, err)
	}
	if i, err := grantstore.Open(ctx, nil); i != nil || !errors.Is(err, grantstore.ErrUnconfigured) {
		t.Fatalf("nil store enabled: %v %v", i, err)
	}
	var i grantstore.Issuer
	g := grant()
	if a, err := i.Authorize(ctx, g, func() time.Time { return g.HardDeadline.Add(-20 * time.Second) }); a != nil || !errors.Is(err, grantstore.ErrUnconfigured) {
		t.Fatalf("zero issuer authorize: %v %v", a, err)
	}
	if err := i.Revoke(ctx); !errors.Is(err, grantstore.ErrUnconfigured) {
		t.Fatalf("zero revoke: %v", err)
	}
	if err := i.Close(ctx); !errors.Is(err, grantstore.ErrUnconfigured) {
		t.Fatalf("zero close: %v", err)
	}
}

func TestInterruptedCommitsNeverReturnAuthorityAndKeepWriterClaim(t *testing.T) {
	for _, tc := range []struct {
		name        string
		phase       grantstore.Phase
		after, half bool
	}{
		{"before-intent", grantstore.Intent, false, false}, {"after-intent", grantstore.Intent, true, false}, {"partial-intent", grantstore.Intent, false, true},
		{"before-consumed", grantstore.Consumed, false, false}, {"after-consumed", grantstore.Consumed, true, false}, {"partial-consumed", grantstore.Consumed, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := newTempStore(t, grantstore.Ready)
			store.failPhase = tc.phase
			store.after = tc.after
			store.half = tc.half
			i, err := grantstore.Open(ctx, store)
			if err != nil {
				t.Fatal(err)
			}
			g := grant()
			clock := func() time.Time { return g.HardDeadline.Add(-20 * time.Second) }
			if a, err := i.Authorize(ctx, g, clock); a != nil || !errors.Is(err, grantstore.ErrUnconfirmed) {
				t.Fatalf("interrupted commit issued authority: %v %v", a, err)
			}
			if a, err := i.Authorize(ctx, g, clock); a != nil || err == nil {
				t.Fatalf("poisoned issuer reused: %v %v", a, err)
			}
			if err := i.Close(ctx); !errors.Is(err, grantstore.ErrUnconfirmed) {
				t.Fatalf("unknown commit closed normally: %v", err)
			}
			if another, err := grantstore.Open(ctx, &tempStore{dir: store.dir}); another != nil || !errors.Is(err, grantstore.ErrUnavailable) {
				t.Fatalf("restart ignored persistent claim: %v %v", another, err)
			}
		})
	}
}

func TestSpentCorruptOrMissingRecordsAreNotRecoveredAsNew(t *testing.T) {
	for _, phase := range []grantstore.Phase{grantstore.Intent, grantstore.Consumed, grantstore.Revoked} {
		store := newTempStore(t, phase)
		if i, err := grantstore.Open(context.Background(), store); i != nil || !errors.Is(err, grantstore.ErrSpent) {
			t.Fatalf("phase %v restored: %v %v", phase, i, err)
		}
	}
	for _, name := range []string{"missing", "corrupt", "partial"} {
		t.Run(name, func(t *testing.T) {
			store := newTempStore(t, grantstore.Ready)
			path := filepath.Join(store.dir, "record")
			if name == "missing" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if name == "corrupt" {
					data[10] ^= 1
				} else {
					data = data[:10]
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if i, err := grantstore.Open(context.Background(), store); i != nil || err == nil {
				t.Fatalf("bad record recovered: %v %v", i, err)
			}
			if i, err := grantstore.Open(context.Background(), store); i != nil || !errors.Is(err, grantstore.ErrUnavailable) {
				t.Fatalf("bad read released claim: %v %v", i, err)
			}
		})
	}
}

func TestDuplicateOpenAndConcurrentAuthorizeCannotIssueTwice(t *testing.T) {
	ctx := context.Background()
	store := newTempStore(t, grantstore.Ready)
	i, err := grantstore.Open(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := grantstore.Open(ctx, &tempStore{dir: store.dir}); other != nil || !errors.Is(err, grantstore.ErrUnavailable) {
		t.Fatalf("duplicate open: %v %v", other, err)
	}
	var workers sync.WaitGroup
	results := make(chan *supervisor.Authority, 16)
	g := grant()
	now := g.HardDeadline.Add(-20 * time.Second)
	for n := 0; n < 16; n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			a, err := i.Authorize(ctx, g, func() time.Time { return now })
			if a == nil && !errors.Is(err, grantstore.ErrUsed) {
				t.Errorf("unexpected authorization result: %v", err)
			}
			results <- a
		}()
	}
	workers.Wait()
	close(results)
	var issued *supervisor.Authority
	count := 0
	for a := range results {
		if a != nil {
			issued = a
			count++
		}
	}
	if count != 1 {
		t.Fatalf("issued %d authorities", count)
	}
	for n := 0; n < 16; n++ {
		workers.Add(1)
		go func() { defer workers.Done(); i.Revoke(ctx); i.Close(ctx) }()
	}
	workers.Wait()
	if issued.Snapshot(now).State != supervisor.Revoked {
		t.Fatal("concurrent close left authority valid")
	}
}

func TestFailedRevocationStillRevokesBeforeQuarantine(t *testing.T) {
	for _, mode := range []string{"before", "after", "partial"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store := newTempStore(t, grantstore.Ready)
			i, err := grantstore.Open(ctx, store)
			if err != nil {
				t.Fatal(err)
			}
			g := grant()
			now := g.HardDeadline.Add(-20 * time.Second)
			a, err := i.Authorize(ctx, g, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			store.failPhase = grantstore.Revoked
			store.after = mode == "after"
			store.half = mode == "partial"
			store.beforeQuarantine = func() {
				if a.Snapshot(now).State != supervisor.Revoked {
					t.Error("quarantine preceded memory revoke")
				}
			}
			if err := i.Close(ctx); !errors.Is(err, grantstore.ErrUnconfirmed) {
				t.Fatalf("failed revocation acknowledged clean: %v", err)
			}
			if a.Snapshot(now).State != supervisor.Revoked {
				t.Fatal("failed store revived authority")
			}
			if err := i.Close(ctx); !errors.Is(err, grantstore.ErrUnconfirmed) {
				t.Fatalf("repeated close hid failure: %v", err)
			}
			if other, err := grantstore.Open(ctx, &tempStore{dir: store.dir}); other != nil || err == nil {
				t.Fatalf("quarantine released claim: %v %v", other, err)
			}
		})
	}
}

func TestEveryImmutableGrantFieldIsBoundToTheSlot(t *testing.T) {
	for _, name := range []string{"generation", "launch", "epoch", "policy", "keys", "guest", "hard-cap", "lease"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newTempStore(t, grantstore.Ready)
			i, err := grantstore.Open(ctx, store)
			if err != nil {
				t.Fatal(err)
			}
			defer i.Close(ctx)
			g := grant()
			switch name {
			case "generation":
				g.Binding.Generation--
			case "launch":
				g.Binding.Identity.LaunchID = "another"
			case "epoch":
				g.Binding.Identity.Epoch = "2"
			case "policy":
				g.Binding.Identity.PolicyDigest = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
			case "keys":
				g.KeySetDigest[0]++
			case "guest":
				g.GuestBoundary = 2
			case "hard-cap":
				g.HardDeadline = g.HardDeadline.Add(time.Second)
			case "lease":
				g.BackendLeaseCeiling++
			}
			if a, err := i.Authorize(ctx, g, func() time.Time { return g.HardDeadline.Add(-20 * time.Second) }); a != nil || !errors.Is(err, grantstore.ErrRejected) {
				t.Fatalf("modified grant accepted: %v %v", a, err)
			}
			if len(store.commits) != 0 {
				t.Fatal("mismatched approval changed durable record")
			}
		})
	}
}

type partialAcquireStore struct{ inner *tempStore }

func (s partialAcquireStore) Acquire(ctx context.Context) (grantstore.Lease, error) {
	lease, err := s.inner.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return lease, errors.New("synthetic partial acquire")
}

func TestPartialAcquireQuarantinesItsReturnedLease(t *testing.T) {
	store := newTempStore(t, grantstore.Ready)
	quarantined := false
	store.beforeQuarantine = func() { quarantined = true }
	if i, err := grantstore.Open(context.Background(), partialAcquireStore{store}); i != nil || !errors.Is(err, grantstore.ErrUnavailable) {
		t.Fatalf("partial acquire accepted: %v %v", i, err)
	}
	if !quarantined {
		t.Fatal("returned lease was lost without quarantine")
	}
}

func TestClockFaultAndCancellationDoNotIssueAuthority(t *testing.T) {
	for _, name := range []string{"nil", "zero", "panic", "canceled-after-consume"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := newTempStore(t, grantstore.Ready)
			i, err := grantstore.Open(ctx, store)
			if err != nil {
				t.Fatal(err)
			}
			g := grant()
			var clock func() time.Time
			switch name {
			case "zero":
				clock = func() time.Time { return time.Time{} }
			case "panic":
				clock = func() time.Time { panic("synthetic clock unavailable") }
			case "canceled-after-consume":
				clock = func() time.Time { cancel(); return g.HardDeadline.Add(-20 * time.Second) }
			}
			if a, err := i.Authorize(ctx, g, clock); a != nil || err == nil {
				t.Fatalf("fault issued authority: %v %v", a, err)
			}
			if a, err := i.Authorize(context.Background(), g, func() time.Time { return g.HardDeadline.Add(-20 * time.Second) }); a != nil || err == nil {
				t.Fatalf("failed issuer retried: %v %v", a, err)
			}
			i.Close(context.Background())
		})
	}
}

func TestCoreStillRejectsUnsupportedGuestAfterDurableConsume(t *testing.T) {
	ctx := context.Background()
	store := newTempStore(t, grantstore.Ready)
	g := grant()
	g.GuestBoundary = 2
	r, err := grantstore.InitialRecord(g)
	if err != nil {
		t.Fatal(err)
	}
	data, err := grantstore.EncodeRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, "record"), data, 0600); err != nil {
		t.Fatal(err)
	}
	i, err := grantstore.Open(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if a, err := i.Authorize(ctx, g, func() time.Time { return g.HardDeadline.Add(-20 * time.Second) }); a != nil || !errors.Is(err, grantstore.ErrRejected) {
		t.Fatalf("store bypassed core policy: %v %v", a, err)
	}
	if other, err := grantstore.Open(ctx, store); other != nil || !errors.Is(err, grantstore.ErrUnavailable) {
		t.Fatalf("invalid core grant lost quarantine: %v %v", other, err)
	}
}

func TestRejectedApprovalCannotBecomeFreshAfterCloseAndReopen(t *testing.T) {
	ctx := context.Background()
	store := newTempStore(t, grantstore.Ready)
	i, err := grantstore.Open(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	g := grant()
	g.Binding.Generation--
	if a, err := i.Authorize(ctx, g, func() time.Time { return g.HardDeadline.Add(-20 * time.Second) }); a != nil || !errors.Is(err, grantstore.ErrRejected) {
		t.Fatalf("old generation accepted: %v %v", a, err)
	}
	i.Close(ctx)
	if another, err := grantstore.Open(ctx, &tempStore{dir: store.dir}); another != nil || !errors.Is(err, grantstore.ErrUnavailable) {
		t.Fatalf("rejected attempt returned to fresh state: %v %v", another, err)
	}
}
