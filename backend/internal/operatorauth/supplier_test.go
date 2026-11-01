package operatorauth

import (
	"context"
	"errors"
	"testing"
	"time"
)

type supplierFixture struct {
	events                                      *[]string
	snapshot                                    supplierSnapshot
	started                                     supplierGeneration
	borrowed                                    uintptr
	acquires, inspects, releases, stops, closes int
	acquireErr, releaseErr, stopErr, closeErr   error
	onAcquire, onInspect                        func()
}

func (f *supplierFixture) record(event string) {
	if f.events != nil {
		*f.events = append(*f.events, event)
	}
}
func (f *supplierFixture) acquire(context.Context, HelperSupplierConfig) (supplierEvidence, error) {
	f.acquires++
	f.record("supplier-start")
	if f.onAcquire != nil {
		f.onAcquire()
	}
	return f, f.acquireErr
}
func (f *supplierFixture) generation() supplierGeneration { return f.started }
func (f *supplierFixture) inspect(context.Context) (supplierSnapshot, error) {
	f.inspects++
	if f.onInspect != nil {
		f.onInspect()
	}
	return cloneSupplierSnapshot(f.snapshot), nil
}
func (f *supplierFixture) token() uintptr { return f.borrowed }
func (f *supplierFixture) release(context.Context) error {
	f.releases++
	f.record("supplier-release")
	return f.releaseErr
}
func (f *supplierFixture) stop(context.Context) error {
	f.stops++
	f.record("supplier-stop")
	return f.stopErr
}
func (f *supplierFixture) close(context.Context) error {
	f.closes++
	f.record("supplier-close")
	return f.closeErr
}

// supplierSnapshotFixture is generation {123, 456} observed with c's values.
func supplierSnapshotFixture(c HelperSupplierConfig) supplierSnapshot {
	return supplierSnapshot{pid: 123, creation: 456, tokenID: 789, modifiedID: 12, serviceType: 0x10, startType: 3, state: 4, sidType: 1, serviceAccount: `NT AUTHORITY\LocalService`, commandLine: c.ServiceCommandLine, imagePath: c.Image.Path, primary: tokenProjection{kind: 1, user: "S-1-5-19", authentication: 99, session: 0, integrity: 0x4000}, group: helperServiceGroup{c.ServiceSID, 7}, serviceSecurity: c.ServiceSecurity, processSecurity: c.ProcessSecurity, tokenSecurity: c.TokenSecurity}
}

func supplierFixtureLease(t *testing.T) (*supplierLease, *supplierFixture) {
	t.Helper()
	parent, p, c := supplierConfigFixture()
	f := &supplierFixture{borrowed: 43, started: supplierGeneration{pid: 123, creation: 456}, snapshot: supplierSnapshotFixture(c)}
	s, err := newSupplierLease(parent, p, c, f)
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}

// The lease-level owner is an unprepared Batch used only as identity.
func supplierOwnedLease(t *testing.T) (*supplierLease, *supplierFixture, *nativeBatch) {
	t.Helper()
	s, f := supplierFixtureLease(t)
	owner := &nativeBatch{}
	if !s.bind(owner) {
		t.Fatal("fresh lease refused its first owner")
	}
	return s, f, owner
}

func supplierCleanupCounts(f *supplierFixture, release, stop, closed int) bool {
	return f.releases == release && f.stops == stop && f.closes == closed
}

func TestSupplierLeaseKeepsSingleAcquisitionAndActualToken(t *testing.T) {
	s, k, owner := supplierOwnedLease(t)
	if k.acquires != 0 || s.token() != 0 {
		t.Fatal("pure constructor acquired token")
	}
	if err := s.acquire(requestContext(t), owner); err != nil {
		t.Fatal(err)
	}
	if s.token() != 43 || k.acquires != 1 {
		t.Fatal("actual token was not borrowed")
	}
	if err := s.acquire(requestContext(t), owner); err == nil || k.acquires != 1 {
		t.Fatal("second acquisition")
	}
	if err := s.recheck(requestContext(t), owner); err != nil {
		t.Fatal(err)
	}
	if err := s.close(requestContext(t), owner); err != nil || s.token() != 0 || !supplierCleanupCounts(k, 1, 1, 1) {
		t.Fatal("same owner failed close", err)
	}
	if err := s.recheck(requestContext(t), owner); !errors.Is(err, ErrDenied) {
		t.Fatal("closed lease revived", err)
	}
	if err := s.close(requestContext(t), owner); err != nil || !supplierCleanupCounts(k, 1, 1, 1) {
		t.Fatal("completed cleanup repeated native work", err)
	}
}

func TestSupplierLeaseRejectsDuplicateAcquisitionAndForeignOwner(t *testing.T) {
	s, k, owner := supplierOwnedLease(t)
	foreign := &nativeBatch{}
	if s.bind(foreign) || s.bind(owner) {
		t.Fatal("bound lease accepted another binding")
	}
	if err := s.acquire(requestContext(t), foreign); err == nil || k.acquires != 0 {
		t.Fatal("foreign owner acquired", err)
	}
	if err := s.acquire(requestContext(t), owner); err != nil {
		t.Fatal(err)
	}
	if err := s.acquire(requestContext(t), owner); err == nil || k.acquires != 1 {
		t.Fatal("duplicate acquisition reached native supplier", err)
	}
	if err := s.recheck(requestContext(t), foreign); err == nil {
		t.Fatal("foreign owner rechecked")
	}
	if err := s.close(requestContext(t), foreign); err == nil || !supplierCleanupCounts(k, 0, 0, 0) || s.token() != 43 {
		t.Fatal("foreign owner changed the borrowed lease", err)
	}
	if err := s.recheck(requestContext(t), owner); err != nil {
		t.Fatal("foreign calls revoked the owner", err)
	}
	if err := s.close(requestContext(t), owner); err != nil || !supplierCleanupCounts(k, 1, 1, 1) {
		t.Fatal(err)
	}
	if s.bind(foreign) {
		t.Fatal("closed lease moved to another owner")
	}
}

func TestSupplierLeaseRejectsGenerationMismatchAndReclaimsOwnEvidence(t *testing.T) {
	for _, name := range []string{"pid", "creation", "zero"} {
		t.Run(name, func(t *testing.T) {
			s, k, owner := supplierOwnedLease(t)
			switch name {
			case "pid":
				k.started.pid++
			case "creation":
				k.started.creation++
			case "zero":
				k.started = supplierGeneration{}
				k.snapshot.pid, k.snapshot.creation = 0, 0
			}
			if err := s.acquire(requestContext(t), owner); !errors.Is(err, ErrDenied) || s.token() != 0 {
				t.Fatal("unowned generation was borrowed", err)
			}
			if err := s.recheck(requestContext(t), owner); err == nil {
				t.Fatal("rejected generation revived")
			}
			if err := s.close(requestContext(t), owner); err != nil || !supplierCleanupCounts(k, 1, 1, 1) {
				t.Fatal("started evidence was not reclaimed by its owner", err)
			}
		})
	}
}

// After this owner requested a start, only the generation pinned during
// acquisition is stopped or counted. A later RUNNING PID, including another
// owner's replacement, is never adopted or stopped and confirms nothing.
func TestSupplierStopDecisionNeverAdoptsLateForeignGeneration(t *testing.T) {
	own := supplierGeneration{pid: 123, creation: 456}
	for name, tc := range map[string]struct {
		started    supplierGeneration
		state, pid uint32
		exited     bool
		want       supplierStopStep
	}{
		"unpinned late foreign running": {supplierGeneration{}, supplierRunning, 777, false, supplierStopUnowned},
		"unpinned start pending":        {supplierGeneration{}, 2, 0, false, supplierStopUnowned},
		"unpinned stopped":              {supplierGeneration{}, supplierStopped, 0, true, supplierStopUnowned},
		"pid without creation":          {supplierGeneration{pid: 123}, supplierRunning, 123, false, supplierStopUnowned},
		"pinned then foreign running":   {own, supplierRunning, 777, true, supplierStopUnowned},
		"pinned then foreign pending":   {own, 2, 777, true, supplierStopUnowned},
		"pinned running":                {own, supplierRunning, 123, false, supplierStopRequest},
		"pinned stop pending":           {own, 3, 123, false, supplierStopWait},
		"stopped before held exit":      {own, supplierStopped, 0, false, supplierStopWait},
		"pinned exited and stopped":     {own, supplierStopped, 0, true, supplierStopConfirmed},
	} {
		t.Run(name, func(t *testing.T) {
			if got := supplierStopDecision(tc.started, tc.state, tc.pid, tc.exited); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestSupplierLeaseRejectsDriftAndNeverRefreshesBaseline(t *testing.T) {
	for _, field := range []string{"pid", "creation", "generation", "token", "modified", "sid", "account", "kind", "start-type", "recovery", "status", "service-acl", "process-acl", "token-acl", "path", "handle"} {
		t.Run(field, func(t *testing.T) {
			s, k, owner := supplierOwnedLease(t)
			if err := s.acquire(requestContext(t), owner); err != nil {
				t.Fatal(err)
			}
			original, started := cloneSupplierSnapshot(k.snapshot), k.started
			switch field {
			case "pid":
				k.snapshot.pid++
			case "creation":
				k.snapshot.creation++
			case "generation":
				k.started.creation++
			case "token":
				k.snapshot.tokenID++
			case "modified":
				k.snapshot.modifiedID++
			case "sid":
				k.snapshot.group.attributes = 0x10
			case "account":
				k.snapshot.serviceAccount = "LocalSystem"
			case "kind":
				k.snapshot.serviceType = 0x20
			case "start-type":
				k.snapshot.startType = 2
			case "recovery":
				k.snapshot.failureActions = 1
			case "status":
				k.snapshot.state = 1
			case "service-acl":
				k.snapshot.serviceSecurity.ACL = nil
			case "process-acl":
				k.snapshot.processSecurity.ACL = nil
			case "token-acl":
				k.snapshot.tokenSecurity.ACL = nil
			case "path":
				k.snapshot.imagePath = `C:\foreign.exe`
			case "handle":
				k.borrowed++
			}
			if err := s.recheck(requestContext(t), owner); err == nil || s.token() != 0 {
				t.Fatal("changed supplier accepted")
			}
			k.snapshot, k.started, k.borrowed = original, started, 43
			if err := s.recheck(requestContext(t), owner); err == nil {
				t.Fatal("failed lease revived")
			}
			if err := s.close(requestContext(t), owner); err != nil || !supplierCleanupCounts(k, 1, 1, 1) {
				t.Fatal(err)
			}
		})
	}
}

func TestSupplierLeaseRevokedBeforeBorrowingNeverLendsToken(t *testing.T) {
	t.Run("before-acquire", func(t *testing.T) {
		s, k, owner := supplierOwnedLease(t)
		s.revoke()
		if err := s.acquire(requestContext(t), owner); err == nil || k.acquires != 0 {
			t.Fatal("revoked lease reached native supplier", err)
		}
		if err := s.close(requestContext(t), owner); err != nil || !supplierCleanupCounts(k, 0, 0, 0) {
			t.Fatal("unacquired lease invented cleanup", err)
		}
	})
	t.Run("during-acquire", func(t *testing.T) {
		s, k, owner := supplierOwnedLease(t)
		k.onAcquire = s.revoke
		if err := s.acquire(requestContext(t), owner); !errors.Is(err, ErrDenied) || s.token() != 0 || k.inspects != 0 {
			t.Fatal("revoked acquisition lent its token", err)
		}
		if err := s.close(requestContext(t), owner); err != nil || !supplierCleanupCounts(k, 1, 1, 1) {
			t.Fatal("late started evidence was lost", err)
		}
	})
	t.Run("after-borrow", func(t *testing.T) {
		s, k, owner := supplierOwnedLease(t)
		if err := s.acquire(requestContext(t), owner); err != nil {
			t.Fatal(err)
		}
		s.revoke()
		if s.token() != 0 || s.recheck(requestContext(t), owner) == nil {
			t.Fatal("revocation left borrowing open")
		}
		if !supplierCleanupCounts(k, 0, 0, 0) {
			t.Fatal("revocation alone released or stopped the supplier")
		}
		if err := s.close(requestContext(t), owner); err != nil || !supplierCleanupCounts(k, 1, 1, 1) {
			t.Fatal(err)
		}
	})
}

func TestSupplierLeaseCancelledDuringAcquisitionRetainsLateEvidence(t *testing.T) {
	s, k, owner := supplierOwnedLease(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	k.onAcquire = cancel
	if err := s.acquire(ctx, owner); !errors.Is(err, ErrDenied) || s.token() != 0 || k.inspects != 0 {
		t.Fatal("cancelled acquisition was accepted", err)
	}
	if err := s.close(ctx, owner); !errors.Is(err, ErrUnconfirmed) || !supplierCleanupCounts(k, 0, 0, 0) {
		t.Fatal("expired context confirmed cleanup", err)
	}
	if err := s.close(requestContext(t), owner); err != nil || !supplierCleanupCounts(k, 1, 1, 1) {
		t.Fatal("late evidence was not reclaimed by the same owner", err)
	}
}

func TestSupplierLeaseCleanupFailureResumesAtFailedPhase(t *testing.T) {
	s, k, owner := supplierOwnedLease(t)
	if err := s.acquire(requestContext(t), owner); err != nil {
		t.Fatal(err)
	}
	k.releaseErr = ErrUnconfirmed
	if err := s.close(requestContext(t), owner); err == nil || !supplierCleanupCounts(k, 1, 0, 0) || s.token() != 0 {
		t.Fatal("release failure advanced to stop", err)
	}
	k.releaseErr, k.stopErr = nil, ErrUnconfirmed
	if err := s.close(requestContext(t), owner); err == nil || !supplierCleanupCounts(k, 2, 1, 0) {
		t.Fatal("stop failure treated as stopped", err)
	}
	k.stopErr, k.closeErr = nil, ErrUnconfirmed
	if err := s.close(requestContext(t), owner); err == nil || !supplierCleanupCounts(k, 2, 2, 1) {
		t.Fatal("confirmed phases were repeated or close failure hidden", err)
	}
	k.closeErr = nil
	if err := s.close(requestContext(t), owner); err != nil || !supplierCleanupCounts(k, 2, 2, 2) {
		t.Fatal("same-owner retry failed", err)
	}
}

func TestSupplierLeasePartialErrorIsOwnedAndCleanupFailureRetained(t *testing.T) {
	s, k, owner := supplierOwnedLease(t)
	k.acquireErr, k.closeErr = ErrDenied, ErrUnconfirmed
	if err := s.acquire(requestContext(t), owner); err == nil || s.token() != 0 {
		t.Fatal("partial accepted", err)
	}
	if err := s.close(requestContext(t), owner); err == nil || !supplierCleanupCounts(k, 1, 1, 1) {
		t.Fatal("cleanup falsely completed", err)
	}
	k.closeErr = nil
	if err := s.close(requestContext(t), owner); err != nil || !supplierCleanupCounts(k, 1, 1, 2) {
		t.Fatal("partial owner lost", err)
	}
}

// supplierLiveChild keeps the held helper unexited until the test releases it.
type supplierLiveChild struct {
	batchChild
	alive *bool
}

func (c *supplierLiveChild) waitExit(ctx context.Context) (uint32, error) {
	if *c.alive {
		return 0, ErrUnconfirmed
	}
	return c.batchChild.waitExit(ctx)
}
func (c *supplierLiveChild) close(ctx context.Context) error {
	if *c.alive {
		return ErrUnconfirmed
	}
	return c.batchChild.close(ctx)
}

func supplierBatchFixture(t *testing.T, wrap func(batchChild) batchChild) (*Batch, *supplierFixture, *[]string, *memoryBatchChild, batchOwner) {
	t.Helper()
	s, o, ops, events, _, child := batchFixture(t)
	lease, k := supplierFixtureLease(t)
	k.events = events
	s.supplier = lease
	s.launch.primaryToken = 0
	s.launch.helperUserSID = supplierLocalService
	s.launch.helperServiceSID = lease.config.ServiceSID
	s.report.helperSID = lease.config.ServiceSID
	s.launch.imagePath = lease.config.Image.Path
	s.launch.imageSHA256 = lease.config.Image.SHA256
	s.launch.maxImageBytes = lease.config.Image.MaxBytes
	s.launch.processSDDL = lease.config.ChildProcessSDDL
	s.launch.threadSDDL = lease.config.ChildThreadSDDL
	start := ops.start
	ops.start = func(ctx context.Context, l nativeLaunchSpec) (batchChild, error) {
		if l.primaryToken != 43 {
			return nil, ErrDenied
		}
		c, err := start(ctx, l)
		if wrap != nil && c != nil {
			c = wrap(c)
		}
		return c, err
	}
	b, err := prepareBatch(s, o, ops)
	if err != nil {
		t.Fatal(err)
	}
	return b, k, events, child, o
}

func supplierEventIndex(events []string, want string) int {
	for i, e := range events {
		if e == want {
			return i
		}
	}
	return -1
}

// The supplier is returned only after the held child and its I/O are closed.
func supplierReturnedAfterChild(events []string) bool {
	child := supplierEventIndex(events, "close-child")
	release := supplierEventIndex(events, "supplier-release")
	stop := supplierEventIndex(events, "supplier-stop")
	closed := supplierEventIndex(events, "supplier-close")
	return child >= 0 && child < release && release < stop && stop < closed
}

func TestSupplierBatchAcquiresAfterPrepareAndClosesAfterChild(t *testing.T) {
	b, k, events, child, _ := supplierBatchFixture(t, nil)
	if k.acquires != 0 {
		t.Fatal("Prepare acquired supplier")
	}
	if err := b.Start(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	child.closeErr = ErrUnconfirmed
	if err := b.Close(requestContext(t)); err == nil || !supplierCleanupCounts(k, 0, 0, 0) {
		t.Fatal("supplier returned while child unconfirmed", err)
	}
	child.closeErr, k.stopErr = nil, ErrUnconfirmed
	if err := b.Close(requestContext(t)); err == nil || !supplierCleanupCounts(k, 1, 1, 0) {
		t.Fatal("supplier stop failure confirmed cleanup", err)
	}
	k.stopErr = nil
	if err := b.Close(requestContext(t)); err != nil || !supplierCleanupCounts(k, 1, 2, 1) {
		t.Fatal("supplier cleanup retry lost owner", err)
	}
	if !supplierReturnedAfterChild(*events) {
		t.Fatal(*events)
	}
}

func TestSupplierBatchRejectsLeaseReuseAndSecondStart(t *testing.T) {
	b, k, _, _, _ := supplierBatchFixture(t, nil)
	_, other, _, _, _, _ := batchFixture(t)
	if _, err := prepareBatch(b.spec, other, b.ops); !errors.Is(err, ErrUnconfigured) {
		t.Fatal("bound lease was prepared for a second owner", err)
	}
	if err := b.Start(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(requestContext(t)); err == nil || k.acquires != 1 {
		t.Fatal("second Start reached the supplier", err)
	}
	if err := b.Close(requestContext(t)); err != nil || !supplierCleanupCounts(k, 1, 1, 1) {
		t.Fatal(err)
	}
}

func TestSupplierBatchGenerationDriftStopsBeforeHelperStart(t *testing.T) {
	b, k, events, child, _ := supplierBatchFixture(t, nil)
	k.onInspect = func() {
		if k.inspects == 2 {
			k.snapshot.creation++
		}
	}
	if err := b.Start(requestContext(t)); err == nil || child.resumeCalled || supplierEventIndex(*events, "suspended") >= 0 {
		t.Fatal("drifted generation launched the helper", err, *events)
	}
	if b.spec.supplier.token() != 0 || !supplierCleanupCounts(k, 1, 1, 1) {
		t.Fatal("drifted generation was not reclaimed by the same Batch")
	}
}

func TestSupplierBatchCancelledLateAcquireRetainsPartialOwnerForRetry(t *testing.T) {
	b, k, _, child, _ := supplierBatchFixture(t, nil)
	k.events = nil
	entered, release := make(chan struct{}), make(chan struct{})
	k.onAcquire = func() { close(entered); <-release }
	k.stopErr = ErrUnconfirmed
	done := make(chan error, 1)
	go func() { done <- b.Start(requestContext(t)) }()
	<-entered
	ctx, cancel := context.WithCancel(requestContext(t))
	cancel()
	if err := b.Close(ctx); err == nil {
		close(release)
		<-done
		t.Fatal("late owner was discarded", err)
	}
	close(release)
	if err := <-done; err == nil || child.resumeCalled {
		t.Fatal("cancelled supplier started child", err)
	}
	if !supplierCleanupCounts(k, 1, 1, 0) || b.spec.supplier.token() != 0 {
		t.Fatal("partial supplier not retained")
	}
	k.stopErr = nil
	if err := b.Close(requestContext(t)); err != nil || !supplierCleanupCounts(k, 1, 2, 1) {
		t.Fatal("same-owner retry failed", err)
	}
}

func TestSupplierBatchRevokedBeforeStartDoesNotAcquire(t *testing.T) {
	b, k, _, _, o := supplierBatchFixture(t, nil)
	if err := o.owner.Revoke(); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(requestContext(t)); err == nil || k.acquires != 0 {
		t.Fatal("revoked owner acquired", err)
	}
}

func TestSupplierBatchRevokedAfterBorrowingReclaimsAfterChild(t *testing.T) {
	b, k, events, _, o := supplierBatchFixture(t, nil)
	if err := b.Start(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	lease := b.spec.supplier
	if lease.token() != 43 {
		t.Fatal("token was not borrowed")
	}
	if err := o.owner.Revoke(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Collect(requestContext(t)); err == nil {
		t.Fatal("revoked owner collected")
	}
	if lease.token() != 0 || !supplierCleanupCounts(k, 1, 1, 1) || !supplierReturnedAfterChild(*events) {
		t.Fatal("revoked supplier was not returned after the child", *events)
	}
	if err := b.Close(requestContext(t)); err != nil || !supplierCleanupCounts(k, 1, 1, 1) {
		t.Fatal("confirmed cleanup repeated", err)
	}
}

// Collection after the lending budget would let the child's result outlive the
// authority that lent its token; the same owner still reclaims afterwards.
func TestSupplierBatchCollectEndsAtLendingBudget(t *testing.T) {
	b, k, events, _, _ := supplierBatchFixture(t, nil)
	if err := b.Start(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	lease := b.spec.supplier
	if end := lease.borrowEnd(); end.IsZero() || !end.Equal(lease.expires) {
		t.Fatal("lending lease did not expose its budget end")
	}
	lease.mu.Lock()
	lease.expires = time.Now().Add(-time.Nanosecond)
	lease.mu.Unlock()
	if r, err := b.Collect(requestContext(t)); r != nil || err == nil {
		t.Fatal("collected after the lending budget", err)
	}
	if lease.token() != 0 || !supplierCleanupCounts(k, 1, 1, 1) || !supplierReturnedAfterChild(*events) {
		t.Fatal("expired collection was not reclaimed by the same owner after the child", *events)
	}
	if err := b.Close(requestContext(t)); err != nil || !supplierCleanupCounts(k, 1, 1, 1) {
		t.Fatal("confirmed cleanup repeated", err)
	}
}

type supplierRegistrarFixture struct {
	registers, removes, closes       int
	registerErr, removeErr, closeErr error
	panics, nilHeld                  bool
}

func (f *supplierRegistrarFixture) register(context.Context, HelperSupplierConfig) (supplierRegistered, error) {
	f.registers++
	if f.panics {
		panic("synthetic registrar fault")
	}
	if f.nilHeld {
		return nil, f.registerErr
	}
	return f, f.registerErr
}
func (f *supplierRegistrarFixture) remove(context.Context) error {
	f.removes++
	return f.removeErr
}
func (f *supplierRegistrarFixture) close(context.Context) error {
	f.closes++
	return f.closeErr
}

func supplierRegistrationCounts(f *supplierRegistrarFixture, register, remove, closed int) bool {
	return f.registers == register && f.removes == remove && f.closes == closed
}

// The public entry shares the acquisition gate: unset, it creates nothing.
func TestSupplierRegistrationClosedGateCreatesNothing(t *testing.T) {
	parent, p, c := supplierConfigFixture()
	if r, err := RegisterHelperSupplier(requestContext(t), parent, p, c); r != nil || !errors.Is(err, ErrUnconfigured) {
		t.Fatal("closed gate registered a supplier", err)
	}
	bad := c
	bad.ServiceCommandLine = `"C:\other.exe" --auth-supplier-internal`
	withSupplierImageValue(t, supplierImageValue(parent, c))
	f := &supplierRegistrarFixture{}
	if r, err := registerHelperSupplier(requestContext(t), parent, p, bad, f); r != nil || !errors.Is(err, ErrUnconfigured) || f.registers != 0 {
		t.Fatal("invalid Record reached the registrar", err)
	}
	if r, err := registerHelperSupplier(context.Background(), parent, p, c, f); r != nil || !errors.Is(err, ErrDenied) || f.registers != 0 {
		t.Fatal("unbounded context reached the registrar", err)
	}
}

// A partial registration is retained before its error; removal and handle
// return are separate phases, and a retry resumes at the failed one.
func TestSupplierRegistrationRetainsPartialAndResumesRemoval(t *testing.T) {
	parent, p, c := supplierConfigFixture()
	f := &supplierRegistrarFixture{registerErr: errors.New("synthetic verify mismatch"), removeErr: ErrUnconfirmed}
	r, err := registerHelperSupplier(requestContext(t), parent, p, c, f)
	if r == nil || !errors.Is(err, ErrDenied) {
		t.Fatal("partial registration was dropped", err)
	}
	if err = r.Close(context.Background()); !errors.Is(err, ErrUnconfirmed) || !supplierRegistrationCounts(f, 1, 0, 0) {
		t.Fatal("unbounded cleanup reached native removal", err)
	}
	if err = r.Close(requestContext(t)); !errors.Is(err, ErrUnconfirmed) || !supplierRegistrationCounts(f, 1, 1, 0) {
		t.Fatal("unconfirmed removal returned handles", err)
	}
	f.removeErr, f.closeErr = nil, ErrUnconfirmed
	if err = r.Close(requestContext(t)); !errors.Is(err, ErrUnconfirmed) || !supplierRegistrationCounts(f, 1, 2, 1) {
		t.Fatal(err)
	}
	f.closeErr = nil
	if err = r.Close(requestContext(t)); err != nil || !supplierRegistrationCounts(f, 1, 2, 2) {
		t.Fatal("confirmed removal was repeated", err)
	}
	if err = r.Close(requestContext(t)); err != nil || !supplierRegistrationCounts(f, 1, 2, 2) {
		t.Fatal("closed registration resumed native work", err)
	}
	none := &supplierRegistrarFixture{nilHeld: true, registerErr: ErrDenied}
	r, err = registerHelperSupplier(requestContext(t), parent, p, c, none)
	if r == nil || err == nil || r.Close(requestContext(t)) != nil || !supplierRegistrationCounts(none, 1, 0, 0) {
		t.Fatal("a refused creation held or removed a Service", err)
	}
}

// A faulted registrar retires the owner: no removal is attempted on it again.
func TestSupplierRegistrationFaultNeverResumesNativeWork(t *testing.T) {
	parent, p, c := supplierConfigFixture()
	f := &supplierRegistrarFixture{panics: true}
	r, err := registerHelperSupplier(requestContext(t), parent, p, c, f)
	if r == nil || !errors.Is(err, ErrFatal) {
		t.Fatal("registrar fault was not fatal", err)
	}
	for range 2 {
		if err = r.Close(requestContext(t)); !errors.Is(err, ErrFatal) || !supplierRegistrationCounts(f, 1, 0, 0) {
			t.Fatal("retired registration resumed native work", err)
		}
	}
	thread := &supplierRegistrarFixture{removeErr: errors.Join(ErrFatal, errors.New("synthetic owner thread drift"))}
	r, err = registerHelperSupplier(requestContext(t), parent, p, c, thread)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = r.Close(requestContext(t)); !errors.Is(err, ErrFatal) || !supplierRegistrationCounts(thread, 1, 1, 0) {
			t.Fatal("removal resumed after a fatal owner thread", err)
		}
	}
}

func TestSupplierBatchEarlyCloseWhileChildAliveKeepsLease(t *testing.T) {
	alive := true
	b, k, events, _, _ := supplierBatchFixture(t, func(c batchChild) batchChild { return &supplierLiveChild{batchChild: c, alive: &alive} })
	if err := b.Start(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	lease := b.spec.supplier
	if err := b.Close(requestContext(t)); err == nil {
		t.Fatal("close confirmed while helper alive")
	}
	if lease.token() != 0 || lease.recheck(requestContext(t), b) == nil {
		t.Fatal("borrowing stayed open after close began")
	}
	if !supplierCleanupCounts(k, 0, 0, 0) {
		t.Fatal("supplier returned before helper exit", *events)
	}
	alive = false
	if err := b.Close(requestContext(t)); err != nil || !supplierCleanupCounts(k, 1, 1, 1) {
		t.Fatal("same-owner retry failed", err)
	}
	if !supplierReturnedAfterChild(*events) {
		t.Fatal(*events)
	}
}
