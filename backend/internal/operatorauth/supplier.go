package operatorauth

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

const (
	supplierOwnProcess      = 0x10
	supplierDemandStart     = 3
	supplierStopped         = 1
	supplierRunning         = 4
	supplierSIDUnrestricted = 1
	supplierLocalService    = "S-1-5-19"
)

// These private values come from held native references. No public constructor
// accepts a snapshot or a successful-inspection flag from a request.
type supplierSnapshot struct {
	pid                                             uint32
	creation, tokenID, modifiedID                   uint64
	serviceType, startType, state, sidType          uint32
	failureActions                                  uint32
	serviceAccount, commandLine, imagePath          string
	primary                                         tokenProjection
	group                                           helperServiceGroup
	serviceSecurity, processSecurity, tokenSecurity serviceready.SecurityPolicy
}

// supplierGeneration is the process that the same evidence observed after its
// own start request. It is a correlation value, not issuance provenance.
type supplierGeneration struct {
	pid      uint32
	creation uint64
}

// Cleanup is split so a retry resumes at the failed phase: release ends the
// borrowed handle, stop asks SCM to stop only this generation and waits for its
// actual exit, and close returns the remaining references. Registration
// removal belongs to the provisioning owner.
type supplierEvidence interface {
	generation() supplierGeneration
	inspect(context.Context) (supplierSnapshot, error)
	token() uintptr
	release(context.Context) error
	stop(context.Context) error
	close(context.Context) error
}
type supplierKernel interface {
	acquire(context.Context, HelperSupplierConfig) (supplierEvidence, error)
}

// The existing Batch owns this lease before acquisition. The token is borrowed
// only by its child launcher. Revocation closes new borrowing at once; native
// references are returned only through the same owner after the child, I/O and
// worker have been confirmed by that Batch.
type supplierLease struct {
	mu                             sync.Mutex
	revoked                        atomic.Bool
	parent                         bootstrapauth.Profile
	policy                         SIDPolicy
	config                         HelperSupplierConfig
	api                            supplierKernel
	owner                          *nativeBatch
	evidence                       supplierEvidence
	started                        supplierGeneration
	baseline                       supplierSnapshot
	borrowed                       uintptr
	expires                        time.Time
	attempted, accepted, uncertain bool
	released, stopped, closed      bool
}

func newSupplierLease(parent bootstrapauth.Profile, p SIDPolicy, c HelperSupplierConfig, k supplierKernel) (*supplierLease, error) {
	if k == nil || ValidateHelperSupplierConfig(parent, p, c) != nil {
		return nil, ErrUnconfigured
	}
	return &supplierLease{parent: parent, policy: p, config: CloneHelperSupplierConfig(c), api: k}, nil
}

// bind records the single Batch before any native call. A lease cannot be moved
// to a second owner, including after a failed or revoked attempt.
func (s *supplierLease) bind(owner *nativeBatch) bool {
	if s == nil || owner == nil || !s.mu.TryLock() {
		return false
	}
	defer s.mu.Unlock()
	if s.owner != nil || s.attempted || s.closed || s.revoked.Load() {
		return false
	}
	s.owner = owner
	return true
}

// revoke never waits for an in-flight acquisition. It only stops new borrowing.
func (s *supplierLease) revoke() {
	if s != nil {
		s.revoked.Store(true)
	}
}

func (s *supplierLease) acquire(ctx context.Context, owner *nativeBatch) (result error) {
	if s == nil || owner == nil {
		return ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner != owner || s.attempted || s.closed || s.api == nil || s.revoked.Load() {
		return ErrDenied
	}
	s.attempted = true
	if !finite(ctx) {
		return ErrDenied
	}
	defer func() {
		if recover() != nil {
			s.accepted, s.uncertain, s.borrowed = false, true, 0
			result = ErrUnconfirmed
		}
	}()
	s.uncertain = true
	// The supplier's compiled lifetime starts no earlier than this request, so
	// borrowing ends by RunBudget measured from before the start request.
	s.expires = time.Now().Add(s.config.RunBudget)
	evidence, err := s.api.acquire(ctx, CloneHelperSupplierConfig(s.config))
	// A late return is retained before its error, context or values are judged.
	s.evidence, s.uncertain = evidence, false
	if err != nil || evidence == nil || !finite(ctx) || s.revoked.Load() {
		return errors.Join(ErrDenied, err)
	}
	s.started = evidence.generation()
	observed, err := evidence.inspect(ctx)
	token := evidence.token()
	if err != nil || !finite(ctx) || s.revoked.Load() || !s.withinBudget() || !supplierSnapshotValid(s.config, s.started, observed) || !helperHandle(token) {
		return errors.Join(ErrDenied, err)
	}
	s.baseline, s.borrowed, s.accepted = cloneSupplierSnapshot(observed), token, true
	return nil
}

// supplierSnapshotValid compares every value with fixed configuration and the
// generation started by the same evidence. Nothing is filled from the snapshot.
func supplierSnapshotValid(c HelperSupplierConfig, g supplierGeneration, v supplierSnapshot) bool {
	return g.pid != 0 && g.creation != 0 && v.pid == g.pid && v.creation == g.creation && v.tokenID != 0 && v.modifiedID != 0 &&
		v.serviceType == supplierOwnProcess && v.startType == supplierDemandStart && v.state == supplierRunning && v.sidType == supplierSIDUnrestricted && v.failureActions == 0 &&
		v.serviceAccount == `NT AUTHORITY\LocalService` && v.commandLine == c.ServiceCommandLine && v.imagePath == c.Image.Path &&
		v.primary.kind == 1 && v.primary.user == supplierLocalService && v.primary.authentication != 0 && !v.primary.restricted && !v.primary.appContainer &&
		v.group.matches(c.ServiceSID) && supplierServiceProtected(supplierWithSecurity(c, v)) && supplierProcessProtected(supplierWithSecurity(c, v)) &&
		reflect.DeepEqual(v.serviceSecurity, c.ServiceSecurity) && reflect.DeepEqual(v.processSecurity, c.ProcessSecurity) &&
		supplierTokenProtected(v.tokenSecurity, c.ServiceSID) && reflect.DeepEqual(v.tokenSecurity, c.TokenSecurity)
}

// supplierWithSecurity judges the observed service/process security, not the
// Record copy, with the same predicates as Record validation.
func supplierWithSecurity(c HelperSupplierConfig, v supplierSnapshot) HelperSupplierConfig {
	c.ServiceSecurity, c.ProcessSecurity = v.serviceSecurity, v.processSecurity
	return c
}

type supplierStopStep uint8

const (
	supplierStopWait supplierStopStep = iota
	supplierStopRequest
	supplierStopConfirmed
	// supplierStopUnowned leaves cleanup unconfirmed and issues no stop.
	supplierStopUnowned
)

// supplierStopDecision is the stop rule after this owner requested a start.
// Only the generation pinned during acquisition may be stopped or counted as
// stopped. Without one, a later RUNNING PID may be another owner's replacement:
// it is never adopted or stopped, and cleanup stays unconfirmed. Any generation
// of this owner then ends only by the supplier's fixed run+cleanup lifetime,
// which is not reported as same-owner cleanup.
func supplierStopDecision(started supplierGeneration, state, pid uint32, exited bool) supplierStopStep {
	switch {
	case started.pid == 0 || started.creation == 0 || pid != 0 && pid != started.pid:
		return supplierStopUnowned
	case exited && state == supplierStopped && pid == 0:
		return supplierStopConfirmed
	case state == supplierRunning && pid == started.pid:
		return supplierStopRequest
	}
	return supplierStopWait
}

func (s *supplierLease) recheck(ctx context.Context, owner *nativeBatch) (result error) {
	if s == nil || !s.mu.TryLock() {
		return ErrDenied
	}
	defer s.mu.Unlock()
	// Another caller cannot change the owner's acceptance.
	if owner == nil || s.owner != owner {
		return ErrDenied
	}
	defer func() {
		if recover() != nil {
			s.accepted, s.uncertain = false, true
			result = ErrUnconfirmed
		}
	}()
	if !finite(ctx) || !s.accepted || s.closed || s.uncertain || s.evidence == nil || s.revoked.Load() {
		s.accepted = false
		return ErrDenied
	}
	v, err := s.evidence.inspect(ctx)
	if err != nil || !finite(ctx) || s.revoked.Load() || !s.withinBudget() || s.evidence.generation() != s.started || !supplierSnapshotValid(s.config, s.started, v) || !reflect.DeepEqual(v, s.baseline) || s.evidence.token() != s.borrowed {
		s.accepted = false
		return errors.Join(ErrDenied, err)
	}
	return nil
}

func (s *supplierLease) token() uintptr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.accepted || s.closed || s.uncertain || s.revoked.Load() || !s.withinBudget() {
		return 0
	}
	return s.borrowed
}

// borrowEnd is the RunBudget end of a lease that lent its token, otherwise the
// zero time, which is already past. The Batch bounds collection by it so no
// result outlives the lending authority.
func (s *supplierLease) borrowEnd() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.accepted || s.closed || s.uncertain {
		return time.Time{}
	}
	return s.expires
}

// withinBudget uses the monotonic reading. Expiry only closes new borrowing;
// reclamation stays with the same owner's close.
func (s *supplierLease) withinBudget() bool {
	return !s.expires.IsZero() && time.Now().Before(s.expires)
}

// close is called by the owner only after its child, I/O and worker are
// confirmed. A failure keeps the evidence and the completed phases so the same
// owner retries from the failed phase with a fresh finite context.
func (s *supplierLease) close(ctx context.Context, owner *nativeBatch) (result error) {
	if s == nil || !s.mu.TryLock() {
		return ErrUnconfirmed
	}
	defer s.mu.Unlock()
	if owner == nil || s.owner != owner {
		return ErrUnconfirmed
	}
	s.revoked.Store(true)
	s.accepted = false
	if !finite(ctx) || s.uncertain {
		return ErrUnconfirmed
	}
	if s.closed {
		return nil
	}
	defer func() {
		if recover() != nil {
			s.uncertain = true
			result = ErrUnconfirmed
		}
	}()
	if s.evidence != nil {
		if !s.released {
			if err := s.evidence.release(ctx); err != nil {
				return errors.Join(ErrUnconfirmed, err)
			}
			s.released, s.borrowed = true, 0
		}
		if !s.stopped {
			if err := s.evidence.stop(ctx); err != nil {
				return errors.Join(ErrUnconfirmed, err)
			}
			s.stopped = true
		}
		if err := s.evidence.close(ctx); err != nil {
			return errors.Join(ErrUnconfirmed, err)
		}
	}
	if !finite(ctx) {
		return ErrUnconfirmed
	}
	s.closed, s.borrowed = true, 0
	return nil
}

func cloneSupplierSnapshot(v supplierSnapshot) supplierSnapshot {
	v.serviceSecurity = supplierCloneSecurity(v.serviceSecurity)
	v.processSecurity = supplierCloneSecurity(v.processSecurity)
	v.tokenSecurity = supplierCloneSecurity(v.tokenSecurity)
	return v
}

// supplierRegistered is one SCM registration created by this provisioning
// owner. remove issues deletion only while SCM reports it STOPPED without a
// process, and succeeds only after the name no longer resolves; close returns
// the handles. A registration that existed before is never held, so never removed.
type supplierRegistered interface {
	remove(context.Context) error
	close(context.Context) error
}
type supplierRegistrar interface {
	register(context.Context, HelperSupplierConfig) (supplierRegistered, error)
}

// SupplierRegistration is the temporary supplier Service of one original
// provisioning owner, created from the same Record before the Service accepts
// requests. The owner removes it only after the Service's inner cleanup has
// confirmed every Batch, generation and child. A partial or uncertain result
// keeps the reference; a retry by the same owner resumes at the failed phase.
type SupplierRegistration struct {
	mu                         sync.Mutex
	held                       supplierRegistered
	removed, closed, uncertain bool
}

// RegisterHelperSupplier is closed by the same gate as acquisition. An
// unconfigured result creates nothing; any other result may hold a partial
// registration that the caller must Close.
func RegisterHelperSupplier(ctx context.Context, parent bootstrapauth.Profile, policy SIDPolicy, c HelperSupplierConfig) (*SupplierRegistration, error) {
	if ValidateHelperSupplierConfig(parent, policy, c) != nil || !supplierAcquisitionOpen(parent, c) {
		return nil, ErrUnconfigured
	}
	return registerHelperSupplier(ctx, parent, policy, c, nativeSupplierRegistrar(parent))
}

func registerHelperSupplier(ctx context.Context, parent bootstrapauth.Profile, policy SIDPolicy, c HelperSupplierConfig, k supplierRegistrar) (r *SupplierRegistration, result error) {
	if k == nil || ValidateHelperSupplierConfig(parent, policy, c) != nil {
		return nil, ErrUnconfigured
	}
	if !finite(ctx) {
		return nil, ErrDenied
	}
	r = &SupplierRegistration{uncertain: true}
	defer func() {
		if recover() != nil {
			r.uncertain = true
			result = errors.Join(ErrFatal, ErrUnconfirmed)
		}
	}()
	held, err := k.register(ctx, CloneHelperSupplierConfig(c))
	// A late return is retained before its error or context is judged.
	r.held, r.uncertain = held, errors.Is(err, ErrFatal)
	if err != nil || held == nil || !finite(ctx) {
		return r, errors.Join(ErrDenied, err)
	}
	return r, nil
}

// Close removes only this owner's registration, then returns its handles.
func (r *SupplierRegistration) Close(ctx context.Context) (result error) {
	if r == nil {
		return nil
	}
	if !r.mu.TryLock() {
		return ErrUnconfirmed
	}
	defer r.mu.Unlock()
	if r.uncertain {
		return errors.Join(ErrFatal, ErrUnconfirmed)
	}
	if !finite(ctx) {
		return ErrUnconfirmed
	}
	if r.closed {
		return nil
	}
	defer func() {
		if recover() != nil {
			result = errors.Join(ErrFatal, ErrUnconfirmed)
		}
		// A fatal owner thread retires; native removal is never resumed on it.
		if errors.Is(result, ErrFatal) {
			r.uncertain = true
		}
	}()
	if r.held != nil {
		if !r.removed {
			if err := r.held.remove(ctx); err != nil {
				return errors.Join(ErrUnconfirmed, err)
			}
			r.removed = true
		}
		if err := r.held.close(ctx); err != nil {
			return errors.Join(ErrUnconfirmed, err)
		}
	}
	if !finite(ctx) {
		return ErrUnconfirmed
	}
	r.closed = true
	return nil
}

func (s *supplierLease) matchesLaunch(l nativeLaunchSpec) bool {
	return s != nil && s.api != nil && l.imagePath == s.config.Image.Path && l.imageSHA256 == s.config.Image.SHA256 && l.maxImageBytes == s.config.Image.MaxBytes &&
		l.helperUserSID == supplierLocalService && l.helperServiceSID == s.config.ServiceSID && l.processSDDL == s.config.ChildProcessSDDL && l.threadSDDL == s.config.ChildThreadSDDL && l.cleanup == s.config.Cleanup
}
