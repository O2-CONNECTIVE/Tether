package serviceprovision

import (
	"context"
	"errors"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

// The only production supplier is the original Prepared owner. The pure value
// is not authentication; no SID is accepted from a message, candidate or cache.
func (p *Prepared) operatorSIDPolicy(ctx context.Context) (operatorauth.SIDPolicy, error) {
	_, record, _, err := p.previewRecord(ctx)
	if err != nil {
		return operatorauth.SIDPolicy{}, err
	}
	return operatorauth.SIDPolicy{OperatorSID: record.Profile.Keys.OperatorSID, GuestSID: record.Profile.Keys.GuestSID}, nil
}

type operatorSIDPolicyStage interface {
	BindOperatorSIDPolicy(context.Context, *servicehost.Service, servicehost.OuterFinalizer) error
}

func (s *nativeExecutionStage) BindOperatorSIDPolicy(ctx context.Context, service *servicehost.Service, owner servicehost.OuterFinalizer) error {
	if s == nil || s.prepared == nil || s.previewSnapshot == nil || service == nil || service != s.service || owner == nil || !rootLive(ctx) {
		return ErrRootDenied
	}
	if err := s.VerifyPreviewConfirmation(ctx, *s.previewSnapshot); err != nil {
		return err
	}
	_, record, _, err := s.prepared.previewRecord(ctx)
	if err != nil {
		return err
	}
	policy := operatorauth.SIDPolicy{OperatorSID: record.Profile.Keys.OperatorSID, GuestSID: record.Profile.Keys.GuestSID}
	if !rootLive(ctx) {
		return ErrRootDenied
	}
	if record.HelperSupplier == nil {
		err = service.BindOperatorSIDPolicy(owner, policy)
	} else {
		if err = s.registerSupplier(ctx, record, policy); err != nil {
			return err
		}
		err = service.BindOperatorSupplier(owner, policy, *record.HelperSupplier)
	}
	if err != nil {
		return err
	}
	if !rootLive(ctx) {
		return ErrRootDenied
	}
	return nil
}

// supplierRegistration is the stage's hold on one temporary supplier
// registration. Production holds only *operatorauth.SupplierRegistration.
type supplierRegistration interface {
	Close(context.Context) error
}

// registerNativeSupplier is the production registrar. A nil registration stays
// a nil interface so a refused creation is never held as a registration.
func registerNativeSupplier(ctx context.Context, record Record, policy operatorauth.SIDPolicy) (supplierRegistration, error) {
	registration, err := operatorauth.RegisterHelperSupplier(ctx, record.Profile, policy, *record.HelperSupplier)
	if registration == nil {
		return nil, err
	}
	return registration, err
}

// registerSupplier creates the held Record's temporary supplier on this
// original thread before the Service is published. The acquisition gate being
// closed creates nothing and Prepare keeps refusing. Any registration, partial
// or complete, stays with this stage until Close after the inner cleanup proof.
func (s *nativeExecutionStage) registerSupplier(ctx context.Context, record Record, policy operatorauth.SIDPolicy) error {
	if s.supplier != nil {
		return ErrRootDenied
	}
	register := s.calls.register
	if register == nil {
		register = registerNativeSupplier
	}
	registration, err := register(ctx, record, policy)
	s.supplier = registration
	if registration == nil && errors.Is(err, operatorauth.ErrUnconfigured) {
		return nil
	}
	if errors.Is(err, operatorauth.ErrFatal) {
		return errors.Join(ErrRootFatal, err)
	}
	if err != nil || registration == nil {
		return errors.Join(ErrRootDenied, err)
	}
	return nil
}
