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
	takeOnce        sync.Once
	releaseOnce     sync.Once
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
	defer f.mu.Unlock()
	if f.releaseFailures > 0 {
		f.releaseFailures--
		return f.releaseErr
	}
	if f.held > 0 {
		f.held--
	}
	return nil
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
		standby.reconcileSleep(cycle.owner, true)
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
	h.standby.beginSleepAdmission() // B is counted before its Owner handoff.
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

	b := newSleepTestSession()
	request := sleepRequest(b, h.source, time.Minute)
	ready := make(chan error, 1)
	approve := request.Approve
	request.Approve = func(ctx context.Context) (*workerowner.Worker, error) {
		err := h.standby.acquireSleep(ctx)
		ready <- err
		if err != nil {
			return nil, err
		}
		return approve(ctx)
	}
	h.requests <- request
	if err := <-request.Reply; err != nil {
		t.Fatalf("B admission failed: %v", err)
	}
	if err := <-ready; err != nil {
		t.Fatalf("B could not confirm assertion: %v", err)
	}
	h.standby.finishSleepAdmission(h.cycle.owner)
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

func TestRequestContextBoundsWaitForTake(t *testing.T) {
	gate := make(chan struct{})
	fake := &fakeSleepAssertion{takeStarted: make(chan struct{}), takeGate: gate}
	h := newSleepHarness(t, StandbyConfig{}, fake)
	session := newSleepTestSession()
	result := make(chan error, 1)
	go func() {
		sent, replied, err := h.standby.submitOwnedRequest(h.cycle, sleepRequest(session, h.source, 50*time.Millisecond), h.requests, h.cycle.ctx, h.source)
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
	h.cancel()
	eventuallySleep(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	waitSleepOperation(t, h.standby, fake, 1, 0)
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
	h.standby.reconcileSleep(h.cycle.owner, true) // OFF/stop ignores Unknown after its wait budget.
	waitSleepOperation(t, h.standby, fake, 1, 0)
}

func TestReleaseFailureRetainsStateAndNextReconcileRetries(t *testing.T) {
	failure := errors.New("synthetic assertion release failure")
	fake := &fakeSleepAssertion{releaseErr: failure, releaseFailures: 100}
	h := newSleepHarness(t, StandbyConfig{}, fake)
	h.standby.beginSleepAdmission()
	if err := h.standby.acquireSleep(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.standby.reconcileSleep(h.cycle.owner, true)
	eventuallySleep(t, func() bool {
		_, releases, _, _ := fake.counts()
		h.standby.sleepMu.Lock()
		defer h.standby.sleepMu.Unlock()
		return releases == 1 && !h.standby.sleepReleasing
	})
	time.Sleep(20 * time.Millisecond)
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 1 || held != 1 {
		t.Fatalf("release failure was not retained for the next reconciliation: takes=%d releases=%d held=%d", takes, releases, held)
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
	h.standby.reconcileSleep(h.cycle.owner, false)
	waitSleepOperation(t, h.standby, fake, 2, 0)
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
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := standby.Close(ctx)
	if err != nil || time.Since(started) >= 150*time.Millisecond {
		t.Fatalf("Close included blocked release in its result: elapsed=%s error=%v", time.Since(started), err)
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
}
