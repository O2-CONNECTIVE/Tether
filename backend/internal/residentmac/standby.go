package residentmac

import (
	"context"
	"errors"
	"log"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/workerowner"
)

const maxStandbyInteger = uint64(9007199254740991)
const standbyCleanup = 40 * time.Second
const standbyProofWait = 25 * time.Second      // collection (15s) and proof write (5s) before OFF ends the control
const initialAuthentication = 30 * time.Second // ON result only, not service lifetime
const sleepTakeWait = 5 * time.Second
const sleepRefusalReserve = 5 * time.Second
const sleepReleaseRetryInitial = time.Second
const sleepReleaseRetryMax = 30 * time.Second
const reconnectWakePoll = time.Second
const reconnectWakeThreshold = 5 * time.Second

type reconnectWakeDetector struct {
	active       bool
	awake        time.Time
	elapsed      time.Time
	network      string
	networkKnown bool
}

func (d *reconnectWakeDetector) observe(elapsed, awake time.Time, network string, networkKnown bool) bool {
	if !d.active {
		d.active = true
		d.awake, d.elapsed = awake, elapsed
		d.network, d.networkKnown = network, networkKnown
		return false
	}
	woke := elapsed.Sub(d.elapsed)-awake.Sub(d.awake) > reconnectWakeThreshold
	networkChanged := networkKnown && d.networkKnown && network != d.network
	d.awake, d.elapsed = awake, elapsed
	if networkKnown {
		d.network, d.networkKnown = network, true
	}
	return woke || networkChanged
}

func reconnectNetworkFingerprint(addresses []string) string {
	copyOf := append([]string(nil), addresses...)
	sort.Strings(copyOf)
	return strings.Join(copyOf, "\n")
}

// Private lifecycle-only fake seam; it cannot mint authority or native work.
type residentConnector interface {
	Run(context.Context) error
	Accept(context.Context) (*agenttransport.ResidentSourceLease, error)
	Stop()
	Close(context.Context) error
	Snapshot() agenttransport.ResidentControlSnapshot
}
type sleepAssertion interface {
	Take() error
	Release() error
}
type sleepOperation struct {
	ctx  context.Context
	done chan struct{}
	err  error // guarded by Standby.sleepMu; immutable once done closes
}
type standbyCycle struct {
	ctx           context.Context
	cancel        context.CancelFunc
	owner         *workerowner.Owner
	done          chan struct{}
	mu            sync.Mutex
	connector     residentConnector
	source        *FixedSource
	lease         *agenttransport.ResidentSourceLease
	work          *agentgateway.ResidentWorkBinding
	proved        chan struct{} // closed once the open source's cleanup proof was attempted
	result        error
	expiryCancel  context.CancelFunc
	initialUntil  time.Time
	until         time.Time // guarded by Standby.mu: the end of standby (#399)
	untilWall     int64     // the same instant on the wall clock, Unix ns
	ranOut        bool      // guarded by Standby.mu: standby was seen to have run out
	runAllowed    bool      // guarded by Standby.mu: the local command-run allowance
	authenticated bool
}

// Standby retains the same transport, native sources and common owner across
// authenticated control generations. Unknown cleanup never releases ownership.
type Standby struct {
	mu               sync.Mutex
	sleepMu          sync.Mutex
	config           StandbyConfig
	sleep            sleepAssertion
	sleepAdmissions  int // guarded by sleepMu
	sleepDesired     bool
	sleepHeld        bool
	sleepErr         error // last Take/Release failure, guarded by sleepMu
	sleepTaking      bool
	sleepReleasing   bool
	sleepTakeWaiters []*sleepOperation
	sleepWorker      bool
	sleepWorkerWake  chan struct{}
	sleepWorkerDone  chan struct{}
	sleepCycleOwner  *workerowner.Owner
	sleepCycleEnded  bool
	sleepNow         func() time.Time
	sleepTimer       func(time.Duration) <-chan time.Time
	sleepBeforeTake  func() // internal scheduling seam for stale-take tests
	factory          *FixedFactory
	makeConnector    func(context.Context) (residentConnector, error)
	clock            func() (mono, wall time.Time) // nil reads elapsedNow and time.Now; tests replace it
	// reportStandby sends one standby report over the control; nil sends it
	// with lease.ReportStandby. Tests replace it: they cannot hold a real lease.
	reportStandby func(*agenttransport.ResidentSourceLease, agentgateway.ResidentStandby)
	updates       chan struct{}
	observation   StandbyObservation
	lastRequest   uint64
	cycle         *standbyCycle
	closed        bool
	// unsent is the one signed cleanup proof a closed control could not carry.
	// It outlives the cycle and is lost with the process.
	unsent struct {
		sync.Mutex
		proof []byte
	}
}

// collect sends the cleanup proof and keeps one it could not write. The
// gateway refuses this member's next work until it has that proof, so consume
// sends it over the next authenticated control.
func (s *Standby) collect(lease *agenttransport.ResidentSourceLease, w agentgateway.ResidentWorkBinding, result error) {
	// Only a confirmed cleanup frees the gateway's slot, and only a failed
	// write (not a refused repeat) leaves a proof worth sending again.
	if e := lease.Collected(w, result); e == nil || result != nil || errors.Is(e, agenttransport.ErrRefused) {
		return
	}
	if proof := lease.Unsent(); proof != nil {
		s.unsent.Lock()
		s.unsent.proof = proof
		s.unsent.Unlock()
	}
}

func LoadStandby(ctx context.Context, path string, options ...StandbyConfig) (*Standby, error) {
	config, err := selectStandbyConfig(options)
	if err != nil {
		return nil, err
	}
	f, e := loadStandbyFactory(ctx, path)
	if e != nil {
		return nil, e
	}
	if !f.native.FixedWirePolicy() {
		return nil, ErrStandbyUnavailable
	}
	s := newStandbyWithConfig(config)
	if !config.AllowSleepDuringWork {
		s.sleep, e = newSleepAssertion()
		if e != nil {
			return nil, e
		}
	}
	s.factory = f
	s.makeConnector = func(ctx context.Context) (residentConnector, error) { return f.native.NewWireConnector(ctx) }
	return s, nil
}
func newStandby() *Standby {
	return newStandbyWithConfig(StandbyConfig{})
}
func newStandbyWithConfig(config StandbyConfig) *Standby {
	return &Standby{config: config, updates: make(chan struct{}, 1), observation: StandbyObservation{Revision: 1, Standby: "off", Registration: "disconnected", Work: "idle", Cleanup: "confirmed", AdmissionSealed: true, CanRequestOn: true}}
}
func selectStandbyConfig(options []StandbyConfig) (StandbyConfig, error) {
	if len(options) > 1 {
		return StandbyConfig{}, ErrStandbyUnavailable
	}
	if len(options) == 1 {
		return options[0], nil
	}
	return StandbyConfig{}, nil
}

func (s *Standby) beginSleepCycle(owner *workerowner.Owner) {
	if s == nil || s.config.AllowSleepDuringWork {
		return
	}
	s.sleepMu.Lock()
	s.sleepCycleOwner = owner
	s.sleepCycleEnded = false
	s.sleepAdmissions = 0
	s.updateSleepDesiredLocked(owner, false)
	s.sleepMu.Unlock()
}

func (s *Standby) endSleepCycle(owner *workerowner.Owner) {
	if s == nil || s.config.AllowSleepDuringWork {
		return
	}
	s.sleepMu.Lock()
	if owner == s.sleepCycleOwner {
		s.sleepCycleEnded = true
		s.sleepAdmissions = 0
		s.setSleepDesiredLocked(false)
	}
	s.sleepMu.Unlock()
}

func (s *Standby) beginSleepAdmission(owner *workerowner.Owner) {
	if s == nil || s.config.AllowSleepDuringWork {
		return
	}
	s.sleepMu.Lock()
	if owner == s.sleepCycleOwner && !s.sleepCycleEnded {
		s.sleepAdmissions++
		s.updateSleepDesiredLocked(owner, false)
	}
	s.sleepMu.Unlock()
}

func (s *Standby) finishSleepAdmission(owner *workerowner.Owner) {
	if s == nil || s.config.AllowSleepDuringWork {
		return
	}
	s.sleepMu.Lock()
	if owner == s.sleepCycleOwner && !s.sleepCycleEnded && s.sleepAdmissions > 0 {
		s.sleepAdmissions--
		s.updateSleepDesiredLocked(owner, false)
	}
	s.sleepMu.Unlock()
}

func (s *Standby) finishPendingSleepAdmissions(owner *workerowner.Owner) {
	if s == nil || s.config.AllowSleepDuringWork {
		return
	}
	s.sleepMu.Lock()
	if owner == s.sleepCycleOwner && !s.sleepCycleEnded {
		s.sleepAdmissions = 0
		s.updateSleepDesiredLocked(owner, false)
	}
	s.sleepMu.Unlock()
}

func (s *Standby) reconcileSleep(owner *workerowner.Owner, force bool) {
	if s == nil || s.config.AllowSleepDuringWork {
		return
	}
	s.sleepMu.Lock()
	s.reconcileSleepLocked(owner, force)
	s.sleepMu.Unlock()
}

func (s *Standby) reconcileSleepLocked(owner *workerowner.Owner, force bool) {
	if owner != nil && owner != s.sleepCycleOwner {
		return
	}
	if s.sleepCycleEnded && !force {
		return
	}
	if owner == nil && !force {
		return
	}
	if force {
		s.sleepAdmissions = 0
		if owner == nil {
			s.sleepCycleEnded = true
		}
	}
	s.updateSleepDesiredLocked(owner, force)
}

func (s *Standby) ensureSleepWorkerLocked() {
	if s.sleepWorker {
		return
	}
	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	s.sleepWorker = true
	s.sleepWorkerWake = wake
	s.sleepWorkerDone = done
	go s.sleepOperationLoop(wake, done)
	signalSleepWorker(wake)
}

func signalSleepWorker(wake chan struct{}) {
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (s *Standby) signalSleepWorkerLocked() {
	if s.sleepWorker {
		signalSleepWorker(s.sleepWorkerWake)
	}
}

func (s *Standby) setSleepDesiredLocked(desired bool) {
	s.sleepDesired = desired
	if !desired && s.sleepHeld {
		s.ensureSleepWorkerLocked()
	}
	s.signalSleepWorkerLocked()
}

func (s *Standby) updateSleepDesiredLocked(owner *workerowner.Owner, force bool) {
	desired := !force && (s.sleepAdmissions > 0 || (owner != nil && owner.Snapshot().Owned > 0))
	s.setSleepDesiredLocked(desired)
}

func (s *Standby) completeSleepWaitersLocked(err error) {
	for _, operation := range s.sleepTakeWaiters {
		result := err
		if operation.ctx != nil && operation.ctx.Err() != nil {
			result = operation.ctx.Err()
		} else if result == nil && !s.sleepDesired {
			result = context.Canceled
		}
		operation.err = result
		close(operation.done)
	}
	s.sleepTakeWaiters = nil
}

func (s *Standby) removeSleepWaiterLocked(operation *sleepOperation) {
	for index, waiter := range s.sleepTakeWaiters {
		if waiter == operation {
			copy(s.sleepTakeWaiters[index:], s.sleepTakeWaiters[index+1:])
			s.sleepTakeWaiters = s.sleepTakeWaiters[:len(s.sleepTakeWaiters)-1]
			return
		}
	}
}

func (s *Standby) pruneCanceledSleepWaitersLocked() {
	active := s.sleepTakeWaiters[:0]
	for _, operation := range s.sleepTakeWaiters {
		if err := operation.ctx.Err(); err != nil {
			operation.err = err
			close(operation.done)
			continue
		}
		active = append(active, operation)
	}
	s.sleepTakeWaiters = active
}

func (s *Standby) sleepNowTime() time.Time {
	if s.sleepNow != nil {
		return s.sleepNow()
	}
	return time.Now()
}

func (s *Standby) sleepAfter(delay time.Duration) <-chan time.Time {
	if s.sleepTimer != nil {
		return s.sleepTimer(delay)
	}
	return time.After(delay)
}

func (s *Standby) sleepOperationLoop(wake <-chan struct{}, done chan struct{}) {
	retryDelay := sleepReleaseRetryInitial
	for {
		s.sleepMu.Lock()
		s.pruneCanceledSleepWaitersLocked()
		if !s.sleepDesired {
			s.completeSleepWaitersLocked(ErrStandbyUnavailable)
		}
		if s.sleepDesired && s.sleepHeld && !s.sleepReleasing {
			s.completeSleepWaitersLocked(nil)
		}
		if s.sleepDesired && !s.sleepHeld && !s.sleepTaking && len(s.sleepTakeWaiters) > 0 {
			s.sleepTaking = true
			beforeTake := s.sleepBeforeTake
			s.sleepMu.Unlock()
			if beforeTake != nil {
				beforeTake()
			}
			s.sleepMu.Lock()
			s.pruneCanceledSleepWaitersLocked()
			if !s.sleepDesired || len(s.sleepTakeWaiters) == 0 {
				s.sleepTaking = false
				if !s.sleepDesired {
					s.completeSleepWaitersLocked(ErrStandbyUnavailable)
				}
				s.sleepMu.Unlock()
				continue
			}
			assertion := s.sleep
			s.sleepMu.Unlock()
			var err error
			if assertion == nil {
				err = ErrStandbyUnavailable
			} else {
				err = assertion.Take()
			}
			s.sleepMu.Lock()
			s.sleepTaking = false
			if err == nil {
				s.sleepHeld = true
				s.sleepErr = nil
			} else {
				s.sleepErr = err
			}
			s.completeSleepWaitersLocked(err)
			s.sleepMu.Unlock()
			if err != nil {
				log.Printf("residentmac: sleep assertion take failed: %v", err)
			} else {
				retryDelay = sleepReleaseRetryInitial
			}
			continue
		}
		if !s.sleepDesired && s.sleepHeld && !s.sleepReleasing {
			s.sleepReleasing = true
			assertion := s.sleep
			s.sleepMu.Unlock()
			var err error
			if assertion == nil {
				err = ErrStandbyUnavailable
			} else {
				err = assertion.Release()
			}
			s.sleepMu.Lock()
			s.sleepReleasing = false
			if err == nil {
				s.sleepHeld = false
				s.sleepErr = nil
			} else {
				s.sleepErr = err
			}
			retry := err != nil && !s.sleepDesired && s.sleepHeld
			s.sleepMu.Unlock()
			if err != nil {
				log.Printf("residentmac: sleep assertion release failed: %v", err)
				if retry {
					if s.waitSleepReleaseRetry(wake, retryDelay) {
						retryDelay = min(retryDelay*2, sleepReleaseRetryMax)
					} else {
						retryDelay = sleepReleaseRetryInitial
					}
				}
			} else {
				retryDelay = sleepReleaseRetryInitial
			}
			continue
		}
		if s.sleepDesired == s.sleepHeld && s.sleepAdmissions == 0 && len(s.sleepTakeWaiters) == 0 && !s.sleepTaking && !s.sleepReleasing {
			s.sleepWorker = false
			s.sleepWorkerWake = nil
			close(done)
			s.sleepMu.Unlock()
			return
		}
		s.sleepMu.Unlock()
		<-wake
	}
}

func (s *Standby) waitSleepReleaseRetry(wake <-chan struct{}, delay time.Duration) bool {
	timer := s.sleepAfter(delay)
	for {
		select {
		case <-timer:
			return true
		case <-wake:
			s.sleepMu.Lock()
			retry := !s.sleepDesired && s.sleepHeld
			s.sleepMu.Unlock()
			if !retry {
				return false
			}
		}
	}
}

func (s *Standby) acquireSleep(ctx context.Context, requestDeadline time.Time) error {
	if s == nil || s.config.AllowSleepDuringWork {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if requestDeadline.IsZero() {
		return context.DeadlineExceeded
	}
	s.sleepMu.Lock()
	if s.sleepCycleEnded || !s.sleepDesired {
		s.sleepMu.Unlock()
		return ErrStandbyUnavailable
	}
	if s.sleep == nil {
		assertion, err := newSleepAssertion()
		if err != nil {
			s.sleepErr = err
			s.sleepMu.Unlock()
			log.Printf("residentmac: sleep assertion take failed: %v", err)
			return err
		}
		s.sleep = assertion
	}
	if s.sleepHeld && !s.sleepTaking && !s.sleepReleasing {
		s.sleepMu.Unlock()
		return nil
	}
	wait := min(sleepTakeWait, requestDeadline.Sub(s.sleepNowTime())-sleepRefusalReserve)
	if wait <= 0 {
		s.sleepMu.Unlock()
		return context.DeadlineExceeded
	}
	operation := &sleepOperation{ctx: ctx, done: make(chan struct{})}
	s.sleepTakeWaiters = append(s.sleepTakeWaiters, operation)
	s.ensureSleepWorkerLocked()
	s.signalSleepWorkerLocked()
	s.sleepMu.Unlock()
	timer := s.sleepAfter(wait)
	select {
	case <-operation.done:
		select {
		case <-timer:
			return context.DeadlineExceeded
		default:
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return operation.err
	case <-ctx.Done():
		s.sleepMu.Lock()
		s.removeSleepWaiterLocked(operation)
		s.signalSleepWorkerLocked()
		s.sleepMu.Unlock()
		return ctx.Err()
	case <-timer:
		s.sleepMu.Lock()
		s.removeSleepWaiterLocked(operation)
		s.signalSleepWorkerLocked()
		s.sleepMu.Unlock()
		return context.DeadlineExceeded
	}
}

func (s *Standby) submitOwnedRequest(c *standbyCycle, request *workerowner.Request, requests chan<- *workerowner.Request, leaseCtx, sourceCtx context.Context) (sent, replied bool, reply error) {
	ready, sent, err := s.queueOwnedRequest(c, request, requests, leaseCtx, sourceCtx)
	if err != nil || !sent {
		return sent, false, err
	}
	replied, reply = s.awaitOwnedRequest(c, request, ready, leaseCtx, sourceCtx)
	if replied {
		s.finishSleepAdmission(c.owner)
	}
	return sent, replied, reply
}

func (s *Standby) queueOwnedRequest(c *standbyCycle, request *workerowner.Request, requests chan<- *workerowner.Request, leaseCtx, sourceCtx context.Context) (<-chan error, bool, error) {
	if c == nil || request == nil || leaseCtx == nil || sourceCtx == nil {
		return nil, false, ErrStandbyUnavailable
	}
	var ready chan error
	if !s.config.AllowSleepDuringWork {
		s.beginSleepAdmission(c.owner)
		ready = make(chan error, 1)
		approve := request.Approve
		request.Approve = func(ctx context.Context) (*workerowner.Worker, error) {
			err := s.acquireSleep(ctx, request.Deadline)
			ready <- err
			if err != nil {
				return nil, err
			}
			return approve(ctx)
		}
	}
	sent := false
	select {
	case requests <- request:
		sent = true
	case <-c.ctx.Done():
	case <-leaseCtx.Done():
	case <-sourceCtx.Done():
	}
	if !sent {
		s.finishSleepAdmission(c.owner)
		return nil, false, nil
	}
	return ready, true, nil
}

func (s *Standby) awaitOwnedRequest(c *standbyCycle, request *workerowner.Request, ready <-chan error, leaseCtx, sourceCtx context.Context) (replied bool, reply error) {
	select {
	case reply = <-request.Reply:
		replied = true
	case <-c.ctx.Done():
	case <-leaseCtx.Done():
	case <-sourceCtx.Done():
	}
	if replied && reply == nil && ready != nil {
		select {
		case err := <-ready:
			if err != nil {
				if s.waitOwnerAttemptEnd(c, c.ctx, leaseCtx, sourceCtx) {
					reply = agenttransport.ErrWorkUnconfigured
				} else {
					replied = false
				}
			}
		case <-c.ctx.Done():
			replied = false
		case <-leaseCtx.Done():
			replied = false
		case <-sourceCtx.Done():
			replied = false
		}
	}
	return replied, reply
}

func (s *Standby) waitOwnerAttemptEnd(c *standbyCycle, host, lease, source context.Context) bool {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if c == nil || c.owner == nil || c.owner.Snapshot().Owned == 0 {
			return true
		}
		select {
		case <-ticker.C:
		case <-host.Done():
			return false
		case <-lease.Done():
			return false
		case <-source.Done():
			return false
		}
	}
}
func cloneObservation(o StandbyObservation) StandbyObservation {
	if o.Caller != nil {
		v := *o.Caller
		o.Caller = &v
	}
	if o.Transition != nil {
		v := *o.Transition
		o.Transition = &v
	}
	return o
}
func (s *Standby) Snapshot() StandbyObservation {
	if s == nil {
		return StandbyObservation{Standby: "unknown", Registration: "unknown", Work: "unknown", Cleanup: "unknown", AdmissionSealed: true}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneObservation(s.observation)
}
func (s *Standby) Changes() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.updates
}
func (s *Standby) changedLocked() {
	if s.observation.Revision < maxStandbyInteger {
		s.observation.Revision++
	} else {
		s.closed = true
		s.observation.Standby = "unknown"
		s.observation.Registration = "unknown"
		s.observation.RegistrationReason = ""
		s.observation.Cleanup = "unknown"
		s.observation.AdmissionSealed = true
		if s.cycle != nil {
			s.cycle.cancel()
		}
	}
	// A sealed standby admits nothing, so it publishes no end.
	if s.observation.AdmissionSealed {
		s.observation.StandbyUntil, s.observation.StandbyEndingSoon = time.Time{}, false
	}
	s.observation.CanRequestOn = !s.closed && s.observation.Standby == "off" && s.observation.Cleanup == "confirmed"
	s.observation.CanRequestOff = !s.closed && s.observation.Standby == "on"
	select {
	case s.updates <- struct{}{}:
	default:
	}
}

// RequestOn turns standby on for d, one of the listed lengths (#399). When d
// has passed, standby turns off as if the person had turned it off.
func (s *Standby) RequestOn(id, revision uint64, d time.Duration) StandbyReceipt {
	return s.request(id, revision, "on", d)
}
func (s *Standby) RequestOff(id, revision uint64) StandbyReceipt {
	return s.request(id, revision, "off", 0)
}
func (s *Standby) request(id, revision uint64, intent string, d time.Duration) StandbyReceipt {
	if s == nil {
		return StandbyReceipt{RequestID: id, Intent: intent, Disposition: "rejected"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.admitLocked(id, revision, intent)
	if !ok {
		return r
	}
	if intent == "on" {
		if !s.observation.CanRequestOn || s.makeConnector == nil || !listedStandbyFor(d) {
			return r
		}
		life, cancel := context.WithCancel(context.Background())
		c := &standbyCycle{ctx: life, cancel: cancel, owner: workerowner.New(10 * time.Second), done: make(chan struct{}), initialUntil: time.Now().Add(initialAuthentication)}
		s.cycle = c
		s.beginSleepCycle(c.owner)
		s.observation.Standby = "on"
		s.observation.Registration = "connecting"
		s.observation.RegistrationReason = ""
		s.observation.AdmissionSealed = false
		s.observation.StandbyExpired = false
		s.endStandbyInLocked(d)
		s.observation.Transition = &StandbyTransition{RequestID: id, Intent: intent, Outcome: "pending"}
		s.changedLocked()
		go s.run(c)
	} else {
		if s.observation.Standby != "on" {
			return r
		}
		// An OFF that comes after the end but before the monitor saw it still
		// stops a cycle that ran out.
		s.observation.StandbyExpired = s.standbyLeftLocked() <= 0
		s.observation.Transition = &StandbyTransition{RequestID: id, Intent: intent, Outcome: "pending"}
		if s.observation.Registration == "needs_action" && s.observation.Cleanup == "confirmed" {
			s.endNeedsActionLocked()
			if s.cycle.expiryCancel != nil {
				s.cycle.expiryCancel()
			}
			s.observation.Transition.Outcome = "applied"
			s.changedLocked()
		} else {
			s.stopLocked()
		}
	}
	r.Disposition = "accepted"
	r.Revision = s.observation.Revision
	return r
}

func (s *Standby) endNeedsActionLocked() {
	s.observation.Standby = "off"
	s.observation.Registration = "disconnected"
	s.observation.RegistrationReason = ""
	s.observation.TransportGeneration = ""
	s.observation.Work = "idle"
	s.observation.Cleanup = "confirmed"
	s.observation.AdmissionSealed = true
	s.observation.Owned = 0
	s.observation.Caller = nil
	s.observation.StandbyUntil = time.Time{}
	s.observation.StandbyEndingSoon = false
}

// stopLocked is standby OFF, whether the person asked for it or standby ran
// out: admission sealed, the source closed so owned work stops,
// and the cycle ended once the open source's cleanup proof was attempted.
func (s *Standby) stopLocked() {
	s.observation.Standby = "stopping"
	s.observation.AdmissionSealed = true
	s.observation.Work = "stopping"
	s.observation.Cleanup = "pending"
	s.changedLocked()
	s.sealSourceLocked()
	s.cycle.cancelAfterProof()
}

// admitLocked is the one replay and staleness check for every local request.
// On false the receipt is final; on true it is "rejected" until the caller accepts.
func (s *Standby) admitLocked(id, revision uint64, intent string) (StandbyReceipt, bool) {
	r := StandbyReceipt{RequestID: id, Intent: intent, Disposition: "rejected", Revision: s.observation.Revision}
	if id == 0 || id > maxStandbyInteger || id <= s.lastRequest || revision != s.observation.Revision {
		r.Disposition = "stale"
		return r, false
	}
	s.lastRequest = id
	if s.closed {
		return r, false
	}
	if s.observation.Standby == "unknown" {
		r.Disposition = "unknown"
		return r, false
	}
	return r, true
}

// Close seals this object permanently; caller timeout never retries collection.
func (s *Standby) Close(ctx context.Context) error {
	if s == nil || ctx == nil {
		return ErrStandbyUnavailable
	}
	if _, ok := ctx.Deadline(); !ok {
		return ErrStandbyUnavailable
	}
	s.mu.Lock()
	s.closed = true
	c := s.cycle
	s.observation.AdmissionSealed = true
	if c == nil {
		s.changedLocked()
		s.mu.Unlock()
		s.reconcileSleep(nil, true)
		return nil
	}
	if s.observation.Registration == "needs_action" && s.observation.Cleanup == "confirmed" {
		s.endNeedsActionLocked()
		if c.expiryCancel != nil {
			c.expiryCancel()
		}
	} else if s.observation.Standby == "on" {
		s.observation.Standby = "stopping"
		s.observation.Work = "stopping"
		s.observation.Cleanup = "pending"
	}
	s.changedLocked()
	s.sealSourceLocked()
	c.cancel()
	s.mu.Unlock()
	select {
	case <-c.done:
		c.mu.Lock()
		e := c.result
		c.mu.Unlock()
		s.reconcileSleep(c.owner, true)
		return e
	case <-ctx.Done():
		return errors.Join(ErrStandbyUnknown, ctx.Err())
	}
}

// cancelAfterProof ends the cycle, and with it the control, once the open
// source's cleanup proof was attempted. The gateway refuses every later run
// while a run's cleanup is unknown, and the proof cannot travel on a closed
// control. Admission is already sealed and the source already closed.
func (c *standbyCycle) cancelAfterProof() {
	c.mu.Lock()
	proved := c.proved
	c.mu.Unlock()
	if proved == nil {
		c.cancel()
		return
	}
	go func() {
		wait := time.NewTimer(standbyProofWait)
		defer wait.Stop()
		select {
		case <-proved:
		case <-wait.C:
		case <-c.ctx.Done():
		}
		c.cancel()
	}()
}
func (s *Standby) sealSourceLocked() {
	if s.cycle == nil {
		return
	}
	s.cycle.mu.Lock()
	defer s.cycle.mu.Unlock()
	if s.cycle.source != nil {
		s.cycle.source.Close()
	}
}
func (s *Standby) run(c *standbyCycle) {
	construction, stop := context.WithTimeout(c.ctx, 10*time.Second)
	connector, e := s.makeConnector(construction)
	stop()
	var runDone chan error
	monitorDone := make(chan struct{})
	var retrySignalsDone chan struct{}
	var reportDone chan struct{}
	if e == nil {
		c.mu.Lock()
		c.connector = connector
		c.mu.Unlock()
		if nudger, ok := connector.(interface{ Nudge(uint64) }); ok {
			retrySignalsDone = make(chan struct{})
			go func() {
				defer close(retrySignalsDone)
				startReconnectSignals(c.ctx, connector, nudger.Nudge)
			}()
		}
		runDone = make(chan error, 1)
		go func() { runDone <- connector.Run(c.ctx) }()
		reports := make(chan standbyReport, 1)
		reportDone = make(chan struct{})
		go func() {
			defer close(reportDone)
			for {
				select {
				case <-c.ctx.Done():
					return
				case report := <-reports:
					if s.reportStandby != nil {
						s.reportStandby(report.lease, report.value)
					} else {
						_ = report.lease.ReportStandby(report.value)
					}
				}
			}
		}()
		go func() { defer close(monitorDone); s.monitor(c, reports) }()
		for c.ctx.Err() == nil {
			lease, err := connector.Accept(c.ctx)
			if err != nil {
				e = err
				break
			}
			if err = s.consume(c, lease); err != nil {
				e = err
				break
			}
		}
	} else {
		close(monitorDone)
	}
	s.mu.Lock()
	s.observation.AdmissionSealed = true
	if s.observation.Standby == "on" {
		s.observation.Standby = "stopping"
		s.observation.Work = "stopping"
		s.observation.Cleanup = "pending"
	}
	s.changedLocked()
	s.mu.Unlock()
	c.cancel()
	c.mu.Lock()
	source := c.source
	c.mu.Unlock()
	if source != nil {
		source.Close()
	}
	cleanup, cancel := context.WithTimeout(context.Background(), standbyCleanup)
	defer cancel()
	ownerErr := c.owner.Stop(cleanup)
	s.endSleepCycle(c.owner)
	var transportErr error
	if connector != nil {
		connector.Stop()
		transportErr = connector.Close(cleanup)
		if runDone != nil {
			select {
			case runErr := <-runDone:
				if _, needsAction := agenttransport.ResidentNeedsActionReason(runErr); needsAction {
					e = runErr
				}
			case <-cleanup.Done():
				transportErr = errors.Join(transportErr, ErrStandbyUnknown)
			}
		}
	}
	if reportDone != nil {
		select {
		case <-reportDone:
		case <-cleanup.Done():
			transportErr = errors.Join(transportErr, ErrStandbyUnknown)
		}
	}
	select {
	case <-monitorDone:
	case <-cleanup.Done():
		ownerErr = errors.Join(ownerErr, ErrStandbyUnknown)
	}
	if retrySignalsDone != nil {
		select {
		case <-retrySignalsDone:
		case <-cleanup.Done():
			transportErr = errors.Join(transportErr, ErrStandbyUnknown)
		}
	}
	result := errors.Join(ownerErr, transportErr)
	if errors.Is(e, workerowner.ErrUnknown) || errors.Is(e, maclaunch.ErrResidentUnknown) || errors.Is(e, agenttransport.ErrCleanupUnknown) {
		result = errors.Join(result, e)
	}
	c.mu.Lock()
	if result != nil {
		result = errors.Join(ErrStandbyUnknown, result)
	}
	c.result = result
	c.mu.Unlock()
	s.mu.Lock()
	var expiryContext context.Context
	needsActionReason := registrationReason(e)
	offRequested := s.observation.Transition != nil && s.observation.Transition.Intent == "off"
	if result == nil && needsActionReason != "" && !offRequested && !s.observation.StandbyExpired && s.standbyLeftLocked() <= 0 {
		s.observation.StandbyExpired = true
	}
	if result != nil {
		s.observation.Standby = "unknown"
		s.observation.Registration = "unknown"
		s.observation.RegistrationReason = ""
		s.observation.Work = "unknown"
		s.observation.Cleanup = "unknown"
		if s.observation.Transition != nil {
			s.observation.Transition.Outcome = "unknown"
		}
	} else if needsActionReason != "" && !s.closed && !s.observation.StandbyExpired && !offRequested {
		s.observation.Standby = "on"
		s.observation.Registration = "needs_action"
		s.observation.RegistrationReason = needsActionReason
		s.observation.AdmissionSealed = true
		s.observation.Work = "idle"
		s.observation.Cleanup = "confirmed"
		s.observation.Owned = 0
		s.observation.Caller = nil
		expiryContext, c.expiryCancel = context.WithCancel(context.Background())
		if s.observation.Transition != nil && s.observation.Transition.Intent == "on" {
			s.observation.Transition.Outcome = "applied"
		}
	} else {
		s.observation.Standby = "off"
		s.observation.Registration = "disconnected"
		s.observation.RegistrationReason = ""
		s.observation.Work = "idle"
		s.observation.Cleanup = "confirmed"
		s.observation.Owned = 0
		s.observation.Caller = nil
		if s.observation.Transition != nil && s.observation.Transition.Intent == "off" {
			s.observation.Transition.Outcome = "applied"
		} else if s.observation.Transition != nil && s.observation.Transition.Outcome == "pending" {
			s.observation.Transition.Outcome = "rejected"
		}
	}
	s.changedLocked()
	s.mu.Unlock()
	if expiryContext != nil {
		s.monitorNeedsActionExpiry(c, expiryContext)
		c.expiryCancel()
	}
	s.mu.Lock()
	close(c.done) // all original joins and needs-action expiry precede Close completion/re-ON
	s.mu.Unlock()
}

func (s *Standby) monitorNeedsActionExpiry(c *standbyCycle, ctx context.Context) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			if s.cycle != c || s.closed || s.observation.Standby != "on" || s.observation.Registration != "needs_action" {
				s.mu.Unlock()
				return
			}
			expired := s.expireStandbyLocked()
			s.mu.Unlock()
			if expired {
				return
			}
		}
	}
}

func registrationReason(err error) RegistrationReason {
	reason, ok := agenttransport.ResidentNeedsActionReason(err)
	if !ok {
		return ""
	}
	switch reason {
	case agenttransport.ResidentFailureIdentityRefused:
		return RegistrationReasonIdentityRefused
	case agenttransport.ResidentFailureConnectionOccupied:
		return RegistrationReasonConnectionOccupied
	case agenttransport.ResidentFailureGatewayRefused:
		return RegistrationReasonGatewayRefused
	default:
		return ""
	}
}

func registrationStatus(control agenttransport.ResidentControlSnapshot, authenticated bool) string {
	if control.Connected {
		return "authenticated"
	}
	if control.Reconnecting || control.RetryWaiting || authenticated {
		return "reconnecting"
	}
	return "connecting"
}
func (s *Standby) consume(c *standbyCycle, lease *agenttransport.ResidentSourceLease) error {
	if lease == nil {
		return ErrStandbyUnknown
	}
	// Accept is a retained actual authentication event, even if the control
	// closes before the sampling monitor observes Connected. Never restart the
	// initial-ON timer after that event.
	s.mu.Lock()
	if s.cycle == c && lease.Receiver() != nil && lease.Receiver().Generation() != "" {
		c.authenticated = true
		if !s.observation.AdmissionSealed {
			s.observation.Registration = "authenticated"
			s.observation.TransportGeneration = lease.Receiver().Generation()
			if s.observation.Transition != nil && s.observation.Transition.Intent == "on" {
				s.observation.Transition.Outcome = "applied"
			}
			s.changedLocked()
		}
	}
	s.mu.Unlock()
	s.unsent.Lock()
	proof := s.unsent.proof
	s.unsent.Unlock()
	if proof != nil && lease.Resend(proof) == nil {
		s.unsent.Lock()
		s.unsent.proof = nil
		s.unsent.Unlock()
	}
	var source *FixedSource
	var err error
	if s.factory == nil {
		err = ErrStandbyUnavailable
	} else {
		source, err = s.factory.OpenSource(lease.Receiver())
	}
	proved := make(chan struct{})
	c.mu.Lock()
	c.source, c.lease = source, lease
	c.work = nil
	c.proved = nil
	if err == nil {
		c.proved = proved
	}
	c.mu.Unlock()
	s.mu.Lock()
	if s.cycle == c && !s.observation.AdmissionSealed {
		caller := lease.Receiver().PrincipalLabel()
		s.observation.Caller = &caller
		s.changedLocked()
	}
	s.mu.Unlock()
	if err != nil {
		ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		return errors.Join(err, lease.Finish(ctx, nil))
	}
	requests := make(chan *workerowner.Request)
	waitDone := make(chan error, 1)
	go func() { waitDone <- c.waitSource(source.Context(), requests) }()
	// Native revocation precedes source cancellation. Propagate that end to the
	// SAME receiver so its synchronous Next can unblock without OFF or traffic.
	// Receiver cancellation is not collection: retain/join the waiter below.
	// Closing the receiver closes the control under a blocked Next, so the
	// cleanup proof for the last work goes out first.
	var collected error
	prove := sync.OnceFunc(func() {
		defer close(proved)
		wait, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		select {
		case collected = <-waitDone:
		case <-wait.Done():
			collected = workerowner.ErrUnknown
		}
		s.finishPendingSleepAdmissions(c.owner)
		c.mu.Lock()
		last := c.work
		c.mu.Unlock()
		if last != nil {
			s.collect(lease, *last, collected)
		}
		s.reconcileSleep(c.owner, false)
	})
	readStopped := make(chan struct{})
	stopRead := context.AfterFunc(source.Context(), func() {
		defer close(readStopped)
		prove()
		lease.Receiver().Close()
	})
	for c.ctx.Err() == nil && lease.Context().Err() == nil && source.Context().Err() == nil {
		i, e := lease.Next()
		if e != nil {
			break
		}
		runIntent, run := i.(agenttransport.ResidentRunIntent)
		s.mu.Lock()
		admitted := s.admissionLocked(c, run)
		s.mu.Unlock()
		if admitted == admitNothing {
			_ = lease.Refuse(i.Correlation())
			break
		}
		if admitted == admitNoRun {
			_ = lease.Refuse(i.Correlation())
			continue
		}
		var request *workerowner.Request
		if admitted == admitRun {
			request, e = source.NewRunRequest(runIntent)
		} else if fixed, ok := i.(agenttransport.ResidentFixedIntent); ok {
			request, e = source.NewRequest(fixed)
		} else {
			e = maclaunch.ErrResident
		}
		if e != nil {
			_ = lease.Refuse(i.Correlation())
			continue
		}
		sent, replied, reply := s.submitOwnedRequest(c, request, requests, lease.Context(), source.Context())
		if !sent {
			break
		}
		if !replied {
			break
		}
		if reply == nil {
			c.mu.Lock()
			w := i.WorkBinding()
			c.work = &w
			c.mu.Unlock()
			if e = lease.Pending(i.Correlation()); e != nil {
				err = e
			}
		} else {
			_ = lease.Refuse(i.Correlation())
		}
		if err != nil {
			break
		}
	}
	source.Close()
	if stopRead() {
		close(readStopped)
	}
	<-readStopped // Joined original cancellation callback.
	close(requests)
	prove()
	cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	err = errors.Join(err, collected)
	finish := lease.Finish(cleanup, collected)
	if collected == nil && finish == nil {
		c.mu.Lock()
		c.source = nil
		c.lease = nil
		c.work = nil
		c.mu.Unlock()
	}
	s.reconcileSleep(c.owner, false)
	return errors.Join(err, finish)
}

type standbyReport struct {
	lease *agenttransport.ResidentSourceLease
	value agentgateway.ResidentStandby
}

func (s *Standby) monitor(c *standbyCycle, reports chan standbyReport) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var reportedLease *agenttransport.ResidentSourceLease
	var reported agentgateway.ResidentStandby
	hasReported := false
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
		}
		c.mu.Lock()
		connector, source, work, lease := c.connector, c.source, c.work, c.lease
		c.mu.Unlock()
		if connector == nil {
			continue
		}
		stopForUnknown := false
		control := connector.Snapshot()
		owned := c.owner.Snapshot()
		var w *maclaunch.ResidentWorker
		if source != nil && work != nil {
			w, _ = source.Worker(work.RequestID)
		}
		s.mu.Lock()
		before := cloneObservation(s.observation)
		if s.cycle != c {
			s.mu.Unlock()
			return
		}
		if s.expireStandbyLocked() {
			s.mu.Unlock()
			continue
		}
		if s.observation.Standby == "on" {
			// Generation is retained by the real connector only AFTER full auth;
			// unlike Connected it cannot disappear between monitor samples.
			if control.Generation != "" {
				c.authenticated = true
				if s.observation.Transition != nil && s.observation.Transition.Intent == "on" && s.observation.Transition.Outcome == "pending" {
					s.observation.Transition.Outcome = "applied"
				}
			}
			if s.observation.Registration != "needs_action" {
				s.observation.Registration = registrationStatus(control, c.authenticated)
				if control.Connected {
					s.observation.RegistrationReason = ""
					c.authenticated = true
					if s.observation.Transition != nil && s.observation.Transition.Intent == "on" && s.observation.Transition.Outcome == "pending" {
						s.observation.Transition.Outcome = "applied"
					}
				}
			}
			if !c.authenticated && !time.Now().Before(c.initialUntil) {
				s.observation.AdmissionSealed = true
				if source != nil {
					source.Close()
				}
				stopForUnknown = true
			}
			s.observation.TransportGeneration = control.Generation
			s.observation.Owned = owned.Owned
			s.observation.Work = "idle"
			s.observation.Cleanup = "confirmed"
			if owned.Owned > 0 {
				s.observation.Work = "preparing"
				s.observation.Cleanup = "pending"
			}
			if w != nil {
				v := w.Observation()
				if v.SummaryObserved && v.Running > 0 {
					s.observation.Work = "running"
				}
				if v.SummaryObserved && v.Stopping > 0 {
					s.observation.Work = "stopping"
				}
			}
			if owned.Unknown || control.CleanupUnknown {
				s.observation.Work = "unknown"
				s.observation.Cleanup = "unknown"
				s.observation.AdmissionSealed = true
				if source != nil {
					source.Close()
				}
				stopForUnknown = true
			}
		}
		if !reflect.DeepEqual(before, s.observation) {
			s.changedLocked()
		}
		var report agentgateway.ResidentStandby
		sendReport := false
		if lease != nil && (s.observation.Standby == "on" || s.observation.Standby == "stopping") {
			report = agentgateway.ResidentStandby{Revision: s.observation.Revision, Standby: s.observation.Standby, Until: s.observation.StandbyUntil}
			sendReport = !hasReported || reportedLease != lease || reported.Standby != report.Standby || !reported.Until.Equal(report.Until)
		}
		s.mu.Unlock()
		if sendReport {
			select {
			case reports <- standbyReport{lease: lease, value: report}:
			default:
				select {
				case <-reports:
				default:
				}
				reports <- standbyReport{lease: lease, value: report}
			}
			reportedLease, reported, hasReported = lease, report, true
		}
		if work != nil && lease != nil && (owned.Unknown || owned.Owned == 0) {
			var collection error
			if owned.Unknown {
				collection = ErrStandbyUnknown
			}
			if w != nil {
				v := w.Observation()
				if !v.CleanupObserved || v.CleanupUnknown {
					collection = ErrStandbyUnknown
				}
			}
			s.collect(lease, *work, collection)
		}
		if stopForUnknown {
			c.cancel()
		}
		s.reconcileSleep(c.owner, false)
	}
}

// Concrete cycle entry shared with consume; no owner/factory/callback injection.
func (c *standbyCycle) waitSource(source context.Context, requests <-chan *workerowner.Request) error {
	return c.owner.WaitSource(c.ctx, source, requests)
}
