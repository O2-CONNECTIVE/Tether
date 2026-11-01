package operatorauth

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"sync"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// raw input は従来の所有移譲済み read。新 typed 経路は具体 SoleInput だけを
// pure 保持し、Start 後の Move 実 return を b.input に保持する。両入力は排他。
// supplier は Batch 準備時に単一 owner へ束縛され、Start 内でだけ取得する。
type nativeBatchSpec struct {
	sidPolicy  SIDPolicy
	supplier   *supplierLease
	input      uintptr
	launch     nativeLaunchSpec
	report     reportConfig
	producer   profilePipeProducer
	soleInput  *serviceready.SoleInput
	inputLease *endpointCandidateLease
}
type batchOwner struct {
	issuer *ephemeral.Issuer
	owner  *serviceauthowner.Owner
}
type batchReport interface {
	childWriter() uintptr
	releaseWriter(context.Context) error
	drain(context.Context) (reportResult, error)
	close(context.Context) error
}
type batchProfile interface {
	childReader() uintptr
	launchID() string
	releaseReader(context.Context) error
	sendAfterResume(context.Context) error
	close(context.Context) error
}
type batchChild interface {
	resume(context.Context) error
	recheck(context.Context) error
	waitExit(context.Context) (uint32, error)
	terminate() error
	close(context.Context) error
}
type nativeBatchOps struct {
	scope         *batchScope
	moveSoleInput func(context.Context, *serviceready.SoleInput) (uintptr, error)
	parent        func(context.Context) error
	clean         func() error
	newReport     func(context.Context, reportConfig) (batchReport, error)
	newProfile    func(context.Context) (batchProfile, error)
	duplicateRead func(uintptr) (uintptr, error)
	start         func(context.Context, nativeLaunchSpec) (batchChild, error)
	closeHandle   func(uintptr) error
}
type nativeBatch struct {
	uncertain                  bool
	self                       *nativeBatch
	issuer                     *ephemeral.Issuer
	serviceFactory             *bootstrapauth.Factory
	mu                         sync.Mutex
	spec                       nativeBatchSpec
	request                    context.Context
	started, starting, closing bool
	startCancel                context.CancelFunc
	collectCancel              context.CancelFunc
	operation                  chan struct{}
	revokeOnce                 sync.Once
	owner                      *serviceauthowner.Owner
	revokeErr                  error
	startDone                  chan struct{}
	ops                        nativeBatchOps
	input, readCopy            uintptr
	helperToken                uintptr
	report                     batchReport
	profile                    batchProfile
	child                      batchChild
	collector                  *Collector
	ready, childClosed         bool
	fatal                      error
}

func (s nativeBatchSpec) valid() bool {
	if s.sidPolicy != (SIDPolicy{}) {
		p, ok := s.producer.(*sidProfileProvider)
		if !ok || p == nil || !s.sidPolicy.valid() || p.material.profile.sid != s.sidPolicy || p.material.launchID != s.launch.launchID || p.material.cleanup != s.launch.cleanup || s.report.guestSID != s.sidPolicy.GuestSID {
			return false
		}
	} else if _, ok := s.producer.(*sidProfileProvider); ok {
		return false
	}
	l := s.launch
	inputValid := helperHandle(s.input) && s.input != l.primaryToken && s.soleInput == nil && s.inputLease == nil
	if s.soleInput != nil {
		inputValid = s.input == 0 && s.inputLease != nil
		if s.sidPolicy != (SIDPolicy{}) {
			inputValid = s.input == 0 && s.inputLease == nil
		}
	}
	return inputValid && l.operatorRead == 0 && l.reportWrite == 0 && l.profileRead == 0 &&
		localHelperImage(l.imagePath) && l.imageSHA256 != ([32]byte{}) && profileLaunchID(l.launchID) && l.maxImageBytes > 0 && l.maxImageBytes <= 1<<30 &&
		(helperHandle(l.primaryToken) && s.supplier == nil || l.primaryToken == 0 && s.supplier != nil && s.supplier.matchesLaunch(l)) && l.cleanup > 0 && l.processSDDL != "" && l.threadSDDL != "" && l.helperIdentityValid() && l.helperReportSID() == s.report.helperSID && s.report.valid()
}

// prepareBatch only validates and constructs the retained owner. It performs no native I/O.
func prepareBatch(s nativeBatchSpec, owner batchOwner, ops nativeBatchOps) (*nativeBatch, error) {
	if !s.valid() || s.soleInput != nil && ops.moveSoleInput == nil || owner.issuer == nil || !owner.owner.MatchesIssuer(owner.issuer) || ops.parent == nil || ops.clean == nil || ops.newReport == nil || ops.newProfile == nil || ops.duplicateRead == nil || ops.start == nil || ops.closeHandle == nil {
		return nil, ErrUnconfigured
	}
	b := &nativeBatch{issuer: owner.issuer, ops: ops, input: s.input, helperToken: s.launch.primaryToken, spec: s, startDone: make(chan struct{}), operation: make(chan struct{}, 1), owner: owner.owner}
	b.self = b
	// 同じ lease を別 Batch へ渡さない。束縛は native 取得より前に行う。
	if s.supplier != nil && !s.supplier.bind(b) {
		return nil, ErrUnconfigured
	}
	b.collector = &Collector{sidPolicy: s.sidPolicy, launchID: s.launch.launchID, authOwner: owner.owner, authIssuer: owner.issuer, cleanup: s.launch.cleanup, ops: collectorOps{
		check: b.recheck, drain: b.drain, terminate: b.terminate, wait: b.wait,
		closeIO: b.closeOwned, closeProcess: b.confirmClosed,
		revoke: b.revoke, reclaim: func(c context.Context) error {
			if e := b.awaitStart(c); e != nil {
				return e
			}
			if e := b.cleanupGuard(); e != nil {
				return e
			}
			return b.owner.AwaitWorkload(c)
		},
	}}
	return b, nil
}

// startBatch is the compatibility path for package fixtures. Service registers
// the prepared owner before calling Start.
func startBatch(ctx context.Context, s nativeBatchSpec, owner batchOwner, ops nativeBatchOps) (*nativeBatch, error) {
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		return nil, err
	}
	return b, b.Start(ctx)
}

// Start admits one attempt against the original finite request. Completion is
// acknowledged only after every constructor has returned its retained owner;
// cancellation does not stand in for that acknowledgement.
func (b *nativeBatch) Start(ctx context.Context) (err error) {
	if b == nil || b.self != b || b.collector == nil {
		return ErrUnconfigured
	}
	b.mu.Lock()
	if b.started || b.closing {
		b.mu.Unlock()
		return ErrDenied
	}
	b.collector.mu.Lock()
	if b.collector.used || b.collector.failed || b.collector.closed {
		b.collector.mu.Unlock()
		b.mu.Unlock()
		return ErrDenied
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.request = ctx
	b.collector.request = ctx
	b.collector.mu.Unlock()
	b.started, b.starting = true, true
	ctx, b.startCancel = context.WithCancel(ctx)
	cancel := b.startCancel
	b.mu.Unlock()
	s, ops := b.spec, b.ops
	defer func() {
		if recover() != nil {
			b.mu.Lock()
			b.uncertain = true
			b.mu.Unlock()
			err = ErrUnconfirmed
		}
		cancel()
		b.mu.Lock()
		if b.closing && err == nil {
			err = ErrDenied
		}
		b.ready = err == nil
		b.starting = false
		b.startCancel = nil
		close(b.startDone)
		b.mu.Unlock()
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), s.launch.cleanup)
			defer cancel()
			err = errors.Join(err, b.Close(cleanup))
		}
	}()
	if p, ok := s.producer.(profileLifetimeProducer); ok {
		if e := p.bindRequest(b.request); e != nil {
			return e
		}
	}
	if !b.admitted(ctx) {
		return ErrDenied
	}
	if ops.scope != nil {
		if e := ops.scope.acquire(ctx); e != nil {
			return b.retainScopeFailure(e)
		}
	}
	if s.supplier != nil {
		// 部分取得や遅延 return は lease が保持し、失敗時も同じ Close が回収する。
		if e := s.supplier.acquire(ctx, b); e != nil {
			return b.retainScopeFailure(e)
		}
		token := s.supplier.token()
		if !helperHandle(token) || token == s.input || ops.scope != nil && token == ops.scope.token {
			return ErrDenied
		}
		s.launch.primaryToken = token
		b.mu.Lock()
		b.helperToken = token
		b.mu.Unlock()
	}
	if e := b.parent(ctx); e != nil {
		return e
	}
	if s.soleInput != nil {
		if e := b.moveInput(ctx); e != nil {
			return e
		}
		if e := b.parent(ctx); e != nil {
			return e
		}
	}
	profile, e := ops.newProfile(ctx)
	b.mu.Lock()
	b.profile = profile
	b.mu.Unlock()
	if alias := b.checkMovedPipeAliases(profile, nil); alias != nil {
		return errors.Join(alias, b.retainScopeFailure(e))
	}
	if e != nil || profile == nil {
		return errors.Join(ErrUnconfigured, b.retainScopeFailure(e))
	}
	if e = b.parent(ctx); e != nil {
		return e
	}
	p, e := ops.newReport(ctx, s.report)
	b.mu.Lock()
	b.report = p
	b.mu.Unlock()
	if alias := b.checkMovedPipeAliases(profile, p); alias != nil {
		return errors.Join(alias, b.retainScopeFailure(e))
	}
	if e != nil || p == nil {
		return ErrDenied
	}
	if e = b.parent(ctx); e != nil {
		return e
	}
	b.readCopy, e = ops.duplicateRead(b.input)
	if !helperHandle(b.readCopy) || b.readCopy == b.input || b.readCopy == s.launch.primaryToken || b.readCopy == p.childWriter() || b.readCopy == profile.childReader() {
		b.readCopy = 0
		return ErrDenied
	}
	if e != nil || !helperHandle(b.readCopy) || b.readCopy == b.input {
		return ErrDenied
	}
	if e = b.parent(ctx); e != nil {
		return e
	}
	l := s.launch
	l.operatorRead = b.readCopy
	l.reportWrite = p.childWriter()
	l.profileRead = profile.childReader()
	if !profileLaunchID(l.launchID) || profile.launchID() != l.launchID {
		return ErrDenied
	}
	if !helperHandle(l.profileRead) || l.profileRead == b.input || l.profileRead == b.readCopy || l.profileRead == l.reportWrite || l.profileRead == l.primaryToken {
		return ErrDenied
	}
	c, e := ops.start(ctx, l)
	b.mu.Lock()
	b.child = c
	b.mu.Unlock()
	if e != nil || c == nil {
		return ErrDenied
	}
	if e = b.parent(ctx); e != nil {
		return e
	}
	if e = profile.releaseReader(ctx); e != nil {
		return e
	}
	if e = p.releaseWriter(ctx); e != nil {
		return e
	}
	if e = b.closeInputs(ctx); e != nil {
		return e
	}
	if e = b.parent(ctx); e != nil {
		return e
	}
	if e = c.resume(ctx); e != nil {
		return e
	}
	if e = b.parent(ctx); e != nil {
		return e
	}
	if e = c.recheck(ctx); e != nil {
		return e
	}
	// The recheck was already admitted, but profile transfer starts a separate
	// acquisition. A late recheck cannot carry admission across revocation.
	if !b.admitted(ctx) {
		return ErrDenied
	}
	if e = profile.sendAfterResume(ctx); e != nil {
		return e
	}
	if e = b.parent(ctx); e != nil {
		return e
	}
	if e = c.recheck(ctx); e != nil {
		return e
	}
	if !b.admitted(ctx) {
		return ErrDenied
	}
	return nil
}
func (b *nativeBatch) parent(ctx context.Context) error {
	if !b.admitted(ctx) {
		return ErrDenied
	}
	if b.ops.scope != nil {
		if e := b.ops.scope.recheck(ctx); e != nil {
			return b.retainScopeFailure(e)
		}
	}
	if b.spec.supplier != nil {
		if e := b.spec.supplier.recheck(ctx, b); e != nil {
			return b.retainScopeFailure(e)
		}
	}
	if e := b.ops.parent(ctx); e != nil {
		if errors.Is(e, ErrFatal) {
			b.mu.Lock()
			b.fatal = e
			b.mu.Unlock()
		}
		return e
	}
	if p, ok := b.spec.producer.(profileLifetimeProducer); ok {
		if e := p.recheck(ctx); e != nil {
			return b.retainScopeFailure(e)
		}
	}
	if !b.admitted(ctx) {
		return ErrDenied
	}
	return nil
}
func (b *nativeBatch) cleanupGuard() error {
	b.mu.Lock()
	e := b.fatal
	starting := b.starting
	uncertain := b.uncertain
	b.mu.Unlock()
	if starting {
		return ErrUnconfirmed
	}
	if e != nil {
		return e
	}
	if uncertain && b.spec.soleInput != nil {
		return ErrUnconfirmed
	}
	if b.ops.scope != nil {
		if uncertain {
			return ErrUnconfirmed
		}
		return b.retainScopeFailure(b.ops.scope.clean())
	}
	if e = b.ops.clean(); e != nil {
		fatal := errors.Join(ErrFatal, e)
		b.mu.Lock()
		b.fatal = fatal
		b.mu.Unlock()
		return fatal
	}
	return nil
}

func (b *nativeBatch) retainScopeFailure(err error) error {
	if errors.Is(err, ErrFatal) {
		b.mu.Lock()
		b.fatal = err
		b.mu.Unlock()
	}
	return err
}
func (b *nativeBatch) recheck(ctx context.Context) error {
	b.mu.Lock()
	ready, child := b.ready && !b.closing, b.child
	b.mu.Unlock()
	if !ready || child == nil {
		return ErrDenied
	}
	if e := b.parent(ctx); e != nil {
		return e
	}
	return child.recheck(ctx)
}
func (b *nativeBatch) drain(ctx context.Context) (reportResult, error) {
	if b.report == nil {
		return reportResult{}, ErrDenied
	}
	return b.report.drain(ctx)
}
func (b *nativeBatch) terminate() error {
	if e := b.cleanupGuard(); e != nil {
		return e
	}
	if b.child == nil {
		return nil
	}
	return b.child.terminate()
}
func (b *nativeBatch) wait(ctx context.Context) (uint32, error) {
	if e := b.cleanupGuard(); e != nil {
		return 0, e
	}
	if b.child == nil {
		return 0, nil
	}
	return b.child.waitExit(ctx)
}
func (b *nativeBatch) closeInputs(ctx context.Context) error {
	var errs []error
	for _, h := range []*uintptr{&b.readCopy, &b.input} {
		if *h == 0 {
			continue
		}
		if !reportContextValid(ctx) {
			return ErrUnconfirmed
		}
		if e := b.ops.closeHandle(*h); e != nil {
			errs = append(errs, ErrUnconfirmed)
		} else {
			*h = 0
		}
	}
	return errors.Join(errs...)
}
func (b *nativeBatch) closeOwned(ctx context.Context) error {
	if e := b.cleanupGuard(); e != nil {
		return e
	}
	if b.profile != nil {
		if e := b.profile.close(ctx); e != nil {
			return e
		}
	}
	if b.report != nil {
		if e := b.report.close(ctx); e != nil {
			return e
		}
	}
	if e := b.closeInputs(ctx); e != nil {
		return e
	}
	if b.child != nil {
		if e := b.child.close(ctx); e != nil {
			return e
		}
	}
	if p, ok := b.spec.producer.(profileLifetimeProducer); ok {
		if e := p.close(ctx); e != nil {
			return b.retainScopeFailure(e)
		}
	}
	// child close が保持 process の終了と Job/I/O 回収を確認した後だけ、
	// 借用 token の解放と同じ起動世代の supplier 停止へ進む。
	if b.spec.supplier != nil {
		if e := b.spec.supplier.close(ctx, b); e != nil {
			return b.retainScopeFailure(e)
		}
	}
	if b.ops.scope != nil {
		if e := b.ops.scope.close(ctx); e != nil {
			return b.retainScopeFailure(e)
		}
	}
	if !reportContextValid(ctx) {
		return ErrUnconfirmed
	}
	b.mu.Lock()
	b.childClosed = true
	b.mu.Unlock()
	return nil
}

// Collectorの最後のcontext-free段階では新しいOS callを開始しない。
// 有限closeOwned内でJob/保持process/全I/Oの回収を確認済みの場合だけ終端とする。
func (b *nativeBatch) confirmClosed() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.childClosed || b.uncertain {
		return ErrUnconfirmed
	}
	return nil
}
