package operatorauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// Only the native supplier, OS handles and the helper's sealed report are memory
// fixtures. The Batch is composed by the factory's supplier spec and the SID
// sole-input spec, and is consumed through the same Collect and
// ConsumeAuthenticated pair that servicehost calls. Passing is not real Windows
// SCM, token or message authentication success.
func supplierAuthenticatedBatch(t *testing.T) (*Batch, *supplierFixture, *[]string, batchOwner) {
	t.Helper()
	parent, p, c := supplierConfigFixture()
	withSupplierImageValue(t, supplierImageValue(parent, c))
	f, err := NewSIDEndpointFactoryWithSupplier(new(bootstrapauth.Factory), 17, p.OperatorSID, p, parent, c)
	if err != nil {
		t.Fatal(err)
	}
	_, k := supplierFixtureLease(t)
	spec, err := f.supplierBatchSpec(k)
	if err != nil {
		t.Fatal(err)
	}
	input := new(serviceready.SoleInput)
	s, err := soleInputSIDSpec(spec, input, p)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := encodeAuthenticatedReport(requestFrame(), s.launch.launchID, p, tokenProjection{user: p.OperatorSID, authentication: 17, session: 3, integrity: 8192, kind: 2, level: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, owner, ops, events, report, _ := batchFixture(t)
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
	ops.newReport = func(context.Context, reportConfig) (batchReport, error) {
		return &sidDeliveredReport{report, &sealed}, nil
	}
	start := ops.start
	ops.start = func(ctx context.Context, l nativeLaunchSpec) (batchChild, error) {
		if l.primaryToken != 43 || l.launchID != s.launch.launchID {
			return nil, ErrDenied
		}
		return start(ctx, l)
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Start(requestContext(t)); err != nil || k.acquires != 1 {
		t.Fatal(err)
	}
	return b, k, events, owner
}

// A valid report collected under a lent token stays consumable until the
// lending budget; returning from Collect neither ends nor extends it.
func TestSupplierBatchAuthenticatedReadConsumableBeforeBorrowEnd(t *testing.T) {
	b, k, events, owner := supplierAuthenticatedBatch(t)
	end := b.spec.supplier.borrowEnd()
	if end.IsZero() {
		t.Fatal("lending lease exposed no budget end")
	}
	read, err := b.Collect(requestContext(t))
	if err != nil || read == nil {
		t.Fatal("valid supplier report was not collected", err)
	}
	if read.request.Err() != nil || read.deadline.After(end) {
		t.Fatal("collected Read ended at Collect return or outlived the lending budget")
	}
	if !supplierCleanupCounts(k, 1, 1, 1) || !supplierReturnedAfterChild(*events) {
		t.Fatal("same owner did not return the generation after the child", *events)
	}
	frame, binding, err := b.Collector().ConsumeAuthenticated(requestContext(t), read)
	if err != nil || string(frame) != string(requestFrame()) || !binding.Matches(b.Collector(), owner.owner, owner.issuer) {
		t.Fatal("authenticated supplier Read was not consumable before the budget end", err)
	}
	if frame, binding, err := b.Collector().ConsumeAuthenticated(requestContext(t), read); frame != nil || binding != nil || !errors.Is(err, ErrDenied) {
		t.Fatal("single-use Read consumed twice", err)
	}
	if err = b.Close(requestContext(t)); err != nil || !supplierCleanupCounts(k, 1, 1, 1) {
		t.Fatal("confirmed cleanup repeated native supplier work", err)
	}
}

func TestSupplierBatchAuthenticatedReadRefusedAfterBorrowEnd(t *testing.T) {
	b, k, _, _ := supplierAuthenticatedBatch(t)
	lease := b.spec.supplier
	end := time.Now().Add(300 * time.Millisecond)
	lease.mu.Lock()
	lease.expires = end
	lease.mu.Unlock()
	read, err := b.Collect(requestContext(t))
	if err != nil || read == nil {
		t.Fatal("valid supplier report was not collected", err)
	}
	if read.request.Err() != nil || !read.deadline.Equal(end) {
		t.Fatal("collected Read did not keep the original lending deadline", read.deadline, end)
	}
	select {
	case <-read.request.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("collected Read outlived the lending budget")
	}
	if time.Now().Before(end) {
		t.Fatal("collected Read ended before the lending budget")
	}
	if frame, binding, err := b.Collector().ConsumeAuthenticated(requestContext(t), read); frame != nil || binding != nil || !errors.Is(err, ErrDenied) {
		t.Fatal("Read consumed after the lending budget", err)
	}
	if err = b.Close(requestContext(t)); err != nil || !supplierCleanupCounts(k, 1, 1, 1) {
		t.Fatal(err)
	}
}

// Close and the Collector's revoke path own the collection's cancellation.
func TestSupplierBatchAuthenticatedReadEndsWithOwnerRevocation(t *testing.T) {
	for _, path := range []string{"close", "revoke"} {
		t.Run(path, func(t *testing.T) {
			b, k, _, _ := supplierAuthenticatedBatch(t)
			read, err := b.Collect(requestContext(t))
			if err != nil || read == nil || read.request.Err() != nil {
				t.Fatal("valid supplier Read was not live after Collect", err)
			}
			if path == "close" {
				if err = b.Close(requestContext(t)); err != nil {
					t.Fatal(err)
				}
			} else {
				_ = b.revoke()
			}
			if read.request.Err() == nil {
				t.Fatal("owner revocation left the collected Read live")
			}
			if frame, binding, err := b.Collector().ConsumeAuthenticated(requestContext(t), read); frame != nil || binding != nil || !errors.Is(err, ErrDenied) {
				t.Fatal("revoked Read consumed", err)
			}
			if err = b.Close(requestContext(t)); err != nil || !supplierCleanupCounts(k, 1, 1, 1) {
				t.Fatal("same-owner cleanup repeated or lost", err)
			}
		})
	}
}
