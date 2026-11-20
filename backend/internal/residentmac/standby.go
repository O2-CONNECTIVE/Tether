package residentmac

import (
	"context"
	"errors"
	"log"
	"reflect"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/workerowner"
)

const maxStandbyInteger = uint64(9007199254740991)
const standbyCleanup = 40 * time.Second
const standbyCollectionWait = 15 * time.Second
const standbyProofSendWait = 5 * time.Second
const standbyProofWait = 25 * time.Second      // collection and proof write before OFF ends the control
const initialAuthentication = 30 * time.Second // ON result only, not service lifetime

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
type ownedWorkSleep struct {
	acquired       bool // guarded by Standby.sleepMu
	released       bool // guarded by Standby.sleepMu
	assertionReady chan error
	proofMu        sync.Mutex
	proofAttempted bool
	proofDone      chan struct{}
	proofDeadline  time.Time
	proofRefused   bool
	ownerConfirmed bool
	collectionEnd  time.Time
	releaseWait    bool
	releaseNow     chan struct{}
	releaseOnce    sync.Once
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
	workSleep     *ownedWorkSleep
	workReady     bool          // gateway admission reply was written; monitor may collect
	proved        chan struct{} // closed once the open source's cleanup proof was attempted
	result        error
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
	sleepOpMu        sync.Mutex
	config           StandbyConfig
	sleep            sleepAssertion
	sleepOwners      int // guarded by sleepMu
	sleepHeld        bool
	sleepErr         error // last Take/Release failure, guarded by sleepMu
	sleepTaking      bool
	sleepTakeDone    chan struct{}
	sleepReleasing   bool
	sleepReleaseDone chan struct{}
	factory          *FixedFactory
	makeConnector    func(context.Context) (residentConnector, error)
	clock            func() (mono, wall time.Time) // nil reads elapsedNow and time.Now; tests replace it
	// reportStandby sends one standby report over the control; nil sends it
	// with lease.ReportStandby. Tests replace it: they cannot hold a real lease.
	reportStandby             func(*agenttransport.ResidentSourceLease, agentgateway.ResidentStandby)
	afterSleepAssertionResult func(error) // tests pause the failed Owner approval before it returns
	updates                   chan struct{}
	observation               StandbyObservation
	lastRequest               uint64
	cycle                     *standbyCycle
	closed                    bool
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
type cleanupProofLease interface {
	Collected(agentgateway.ResidentWorkBinding, error) error
	Unsent() []byte
}

func (s *Standby) collect(lease cleanupProofLease, w agentgateway.ResidentWorkBinding, result error, workSleep *ownedWorkSleep) {
	if workSleep == nil {
		return
	}
	workSleep.proofMu.Lock()
	if workSleep.proofDone == nil {
		workSleep.proofDone = make(chan struct{})
	}
	if workSleep.releaseNow == nil {
		workSleep.releaseNow = make(chan struct{})
	}
	if workSleep.collectionEnd.IsZero() {
		workSleep.collectionEnd = time.Now().Add(standbyCollectionWait)
	}
	if result == nil {
		workSleep.ownerConfirmed = true
	}
	done := workSleep.proofDone
	first := !workSleep.proofAttempted
	if first {
		workSleep.proofAttempted = true
		workSleep.proofDeadline = time.Now().Add(standbyProofSendWait)
	}
	startWait := !workSleep.releaseWait
	workSleep.releaseWait = true
	deadline := workSleep.collectionEnd
	releaseNow := workSleep.releaseNow
	workSleep.proofMu.Unlock()

	if startWait {
		go s.releaseAfterCollectionBudget(workSleep, releaseNow, deadline)
	}
	if !first {
		workSleep.proofMu.Lock()
		var attemptDone bool
		select {
		case <-done:
			attemptDone = true
		default:
		}
		confirmed := workSleep.ownerConfirmed && !workSleep.proofRefused && attemptDone
		workSleep.proofMu.Unlock()
		if confirmed {
			workSleep.releaseOnce.Do(func() { close(releaseNow) })
		}
		return
	}
	go func() {
		var proofErr error
		if lease != nil {
			proofErr = lease.Collected(w, result)
			if proofErr != nil && result == nil && !errors.Is(proofErr, agenttransport.ErrRefused) {
				if proof := lease.Unsent(); proof != nil {
					s.unsent.Lock()
					s.unsent.proof = proof
					s.unsent.Unlock()
				}
			}
		}
		workSleep.proofMu.Lock()
		workSleep.proofRefused = errors.Is(proofErr, agenttransport.ErrRefused)
		confirmed := workSleep.ownerConfirmed && !workSleep.proofRefused
		close(done)
		workSleep.proofMu.Unlock()
		if confirmed {
			workSleep.releaseOnce.Do(func() { close(releaseNow) })
		}
	}()
}
func (s *Standby) collectLastWork(c *standbyCycle, lease cleanupProofLease, result error) error {
	if c == nil {
		return result
	}
	c.mu.Lock()
	last := c.work
	lastSleep := c.workSleep
	c.mu.Unlock()
	if last == nil {
		return result
	}
	s.collect(lease, *last, result, lastSleep)
	return result
}

func (s *Standby) releaseAfterCollectionBudget(workSleep *ownedWorkSleep, releaseNow <-chan struct{}, deadline time.Time) {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-releaseNow:
	case <-timer.C:
	}
	_ = s.releaseOwnedWork(workSleep)
}

func (s *Standby) setCollectionEnd(workSleep *ownedWorkSleep) time.Time {
	if workSleep == nil {
		return time.Now().Add(standbyCollectionWait)
	}
	workSleep.proofMu.Lock()
	if workSleep.collectionEnd.IsZero() {
		workSleep.collectionEnd = time.Now().Add(standbyCollectionWait)
	}
	deadline := workSleep.collectionEnd
	workSleep.proofMu.Unlock()
	return deadline
}

func (s *Standby) waitForCollectionProof(workSleep *ownedWorkSleep) {
	if workSleep == nil {
		return
	}
	workSleep.proofMu.Lock()
	done, deadline := workSleep.proofDone, workSleep.proofDeadline
	workSleep.proofMu.Unlock()
	if done == nil || deadline.IsZero() {
		return
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

// LoadStandby loads a resident with the zero-value config unless one config is passed.
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
func selectStandbyConfig(options []StandbyConfig) (StandbyConfig, error) {
	if len(options) > 1 {
		return StandbyConfig{}, ErrStandbyUnavailable
	}
	if len(options) == 1 {
		return options[0], nil
	}
	return StandbyConfig{}, nil
}
func newStandby() *Standby {
	return newStandbyWithConfig(StandbyConfig{})
}
func newStandbyWithConfig(config StandbyConfig) *Standby {
	return &Standby{config: config, updates: make(chan struct{}, 1), observation: StandbyObservation{Revision: 1, Standby: "off", Registration: "disconnected", Work: "idle", Cleanup: "confirmed", AdmissionSealed: true, CanRequestOn: true}}
}
func (s *Standby) prepareOwnedRequest(request *workerowner.Request, workSleep *ownedWorkSleep) {
	if s == nil || request == nil || request.Approve == nil || s.config.AllowSleepDuringWork || workSleep == nil {
		return
	}
	approve := request.Approve
	workSleep.assertionReady = make(chan error, 1)
	request.Approve = func(ctx context.Context) (*workerowner.Worker, error) {
		err := s.acquireOwnedWork(ctx, workSleep)
		workSleep.assertionReady <- err
		if err != nil && s.afterSleepAssertionResult != nil {
			s.afterSleepAssertionResult(err)
		}
		if err != nil {
			return nil, err
		}
		return approve(ctx)
	}
}
func (s *Standby) submitOwnedRequest(c *standbyCycle, request *workerowner.Request, work agentgateway.ResidentWorkBinding, requests chan<- *workerowner.Request, leaseCtx, sourceCtx context.Context) (sent, replied bool, reply error) {
	workSleep, sent, err := s.queueOwnedRequest(c, request, work, requests, leaseCtx, sourceCtx)
	if err != nil || !sent {
		return sent, false, err
	}
	replied, reply = s.awaitOwnedRequest(c, request, workSleep, leaseCtx, sourceCtx)
	return sent, replied, reply
}
func (s *Standby) queueOwnedRequest(c *standbyCycle, request *workerowner.Request, work agentgateway.ResidentWorkBinding, requests chan<- *workerowner.Request, leaseCtx, sourceCtx context.Context) (*ownedWorkSleep, bool, error) {
	if c == nil || request == nil || leaseCtx == nil || sourceCtx == nil {
		return nil, false, ErrStandbyUnavailable
	}
	workSleep := &ownedWorkSleep{proofDone: make(chan struct{}), releaseNow: make(chan struct{})}
	s.prepareOwnedRequest(request, workSleep)
	c.mu.Lock()
	c.work = &work
	c.workSleep = workSleep
	c.workReady = false
	c.mu.Unlock()
	sent := false
	select {
	case requests <- request:
		sent = true
	case <-c.ctx.Done():
	case <-leaseCtx.Done():
	case <-sourceCtx.Done():
	}
	if !sent {
		c.clearWorkSleep(workSleep)
		_ = s.releaseOwnedWork(workSleep)
		return nil, false, nil
	}
	return workSleep, true, nil
}
func (s *Standby) awaitOwnedRequest(c *standbyCycle, request *workerowner.Request, workSleep *ownedWorkSleep, leaseCtx, sourceCtx context.Context) (replied bool, reply error) {
	select {
	case reply = <-request.Reply:
		replied = true
	case <-c.ctx.Done():
	case <-leaseCtx.Done():
	case <-sourceCtx.Done():
	}
	if replied && reply == nil && workSleep != nil && workSleep.assertionReady != nil {
		select {
		case err := <-workSleep.assertionReady:
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
	if replied && reply != nil {
		c.clearWorkSleep(workSleep)
		_ = s.releaseOwnedWork(workSleep)
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
func (c *standbyCycle) clearWorkSleep(workSleep *ownedWorkSleep) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.workSleep == workSleep {
		c.work = nil
		c.workSleep = nil
		c.workReady = false
	}
}
func (c *standbyCycle) markWorkReady(workSleep *ownedWorkSleep) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.workSleep == workSleep {
		c.workReady = true
	}
}
func (s *Standby) acquireOwnedWork(ctx context.Context, workSleep *ownedWorkSleep) error {
	if s == nil || s.config.AllowSleepDuringWork {
		return nil
	}
	for {
		s.sleepMu.Lock()
		if workSleep == nil || workSleep.released {
			s.sleepMu.Unlock()
			return ErrStandbyUnavailable
		}
		if workSleep.acquired {
			s.sleepMu.Unlock()
			return nil
		}
		if s.sleepTaking {
			done := s.sleepTakeDone
			s.sleepMu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if s.sleepReleasing {
			done := s.sleepReleaseDone
			s.sleepMu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if s.sleepOwners != 0 || s.sleepHeld {
			workSleep.acquired = true
			s.sleepOwners++
			s.sleepMu.Unlock()
			return nil
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
		assertion := s.sleep
		s.sleepTaking = true
		takeDone := make(chan struct{})
		s.sleepTakeDone = takeDone
		s.sleepMu.Unlock()

		s.sleepOpMu.Lock()
		err := assertion.Take()
		s.sleepOpMu.Unlock()

		s.sleepMu.Lock()
		s.sleepTaking = false
		if err != nil {
			s.sleepErr = err
			close(takeDone)
			s.sleepMu.Unlock()
			log.Printf("residentmac: sleep assertion take failed: %v", err)
			return err
		}
		s.sleepHeld = true
		s.sleepErr = nil
		if workSleep.released {
			close(takeDone)
			s.scheduleSleepReleaseLocked()
			s.sleepMu.Unlock()
			return ErrStandbyUnavailable
		}
		workSleep.acquired = true
		s.sleepOwners++
		close(takeDone)
		s.sleepMu.Unlock()
		return nil
	}
}
func (s *Standby) releaseOwnedWork(workSleep *ownedWorkSleep) error {
	if s == nil {
		return nil
	}
	s.sleepMu.Lock()
	defer s.sleepMu.Unlock()
	if workSleep == nil || workSleep.released {
		return nil
	}
	workSleep.released = true
	if !workSleep.acquired {
		return nil
	}
	workSleep.acquired = false
	s.sleepOwners--
	if s.sleepOwners != 0 {
		return nil
	}
	s.scheduleSleepReleaseLocked()
	return nil
}
func (s *Standby) releaseIdleSleep() <-chan struct{} {
	if s == nil {
		return nil
	}
	s.sleepMu.Lock()
	defer s.sleepMu.Unlock()
	if s.sleepOwners == 0 {
		if s.sleepReleasing {
			return s.sleepReleaseDone
		}
		if s.sleepTaking {
			return s.sleepTakeDone
		}
		return s.scheduleSleepReleaseLocked()
	}
	return nil
}

// scheduleSleepReleaseLocked queues the OS release without holding sleepMu
// across it. Acquisitions wait for this operation before starting their work.
func (s *Standby) scheduleSleepReleaseLocked() <-chan struct{} {
	if s.sleepOwners != 0 || !s.sleepHeld || s.sleepTaking {
		return nil
	}
	if s.sleepReleasing {
		return s.sleepReleaseDone
	}
	done := make(chan struct{})
	assertion := s.sleep
	s.sleepReleasing = true
	s.sleepReleaseDone = done
	go func() {
		s.sleepOpMu.Lock()
		err := assertion.Release()
		s.sleepOpMu.Unlock()
		s.sleepMu.Lock()
		if err != nil {
			s.sleepErr = err
		} else {
			s.sleepHeld = false
			s.sleepErr = nil
		}
		s.sleepReleasing = false
		close(done)
		s.sleepMu.Unlock()
		if err != nil {
			log.Printf("residentmac: sleep assertion release failed: %v", err)
		}
	}()
	return done
}

func (s *Standby) closeSleep(ctx context.Context) error {
	for {
		s.sleepMu.Lock()
		if s.sleepOwners != 0 {
			s.sleepMu.Unlock()
			return nil
		}
		if s.sleepReleasing {
			done := s.sleepReleaseDone
			s.sleepMu.Unlock()
			return waitSleepOperation(ctx, done)
		}
		if s.sleepTaking {
			done := s.sleepTakeDone
			s.sleepMu.Unlock()
			if err := waitSleepOperation(ctx, done); err != nil {
				return err
			}
			continue
		}
		if s.sleepHeld {
			done := s.scheduleSleepReleaseLocked()
			s.sleepMu.Unlock()
			return waitSleepOperation(ctx, done)
		}
		s.sleepMu.Unlock()
		return nil
	}
}

func waitSleepOperation(ctx context.Context, done <-chan struct{}) error {
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.Join(ErrStandbyUnknown, ctx.Err())
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
	retrySleep := false
	defer func() {
		s.mu.Unlock()
		if retrySleep {
			_ = s.releaseIdleSleep()
		}
	}()
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
		s.observation.Standby = "on"
		s.observation.Registration = "connecting"
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
		s.stopLocked()
		retrySleep = true
	}
	r.Disposition = "accepted"
	r.Revision = s.observation.Revision
	return r
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
		return s.closeSleep(ctx)
	}
	if s.observation.Standby == "on" {
		s.observation.Standby = "stopping"
		s.observation.Work = "stopping"
		s.observation.Cleanup = "pending"
	}
	s.changedLocked()
	s.sealSourceLocked()
	c.cancelAfterProof()
	s.mu.Unlock()
	_ = s.releaseIdleSleep()
	select {
	case <-c.done:
		c.mu.Lock()
		e := c.result
		c.mu.Unlock()
		return errors.Join(e, s.closeSleep(ctx))
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
	var reportDone chan struct{}
	if e == nil {
		c.mu.Lock()
		c.connector = connector
		c.mu.Unlock()
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
	c.mu.Lock()
	lease := c.lease
	c.mu.Unlock()
	var proofLease cleanupProofLease
	if lease != nil {
		proofLease = lease
	}
	s.collectLastWork(c, proofLease, ownerErr)
	var transportErr error
	if connector != nil {
		connector.Stop()
		transportErr = connector.Close(cleanup)
		if runDone != nil {
			select {
			case <-runDone:
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
	defer s.mu.Unlock()
	if result != nil {
		s.observation.Standby = "unknown"
		s.observation.Registration = "unknown"
		s.observation.Work = "unknown"
		s.observation.Cleanup = "unknown"
		if s.observation.Transition != nil {
			s.observation.Transition.Outcome = "unknown"
		}
	} else {
		s.observation.Standby = "off"
		s.observation.Registration = "disconnected"
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
	close(c.done) // final observation and all original joins precede Close completion/re-ON
	s.changedLocked()
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
		c.mu.Lock()
		workSleep := c.workSleep
		c.mu.Unlock()
		collectionEnd := s.setCollectionEnd(workSleep)
		wait, stop := context.WithDeadline(context.Background(), collectionEnd)
		defer stop()
		select {
		case collected = <-waitDone:
		case <-wait.Done():
			collected = workerowner.ErrUnknown
		}
		collected = s.collectLastWork(c, lease, collected)
		s.waitForCollectionProof(workSleep)
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
		work := i.WorkBinding()
		sent, replied, reply := s.submitOwnedRequest(c, request, work, requests, lease.Context(), source.Context())
		if !sent {
			break
		}
		if !replied {
			break
		}
		if reply == nil {
			if e = lease.Pending(i.Correlation()); e != nil {
				err = e
			} else {
				c.mu.Lock()
				workSleep := c.workSleep
				c.mu.Unlock()
				c.markWorkReady(workSleep)
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
		c.workSleep = nil
		c.workReady = false
		c.mu.Unlock()
	}
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
		connector, source, work, workSleep, workReady, lease := c.connector, c.source, c.work, c.workSleep, c.workReady, c.lease
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
			_ = s.releaseIdleSleep()
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
			s.observation.Registration = "connecting"
			if control.Connected {
				s.observation.Registration = "authenticated"
				c.authenticated = true
				if s.observation.Transition != nil && s.observation.Transition.Intent == "on" && s.observation.Transition.Outcome == "pending" {
					s.observation.Transition.Outcome = "applied"
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
		if work != nil && workReady && lease != nil && owned.Owned == 0 {
			var collection error
			if w != nil {
				v := w.Observation()
				if !v.CleanupObserved || v.CleanupUnknown {
					collection = ErrStandbyUnknown
				}
			}
			s.collect(lease, *work, collection, workSleep)
		}
		if stopForUnknown {
			c.cancel()
		}
	}
}

// Concrete cycle entry shared with consume; no owner/factory/callback injection.
func (c *standbyCycle) waitSource(source context.Context, requests <-chan *workerowner.Request) error {
	return c.owner.WaitSource(c.ctx, source, requests)
}
