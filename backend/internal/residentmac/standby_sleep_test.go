package residentmac

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/workerowner"
)

type fakeSleepAssertion struct {
	mu              sync.Mutex
	takes           int
	releases        int
	held            int
	peak            int
	takeErr         error
	releaseErr      error
	releaseFailures int
	takeStarted     chan struct{}
	takeGate        <-chan struct{}
	releaseStarted  chan struct{}
	releaseGate     <-chan struct{}
	releaseResults  chan error
	takeOnce        sync.Once
	releaseOnce     sync.Once
}

type manualSleepClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*manualSleepTimer
	created chan time.Duration
}

type manualSleepTimer struct {
	at time.Time
	ch chan time.Time
}

type controlledDeadlineContext struct {
	context.Context
	deadline time.Time
	done     chan struct{}
	mu       sync.Mutex
	err      error
	once     sync.Once
}

func newControlledDeadlineContext() *controlledDeadlineContext {
	return &controlledDeadlineContext{Context: context.Background(), deadline: time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC), done: make(chan struct{})}
}

func (c *controlledDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }
func (c *controlledDeadlineContext) Done() <-chan struct{}       { return c.done }
func (c *controlledDeadlineContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}
func (c *controlledDeadlineContext) expire() {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = context.DeadlineExceeded
		close(c.done)
		c.mu.Unlock()
	})
}

func newManualSleepClock(now time.Time) *manualSleepClock {
	return &manualSleepClock{now: now, created: make(chan time.Duration, 16)}
}

func (c *manualSleepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualSleepClock) After(delay time.Duration) <-chan time.Time {
	c.mu.Lock()
	timer := &manualSleepTimer{at: c.now.Add(delay), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, timer)
	c.mu.Unlock()
	c.created <- delay
	return timer.ch
}

func (c *manualSleepClock) Advance(delay time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	now := c.now
	due := make([]*manualSleepTimer, 0, len(c.timers))
	active := c.timers[:0]
	for _, timer := range c.timers {
		if !timer.at.After(now) {
			due = append(due, timer)
		} else {
			active = append(active, timer)
		}
	}
	c.timers = active
	c.mu.Unlock()
	for _, timer := range due {
		timer.ch <- now
	}
}

func waitManualSleepTimer(t *testing.T, clock *manualSleepClock) time.Duration {
	t.Helper()
	select {
	case delay := <-clock.created:
		return delay
	case <-time.After(time.Second):
		t.Fatal("sleep timer was not scheduled")
		return 0
	}
}

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *synchronizedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(value)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func (f *fakeSleepAssertion) Take() error {
	f.mu.Lock()
	f.takes++
	err, gate, started := f.takeErr, f.takeGate, f.takeStarted
	f.mu.Unlock()
	if started != nil {
		f.takeOnce.Do(func() { close(started) })
	}
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		return err
	}
	f.held++
	if f.held > f.peak {
		f.peak = f.held
	}
	return nil
}

func (f *fakeSleepAssertion) Release() error {
	f.mu.Lock()
	f.releases++
	gate, started := f.releaseGate, f.releaseStarted
	f.mu.Unlock()
	if started != nil {
		f.releaseOnce.Do(func() { close(started) })
	}
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	var err error
	if f.releaseFailures > 0 {
		f.releaseFailures--
		err = f.releaseErr
	} else if f.held > 0 {
		f.held--
	}
	results := f.releaseResults
	f.mu.Unlock()
	if results != nil {
		results <- err
	}
	return err
}

func (f *fakeSleepAssertion) counts() (takes, releases, held, peak int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.takes, f.releases, f.held, f.peak
}

type sleepTestSession struct {
	started      chan struct{}
	finish       chan struct{}
	once         sync.Once
	sourceCancel context.CancelFunc
}

func newSleepTestSession() *sleepTestSession {
	return &sleepTestSession{started: make(chan struct{}), finish: make(chan struct{})}
}

func (s *sleepTestSession) Run(ctx context.Context) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.finish:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *sleepTestSession) setSourceCancel(cancel context.CancelFunc) { s.sourceCancel = cancel }
func (s *sleepTestSession) Close(context.Context) error {
	if s.sourceCancel != nil {
		s.sourceCancel()
	}
	return nil
}

type unknownSleepSession struct {
	started      chan struct{}
	sourceCancel context.CancelFunc
}

type closeGateSleepSession struct {
	started      chan struct{}
	closeStarted chan struct{}
	release      chan struct{}
	closeOnce    sync.Once
	sourceCancel context.CancelFunc
}

func (s *closeGateSleepSession) Run(ctx context.Context) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}
func (s *closeGateSleepSession) setSourceCancel(cancel context.CancelFunc) { s.sourceCancel = cancel }
func (s *closeGateSleepSession) Close(ctx context.Context) error {
	if s.sourceCancel != nil {
		s.sourceCancel()
	}
	s.closeOnce.Do(func() { close(s.closeStarted) })
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *unknownSleepSession) Run(ctx context.Context) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}
func (s *unknownSleepSession) setSourceCancel(cancel context.CancelFunc) { s.sourceCancel = cancel }
func (s *unknownSleepSession) Close(context.Context) error {
	if s.sourceCancel != nil {
		s.sourceCancel()
	}
	return errors.New("synthetic cleanup unknown")
}

type sleepHarness struct {
	standby  *Standby
	cycle    *standbyCycle
	source   context.Context
	requests chan *workerowner.Request
	cancel   context.CancelFunc
	done     chan struct{}
	waitErr  error
}

func newSleepHarness(t *testing.T, config StandbyConfig, assertion sleepAssertion) *sleepHarness {
	t.Helper()
	host, cancelHost := context.WithCancel(context.Background())
	source, cancelSource := context.WithCancel(context.Background())
	standby := newStandbyWithConfig(config)
	standby.sleep = assertion
	cycle := &standbyCycle{ctx: host, cancel: cancelHost, owner: workerowner.New(2 * time.Second), done: make(chan struct{})}
	standby.beginSleepCycle(cycle.owner)
	requests := make(chan *workerowner.Request)
	h := &sleepHarness{standby: standby, cycle: cycle, source: source, requests: requests, cancel: cancelSource, done: make(chan struct{})}
	go func() { h.waitErr = cycle.waitSource(source, requests); close(h.done) }()
	t.Cleanup(func() {
		cancelSource()
		cancelHost()
		select {
		case <-h.done:
		case <-time.After(3 * time.Second):
			t.Error("Owner source did not stop")
		}
		ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = cycle.owner.Stop(ctx)
		standby.endSleepCycle(cycle.owner)
	})
	return h
}

type sleepCancelSession interface {
	workerowner.Session
	setSourceCancel(context.CancelFunc)
}

func sleepRequest(session sleepCancelSession, source context.Context, timeout time.Duration) *workerowner.Request {
	deadline := time.Now().Add(timeout)
	requestSource, cancel := context.WithDeadline(source, deadline)
	session.setSourceCancel(cancel)
	return &workerowner.Request{
		SourceContext: requestSource,
		Deadline:      deadline,
		Reply:         make(chan error, 1),
		Approve: func(context.Context) (*workerowner.Worker, error) {
			return &workerowner.Worker{Session: session}, nil
		},
	}
}

func submitSleepWork(t *testing.T, h *sleepHarness, session sleepCancelSession) error {
	t.Helper()
	sent, replied, err := h.standby.submitOwnedRequest(h.cycle, sleepRequest(session, h.source, time.Minute), h.requests, h.cycle.ctx, h.source)
	if !sent || !replied {
		t.Fatalf("request admission sent=%v replied=%v error=%v", sent, replied, err)
	}
	return err
}

func submitWithTimeout(t *testing.T, h *sleepHarness, session sleepCancelSession, timeout time.Duration) error {
	t.Helper()
	sent, replied, err := h.standby.submitOwnedRequest(h.cycle, sleepRequest(session, h.source, timeout), h.requests, h.cycle.ctx, h.source)
	if !sent || !replied {
		t.Fatalf("request admission sent=%v replied=%v error=%v", sent, replied, err)
	}
	return err
}

func eventuallySleep(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("sleep condition did not become true")
}

func waitSleepOperation(t *testing.T, s *Standby, fake *fakeSleepAssertion, releases int, held int) {
	t.Helper()
	eventuallySleep(t, func() bool {
		_, count, actual, _ := fake.counts()
		s.sleepMu.Lock()
		defer s.sleepMu.Unlock()
		return count == releases && actual == held && !s.sleepReleasing
	})
}

func waitSleepWorkerExit(t *testing.T, s *Standby) {
	t.Helper()
	s.sleepMu.Lock()
	done := s.sleepWorkerDone
	running := s.sleepWorker
	s.sleepMu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("sleep worker did not exit")
		}
	}
	s.sleepMu.Lock()
	running = s.sleepWorker
	s.sleepMu.Unlock()
	if running {
		t.Fatal("sleep worker remained active after its done signal")
	}
}

func TestSleepTakeFailureRefusesAfterOwnerAttemptAndNextWorkCanStart(t *testing.T) {
	failure := errors.New("synthetic assertion take failure")
	fake := &fakeSleepAssertion{takeErr: failure}
	h := newSleepHarness(t, StandbyConfig{}, fake)
	var diagnostics synchronizedBuffer
	previous := log.Writer()
	log.SetOutput(&diagnostics)
	t.Cleanup(func() { log.SetOutput(previous) })
	first := newSleepTestSession()
	if err := submitSleepWork(t, h, first); !errors.Is(err, agenttransport.ErrWorkUnconfigured) {
		t.Fatalf("failed Take was not refused: %v", err)
	}
	select {
	case <-first.started:
		t.Fatal("work started after assertion acquisition failed")
	default:
	}
	eventuallySleep(t, func() bool { return strings.Contains(diagnostics.String(), failure.Error()) })
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 0 || held != 0 {
		t.Fatalf("failed Take state: takes=%d releases=%d held=%d", takes, releases, held)
	}

	fake.mu.Lock()
	fake.takeErr = nil
	fake.mu.Unlock()
	second := newSleepTestSession()
	if err := submitSleepWork(t, h, second); err != nil {
		t.Fatalf("next request was incorrectly rejected: %v", err)
	}
	select {
	case <-second.started:
	case <-time.After(time.Second):
		t.Fatal("next work did not start")
	}
	close(second.finish)
	eventuallySleep(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	h.standby.reconcileSleep(h.cycle.owner, false)
	waitSleepOperation(t, h.standby, fake, 1, 0)
}

func TestBusyBDoesNotReplaceAAndACollectionReleases(t *testing.T) {
	fake := &fakeSleepAssertion{}
	h := newSleepHarness(t, StandbyConfig{}, fake)
	a := newSleepTestSession()
	if err := submitSleepWork(t, h, a); err != nil {
		t.Fatal(err)
	}
	<-a.started
	b := newSleepTestSession()
	if err := submitSleepWork(t, h, b); !errors.Is(err, workerowner.ErrBusy) {
		t.Fatalf("B did not receive ErrBusy: %v", err)
	}
	select {
	case <-b.started:
		t.Fatal("busy B started")
	default:
	}
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 0 || held != 1 {
		t.Fatalf("B refusal disturbed A assertion: takes=%d releases=%d held=%d", takes, releases, held)
	}
	close(a.finish)
	eventuallySleep(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	h.standby.reconcileSleep(h.cycle.owner, false)
	waitSleepOperation(t, h.standby, fake, 1, 0)
}

func TestPendingBPreventsACollectionReconcileFromReleasing(t *testing.T) {
	fake := &fakeSleepAssertion{}
	h := newSleepHarness(t, StandbyConfig{}, fake)
	a := newSleepTestSession()
	if err := submitSleepWork(t, h, a); err != nil {
		t.Fatal(err)
	}
	<-a.started
	blockedRequests := make(chan *workerowner.Request)
	continueB := make(chan struct{})
	bReceived := make(chan struct{})
	forwardB := make(chan struct{})
	go func() {
		request := <-blockedRequests
		close(bReceived)
		<-continueB
		h.requests <- request
		close(forwardB)
	}()
	b := newSleepTestSession()
	bResult := make(chan error, 1)
	go func() {
		sent, replied, err := h.standby.submitOwnedRequest(h.cycle, sleepRequest(b, h.source, time.Minute), blockedRequests, h.cycle.ctx, h.source)
		if !sent || !replied {
			bResult <- fmt.Errorf("B request sent=%v replied=%v: %w", sent, replied, err)
			return
		}
		bResult <- err
	}()
	select {
	case <-bReceived:
	case <-time.After(time.Second):
		t.Fatal("B was not held between admission and Owner handoff")
	}
	close(a.finish)
	eventuallySleep(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	h.standby.reconcileSleep(h.cycle.owner, false)
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 0 || held != 1 {
		t.Fatalf("A reconciliation released during B admission: takes=%d releases=%d held=%d", takes, releases, held)
	}
	h.standby.sleepMu.Lock()
	releasing := h.standby.sleepReleasing
	h.standby.sleepMu.Unlock()
	if releasing {
		t.Fatal("release was reserved while B was being admitted")
	}

	close(continueB)
	select {
	case <-forwardB:
	case <-time.After(time.Second):
		t.Fatal("B was not handed to Owner after A collection")
	}
	if err := <-bResult; err != nil {
		t.Fatalf("B request failed through submitOwnedRequest: %v", err)
	}
	select {
	case <-b.started:
	case <-time.After(time.Second):
		t.Fatal("B did not start")
	}
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 0 || held != 1 {
		t.Fatalf("B ran without continuous assertion ownership: takes=%d releases=%d held=%d", takes, releases, held)
	}
	close(b.finish)
	eventuallySleep(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	h.standby.reconcileSleep(h.cycle.owner, false)
	waitSleepOperation(t, h.standby, fake, 1, 0)
}

func TestSleepTakeWaitExpiresBeforeWorkDeadline(t *testing.T) {
	gate := make(chan struct{})
	fake := &fakeSleepAssertion{takeStarted: make(chan struct{}), takeGate: gate}
	h := newSleepHarness(t, StandbyConfig{}, fake)
	clock := newManualSleepClock(time.Now())
	h.standby.sleepNow = clock.Now
	h.standby.sleepTimer = clock.After
	session := newSleepTestSession()
	result := make(chan error, 1)
	go func() {
		sent, replied, err := h.standby.submitOwnedRequest(h.cycle, sleepRequest(session, h.source, time.Minute), h.requests, h.cycle.ctx, h.source)
		if !sent || !replied {
			result <- fmt.Errorf("request sent=%v replied=%v: %w", sent, replied, err)
			return
		}
		result <- err
	}()
	select {
	case <-fake.takeStarted:
	case <-time.After(time.Second):
		t.Fatal("Take did not start")
	}
	wait := waitManualSleepTimer(t, clock)
	if wait != sleepTakeWait {
		t.Fatalf("Take wait = %s, want the bounded %s acquisition wait", wait, sleepTakeWait)
	}
	clock.Advance(wait)
	select {
	case err := <-result:
		if !errors.Is(err, agenttransport.ErrWorkUnconfigured) {
			t.Fatalf("expired Take wait did not refuse work: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not honor its deadline while Take was blocked")
	}
	select {
	case <-session.started:
		t.Fatal("work started after its Take deadline")
	default:
	}
	close(gate)
	eventuallySleep(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	waitSleepOperation(t, h.standby, fake, 1, 0)
}

func TestInsufficientWorkTimeSkipsSleepTake(t *testing.T) {
	fake := &fakeSleepAssertion{}
	h := newSleepHarness(t, StandbyConfig{}, fake)
	session := newSleepTestSession()
	err := submitWithTimeout(t, h, session, sleepRefusalReserve-time.Second)
	if !errors.Is(err, agenttransport.ErrWorkUnconfigured) {
		t.Fatalf("short remaining work time was not refused: %v", err)
	}
	if takes, releases, held, _ := fake.counts(); takes != 0 || releases != 0 || held != 0 {
		t.Fatalf("short remaining time attempted an assertion Take: takes=%d releases=%d held=%d", takes, releases, held)
	}
}

func TestUnknownOwnedKeepsAssertionUntilForcedOffReconcile(t *testing.T) {
	fake := &fakeSleepAssertion{}
	h := newSleepHarness(t, StandbyConfig{}, fake)
	session := &unknownSleepSession{started: make(chan struct{})}
	if err := submitSleepWork(t, h, session); err != nil {
		t.Fatal(err)
	}
	<-session.started
	h.cancel()
	eventuallySleep(t, func() bool { return h.cycle.owner.Snapshot().Unknown })
	owned := h.cycle.owner.Snapshot()
	if owned.Owned != 1 || !owned.Unknown {
		t.Fatalf("test did not establish unknown ownership: %+v", owned)
	}
	h.standby.reconcileSleep(h.cycle.owner, false)
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 0 || held != 1 {
		t.Fatalf("Unknown released while standby remained active: takes=%d releases=%d held=%d", takes, releases, held)
	}
	h.standby.endSleepCycle(h.cycle.owner) // Cycle end releases even though collection is Unknown.
	waitSleepOperation(t, h.standby, fake, 1, 0)
	h.standby.reconcileSleep(h.cycle.owner, false) // A late old-Owner observation cannot turn it back on.
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 1 || held != 0 {
		t.Fatalf("late Owner observation restored the assertion: takes=%d releases=%d held=%d", takes, releases, held)
	}
}

func TestCloseDiscardsReservedTakeBeforeOSCall(t *testing.T) {
	fake := &fakeSleepAssertion{}
	h := newSleepHarness(t, StandbyConfig{}, fake)
	reserved := make(chan struct{})
	continueTake := make(chan struct{})
	h.standby.sleepBeforeTake = func() {
		close(reserved)
		<-continueTake
	}
	h.standby.beginSleepAdmission(h.cycle.owner)
	result := make(chan error, 1)
	go func() {
		result <- h.standby.acquireSleep(context.Background(), time.Now().Add(time.Minute))
	}()
	select {
	case <-reserved:
	case <-time.After(time.Second):
		t.Fatal("Take was not reserved")
	}

	h.standby.mu.Lock()
	h.standby.cycle = h.cycle
	h.standby.mu.Unlock()
	go func() {
		<-h.cycle.ctx.Done()
		h.standby.endSleepCycle(h.cycle.owner)
		close(h.cycle.done)
	}()
	ctx := newControlledDeadlineContext()
	if err := h.standby.Close(ctx); err != nil {
		t.Fatalf("Close did not complete: %v", err)
	}
	close(continueTake)
	if err := <-result; !errors.Is(err, ErrStandbyUnavailable) && !errors.Is(err, context.Canceled) {
		t.Fatalf("stale Take did not refuse its waiter: %v", err)
	}
	if takes, releases, held, _ := fake.counts(); takes != 0 || releases != 0 || held != 0 {
		t.Fatalf("reserved Take ran after Close: takes=%d releases=%d held=%d", takes, releases, held)
	}
	waitSleepWorkerExit(t, h.standby)
}

func acquireSleepWithManualClock(t *testing.T, h *sleepHarness, clock *manualSleepClock) {
	t.Helper()
	h.standby.sleepNow = clock.Now
	h.standby.sleepTimer = clock.After
	h.standby.beginSleepAdmission(h.cycle.owner)
	if err := h.standby.acquireSleep(context.Background(), clock.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if delay := waitManualSleepTimer(t, clock); delay != sleepTakeWait {
		t.Fatalf("Take wait = %s, want %s", delay, sleepTakeWait)
	}
}

func TestReleaseFailureRetriesUntilReleased(t *testing.T) {
	failure := errors.New("synthetic assertion release failure")
	fake := &fakeSleepAssertion{releaseErr: failure, releaseFailures: 1, releaseResults: make(chan error, 4)}
	h := newSleepHarness(t, StandbyConfig{}, fake)
	clock := newManualSleepClock(time.Now())
	acquireSleepWithManualClock(t, h, clock)
	h.standby.finishSleepAdmission(h.cycle.owner)
	if delay := waitManualSleepTimer(t, clock); delay != sleepReleaseRetryInitial {
		t.Fatalf("first release retry delay = %s, want %s", delay, sleepReleaseRetryInitial)
	}
	if err := <-fake.releaseResults; !errors.Is(err, failure) {
		t.Fatalf("first release result = %v, want failure", err)
	}
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 1 || held != 1 {
		t.Fatalf("failed release did not retain the assertion for retry: takes=%d releases=%d held=%d", takes, releases, held)
	}
	h.standby.sleepMu.Lock()
	recorded := h.standby.sleepErr
	h.standby.sleepMu.Unlock()
	if !errors.Is(recorded, failure) {
		t.Fatalf("release failure was not retained: %v", recorded)
	}
	fake.mu.Lock()
	fake.releaseFailures = 0
	fake.mu.Unlock()
	clock.Advance(sleepReleaseRetryInitial)
	if err := <-fake.releaseResults; err != nil {
		t.Fatalf("retry release failed: %v", err)
	}
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 2 || held != 0 {
		t.Fatalf("retry did not release the assertion: takes=%d releases=%d held=%d", takes, releases, held)
	}
	waitSleepWorkerExit(t, h.standby)
}

func TestReleaseRetryBackoffCapsAtThirtySeconds(t *testing.T) {
	failure := errors.New("synthetic assertion release failure")
	fake := &fakeSleepAssertion{releaseErr: failure, releaseFailures: 7, releaseResults: make(chan error, 8)}
	h := newSleepHarness(t, StandbyConfig{}, fake)
	clock := newManualSleepClock(time.Now())
	h.standby.sleepNow = clock.Now
	h.standby.sleepTimer = clock.After
	h.standby.sleepMu.Lock()
	h.standby.sleepHeld = true
	h.standby.sleepDesired = true
	h.standby.setSleepDesiredLocked(false)
	h.standby.sleepMu.Unlock()

	delays := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for _, want := range delays {
		if err := <-fake.releaseResults; !errors.Is(err, failure) {
			t.Fatalf("release attempt result = %v, want failure", err)
		}
		if got := waitManualSleepTimer(t, clock); got != want {
			t.Fatalf("release retry delay = %s, want %s", got, want)
		}
		clock.Advance(want)
	}
	if err := <-fake.releaseResults; err != nil {
		t.Fatalf("release after capped retries failed: %v", err)
	}
	waitSleepWorkerExit(t, h.standby)
}

func TestReleaseRetryStopsWhenWorkReturns(t *testing.T) {
	failure := errors.New("synthetic assertion release failure")
	fake := &fakeSleepAssertion{releaseErr: failure, releaseFailures: 1, releaseResults: make(chan error, 4)}
	h := newSleepHarness(t, StandbyConfig{}, fake)
	clock := newManualSleepClock(time.Now())
	acquireSleepWithManualClock(t, h, clock)
	h.standby.finishSleepAdmission(h.cycle.owner)
	if delay := waitManualSleepTimer(t, clock); delay != sleepReleaseRetryInitial {
		t.Fatalf("first release retry delay = %s, want %s", delay, sleepReleaseRetryInitial)
	}
	if err := <-fake.releaseResults; !errors.Is(err, failure) {
		t.Fatalf("first release result = %v, want failure", err)
	}
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 1 || held != 1 {
		t.Fatalf("failed release did not retain the assertion for retry: takes=%d releases=%d held=%d", takes, releases, held)
	}
	fake.mu.Lock()
	fake.releaseFailures = 0
	fake.mu.Unlock()
	second := newSleepTestSession()
	if err := submitSleepWork(t, h, second); err != nil {
		t.Fatalf("new work could not use the still-held assertion: %v", err)
	}
	select {
	case <-second.started:
	case <-time.After(time.Second):
		t.Fatal("new work did not start while the assertion remained held")
	}
	waitSleepWorkerExit(t, h.standby)
	clock.Advance(sleepReleaseRetryMax)
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 1 || held != 1 {
		t.Fatalf("release retry continued after the desire returned to hold: takes=%d releases=%d held=%d", takes, releases, held)
	}
	close(second.finish)
	eventuallySleep(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	h.standby.reconcileSleep(h.cycle.owner, false)
	if err := <-fake.releaseResults; err != nil {
		t.Fatalf("release after work collection failed: %v", err)
	}
	waitSleepOperation(t, h.standby, fake, 2, 0)
	waitSleepWorkerExit(t, h.standby)
}

func TestIdleStandbyAndAllowSleepDoNotAcquire(t *testing.T) {
	standby, connector, _ := idleStandby(t)
	fake := &fakeSleepAssertion{}
	standby.sleep = fake
	if receipt := standby.RequestOn(1, standby.Snapshot().Revision, 2*time.Hour); receipt.Disposition != "accepted" {
		t.Fatal(receipt)
	}
	if takes, releases, held, _ := fake.counts(); takes != 0 || releases != 0 || held != 0 {
		t.Fatalf("idle standby acquired assertion: takes=%d releases=%d held=%d", takes, releases, held)
	}
	close(connector.gate)

	allowed := newSleepHarness(t, StandbyConfig{AllowSleepDuringWork: true}, fake)
	session := newSleepTestSession()
	if err := submitSleepWork(t, allowed, session); err != nil {
		t.Fatal(err)
	}
	<-session.started
	close(session.finish)
	eventuallySleep(t, func() bool { return allowed.cycle.owner.Snapshot().Owned == 0 })
	allowed.standby.reconcileSleep(allowed.cycle.owner, false)
	if takes, releases, held, _ := fake.counts(); takes != 0 || releases != 0 || held != 0 {
		t.Fatalf("AllowSleepDuringWork acquired assertion: takes=%d releases=%d held=%d", takes, releases, held)
	}
}

func TestCloseReturnsCollectionResultWhileReleaseIsBlocked(t *testing.T) {
	gate := make(chan struct{})
	fake := &fakeSleepAssertion{releaseStarted: make(chan struct{}), releaseGate: gate}
	standby, connector, _ := idleStandby(t)
	standby.sleep = fake
	if receipt := standby.RequestOn(1, standby.Snapshot().Revision, 2*time.Hour); receipt.Disposition != "accepted" {
		t.Fatal(receipt)
	}
	cycle := standby.cycle
	source, cancelSource := context.WithCancel(context.Background())
	defer cancelSource()
	requests := make(chan *workerowner.Request)
	ownerDone := make(chan error, 1)
	go func() { ownerDone <- cycle.waitSource(source, requests) }()
	session := newSleepTestSession()
	if sent, replied, err := standby.submitOwnedRequest(cycle, sleepRequest(session, source, time.Minute), requests, cycle.ctx, source); !sent || !replied || err != nil {
		t.Fatalf("work admission sent=%v replied=%v error=%v", sent, replied, err)
	}
	<-session.started
	close(connector.gate)
	ctx := newControlledDeadlineContext()
	closeResult := make(chan error, 1)
	go func() { closeResult <- standby.Close(ctx) }()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close included blocked release in its result: %v", err)
		}
	case <-time.After(time.Second):
		ctx.expire()
		err := <-closeResult
		t.Fatalf("Close waited for blocked release: %v", err)
	}
	select {
	case <-fake.releaseStarted:
	case <-time.After(time.Second):
		t.Fatal("Close did not schedule the release")
	}
	if observation := standby.Snapshot(); observation.Cleanup != "confirmed" {
		t.Fatalf("confirmed collection became Unknown: %+v", observation)
	}
	close(gate)
	select {
	case <-ownerDone:
	case <-time.After(time.Second):
		t.Fatal("Owner source did not stop")
	}
	waitSleepOperation(t, standby, fake, 1, 0)
	waitSleepWorkerExit(t, standby)
}

func TestCycleEndReleasesAfterCloseCallerDeadlineDuringCollection(t *testing.T) {
	release := make(chan struct{})
	fake := &fakeSleepAssertion{}
	standby, connector, _ := idleStandby(t)
	standby.sleep = fake
	if receipt := standby.RequestOn(1, standby.Snapshot().Revision, 2*time.Hour); receipt.Disposition != "accepted" {
		t.Fatal(receipt)
	}
	cycle := standby.cycle
	source, cancelSource := context.WithCancel(context.Background())
	defer cancelSource()
	requests := make(chan *workerowner.Request)
	ownerDone := make(chan error, 1)
	go func() { ownerDone <- cycle.waitSource(source, requests) }()
	session := &closeGateSleepSession{started: make(chan struct{}), closeStarted: make(chan struct{}), release: release}
	if sent, replied, err := standby.submitOwnedRequest(cycle, sleepRequest(session, source, time.Minute), requests, cycle.ctx, source); !sent || !replied || err != nil {
		t.Fatalf("work admission sent=%v replied=%v error=%v", sent, replied, err)
	}
	<-session.started
	close(connector.gate)
	closeResult := make(chan error, 1)
	ctx := newControlledDeadlineContext()
	go func() { closeResult <- standby.Close(ctx) }()
	select {
	case <-session.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("Close did not enter owner collection")
	}
	ctx.expire()
	select {
	case err := <-closeResult:
		if !errors.Is(err, ErrStandbyUnknown) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close caller deadline did not expire during collection: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close caller did not return at its deadline")
	}
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 0 || held != 1 {
		t.Fatalf("assertion released before collection ended: takes=%d releases=%d held=%d", takes, releases, held)
	}
	close(release)
	select {
	case <-cycle.done:
	case <-time.After(time.Second):
		t.Fatal("cycle did not finish after collection was released")
	}
	select {
	case <-ownerDone:
	case <-time.After(time.Second):
		t.Fatal("Owner source did not stop")
	}
	waitSleepOperation(t, standby, fake, 1, 0)
	waitSleepWorkerExit(t, standby)
}
