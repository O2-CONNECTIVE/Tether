package operatorauth

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// production は具体 SoleInput の固定 method にだけ接続する。
// nativeBatchOps の同型 field は package 内 memory 試験専用の差替え seam。
func moveNativeSoleInput(ctx context.Context, input *serviceready.SoleInput) (uintptr, error) {
	return input.Move(ctx)
}

// 具体 source と既存候補 lease を保持するだけで、Move/query はしない。
func soleInputProfileSpec(s nativeBatchSpec, input *serviceready.SoleInput, policy profileProviderPolicy, api endpointCandidateKernel) (nativeBatchSpec, error) {
	if input == nil || s.input != 0 || s.soleInput != nil || s.inputLease != nil || s.producer != nil || api == nil || policy.material.launchID != s.launch.launchID || policy.material.cleanup != s.launch.cleanup {
		return nativeBatchSpec{}, ErrUnconfigured
	}
	lease := &endpointCandidateLease{api: api}
	provider, err := newProtectedProfileProvider(policy, lease)
	if err != nil {
		return nativeBatchSpec{}, err
	}
	s.soleInput, s.inputLease, s.producer = input, lease, provider
	return s, nil
}

// SID mode retains the exact source and a fresh fixed-policy producer without
// a candidate-query lease. This pure step supplies no helper token or launch
// evidence and performs no Move, profile I/O or operator authentication.
func soleInputSIDSpec(s nativeBatchSpec, input *serviceready.SoleInput, policy SIDPolicy) (nativeBatchSpec, error) {
	if input == nil || s.input != 0 || s.soleInput != nil || s.inputLease != nil || s.producer != nil || s.sidPolicy != (SIDPolicy{}) || s.report.guestSID != policy.GuestSID {
		return nativeBatchSpec{}, ErrUnconfigured
	}
	provider, err := newSIDProfileProvider(s.launch.launchID, policy, s.launch.cleanup)
	if err != nil {
		return nativeBatchSpec{}, err
	}
	s.soleInput, s.sidPolicy, s.producer = input, policy, provider
	return s, nil
}

func (b *nativeBatch) moveInput(ctx context.Context) error {
	if b.spec.soleInput == nil {
		return nil
	}
	h, err := b.ops.moveSoleInput(ctx, b.spec.soleInput)
	// 非零の実 return は error や値の検査より先に、この Batch が保持する。
	if h != 0 {
		b.mu.Lock()
		b.input = h
		b.mu.Unlock()
	}
	if errors.Is(err, serviceready.ErrFatal) || errors.Is(err, ErrFatal) {
		return b.retainScopeFailure(errors.Join(ErrFatal, err))
	}
	if h != 0 && (!helperHandle(h) || h == b.helperToken || b.ops.scope != nil && h == b.ops.scope.token) {
		// pseudo/借用 token との alias は close せず、同じ owner に未知を残す。
		b.mu.Lock()
		b.uncertain = true
		b.mu.Unlock()
		return ErrUnconfirmed
	}
	if err != nil || !helperHandle(h) {
		return errors.Join(ErrDenied, err)
	}
	if !b.admitted(ctx) {
		return ErrDenied
	}
	// SID policy is fixed before Move. Only the helper's actual message token
	// authenticates the operator; no legacy candidate projection is acquired.
	if b.spec.sidPolicy != (SIDPolicy{}) {
		return nil
	}
	return b.spec.inputLease.bindInput(h)
}

// typed 経路は raw spec.input がゼロのため、実所有 input と借用 token を使う。
// 別 owner の同番号 handle が返った場合は二重 close せず未知を保持する。
func (b *nativeBatch) checkMovedPipeAliases(profile batchProfile, report batchReport) error {
	if b.spec.soleInput == nil {
		return nil
	}
	retained := []uintptr{b.input, b.helperToken}
	if b.ops.scope != nil {
		retained = append(retained, b.ops.scope.token)
	}
	var wires []uintptr
	if profile != nil {
		wires = append(wires, profile.childReader())
	}
	if report != nil {
		wires = append(wires, report.childWriter())
	}
	for _, h := range wires {
		alias := h != 0 && !helperHandle(h)
		for _, prior := range retained {
			alias = alias || h != 0 && h == prior
		}
		if alias {
			b.mu.Lock()
			b.uncertain = true
			b.mu.Unlock()
			return ErrUnconfirmed
		}
		retained = append(retained, h)
	}
	return nil
}
