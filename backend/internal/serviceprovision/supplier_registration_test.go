package serviceprovision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
)

// Only root OS observations, the existing scope seam and the private supplier
// registrar are in-memory models. The held Record, the original owner, the
// stage's registration retention and its Close order are the production code.
// Passing is not real SCM registration or removal.

func helperRecordPrepared(t *testing.T) (*Prepared, func() bool, *previewMemoryScope) {
	t.Helper()
	a := rootAnchor()
	k := provisionRecordKernel(a)
	k.identity.user = "S-1-5-18"
	raw := []byte(helperRecordJSON())
	k.files[a.RecordPath].data = raw
	k.files[a.RecordPath].state.size = int64(len(raw))
	digest := sha256.Sum256(raw)
	k.files[a.DigestPath].data = []byte(hex.EncodeToString(digest[:]) + "\n")
	r, _ := NewNativeRoot(a)
	r.api = k
	loaded, err := r.Open(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	scope := &previewMemoryScope{}
	p := &Prepared{loaded: loaded, scope: scope}
	p.accepted.Store(true)
	// The mutable cache cannot supply the registered Record.
	p.record.HelperSupplier = nil
	// held reports whether the prepared scope and the Root's token are still open.
	return p, func() bool { return scope.closes == 0 && k.token.closes == 0 }, scope
}

// stageRegistration models SupplierRegistration.Close results per call; the
// last result repeats. It records whether the other references were held.
type stageRegistration struct {
	results     []error
	closes      int
	heldAtClose []bool
	held        func() bool
}

func (r *stageRegistration) Close(context.Context) error {
	r.heldAtClose = append(r.heldAtClose, r.held())
	r.closes++
	if len(r.results) == 0 {
		return nil
	}
	return r.results[min(r.closes, len(r.results))-1]
}

func (r *stageRegistration) heldThroughout() bool {
	for _, held := range r.heldAtClose {
		if !held {
			return false
		}
	}
	return len(r.heldAtClose) == r.closes
}

type stageRegistrar struct {
	calls  int
	record Record
	policy operatorauth.SIDPolicy
	held   *stageRegistration
	err    error
}

func (r *stageRegistrar) register(_ context.Context, record Record, policy operatorauth.SIDPolicy) (supplierRegistration, error) {
	r.calls++
	r.record, r.policy = record, policy
	if r.held == nil {
		return nil, r.err
	}
	return r.held, r.err
}

func (r *stageRegistrar) fromHeldRecord() bool {
	s := r.record.HelperSupplier
	return r.calls == 1 && s != nil && s.ServiceName == "SyntheticAuth" && r.policy.OperatorSID == r.record.Profile.Keys.OperatorSID && r.policy.GuestSID == r.record.Profile.Keys.GuestSID
}

func supplierPreviewStage(e *Execution, p *Prepared, registrar *stageRegistrar) *nativeExecutionStage {
	return &nativeExecutionStage{cfg: e.cfg, preview: e.preview, calls: nativeExecutionCalls{
		prepare:  func(context.Context, Anchor) (*Prepared, error) { return p, nil },
		register: registrar.register,
	}}
}

func ownSupplierStage(t *testing.T, p *Prepared, registrar *stageRegistrar) (*Execution, *nativeExecutionStage, executionReady) {
	t.Helper()
	e, err := NewNativePreviewExecution(previewExecutionConfig())
	if err != nil {
		t.Fatal(err)
	}
	stage := supplierPreviewStage(e, p, registrar)
	ready := make(chan executionReady, 1)
	go e.own(rootContext(t), func() executionStage { return stage }, ready)
	select {
	case state := <-ready:
		return e, stage, state
	case <-time.After(3 * time.Second):
		t.Fatal("owner did not return")
		return nil, nil, executionReady{}
	}
}

// Registration removal runs first in the stage's Close. While it is
// unconfirmed, the Execution keeps every other reference and is not cleaned.
func TestOwnerRemovesSupplierRegistrationBeforeRootReferences(t *testing.T) {
	p, held, scope := helperRecordPrepared(t)
	reg := &stageRegistration{results: []error{operatorauth.ErrUnconfirmed, nil}, held: held}
	registrar := &stageRegistrar{held: reg}
	e, stage, state := ownSupplierStage(t, p, registrar)
	if state.err != nil || state.service == nil {
		t.Fatal(state.err)
	}
	if !registrar.fromHeldRecord() || stage.supplier != reg {
		t.Fatal("registration was not created from the held Record and retained by the stage")
	}
	ctx := rootContext(t)
	if err := state.service.Close(ctx); err == nil {
		t.Fatal("unconfirmed registration removal confirmed cleanup")
	}
	select {
	case <-e.cleaned:
		t.Fatal("unconfirmed registration removal cleaned the owner")
	default:
	}
	if reg.closes != 1 || !held() || stage.supplier != reg || stage.prepared != p {
		t.Fatal("unconfirmed removal released the registration or other references")
	}
	if err := state.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.cleaned:
	case <-ctx.Done():
		t.Fatal("owner was not cleaned after confirmed removal")
	}
	if reg.closes != 2 || !reg.heldThroughout() || stage.supplier != nil || stage.prepared != nil || scope.closes != 1 || held() {
		t.Fatal("registration removal did not precede the root references", reg.heldAtClose)
	}
}

// A partial registration returned with its error stays with the stage and is
// removed by the same owner before the unpublished Service's references.
func TestOwnerRetainsPartialSupplierRegistrationUntilRemoved(t *testing.T) {
	p, held, scope := helperRecordPrepared(t)
	reg := &stageRegistration{results: []error{operatorauth.ErrUnconfirmed, nil}, held: held}
	registrar := &stageRegistrar{held: reg, err: errors.Join(operatorauth.ErrDenied, errors.New("synthetic verify mismatch"))}
	e, stage, state := ownSupplierStage(t, p, registrar)
	if state.service != nil || !errors.Is(state.err, ErrRootDenied) || errors.Is(state.err, ErrRootFatal) {
		t.Fatal("partial registration mapping changed", state.err)
	}
	select {
	case <-e.cleaned:
	default:
		t.Fatal("refused owner reported before cleanup")
	}
	if !registrar.fromHeldRecord() || reg.closes != 2 || !reg.heldThroughout() || stage.supplier != nil || stage.prepared != nil || scope.closes != 1 {
		t.Fatal("partial registration was not retained until its own removal", reg.closes, reg.heldAtClose)
	}
}

// A fatal registration or removal retires the stage: it maps to ErrRootFatal
// and the stage never proceeds to the Root's references. The Execution owner's
// thread retirement on ErrRootFatal is the existing path, not run here.
func TestStageFatalSupplierRegistrationRetainsReferences(t *testing.T) {
	fatal := errors.Join(operatorauth.ErrFatal, operatorauth.ErrUnconfirmed)
	for _, phase := range []string{"register", "remove"} {
		t.Run(phase, func(t *testing.T) {
			p, held, scope := helperRecordPrepared(t)
			reg := &stageRegistration{results: []error{fatal}, held: held}
			registrar := &stageRegistrar{held: reg}
			if phase == "register" {
				registrar.err = fatal
			}
			e, err := NewNativePreviewExecution(previewExecutionConfig())
			if err != nil {
				t.Fatal(err)
			}
			stage := supplierPreviewStage(e, p, registrar)
			service, err := stage.Initialize(rootContext(t))
			if err != nil || service == nil {
				t.Fatal(err)
			}
			if err = service.BindOuterFinalizer(e); err != nil {
				t.Fatal(err)
			}
			err = stage.BindOperatorSIDPolicy(rootContext(t), service, e)
			if phase == "register" && !errors.Is(err, ErrRootFatal) || phase == "remove" && err != nil {
				t.Fatal("fatal registration mapping changed", err)
			}
			if !registrar.fromHeldRecord() || stage.supplier != reg {
				t.Fatal("registration was not retained by the stage")
			}
			for range 2 {
				if err = stage.Close(rootContext(t)); !errors.Is(err, ErrRootFatal) {
					t.Fatal("fatal removal was not retirement", err)
				}
				if !held() || stage.supplier != reg || stage.prepared != p || scope.closes != 0 {
					t.Fatal("retired stage released other references")
				}
			}
			// No owner thread serves Finalize here; the Service stays unconfirmed.
			_ = service.Close(rootContext(t))
		})
	}
}

// The production default is unchanged: with no seam, the closed acquisition
// gate registers nothing (see TestOriginalOwnerComposesHeldHelperRecordWithoutNativeAcquisition).
// A refused creation that holds nothing is not retained either.
func TestStageRefusedSupplierRegistrationHoldsNothing(t *testing.T) {
	p, _, _ := helperRecordPrepared(t)
	registrar := &stageRegistrar{err: operatorauth.ErrDenied}
	e, stage, state := ownSupplierStage(t, p, registrar)
	if state.service != nil || !errors.Is(state.err, ErrRootDenied) || !registrar.fromHeldRecord() {
		t.Fatal("refused registration mapping changed", state.err)
	}
	select {
	case <-e.cleaned:
	default:
		t.Fatal("refused owner reported before cleanup")
	}
	if stage.supplier != nil || stage.prepared != nil {
		t.Fatal("refused registration was retained")
	}
}
