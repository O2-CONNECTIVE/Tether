package servicehost

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// BindOperatorSIDPolicy is a pre-use composition step, not authentication or a
// Root capability. The original Prepared owner supplies only its held Record.
func (s *Service) BindOperatorSIDPolicy(owner OuterFinalizer, policy operatorauth.SIDPolicy) error {
	return s.bindOperatorSupplier(owner, policy, nil)
}

// BindOperatorSupplier is fixed configuration composition by the original outer
// owner. It neither authenticates a request nor starts or registers a service.
// Acquisition opens only when the compiled helper value equals this Record.
func (s *Service) BindOperatorSupplier(owner OuterFinalizer, policy operatorauth.SIDPolicy, supplier operatorauth.HelperSupplierConfig) error {
	return s.bindOperatorSupplier(owner, policy, &supplier)
}

func (s *Service) bindOperatorSupplier(owner OuterFinalizer, policy operatorauth.SIDPolicy, supplier *operatorauth.HelperSupplierConfig) error {
	if s == nil {
		return ErrProvisioningUnconfigured
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !sameGuestOwner(s.outer, owner) || s.auth == nil || s.control == nil || !s.control.factories.canonical() {
		return ErrProvisioningRejected
	}
	if s.used || s.started || s.stopping || s.auth.sidRequired || s.control.factories.batch != nil {
		return ErrAlreadyUsed
	}
	factories := s.control.factories
	if policy.OperatorSID != factories.operatorSID || policy.GuestSID != factories.guestSID {
		return ErrProvisioningRejected
	}
	var factory *operatorauth.EndpointFactory
	var err error
	if supplier == nil {
		factory, err = operatorauth.NewSIDEndpointFactory(factories.auth, factories.operatorSID, policy)
	} else {
		factory, err = operatorauth.NewSIDEndpointFactoryWithSupplier(factories.auth, factories.primaryToken, factories.operatorSID, policy, factories.profile, *supplier)
	}
	if err != nil {
		return ErrProvisioningRejected
	}
	s.control.factories.batch = factory
	s.auth.sidRequired = true
	return nil
}

// Only this concrete wrapper is built by production. The package-private model
// seam replaces the existing kernel/report boundary, never a public proof setter.
type serviceOperatorSubject interface {
	matches(*operatorauth.Collector, *serviceauthowner.Owner, *ephemeral.Issuer) bool
	matchesPolicy(string, string) bool
	same(serviceOperatorSubject) bool
}
type nativeOperatorSubject struct{ binding *operatorauth.OperatorBinding }

func (n *nativeOperatorSubject) matches(c *operatorauth.Collector, o *serviceauthowner.Owner, i *ephemeral.Issuer) bool {
	return n != nil && n.binding.Matches(c, o, i)
}
func (n *nativeOperatorSubject) matchesPolicy(op, guest string) bool {
	return n != nil && n.binding.MatchesSIDPolicy(op, guest)
}
func (n *nativeOperatorSubject) same(other serviceOperatorSubject) bool {
	v, ok := other.(*nativeOperatorSubject)
	return n != nil && ok && v != nil && n.binding.SameSubject(v.binding)
}

type serviceOperatorRecord struct {
	subject  serviceOperatorSubject
	owner    *serviceauthowner.Owner
	issuer   *ephemeral.Issuer
	dispatch *serviceDispatcher
}

func (r *serviceAuthRequest) acceptOperatorLocked(subject serviceOperatorSubject) bool {
	a, s := r.adapter, r.adapter.service
	if !r.liveLocked() || !authBudget(r.request) || !a.sidRequired || subject == nil || !subject.matches(r.batch.collector, r.owner, r.issuer) || !subject.matchesPolicy(s.control.factories.operatorSID, s.control.factories.guestSID) {
		return false
	}
	if a.operator == nil {
		a.operator = &serviceOperatorRecord{subject: subject, owner: r.owner, issuer: r.issuer, dispatch: a.dispatch}
		return true
	}
	old := a.operator
	return old.owner == r.owner && old.issuer == r.issuer && old.dispatch == a.dispatch && old.subject.same(subject)
}
func consumeNativeOperator(ctx context.Context, c *operatorauth.Collector, read *operatorauth.Read) ([]byte, serviceOperatorSubject, error) {
	frame, binding, err := c.ConsumeAuthenticated(ctx, read)
	if err != nil || binding == nil {
		return nil, nil, authFailure(err, ErrProvisioningRejected)
	}
	return frame, &nativeOperatorSubject{binding: binding}, nil
}
