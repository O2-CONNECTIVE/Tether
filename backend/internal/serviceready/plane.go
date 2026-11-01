package serviceready

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

type preparedScope interface {
	Recheck(context.Context) error
	Close(context.Context) error
}
type serviceRef interface {
	inspect() (serviceState, error)
	close() error
}
type processRef interface {
	inspect() (processState, error)
	inspectToken(uintptr) (processTokenState, error)
	close() error
}
type listenerRef interface {
	inspect() (listenerState, error)
	connect() (operation, error)
	close() error
}
type kernel interface {
	checkCaller(context.Context) error
	prepare(context.Context, uintptr) (preparedScope, error)
	threadID() uint32
	currentPID() uint32
	openService(string) (serviceRef, error)
	openProcess(uint32) (processRef, error)
	createListener(string, string) (listenerRef, error)
}
type serviceState struct {
	state, pid, kind uint32
	commandLine      string
	security         SecurityPolicy
}
type processState struct {
	pid      uint32
	creation uint64
	alive    bool
	security SecurityPolicy
}
type listenerState struct {
	kind, flags, mode, maxInstances, inBytes, outBytes, serverPID, handleFlags uint32
	security                                                                   SecurityPolicy
}

// Planeは初回利用後にコピーせず、回収不明でも同じpointerを保持する。
type Plane struct {
	c                          Config
	api                        kernel
	gate                       sync.Mutex
	scope                      preparedScope
	svc                        serviceRef
	process                    processRef
	listener                   listenerRef
	op                         *ownedOperation
	thread, pid                uint32
	creation                   uint64
	tokenEvidence              *processTokenState
	prepared, running          bool
	revoked, stopped, poisoned atomic.Bool
}

func finite(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Deadline()
	return ok
}
func live(ctx context.Context) bool { return finite(ctx) && ctx.Err() == nil }
func (f *Factory) Prepare(ctx context.Context) (p *Plane, err error) {
	p, err = f.prepareQuery(ctx)
	if err != nil {
		return p, err
	}
	defer func() {
		if err != nil {
			p.invalidate(err)
		}
	}()
	p.listener, err = p.api.createListener(p.c.Endpoint, listenerSDDL(p.c.Profile))
	if err != nil || p.listener == nil {
		return p, errors.Join(ErrDenied, err)
	}
	if err = p.inspectListener(); err != nil {
		return p, err
	}
	raw, startErr := p.listener.connect()
	if raw != nil {
		p.op = &ownedOperation{raw: raw}
	}
	if startErr != nil || raw == nil {
		return p, errors.Join(ErrDenied, startErr)
	}
	if _, err = p.op.tryStep(false); err != nil {
		return p, err
	}
	if _, err = p.verify(ctx, true); err != nil {
		return p, err
	}
	p.prepared = true
	return p, nil
}
func (f *Factory) prepareQuery(ctx context.Context) (p *Plane, err error) {
	if f == nil || !f.configured {
		return nil, ErrConfig
	}
	if !live(ctx) {
		return nil, ErrDenied
	}
	api := f.api
	if api == nil {
		api = platformKernel(f.auth)
	}
	if api == nil {
		return nil, ErrUnavailable
	}
	p = &Plane{c: f.c, api: api, thread: api.threadID()}
	defer func() {
		if err != nil {
			p.invalidate(err)
		}
	}()
	if p.thread == 0 {
		return p, ErrFatal
	}
	p.scope, err = api.prepare(ctx, p.c.PrimaryToken)
	if err != nil {
		return p, err
	}
	if p.scope == nil {
		return p, ErrDenied
	}
	if err = p.subject(ctx); err != nil {
		return p, err
	}
	p.pid = api.currentPID()
	if p.pid == 0 {
		return p, ErrDenied
	}
	p.svc, err = api.openService(p.c.Profile.ServiceName)
	if err != nil || p.svc == nil {
		return p, errors.Join(ErrDenied, err)
	}
	p.process, err = api.openProcess(p.pid)
	if err != nil || p.process == nil {
		return p, errors.Join(ErrDenied, err)
	}
	if _, err = p.verify(ctx, false); err != nil {
		return p, err
	}
	return p, nil
}

func (p *Plane) invalidate(err error) error {
	p.revoked.Store(true)
	if errors.Is(err, ErrFatal) {
		p.poisoned.Store(true)
	}
	return err
}
func (p *Plane) subject(ctx context.Context) error {
	if p.poisoned.Load() || p.thread == 0 || p.api.threadID() != p.thread {
		return ErrFatal
	}
	if !live(ctx) || p.scope == nil {
		return ErrDenied
	}
	return p.scope.Recheck(ctx)
}
func (p *Plane) verify(ctx context.Context, listener bool) (serviceState, error) {
	if err := p.subject(ctx); err != nil {
		return serviceState{}, err
	}
	s, err := p.svc.inspect()
	if err != nil {
		return s, err
	}
	if s.kind != 0x10 || s.commandLine != p.c.ServiceCommandLine || !sameSecurity(s.security, p.c.ServiceSecurity) || s.state != 2 && s.state != 4 || s.state == 4 && s.pid != p.pid || p.running && s.state != 4 {
		return s, ErrDenied
	}
	process, err := p.process.inspect()
	if err != nil {
		return s, err
	}
	if !process.alive || process.pid != p.pid || p.api.currentPID() != p.pid || process.creation == 0 || !sameSecurity(process.security, p.c.ProcessSecurity) {
		return s, ErrDenied
	}
	if p.creation == 0 {
		p.creation = process.creation
	} else if p.creation != process.creation {
		return s, ErrDenied
	}
	if processQueryExposed(p.c.Profile, p.c.ProcessSecurity) {
		if err := p.verifyProcessToken(); err != nil {
			return s, err
		}
	}
	if listener {
		if err := p.inspectListener(); err != nil {
			return s, err
		}
		if p.op == nil {
			return s, ErrDenied
		}
		if _, err := p.op.tryStep(false); err != nil {
			return s, errors.Join(ErrDenied, err)
		}
	}
	if err := p.subject(ctx); err != nil {
		return s, err
	}
	return s, nil
}
func (p *Plane) Recheck(ctx context.Context) (err error) {
	if p == nil {
		return ErrDenied
	}
	defer func() {
		if err != nil {
			p.invalidate(err)
		}
	}()
	if p.poisoned.Load() {
		return ErrFatal
	}
	if !live(ctx) || !p.prepared || p.revoked.Load() || p.stopped.Load() {
		return ErrDenied
	}
	if !admit(ctx, &p.gate) {
		return ErrDenied
	}
	defer p.gate.Unlock()
	if p.poisoned.Load() {
		return ErrFatal
	}
	if p.revoked.Load() || p.stopped.Load() {
		return ErrDenied
	}
	_, err = p.verify(ctx, true)
	if err == nil && (p.revoked.Load() || p.stopped.Load()) {
		err = ErrDenied
	}
	return err
}
func (p *Plane) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.revoked.Store(true)
	p.stopped.Store(true)
	if p.api != nil && p.thread != 0 && p.api.threadID() != p.thread {
		p.poisoned.Store(true)
	}
	if !finite(ctx) {
		return p.closeResult(errors.Join(ErrConfig, ErrUnconfirmed))
	}
	deadline, _ := ctx.Deadline()
	if end := time.Now().Add(p.c.CleanupTimeout); end.Before(deadline) {
		deadline = end
	}
	cleanup, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if !admit(cleanup, &p.gate) {
		return p.closeResult(ErrUnconfirmed)
	}
	defer p.gate.Unlock()
	// connectの未完了と独立に、query refsとpreflightの回収を試みる。
	complete := p.op == nil
	if p.op != nil {
		complete, _ = p.op.tryStep(true)
	}
	var result error
	if p.svc != nil {
		if err := p.svc.close(); err != nil {
			result = errors.Join(result, ErrUnconfirmed, err)
		} else {
			p.svc = nil
		}
	}
	if p.process != nil {
		if err := p.process.close(); err != nil {
			result = errors.Join(result, ErrUnconfirmed, err)
		} else {
			p.process = nil
		}
	}
	if p.scope != nil {
		err := p.scope.Close(cleanup)
		// authのexact ErrFatalだけは全refs回収済みの契約。unknown joinへ拡張しない。
		if err == nil || err == ErrFatal {
			p.scope = nil
			if err == ErrFatal {
				p.poisoned.Store(true)
				result = errors.Join(result, ErrFatal)
			}
		} else {
			result = errors.Join(result, ErrUnconfirmed, err)
			if errors.Is(err, ErrFatal) {
				p.poisoned.Store(true)
			}
		}
	}
	for !complete {
		if cleanup.Err() != nil || !time.Now().Before(deadline) {
			return p.closeResult(errors.Join(result, ErrUnconfirmed))
		}
		pause(cleanup)
		complete, _ = p.op.tryStep(true)
	}
	if p.listener != nil {
		if err := p.listener.close(); err != nil {
			result = errors.Join(result, ErrUnconfirmed, err)
		} else {
			p.listener = nil
		}
	}
	return p.closeResult(result)
}
func (p *Plane) closeResult(err error) error {
	if errors.Is(err, ErrFatal) {
		p.poisoned.Store(true)
	}
	if p.poisoned.Load() {
		return errors.Join(ErrFatal, err)
	}
	return err
}
func admit(ctx context.Context, mu *sync.Mutex) bool {
	for {
		if mu.TryLock() {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		deadline, _ := ctx.Deadline()
		if !time.Now().Before(deadline) {
			return false
		}
		pause(ctx)
	}
}
func pause(ctx context.Context) {
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// ConfirmRunningはstatus channelの送信ではなくheld serviceのkernel照会を確認する。
// START_PENDINGのPIDを根拠にせず、他state/identity/期限/admission失敗で不可逆に失効する。
func (p *Plane) ConfirmRunning(ctx context.Context) (err error) {
	if p == nil {
		return ErrDenied
	}
	defer func() {
		if err != nil {
			p.invalidate(err)
		}
	}()
	if p.poisoned.Load() {
		return ErrFatal
	}
	if !live(ctx) || !p.prepared || p.revoked.Load() || p.stopped.Load() {
		return ErrDenied
	}
	if !admit(ctx, &p.gate) {
		return ErrDenied
	}
	defer p.gate.Unlock()
	for {
		if p.poisoned.Load() {
			return ErrFatal
		}
		if p.revoked.Load() || p.stopped.Load() || !live(ctx) {
			return ErrDenied
		}
		state, err := p.verify(ctx, true)
		if err != nil {
			return err
		}
		if state.state == 4 {
			if p.poisoned.Load() {
				return ErrFatal
			}
			if p.revoked.Load() || p.stopped.Load() {
				return ErrDenied
			}
			p.running = true
			return nil
		}
		pause(ctx)
	}
}

func (p *Plane) inspectListener() error {
	l, err := p.listener.inspect()
	if err != nil {
		return err
	}
	if l.kind != 3 || l.flags != 5 || l.mode != 2 || l.maxInstances != 1 || l.inBytes != 16384 || l.outBytes != 16384 || l.serverPID != p.pid || l.handleFlags != 0 || !sameSecurity(l.security, listenerSecurity(p.c.Profile)) {
		return ErrDenied
	}
	return nil
}
