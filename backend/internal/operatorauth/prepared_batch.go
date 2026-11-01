package operatorauth

import (
	"context"
	"errors"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// Batch is a concrete retained native owner. A future protected provider must
// prepare it privately and register it with Service before Start. There is no
// public factory; the zero value is unconfigured and cannot acquire resources.
type Batch = nativeBatch

// MatchesSoleInput is immutable provenance only, never authentication/admission.
func (b *nativeBatch) MatchesSoleInput(input *serviceready.SoleInput) bool {
	return b != nil && b.self == b && input != nil && b.spec.soleInput == input
}

// MatchesIssuer compares the immutable issuer retained during preparation.
// Its concrete Owner always holds this same actual issuer.
func (b *nativeBatch) MatchesIssuer(issuer *ephemeral.Issuer) bool {
	return b != nil && b.self == b && issuer != nil && b.issuer == issuer
}

// MatchesOwner also requires the canonical owner retained by Service. Creating a
// second pair with the same issuer cannot substitute for that provenance.
func (b *nativeBatch) MatchesOwner(owner *serviceauthowner.Owner, issuer *ephemeral.Issuer) bool {
	return b.MatchesIssuer(issuer) && owner != nil && b.owner == owner && owner.MatchesIssuer(issuer)
}

// Collector returns the exact collector created during preparation. It does not
// authenticate a Read or replace the collector's issuer checks.
func (b *nativeBatch) Collector() *Collector {
	if b == nil || b.self != b {
		return nil
	}
	return b.collector
}

func (b *nativeBatch) revoke() error {
	b.mu.Lock()
	b.closing = true
	b.collector.mu.Lock()
	b.collector.failed, b.collector.closed = true, true
	b.collector.mu.Unlock()
	b.mu.Unlock()
	// 新規借用だけを先に閉じる。進行中の取得を待たず、保持参照は回収しない。
	b.spec.supplier.revoke()
	defer func() {
		b.mu.Lock()
		startCancel, collectCancel := b.startCancel, b.collectCancel
		b.mu.Unlock()
		b.collector.mu.Lock()
		activeCancel := b.collector.activeCancel
		b.collector.mu.Unlock()
		if startCancel != nil {
			startCancel()
		}
		if collectCancel != nil {
			collectCancel()
		}
		if activeCancel != nil {
			activeCancel()
		}
	}()
	b.revokeOnce.Do(func() {
		b.revokeErr = ErrUnconfirmed
		b.revokeErr = b.owner.Revoke()
	})
	return b.revokeErr
}

func (b *nativeBatch) awaitStart(ctx context.Context) error {
	if !finite(ctx) {
		return ErrUnconfirmed
	}
	b.mu.Lock()
	starting := b.starting
	b.mu.Unlock()
	if starting {
		select {
		case <-b.startDone:
		case <-ctx.Done():
			return ErrUnconfirmed
		}
	}
	return nil
}

func (b *nativeBatch) admitted(ctx context.Context) bool {
	b.mu.Lock()
	live := !b.closing && reportContextValid(ctx) && reportContextValid(b.request)
	b.mu.Unlock()
	return live && b.owner.Admit(ctx) == nil
}

func (b *nativeBatch) acquireOperation(ctx context.Context) bool {
	if !finite(ctx) {
		return false
	}
	select {
	case b.operation <- struct{}{}:
		if !finite(ctx) {
			<-b.operation
			return false
		}
		return true
	case <-ctx.Done():
		return false
	}
}

func (b *nativeBatch) Collect(ctx context.Context) (*Read, error) {
	if b == nil || b.self != b || b.collector == nil || b.operation == nil {
		return nil, ErrUnconfigured
	}
	if !finite(ctx) {
		return nil, ErrDenied
	}
	if !b.acquireOperation(ctx) {
		return nil, ErrDenied
	}
	defer func() { <-b.operation }()
	// A supplier-lent child is collected only until the lending budget ends, so
	// no result outlives that authority. The returned Read carries this context:
	// it ends at that budget or when Close/revoke cancels the collection, never
	// when Collect returns. A past end fails through the Collector's own path,
	// which reclaims with the same owner.
	release := func() {}
	if s := b.spec.supplier; s != nil {
		bounded, stop := context.WithDeadline(ctx, s.borrowEnd())
		ctx, release = bounded, stop
	}
	b.mu.Lock()
	if !b.ready || b.closing {
		b.mu.Unlock()
		release()
		return nil, ErrDenied
	}
	ctx, cancel := context.WithCancel(ctx)
	end := func() { cancel(); release() }
	b.collectCancel = end
	b.mu.Unlock()

	r, err := b.collector.Collect(ctx)
	if err != nil {
		end()
	}
	b.mu.Lock()
	fatal := b.fatal
	closing := b.closing
	b.mu.Unlock()
	if fatal != nil {
		end()
		return nil, errors.Join(err, ErrFatal, fatal)
	}
	if closing {
		end()
		return nil, ErrDenied
	}
	return r, err
}

// Close stops admission and revokes the same issuer before cancellation. It
// cannot confirm cleanup until the in-flight constructor returns all references
// and acknowledges completion. Timeout retains this Batch and its Collector.
func (b *nativeBatch) Close(ctx context.Context) (result error) {
	if b == nil || b.self != b || b.collector == nil || b.operation == nil {
		return ErrUnconfigured
	}
	defer func() {
		if recover() != nil {
			result = ErrUnconfirmed
		}
	}()
	b.mu.Lock()
	b.closing = true
	startCancel, collectCancel, starting := b.startCancel, b.collectCancel, b.starting
	b.mu.Unlock()
	_ = b.revoke()
	if startCancel != nil {
		startCancel()
	}
	if collectCancel != nil {
		collectCancel()
	}
	if !finite(ctx) {
		return ErrUnconfirmed
	}
	if starting {
		select {
		case <-b.startDone:
		case <-ctx.Done():
			return ErrUnconfirmed
		}
	}
	if !b.acquireOperation(ctx) {
		return ErrUnconfirmed
	}
	defer func() { <-b.operation }()
	err := b.collector.Close(ctx)
	b.mu.Lock()
	fatal := b.fatal
	b.mu.Unlock()
	if fatal != nil {
		return errors.Join(err, ErrFatal, fatal)
	}
	return err
}

// EndpointFactory は不変なpure policy保持者。方式Bの公開constructorは
// SID policyだけを保持し、正規helper token/runtime sourceを供給しない。
// 従来のAuthId exact policyはprivate経路のまま。zero/copyはBatchを準備できない。
// 準備とpolicyの値一致は実messageの認証を証明しない。
type EndpointFactory struct {
	self         *EndpointFactory
	service      *bootstrapauth.Factory
	serviceToken uintptr
	spec         nativeBatchSpec
	policy       profileProviderPolicy
	sidPolicy    SIDPolicy
	// supplier/parent は元Recordの複製。lease は要求ごとに Prepare で作る。
	supplier *HelperSupplierConfig
	parent   bootstrapauth.Profile
}

func newEndpointFactory(s nativeBatchSpec, f *bootstrapauth.Factory, token uintptr, p profileProviderPolicy) (*EndpointFactory, error) {
	if f == nil || !helperHandle(token) || s.input != 0 || s.soleInput != nil || s.inputLease != nil || s.producer != nil || p.material.launchID != s.launch.launchID || p.material.cleanup != s.launch.cleanup {
		return nil, ErrUnconfigured
	}
	result := &EndpointFactory{service: f, serviceToken: token, spec: s, policy: p}
	result.self = result
	return result, nil
}
func (f *EndpointFactory) MatchesServiceFactory(service *bootstrapauth.Factory) bool {
	return f != nil && f.self == f && service != nil && f.service == service
}
func (f *EndpointFactory) Prepare(input *serviceready.SoleInput, owner *serviceauthowner.Owner, issuer *ephemeral.Issuer) (*Batch, error) {
	if f == nil || f.self != f || input == nil || !owner.MatchesIssuer(issuer) {
		return nil, ErrUnconfigured
	}
	// SID policy構成とhelper資源の供給は別契約。supplierが未構成、または
	// image内の固定値がRecordと一致しなければSoleInput.Moveやnative取得より前に閉じる。
	if f.sidPolicy != (SIDPolicy{}) {
		if !f.supplierEnabled() {
			return nil, ErrUnconfigured
		}
		return prepareNativeSupplierSIDBatch(f, input, batchOwner{issuer: issuer, owner: owner})
	}
	return prepareNativeSoleInputProfileBatch(f.spec, input, f.service, f.serviceToken, batchOwner{issuer: issuer, owner: owner}, f.policy)
}

// MatchesServiceFactory compares only the immutable constructor provenance. It
// neither authenticates a Read nor converts an observed candidate into policy.
func (b *nativeBatch) MatchesServiceFactory(service *bootstrapauth.Factory) bool {
	return b != nil && b.self == b && service != nil && b.serviceFactory == service
}
