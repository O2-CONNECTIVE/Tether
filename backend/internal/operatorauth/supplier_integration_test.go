package operatorauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// These tests replace only the native supplier, SCM dispatcher, launch witness
// and Batch kernel. Passing them is not real Windows SCM, token or ACL success.

func supplierImageValue(parent bootstrapauth.Profile, c HelperSupplierConfig) helperLaunchFixed {
	return helperLaunchFixed{parentService: parent.ServiceName, supplierService: c.ServiceName, parentImage: parent.SupervisorImage.Path,
		helperImage: c.Image.Path, helperServiceSID: c.ServiceSID, parentServiceSID: parent.Keys.ServiceSID, runBudget: c.RunBudget, cleanup: c.Cleanup}
}

func withSupplierImageValue(t *testing.T, v helperLaunchFixed) {
	t.Helper()
	prior := protectedHelperLaunch
	protectedHelperLaunch = v
	t.Cleanup(func() { protectedHelperLaunch = prior })
}

type launchBoundProfile struct {
	*memoryBatchProfile
	id string
}

func (p launchBoundProfile) launchID() string { return p.id }

type supplierEntryFixture struct {
	calls    int
	name     string
	lifetime time.Duration
}

func (f *supplierEntryFixture) run(_ context.Context, name string, lifetime time.Duration) error {
	f.calls++
	f.name, f.lifetime = name, lifetime
	return nil
}

func TestSupplierFactoryOpensOnlyForEqualImageValue(t *testing.T) {
	parent, p, c := supplierConfigFixture()
	f, err := NewSIDEndpointFactoryWithSupplier(new(bootstrapauth.Factory), 17, p.OperatorSID, p, parent, c)
	if err != nil {
		t.Fatal(err)
	}
	_, owner, _, _, _, _ := batchFixture(t)
	_, k := supplierFixtureLease(t)
	if f.RuntimeConfigured() {
		t.Fatal("unset image value enabled supplier")
	}
	if _, err = f.supplierBatchSpec(k); !errors.Is(err, ErrUnconfigured) {
		t.Fatal(err)
	}
	if b, err := f.Prepare(new(serviceready.SoleInput), owner.owner, owner.issuer); b != nil || !errors.Is(err, ErrUnconfigured) {
		t.Fatal("unset image value reached preparation", err)
	}
	for name, mutate := range map[string]func(*helperLaunchFixed){
		"parent service":   func(v *helperLaunchFixed) { v.parentService = "OtherSupervisor" },
		"parent image":     func(v *helperLaunchFixed) { v.parentImage = `C:\other.exe` },
		"parent SID":       func(v *helperLaunchFixed) { v.parentServiceSID = "S-1-5-80-21-22-23-24-25" },
		"supplier service": func(v *helperLaunchFixed) { v.supplierService = "OtherSupplier" },
		"helper image":     func(v *helperLaunchFixed) { v.helperImage = `C:\other-helper.exe` },
		"helper SID":       func(v *helperLaunchFixed) { v.helperServiceSID = "S-1-5-80-31-32-33-34-35" },
		"run budget":       func(v *helperLaunchFixed) { v.runBudget = 2 * time.Second },
		"cleanup":          func(v *helperLaunchFixed) { v.cleanup = 2 * time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			v := supplierImageValue(parent, c)
			mutate(&v)
			withSupplierImageValue(t, v)
			if f.RuntimeConfigured() {
				t.Fatal("image value unequal to the Record enabled supplier")
			}
			if _, err := f.supplierBatchSpec(k); !errors.Is(err, ErrUnconfigured) {
				t.Fatal(err)
			}
		})
	}
	withSupplierImageValue(t, supplierImageValue(parent, c))
	if !f.RuntimeConfigured() {
		t.Fatal("equal protected values stayed disabled")
	}
	if k.acquires != 0 {
		t.Fatal("configuration acquired a supplier")
	}
}

func TestSupplierFactoryComposesOneRequestOwnedBatchAndReclaimsAfterChild(t *testing.T) {
	parent, p, c := supplierConfigFixture()
	withSupplierImageValue(t, supplierImageValue(parent, c))
	f, err := NewSIDEndpointFactoryWithSupplier(new(bootstrapauth.Factory), 17, p.OperatorSID, p, parent, c)
	if err != nil {
		t.Fatal(err)
	}
	_, k := supplierFixtureLease(t)
	first, err := f.supplierBatchSpec(k)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.supplierBatchSpec(k)
	if err != nil || first.supplier == second.supplier || first.launch.launchID == second.launch.launchID || !profileLaunchID(first.launch.launchID) {
		t.Fatal("requests shared a lease or launch", err)
	}
	input := new(serviceready.SoleInput)
	s, err := soleInputSIDSpec(first, input, p)
	if err != nil || !s.valid() || s.launch.primaryToken != 0 || s.report != (reportConfig{"S-1-5-18", parent.Keys.ServiceSID, c.ServiceSID, p.GuestSID, c.Cleanup}) {
		t.Fatal("Record values did not compose the SID Batch", err)
	}
	_, owner, ops, events, _, _ := batchFixture(t)
	k.events = events
	ops.moveSoleInput = func(_ context.Context, got *serviceready.SoleInput) (uintptr, error) {
		if got != input {
			t.Error("typed source replaced")
		}
		return 31, nil
	}
	ops.newProfile = func(ctx context.Context) (batchProfile, error) {
		if _, _, err := prepareProfilePipe(ctx, s.producer); err != nil {
			return nil, err
		}
		*events = append(*events, "profile")
		return launchBoundProfile{&memoryBatchProfile{events: events}, s.launch.launchID}, nil
	}
	start := ops.start
	ops.start = func(ctx context.Context, l nativeLaunchSpec) (batchChild, error) {
		// The child is created only with the borrowed generation's token.
		if l.primaryToken != 43 || l.launchID != s.launch.launchID || l.imagePath != c.Image.Path || l.helperUserSID != supplierLocalService || l.helperServiceSID != c.ServiceSID {
			return nil, ErrDenied
		}
		return start(ctx, l)
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil || k.acquires != 0 {
		t.Fatal("preparation acquired a supplier", err)
	}
	_, other, _, _, _, _ := batchFixture(t)
	if _, err = prepareBatch(s, other, ops); !errors.Is(err, ErrUnconfigured) {
		t.Fatal("request lease moved to a second owner", err)
	}
	if err = b.Start(requestContext(t)); err != nil || k.acquires != 1 {
		t.Fatal(err, k.acquires)
	}
	if err = b.Close(requestContext(t)); err != nil || !supplierCleanupCounts(k, 1, 1, 1) || !supplierReturnedAfterChild(*events) {
		t.Fatal("same owner did not reclaim the generation after the child", err, *events)
	}
	if err = b.Start(requestContext(t)); err == nil || k.acquires != 1 {
		t.Fatal("closed Batch acquired again", err)
	}
}

// A fatal owner never resumes native work. Its started generation is stopped by
// the supplier's compiled lifetime, not by the retired owner or a new owner.
func TestSupplierBatchFatalRetirementLeavesStopToFiniteSupplierLifetime(t *testing.T) {
	b, k, _, _, _ := supplierBatchFixture(t, nil)
	b.ops.parent = func(context.Context) error {
		if k.acquires == 1 {
			return errors.Join(ErrFatal, errors.New("synthetic thread retirement"))
		}
		return nil
	}
	if err := b.Start(requestContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal(err)
	}
	for range 2 {
		if err := b.Close(requestContext(t)); !errors.Is(err, ErrFatal) || !supplierCleanupCounts(k, 0, 0, 0) {
			t.Fatal("native supplier work resumed on a retired owner", err)
		}
	}
	if b.spec.supplier.token() != 0 {
		t.Fatal("retired owner can still lend the supplier token")
	}
}

func TestSupplierLeaseBudgetEndsBorrowingWithoutReclaiming(t *testing.T) {
	s, f, owner := supplierOwnedLease(t)
	if err := s.acquire(requestContext(t), owner); err != nil || s.token() != 43 {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.expires = time.Now().Add(-time.Nanosecond)
	s.mu.Unlock()
	if s.token() != 0 || !errors.Is(s.recheck(requestContext(t), owner), ErrDenied) || !supplierCleanupCounts(f, 0, 0, 0) {
		t.Fatal("expired generation lent its token or was reclaimed without the owner")
	}
	if err := s.close(requestContext(t), owner); err != nil || !supplierCleanupCounts(f, 1, 1, 1) {
		t.Fatal(err)
	}
}

func TestHelperProcessRolesUseOnlyCompiledValue(t *testing.T) {
	witness, factory := launchSourceFixture(t)
	k := &supplierEntryFixture{}
	for _, args := range [][]string{{supplierEntryArg}, entryArgs()} {
		if err := runHelperProcess(requestContext(t), args, helperLaunchFixed{}, witness, k, factory); !errors.Is(err, ErrUnconfigured) {
			t.Fatal("unset image value opened a role", err)
		}
	}
	if err := runNativeFixedHelper(requestContext(t), []string{supplierEntryArg}); !errors.Is(err, ErrUnconfigured) {
		t.Fatal("default image opened the supplier role", err)
	}
	if witness.acquired != 0 || k.calls != 0 {
		t.Fatal("unconfigured role reached native work")
	}
	// argv selects the role only; it cannot name the Service or the lifetime.
	for _, args := range [][]string{{supplierEntryArg, "OtherSupplier"}, {supplierEntryArg, "--lifetime=1h"}} {
		if err := runHelperProcess(requestContext(t), args, launchFixed(), witness, k, factory); !errors.Is(err, ErrDenied) || k.calls != 0 || witness.acquired != 0 {
			t.Fatal("supplier argv was accepted", args, err)
		}
	}
	if err := runHelperProcess(requestContext(t), []string{supplierEntryArg}, launchFixed(), witness, nil, factory); !errors.Is(err, ErrUnconfigured) {
		t.Fatal(err)
	}
	if err := runHelperProcess(requestContext(t), []string{supplierEntryArg}, launchFixed(), witness, k, factory); err != nil ||
		k.calls != 1 || k.name != "RescueAuthSupplier" || k.lifetime != 1100*time.Millisecond || witness.acquired != 0 {
		t.Fatal("supplier role did not use the compiled Service and finite lifetime", err, k)
	}
	if err := runHelperProcess(requestContext(t), entryArgs(), launchFixed(), witness, k, factory); err != nil || witness.acquired != 1 || witness.closed != 1 || k.calls != 1 {
		t.Fatal("helper role did not run one bound entry", err)
	}
}

func TestSupplierEntryEndsAtStopLifetimeOrCancelOnly(t *testing.T) {
	serve := func(ctx context.Context, lifetime time.Duration, requests <-chan int) (running int, handled []int) {
		serveSupplierEntry(ctx, lifetime, requests, func() { running++ }, func(r int) bool {
			handled = append(handled, r)
			return r == 1
		})
		return running, handled
	}
	// Interrogations are answered by the handler but neither stop nor extend.
	requests := make(chan int, 3)
	requests <- 0
	requests <- 0
	requests <- 1
	if running, handled := serve(requestContext(t), time.Minute, requests); running != 1 || len(handled) != 3 {
		t.Fatal("stop request was not the only exit", running, handled)
	}
	// With no owner left to stop it, the compiled lifetime ends the generation.
	began := time.Now()
	if running, handled := serve(context.Background(), 20*time.Millisecond, make(chan int)); running != 1 || len(handled) != 0 || time.Since(began) < 20*time.Millisecond {
		t.Fatal("supplier lifetime was not finite or ended early")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if running, _ := serve(cancelled, time.Minute, make(chan int)); running != 1 {
		t.Fatal(running)
	}
	closed := make(chan int)
	close(closed)
	if running, _ := serve(requestContext(t), time.Minute, closed); running != 1 {
		t.Fatal(running)
	}
}
