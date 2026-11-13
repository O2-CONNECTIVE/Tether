package residentmac

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/workerowner"
)

const maxStandbyInteger = uint64(9007199254740991)
const standbyCleanup = 40 * time.Second
const standbyProofWait = 25 * time.Second      // collection (15s) and proof write (5s) before OFF ends the control
const initialAuthentication = 30 * time.Second // ON result only, not service lifetime

// Private lifecycle-only fake seam; it cannot mint authority or native work.
type residentConnector interface {
	Run(context.Context) error
	Accept(context.Context) (*agenttransport.ResidentSourceLease, error)
	Stop()
	Close(context.Context) error
	Snapshot() agenttransport.ResidentControlSnapshot
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
	intent        *agenttransport.ResidentFixedIntent
	proved        chan struct{} // closed once the open source's cleanup proof was attempted
	result        error
	initialUntil  time.Time
	runUntil      time.Time // guarded by Standby.mu; zero means run is not allowed
	runUntilWall  time.Time // the same instant without the monotonic reading
	authenticated bool
}

// Standby retains the same transport, native sources and common owner across
// authenticated control generations. Unknown cleanup never releases ownership.
type Standby struct {
	mu            sync.Mutex
	factory       *FixedFactory
	makeConnector func(context.Context) (residentConnector, error)
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
func (s *Standby) collect(lease *agenttransport.ResidentSourceLease, i agenttransport.ResidentFixedIntent, result error) {
	// Only a confirmed cleanup frees the gateway's slot, and only a failed
	// write (not a refused repeat) leaves a proof worth sending again.
	if e := lease.Collected(i, result); e == nil || result != nil || errors.Is(e, agenttransport.ErrRefused) {
		return
	}
	if proof := lease.Unsent(); proof != nil {
		s.unsent.Lock()
		s.unsent.proof = proof
		s.unsent.Unlock()
	}
}

func LoadStandby(ctx context.Context, path string) (*Standby, error) {
	f, e := loadStandbyFactory(ctx, path)
	if e != nil {
		return nil, e
	}
	if !f.native.FixedWirePolicy() {
		return nil, ErrStandbyUnavailable
	}
	s := newStandby()
	s.factory = f
	s.makeConnector = func(ctx context.Context) (residentConnector, error) { return f.native.NewWireConnector(ctx) }
	return s, nil
}
func newStandby() *Standby {
	return &Standby{updates: make(chan struct{}, 1), observation: StandbyObservation{Revision: 1, Standby: "off", Registration: "disconnected", Work: "idle", Cleanup: "confirmed", AdmissionSealed: true, CanRequestOn: true}}
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
	s.observation.CanRequestOn = !s.closed && s.observation.Standby == "off" && s.observation.Cleanup == "confirmed"
	s.observation.CanRequestOff = !s.closed && s.observation.Standby == "on"
	select {
	case s.updates <- struct{}{}:
	default:
	}
}
func (s *Standby) RequestOn(id, revision uint64) StandbyReceipt { return s.request(id, revision, "on") }
func (s *Standby) RequestOff(id, revision uint64) StandbyReceipt {
	return s.request(id, revision, "off")
}
func (s *Standby) request(id, revision uint64, intent string) StandbyReceipt {
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
		if !s.observation.CanRequestOn || s.makeConnector == nil {
			return r
		}
		life, cancel := context.WithCancel(context.Background())
		c := &standbyCycle{ctx: life, cancel: cancel, owner: workerowner.New(10 * time.Second), done: make(chan struct{}), initialUntil: time.Now().Add(initialAuthentication)}
		s.cycle = c
		s.observation.Standby = "on"
		s.observation.Registration = "connecting"
		s.observation.AdmissionSealed = false
		s.observation.Transition = &StandbyTransition{RequestID: id, Intent: intent, Outcome: "pending"}
		s.changedLocked()
		go s.run(c)
	} else {
		if s.observation.Standby != "on" {
			return r
		}
		s.observation.Standby = "stopping"
		s.observation.AdmissionSealed = true
		s.observation.Work = "stopping"
		s.observation.Cleanup = "pending"
		s.observation.Transition = &StandbyTransition{RequestID: id, Intent: intent, Outcome: "pending"}
		s.changedLocked()
		s.sealSourceLocked()
		s.cycle.cancelAfterProof()
	}
	r.Disposition = "accepted"
	r.Revision = s.observation.Revision
	return r
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
		return nil
	}
	if s.observation.Standby == "on" {
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
	if e == nil {
		c.mu.Lock()
		c.connector = connector
		c.mu.Unlock()
		runDone = make(chan error, 1)
		go func() { runDone <- connector.Run(c.ctx) }()
		go func() { defer close(monitorDone); s.monitor(c) }()
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
	c.intent = nil
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
		c.mu.Lock()
		last := c.intent
		c.mu.Unlock()
		if last != nil {
			s.collect(lease, *last, collected)
		}
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
		run := i.Operation() == agenttransport.ResidentRunOperation
		s.mu.Lock()
		admitted := s.admissionLocked(c, run)
		s.mu.Unlock()
		if admitted == admitNothing {
			_ = lease.Refuse(i)
			break
		}
		if admitted == admitNoRun {
			_ = lease.Refuse(i)
			continue
		}
		var request *workerowner.Request
		if admitted == admitRun {
			request, e = source.NewRunRequest(i)
		} else {
			request, e = source.NewRequest(i)
		}
		if e != nil {
			_ = lease.Refuse(i)
			continue
		}
		sent := false
		select {
		case requests <- request:
			sent = true
		case <-c.ctx.Done():
		case <-lease.Context().Done():
		case <-source.Context().Done():
		}
		if !sent {
			break
		}
		select {
		case e = <-request.Reply:
			if e == nil {
				c.mu.Lock()
				copy := i
				c.intent = &copy
				c.mu.Unlock()
				if e = lease.Pending(i); e != nil {
					err = e
				}
			} else {
				_ = lease.Refuse(i)
			}
		case <-c.ctx.Done():
		case <-lease.Context().Done():
		case <-source.Context().Done():
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
		c.intent = nil
		c.mu.Unlock()
	}
	return errors.Join(err, finish)
}
func (s *Standby) monitor(c *standbyCycle) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
		}
		c.mu.Lock()
		connector, source, intent, lease := c.connector, c.source, c.intent, c.lease
		c.mu.Unlock()
		if connector == nil {
			continue
		}
		stopForUnknown := false
		control := connector.Snapshot()
		owned := c.owner.Snapshot()
		var w *maclaunch.ResidentWorker
		if source != nil && intent != nil {
			w, _ = source.Worker(intent.RequestID())
		}
		s.mu.Lock()
		before := cloneObservation(s.observation)
		if s.cycle != c {
			s.mu.Unlock()
			return
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
		s.mu.Unlock()
		if intent != nil && lease != nil && (owned.Unknown || owned.Owned == 0) {
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
			s.collect(lease, *intent, collection)
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
