package servicehost

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/servicecontrol"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

type guestOwnerFixture struct {
	service                              *Service
	prepares, confirmedCloses, finalizes atomic.Int32
	saved                                GuestStagingTicket
	descriptor                           GuestStagingDescriptor
}

func TestGuestStagingTypedNilOwnerCannotBind(t *testing.T) {
	s, err := NewPreviewService(previewConfigFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var owner *guestOwnerFixture
	if err := s.BindOuterFinalizer(owner); err != nil {
		t.Fatal(err)
	}
	if err := s.BindGuestStagingOwner(owner); err == nil {
		t.Fatal("typed nil outer owner opened staging")
	}
}

func (o *guestOwnerFixture) ResolvePreviewConfirmation(ctx context.Context, t PreviewConfirmationTicket) error {
	_, e := t.PreviewFor(o.service)
	return e
}
func (o *guestOwnerFixture) PrepareNeverCreatedGuest(context.Context, GuestStagingTicket, *serviceready.InspectionLease) error {
	o.prepares.Add(1)
	return ErrProvisioningUnconfigured
}
func (o *guestOwnerFixture) CloseNeverCreatedGuest(ctx context.Context, ticket GuestStagingTicket, lease *serviceready.InspectionLease) error {
	if lease != nil {
		return ErrRuntimeUnconfirmed
	}
	d, err := ticket.CleanupDescriptorFor(ctx, o)
	if err != nil {
		return err
	}
	if err := ticket.RevokeNeverCreated(ctx, o, d.Namespace, ""); err != nil {
		return err
	}
	if err := ticket.ConfirmNeverCreated(ctx, o, d.Namespace, ""); err != nil {
		return err
	}
	// The owner can retry an unconfirmed attempt before this acknowledgement.
	o.saved, o.descriptor = ticket, d
	o.confirmedCloses.Add(1)
	return nil
}
func (o *guestOwnerFixture) Finalize(_ context.Context, p InnerCleanupProof) error {
	if !p.Confirms(o.service) {
		return ErrRuntimeUnconfirmed
	}
	o.finalizes.Add(1)
	return nil
}

func TestGuestStagingOwnerBindsOnlySameUnusedOuterAndKeepsMissingEndpointClosed(t *testing.T) {
	s, err := NewPreviewService(previewConfigFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	o := &guestOwnerFixture{service: s}
	if err := s.BindGuestStagingOwner(o); err == nil {
		t.Fatal("guest owner accepted before outer binding")
	}
	if err := s.BindOuterFinalizer(o); err != nil {
		t.Fatal(err)
	}
	if err := s.BindGuestStagingOwner(&guestOwnerFixture{service: s}); err == nil {
		t.Fatal("foreign outer owner accepted")
	}
	if err := s.BindGuestStagingOwner(o); err != nil {
		t.Fatal(err)
	}
	if err := s.BindGuestStagingOwner(o); !errors.Is(err, ErrAlreadyUsed) {
		t.Fatal("owner replaced", err)
	}
	s.control.prepare = func(context.Context) (serviceControlWitness, error) {
		return serviceControlWitness{confirm: func(context.Context) error { return nil }, recheck: func(context.Context) error { return nil }, close: func(context.Context) error { return nil }}, nil
	}
	s.cfg.Budgets.Observe = time.Hour
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Serve(context.Background(), nil, make(chan ServiceState, 32)) }()
	t.Cleanup(func() {
		ctx, end := context.WithTimeout(context.Background(), 2*time.Second)
		defer end()
		_ = s.Close(ctx)
		select {
		case <-done:
		case <-ctx.Done():
			t.Error("guest Service worker did not join")
		}
	})
	ctx, end := context.WithTimeout(context.Background(), 2*time.Second)
	defer end()
	for s.Snapshot().State != Running {
		select {
		case <-ctx.Done():
			t.Fatal("Service did not run")
		case <-time.After(time.Millisecond):
		}
	}
	hello := previewExchange(t, s.auth, dispatchHello)
	prepared := previewExchange(t, s.auth, dispatchRequest(hello, 1, "22222222222222222222222222222222", servicecontrol.PrepareIntent))
	response := previewExchange(t, s.auth, confirmationFrame(hello, prepared.Intent, 2, "33333333333333333333333333333333"))
	if response.Error == nil || response.Error.Code != "SERVICE_NOT_READY" || o.prepares.Load() != 0 {
		t.Fatal("absent authenticated Endpoint opened staging", response)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if !o.saved.MatchesService(s) || o.saved.MatchesService(new(Service)) || (GuestStagingTicket{}).MatchesService(s) {
		t.Fatal("ticket changed exact Service provenance")
	}
	ns := o.descriptor.Namespace
	if ns.Epoch == ([32]byte{}) || ns.Intent == ([32]byte{}) || ns.Generation != 1 {
		t.Fatalf("guest namespace: epoch=%t intent=%t generation=%d", ns.Epoch != ([32]byte{}), ns.Intent != ([32]byte{}), ns.Generation)
	}
	if closes, finalizes := o.confirmedCloses.Load(), o.finalizes.Load(); closes != 1 || finalizes != 1 {
		t.Fatalf("confirmed guest closes=%d outer finalizations=%d, want one each", closes, finalizes)
	}
	snapshot := s.Snapshot()
	if !s.executionClosed || s.executionAdmissions != 0 || snapshot.Generation != 0 || snapshot.HasAuthority {
		t.Fatalf("guest execution: closed=%t admissions=%d generation=%d authority=%t", s.executionClosed, s.executionAdmissions, snapshot.Generation, snapshot.HasAuthority)
	}
	if _, err := o.saved.DescriptorFor(ctx, o); err == nil {
		t.Fatal("cleanup ticket reopened acquisition")
	}
	if _, err := o.saved.CleanupDescriptorFor(ctx, &guestOwnerFixture{service: s}); err == nil {
		t.Fatal("foreign owner observed cleanup")
	}
	wrong := ns
	wrong.Generation++
	if err := o.saved.ConfirmNeverCreated(ctx, o, wrong, ""); err == nil {
		t.Fatal("foreign guest namespace accepted")
	}
	if _, err := (GuestStagingTicket{}).DescriptorFor(ctx, o); err == nil {
		t.Fatal("zero ticket accepted")
	}
}

func TestGuestStagingProductionEndpointKeepsMissingExpectedOperatorSourceClosed(t *testing.T) {
	s, err := NewPreviewService(previewConfigFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	owner := &guestOwnerFixture{service: s}
	if err = s.BindOuterFinalizer(owner); err != nil {
		t.Fatal(err)
	}
	if err = s.BindGuestStagingOwner(owner); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = s.auth.exchangeControlEndpoint(ctx); !errors.Is(err, ErrProvisioningUnconfigured) {
		t.Fatal("missing canonical expected policy entered native exchange", err)
	}
	if s.auth.attempts != 0 || s.auth.active != nil || owner.prepares.Load() != 0 || s.staging != nil || s.Snapshot().Generation != 0 || s.Snapshot().HasAuthority {
		t.Fatal("nil producer acquired owner/native/grant state")
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
