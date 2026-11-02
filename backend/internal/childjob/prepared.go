package childjob

import (
	"context"
	"runtime"
	"sync/atomic"
)

const (
	preparedStaged uint32 = iota
	preparedStarted
	preparedRevoked
	preparedInspected
)

// Prepared is a spike-only candidate. It is not wired into product authority.
// Do not copy it after first use; Close owns cleanup even when Commit fails.
type Prepared struct {
	child                *Child
	lease                context.Context
	spec                 Spec
	api                  inspectionKernel
	state                atomic.Uint32 // admission states are monotonic; never reset.
	queryToken           uintptr
	commitQueryToken     uintptr
	inspection           *Inspection
	guestQueryToken      uintptr
	guestObservation     *GuestTokenObservation
	protectedObservation *ProtectedGuestObservation
}

type inspectionKernel interface {
	kernel
	inspectTarget(Spec, uintptr, uintptr, uintptr) (uintptr, error)
}

// Prepare requires a trusted owner to retain the approved primary token and
// its admission context. This spike does not authenticate or issue that context.
func Prepare(ctx context.Context, spec Spec) (*Prepared, error) {
	spec = freezeSpec(spec)
	if !bounded(ctx) || !configured(spec) {
		return nil, ErrConfig
	}
	return platformPrepare(ctx, spec)
}

func prepareWithKernel(ctx context.Context, spec Spec, api inspectionKernel) (*Prepared, error) {
	spec = freezeSpec(spec)
	c, err := stage(ctx, spec, api)
	if c == nil {
		return nil, err
	}
	p := &Prepared{child: c, lease: ctx, spec: spec, api: api}
	if err != nil {
		p.state.Store(preparedRevoked)
	}
	return p, err
}

// Commit checks the same held target before its one resume. The CAS orders
// commit against Close: if Close wins, resume is forbidden; if commit wins,
// Close must reclaim that same target. It is not a physical scheduling barrier.
func (p *Prepared) Commit(ctx context.Context) error {
	if p == nil || p.child == nil || !bounded(ctx) {
		return ErrConfig
	}
	if p.spec.Protection != nil {
		return ErrStart
	}
	if p.state.Load() != preparedStaged {
		return ErrStart
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := p.child.take(ctx); err != nil {
		return err
	}
	if p.state.Load() != preparedStaged {
		p.child.gate <- struct{}{}
		return ErrStart
	}
	err := p.commitHeld(ctx)
	if err != nil {
		// 検査失敗をpermit返却前に確定し、後続Commitの再検査と参照上書きを防ぐ。
		p.state.Store(preparedRevoked)
	}
	p.child.gate <- struct{}{}
	if err == nil {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.Background(), p.spec.CleanupTimeout)
	defer cancel()
	if p.Close(cleanup) != nil {
		return ErrUnconfirmed
	}
	return ErrStart
}

func (p *Prepared) commitHeld(ctx context.Context) error {
	if p.state.Load() != preparedStaged || !bounded(p.lease) {
		return ErrStart
	}
	var err error
	p.queryToken, err = p.api.inspectTarget(p.spec, p.child.process, p.child.thread, p.child.job)
	if err != nil || !ordinaryHandle(p.queryToken) || !bounded(ctx) || !bounded(p.lease) || !p.state.CompareAndSwap(preparedStaged, preparedStarted) {
		return ErrStart
	}
	if p.child.resumeOwned(ctx) != nil || p.state.Load() != preparedStarted || !bounded(p.lease) || !bounded(ctx) {
		return ErrStart
	}
	return nil
}

func (p *Prepared) closeQuery(ctx context.Context) error {
	if err := p.child.take(ctx); err != nil {
		return err
	}
	defer func() { p.child.gate <- struct{}{} }()
	if p.guestQueryToken != 0 && (p.guestQueryToken == p.queryToken || p.guestQueryToken == p.commitQueryToken) {
		p.guestQueryToken = 0
	}
	if p.child.release(&p.guestQueryToken) != nil {
		return ErrUnconfirmed
	}
	if p.commitQueryToken != 0 && p.commitQueryToken == p.queryToken {
		// A duplicate numeric reference is not closed twice. Real kernel opens
		// provide distinct handles; keep test/failed-provider ownership bounded.
		p.commitQueryToken = 0
	}
	if p.child.release(&p.commitQueryToken) != nil {
		return ErrUnconfirmed
	}
	return p.child.release(&p.queryToken)
}

func (p *Prepared) Close(ctx context.Context) error {
	if p == nil || p.child == nil {
		return ErrConfig
	}
	p.state.Store(preparedRevoked)
	if err := p.child.Close(ctx); err != nil {
		return err
	}
	return p.closeQuery(ctx)
}

// Revoke is nonblocking admission withdrawal; it is not termination evidence.
func (p *Prepared) Revoke() error {
	if p == nil || p.child == nil {
		return ErrConfig
	}
	p.state.Store(preparedRevoked)
	return nil
}

func (p *Prepared) Wait(ctx context.Context) error {
	if p == nil || p.child == nil || p.state.Load() != preparedStarted {
		return ErrConfig
	}
	err := p.child.Wait(ctx)
	if err != nil && err != ErrExit {
		return err
	}
	if p.closeQuery(ctx) != nil {
		return ErrUnconfirmed
	}
	return err
}

// WaitExit is observation through this same retained owner, not admission.
// Revoke does not erase an observed exit or prevent cleanup-time observation.
// Suspended/forced exits are not natural command success; the caller separately
// owns that distinction. No inspection/query handle is closed by this method.
func (p *Prepared) WaitExit(ctx context.Context) (uint32, error) {
	if p == nil || p.child == nil {
		return 0, ErrConfig
	}
	return p.child.WaitExit(ctx)
}
