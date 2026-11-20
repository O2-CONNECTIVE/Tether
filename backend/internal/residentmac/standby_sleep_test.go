package residentmac

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
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
	releaseStarted  chan struct{}
	releaseGate     <-chan struct{}
	releaseOnce     sync.Once
}

func (f *fakeSleepAssertion) Take() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.takes++
	if f.takeErr != nil {
		return f.takeErr
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
	f.mu.Unlock()
	if f.releaseStarted != nil {
		f.releaseOnce.Do(func() { close(f.releaseStarted) })
	}
	if f.releaseGate != nil {
		<-f.releaseGate
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

type sleepProofLease struct {
	entered chan struct{}
	gate    <-chan struct{}
	err     error
	once    sync.Once
	mu      sync.Mutex
	calls   int
	result  error
}

func (l *sleepProofLease) Collected(_ agentgateway.ResidentWorkBinding, errorResult error) error {
	l.mu.Lock()
	l.calls++
	l.result = errorResult
	l.mu.Unlock()
	if l.entered != nil {
		l.once.Do(func() { close(l.entered) })
	}
	if l.gate != nil {
		<-l.gate
	}
	return l.err
}
func (l *sleepProofLease) Unsent() []byte { return []byte("signed-test-proof") }
func (l *sleepProofLease) stats() (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls, l.result
}

type sleepTestSession struct {
	started      chan struct{}
	finish       chan struct{}
	closeStarted chan struct{}
	closeGate    <-chan struct{}
	cancelSource context.CancelFunc
	startOnce    sync.Once
	closeOnce    sync.Once
}

func newSleepTestSession() *sleepTestSession {
	return &sleepTestSession{started: make(chan struct{}), finish: make(chan struct{})}
}
func (s *sleepTestSession) Run(ctx context.Context) error {
	s.startOnce.Do(func() { close(s.started) })
	select {
	case <-s.finish:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *sleepTestSession) Close(ctx context.Context) error {
	if s.cancelSource != nil {
		defer s.cancelSource()
	}
	if s.closeStarted != nil {
		s.closeOnce.Do(func() { close(s.closeStarted) })
	}
	if s.closeGate == nil {
		return nil
	}
	select {
	case <-s.closeGate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type sleepHarness struct {
	standby      *Standby
	cycle        *standbyCycle
	source       context.Context
	requests     chan *workerowner.Request
	cancelSource context.CancelFunc
	done         chan struct{}
	mu           sync.Mutex
	err          error
}

func newSleepHarness(t *testing.T, config StandbyConfig) *sleepHarness {
	t.Helper()
	host, cancelHost := context.WithCancel(context.Background())
	source, cancelSource := context.WithCancel(context.Background())
	h := &sleepHarness{
		standby: newStandbyWithConfig(config),
		cycle:   &standbyCycle{ctx: host, cancel: cancelHost, owner: workerowner.New(2 * time.Second), done: make(chan struct{})},
		source:  source, requests: make(chan *workerowner.Request), cancelSource: cancelSource, done: make(chan struct{}),
	}
	go func() {
		err := h.cycle.waitSource(source, h.requests)
		h.mu.Lock()
		h.err = err
		h.mu.Unlock()
		close(h.done)
	}()
	t.Cleanup(func() {
		cancelSource()
		cancelHost()
		ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = h.cycle.owner.Stop(ctx)
		select {
		case <-h.done:
		case <-ctx.Done():
		}
	})
	return h
}

func sleepBinding(id int) agentgateway.ResidentWorkBinding {
	return agentgateway.ResidentWorkBinding{RequestID: fmt.Sprintf("%032x", id)}
}

func sleepRequest(session *sleepTestSession, source context.Context) *workerowner.Request {
	deadline := time.Now().Add(time.Minute)
	requestSource, cancelSource := context.WithDeadline(source, deadline)
	session.cancelSource = cancelSource
	return &workerowner.Request{
		SourceContext: requestSource,
		Deadline:      deadline,
		Reply:         make(chan error, 1),
		Approve: func(context.Context) (*workerowner.Worker, error) {
			return &workerowner.Worker{Session: session}, nil
		},
	}
}

func submitSleepWork(t *testing.T, s *Standby, c *standbyCycle, requests chan *workerowner.Request, session *sleepTestSession, id int, leaseCtx, sourceCtx context.Context) *ownedWorkSleep {
	t.Helper()
	sent, replied, err := s.submitOwnedRequest(c, sleepRequest(session, sourceCtx), sleepBinding(id), requests, leaseCtx, sourceCtx)
	if !sent || !replied || err != nil {
		t.Fatalf("request admission sent=%v replied=%v error=%v", sent, replied, err)
	}
	c.mu.Lock()
	workSleep := c.workSleep
	valid := c.work != nil && workSleep != nil
	c.mu.Unlock()
	if !valid {
		t.Fatal("accepted request was not tied to its work lease")
	}
	c.markWorkReady(workSleep) // model lease.Pending succeeding after Owner admission
	return workSleep
}

func waitSleepOwner(t *testing.T, h *sleepHarness) error {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(3 * time.Second):
		t.Fatal("owner did not finish")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

func waitOwnedCount(t *testing.T, s *Standby, want int) {
	t.Helper()
	eventually(t, func() bool {
		s.sleepMu.Lock()
		defer s.sleepMu.Unlock()
		return s.sleepOwners == want
	})
}

func waitSleepProofAttempt(t *testing.T, workSleep *ownedWorkSleep) {
	t.Helper()
	if workSleep == nil {
		t.Fatal("missing work sleep record")
	}
	select {
	case <-workSleep.proofDone:
	case <-time.After(time.Second):
		t.Fatal("collection proof attempt did not finish")
	}
}

func waitSleepReleases(t *testing.T, s *Standby, fake *fakeSleepAssertion, want int) {
	t.Helper()
	eventually(t, func() bool {
		_, releases, _, _ := fake.counts()
		s.sleepMu.Lock()
		defer s.sleepMu.Unlock()
		return releases == want && !s.sleepReleasing
	})
}

func silenceSleepDiagnostics(t *testing.T) {
	t.Helper()
	previous := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(previous) })
}

func TestStandbySleepAssertionFailureRefusesAndNextWorkProceeds(t *testing.T) {
	var diagnostics bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&diagnostics)
	t.Cleanup(func() { log.SetOutput(previous) })
	h := newSleepHarness(t, StandbyConfig{})
	failure := errors.New("synthetic sleep assertion failure")
	fake := &fakeSleepAssertion{takeErr: failure}
	h.standby.sleep = fake
	first := newSleepTestSession()
	ownerAttemptEntered := make(chan struct{})
	ownerAttemptGate := make(chan struct{})
	var releaseOwnerAttempt sync.Once
	allowOwnerAttemptReturn := func() { releaseOwnerAttempt.Do(func() { close(ownerAttemptGate) }) }
	t.Cleanup(allowOwnerAttemptReturn)
	h.standby.afterSleepAssertionResult = func(error) {
		close(ownerAttemptEntered)
		<-ownerAttemptGate
	}
	type submitResult struct {
		sent, replied bool
		err           error
	}
	firstResult := make(chan submitResult, 1)
	go func() {
		sent, replied, err := h.standby.submitOwnedRequest(h.cycle, sleepRequest(first, h.source), sleepBinding(8), h.requests, h.cycle.ctx, h.source)
		firstResult <- submitResult{sent: sent, replied: replied, err: err}
	}()
	select {
	case <-ownerAttemptEntered:
	case <-time.After(time.Second):
		t.Fatal("failed Owner approval did not reach the completion gate")
	}
	if snapshot := h.cycle.owner.Snapshot(); snapshot.Owned != 1 || !snapshot.Pending {
		t.Fatalf("Owner attempt was not held at the failed approval: %+v", snapshot)
	}
	select {
	case result := <-firstResult:
		t.Fatalf("refusal returned before the Owner attempt completed: %+v", result)
	case <-time.After(20 * time.Millisecond):
	}
	allowOwnerAttemptReturn()
	var sent, replied bool
	var result error
	select {
	case first := <-firstResult:
		sent, replied, result = first.sent, first.replied, first.err
	case <-time.After(time.Second):
		t.Fatal("refusal did not follow Owner attempt completion")
	}
	refused := sent && replied && errors.Is(result, agenttransport.ErrWorkUnconfigured)
	select {
	case <-first.started:
		t.Fatal("work started after assertion acquisition failed")
	case <-time.After(100 * time.Millisecond):
	}
	if !refused {
		t.Fatalf("assertion failure was not refused: sent=%v replied=%v result=%v", sent, replied, result)
	}
	h.standby.sleepMu.Lock()
	recorded := h.standby.sleepErr
	h.standby.sleepMu.Unlock()
	if !errors.Is(recorded, failure) || !strings.Contains(diagnostics.String(), failure.Error()) {
		t.Fatalf("assertion failure reason was not retained in diagnostics: error=%v log=%q", recorded, diagnostics.String())
	}
	if observation := h.standby.Snapshot(); observation.Cleanup != "confirmed" {
		t.Fatalf("failed acquisition changed cleanup observation: %+v", observation)
	}

	fake.mu.Lock()
	fake.takeErr = nil
	fake.mu.Unlock()
	second := newSleepTestSession()
	workSleep := submitSleepWork(t, h.standby, h.cycle, h.requests, second, 9, h.cycle.ctx, h.source)
	select {
	case <-second.started:
	case <-time.After(time.Second):
		t.Fatal("next work did not start")
	}
	if takes, releases, held, _ := fake.counts(); takes != 2 || releases != 0 || held != 1 {
		t.Fatalf("next work did not acquire assertion: takes=%d releases=%d held=%d", takes, releases, held)
	}
	close(second.finish)
	eventually(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	h.standby.collect(&sleepProofLease{}, sleepBinding(9), nil, workSleep)
	waitSleepProofAttempt(t, workSleep)
	waitSleepReleases(t, h.standby, fake, 1)
	if takes, releases, held, _ := fake.counts(); takes != 2 || releases != 1 || held != 0 {
		t.Fatalf("next work collection mismatch: takes=%d releases=%d held=%d", takes, releases, held)
	}
	h.cancelSource()
	if err := waitSleepOwner(t, h); err != nil {
		t.Fatal(err)
	}
}

func TestStandbySleepPreventionFollowsOwnedRequestAndCollection(t *testing.T) {
	h := newSleepHarness(t, StandbyConfig{})
	fake := &fakeSleepAssertion{}
	h.standby.sleep = fake
	session := newSleepTestSession()
	binding := sleepBinding(1)
	workSleep := submitSleepWork(t, h.standby, h.cycle, h.requests, session, 1, h.cycle.ctx, h.source)
	select {
	case <-session.started:
	case <-time.After(time.Second):
		t.Fatal("owned work did not start")
	}
	if takes, releases, held, peak := fake.counts(); takes != 1 || releases != 0 || held != 1 || peak != 1 {
		t.Fatalf("owned work assertion counts: takes=%d releases=%d held=%d peak=%d", takes, releases, held, peak)
	}
	close(session.finish)
	eventually(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	proof := &sleepProofLease{}
	h.standby.collect(proof, binding, nil, workSleep)
	waitSleepProofAttempt(t, workSleep)
	waitSleepReleases(t, h.standby, fake, 1)
	if takes, releases, held, peak := fake.counts(); takes != 1 || releases != 1 || held != 0 || peak != 1 {
		t.Fatalf("confirmed collection did not release exactly once: takes=%d releases=%d held=%d peak=%d", takes, releases, held, peak)
	}
	h.cancelSource()
	if err := waitSleepOwner(t, h); err != nil {
		t.Fatal(err)
	}
}

func TestStandbyUnknownCollectionRetainsAssertionUntilBudget(t *testing.T) {
	h := newSleepHarness(t, StandbyConfig{})
	fake := &fakeSleepAssertion{}
	h.standby.sleep = fake
	workSleep := &ownedWorkSleep{proofDone: make(chan struct{}), releaseNow: make(chan struct{})}
	if err := h.standby.acquireOwnedWork(context.Background(), workSleep); err != nil {
		t.Fatal(err)
	}
	workSleep.proofMu.Lock()
	deadline := time.Now().Add(200 * time.Millisecond)
	workSleep.collectionEnd = deadline
	workSleep.proofMu.Unlock()
	proof := &sleepProofLease{}
	h.standby.collect(proof, sleepBinding(10), ErrStandbyUnknown, workSleep)
	waitSleepProofAttempt(t, workSleep)
	if calls, result := proof.stats(); calls != 1 || !errors.Is(result, ErrStandbyUnknown) {
		t.Fatalf("Unknown collection proof changed: calls=%d result=%v", calls, result)
	}
	if _, releases, held, _ := fake.counts(); releases != 0 || held != 1 {
		t.Fatalf("Unknown released before the original budget: releases=%d held=%d", releases, held)
	}
	time.Sleep(time.Until(deadline) / 2)
	if _, releases, held, _ := fake.counts(); releases != 0 || held != 1 {
		t.Fatalf("Unknown released before budget expiry: releases=%d held=%d", releases, held)
	}
	waitSleepReleases(t, h.standby, fake, 1)
	if _, _, held, _ := fake.counts(); held != 0 {
		t.Fatalf("Unknown did not release at budget expiry: held=%d", held)
	}
	h.cancelSource()
	if err := waitSleepOwner(t, h); err != nil {
		t.Fatal(err)
	}
}

func TestStandbyDuplicateCollectionDoesNotReleaseDuringOriginalProof(t *testing.T) {
	h := newSleepHarness(t, StandbyConfig{})
	fake := &fakeSleepAssertion{}
	h.standby.sleep = fake
	workSleep := &ownedWorkSleep{proofDone: make(chan struct{}), releaseNow: make(chan struct{})}
	if err := h.standby.acquireOwnedWork(context.Background(), workSleep); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	entered := make(chan struct{})
	original := &sleepProofLease{entered: entered, gate: gate}
	proved := make(chan struct{})
	go func() {
		h.standby.collect(original, sleepBinding(11), nil, workSleep)
		h.standby.waitForCollectionProof(workSleep)
		close(proved)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("original collection proof did not start")
	}
	duplicate := &sleepProofLease{err: agenttransport.ErrRefused}
	h.standby.collect(duplicate, sleepBinding(11), nil, workSleep)
	select {
	case <-proved:
		t.Fatal("proof completion was reported while the original write was blocked")
	case <-time.After(20 * time.Millisecond):
	}
	if calls, _ := duplicate.stats(); calls != 0 {
		t.Fatalf("duplicate collection proof was attempted: calls=%d", calls)
	}
	if _, releases, held, _ := fake.counts(); releases != 0 || held != 1 {
		t.Fatalf("duplicate path released during original proof: releases=%d held=%d", releases, held)
	}
	close(gate)
	select {
	case <-proved:
	case <-time.After(time.Second):
		t.Fatal("proof completion did not follow the original write")
	}
	waitSleepReleases(t, h.standby, fake, 1)
	h.cancelSource()
	if err := waitSleepOwner(t, h); err != nil {
		t.Fatal(err)
	}
}

func TestStandbyBlockedReleaseDoesNotDelayOFFOrCloseDeadline(t *testing.T) {
	gate := make(chan struct{})
	fake := &fakeSleepAssertion{releaseStarted: make(chan struct{}), releaseGate: gate}
	s, connector, _ := idleStandby(t)
	s.sleep = fake
	if r := s.RequestOn(1, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	s.sleepMu.Lock()
	s.sleepHeld = true
	s.sleepMu.Unlock()
	offStarted := time.Now()
	if r := s.RequestOff(2, s.Snapshot().Revision); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	if time.Since(offStarted) > 100*time.Millisecond {
		t.Fatal("OFF waited for the OS release")
	}
	select {
	case <-fake.releaseStarted:
	case <-time.After(time.Second):
		t.Fatal("OS release was not started asynchronously")
	}
	close(connector.gate)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	closeStarted := time.Now()
	err := s.Close(ctx)
	if time.Since(closeStarted) > 250*time.Millisecond || !errors.Is(err, ErrStandbyUnknown) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close did not honor its deadline while release was blocked: elapsed=%s error=%v", time.Since(closeStarted), err)
	}
	if connector.closes.Load() != 1 {
		t.Fatalf("transport stop waited for OS release: closes=%d", connector.closes.Load())
	}
	close(gate)
	eventually(t, func() bool {
		s.sleepMu.Lock()
		defer s.sleepMu.Unlock()
		return !s.sleepHeld && !s.sleepReleasing
	})
}

func TestStandbySleepPreventionKeepsAssertionAcrossSuccessiveWorks(t *testing.T) {
	h := newSleepHarness(t, StandbyConfig{})
	fake := &fakeSleepAssertion{}
	h.standby.sleep = fake
	first := newSleepTestSession()
	firstLease := submitSleepWork(t, h.standby, h.cycle, h.requests, first, 1, h.cycle.ctx, h.source)
	<-first.started
	close(first.finish)
	eventually(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })

	proofGate := make(chan struct{})
	proofEntered := make(chan struct{})
	collected := make(chan struct{})
	go func() {
		h.standby.collect(&sleepProofLease{entered: proofEntered, gate: proofGate}, sleepBinding(1), nil, firstLease)
		close(collected)
	}()
	select {
	case <-proofEntered:
	case <-time.After(time.Second):
		t.Fatal("first collection proof did not pause")
	}

	second := newSleepTestSession()
	secondLease := submitSleepWork(t, h.standby, h.cycle, h.requests, second, 2, h.cycle.ctx, h.source)
	<-second.started
	waitOwnedCount(t, h.standby, 2)
	if takes, releases, held, peak := fake.counts(); takes != 1 || releases != 0 || held != 1 || peak != 1 {
		t.Fatalf("second work lost first work's assertion: takes=%d releases=%d held=%d peak=%d", takes, releases, held, peak)
	}
	close(proofGate)
	<-collected
	waitSleepProofAttempt(t, firstLease)
	waitOwnedCount(t, h.standby, 1)
	if takes, releases, held, peak := fake.counts(); takes != 1 || releases != 0 || held != 1 || peak != 1 {
		t.Fatalf("first release dropped assertion held by second work: takes=%d releases=%d held=%d peak=%d", takes, releases, held, peak)
	}
	close(second.finish)
	eventually(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	h.standby.collect(&sleepProofLease{}, sleepBinding(2), nil, secondLease)
	waitSleepProofAttempt(t, secondLease)
	waitSleepReleases(t, h.standby, fake, 1)
	if takes, releases, held, peak := fake.counts(); takes != 1 || releases != 1 || held != 0 || peak != 1 {
		t.Fatalf("last work did not release assertion: takes=%d releases=%d held=%d peak=%d", takes, releases, held, peak)
	}
	h.cancelSource()
	if err := waitSleepOwner(t, h); err != nil {
		t.Fatal(err)
	}
}

func TestStandbySleepPreventionRetainsWorkBeforeReplyOnDisconnect(t *testing.T) {
	h := newSleepHarness(t, StandbyConfig{})
	fake := &fakeSleepAssertion{}
	h.standby.sleep = fake
	session := newSleepTestSession()
	binding := sleepBinding(3)
	request := sleepRequest(session, h.source)
	workSleep, sent, err := h.standby.queueOwnedRequest(h.cycle, request, binding, h.requests, h.cycle.ctx, context.Background())
	if err != nil || !sent {
		t.Fatalf("request queue sent=%v error=%v", sent, err)
	}
	<-session.started
	h.cycle.mu.Lock()
	if h.cycle.work == nil || h.cycle.workSleep != workSleep {
		h.cycle.mu.Unlock()
		t.Fatal("work lease was not recorded before the reply")
	}
	h.cycle.mu.Unlock()
	h.cancelSource()
	if err := waitSleepOwner(t, h); err != nil {
		t.Fatal(err)
	}
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 0 || held != 1 {
		t.Fatalf("disconnect before reply did not retain assertion: takes=%d releases=%d held=%d", takes, releases, held)
	}
	if err := h.standby.collectLastWork(h.cycle, &sleepProofLease{}, nil); err != nil {
		t.Fatal(err)
	}
	waitSleepProofAttempt(t, workSleep)
	waitSleepReleases(t, h.standby, fake, 1)
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 1 || held != 0 {
		t.Fatalf("disconnect collection did not release assertion: takes=%d releases=%d held=%d", takes, releases, held)
	}
}

func TestStandbySleepPreventionOFFWaitsForCollectionProof(t *testing.T) {
	s, connector, _ := idleStandby(t)
	fake := &fakeSleepAssertion{}
	s.sleep = fake
	if r := s.RequestOn(1, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	cycle := s.cycle
	proved := make(chan struct{})
	cycle.mu.Lock()
	cycle.proved = proved
	cycle.mu.Unlock()
	source, cancelSource := context.WithCancel(context.Background())
	defer cancelSource()
	requests := make(chan *workerowner.Request)
	ownerDone := make(chan error, 1)
	go func() { ownerDone <- cycle.waitSource(source, requests) }()
	gate := make(chan struct{})
	session := newSleepTestSession()
	session.closeStarted = make(chan struct{})
	session.closeGate = gate
	binding := sleepBinding(4)
	workSleep := submitSleepWork(t, s, cycle, requests, session, 4, cycle.ctx, source)
	<-session.started
	if r := s.RequestOff(2, s.Snapshot().Revision); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	cancelSource()
	select {
	case <-session.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("work cleanup did not start")
	}
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 0 || held != 1 {
		t.Fatalf("OFF released during cleanup: takes=%d releases=%d held=%d", takes, releases, held)
	}
	close(gate)
	select {
	case err := <-ownerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owner did not confirm collection")
	}
	proofGate := make(chan struct{})
	proofEntered := make(chan struct{})
	collected := make(chan struct{})
	go func() {
		s.collect(&sleepProofLease{entered: proofEntered, gate: proofGate}, binding, nil, workSleep)
		close(collected)
	}()
	<-proofEntered
	if _, releases, held, _ := fake.counts(); releases != 0 || held != 1 {
		t.Fatalf("assertion released before proof attempt ended: releases=%d held=%d", releases, held)
	}
	close(proofGate)
	<-collected
	waitSleepProofAttempt(t, workSleep)
	waitSleepReleases(t, s, fake, 1)
	if _, releases, held, _ := fake.counts(); releases != 1 || held != 0 {
		t.Fatalf("assertion not released after proof: releases=%d held=%d", releases, held)
	}
	close(proved)
	close(connector.gate)
	eventually(t, func() bool { return s.Snapshot().Standby == "off" })
}

func TestStandbySleepPreventionCloseWaitsForOwnerCollection(t *testing.T) {
	s, connector, _ := idleStandby(t)
	fake := &fakeSleepAssertion{}
	s.sleep = fake
	if r := s.RequestOn(1, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	cycle := s.cycle
	source, cancelSource := context.WithCancel(context.Background())
	defer cancelSource()
	requests := make(chan *workerowner.Request)
	ownerDone := make(chan error, 1)
	go func() { ownerDone <- cycle.waitSource(source, requests) }()
	gate := make(chan struct{})
	session := newSleepTestSession()
	session.closeStarted = make(chan struct{})
	session.closeGate = gate
	workSleep := submitSleepWork(t, s, cycle, requests, session, 5, cycle.ctx, source)
	<-session.started
	closeResult := make(chan error, 1)
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelClose()
	go func() { closeResult <- s.Close(closeCtx) }()
	select {
	case <-session.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("Close did not start owner cleanup")
	}
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 0 || held != 1 {
		t.Fatalf("Close released during cleanup: takes=%d releases=%d held=%d", takes, releases, held)
	}
	close(gate)
	select {
	case err := <-ownerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("owner did not finish")
	}
	waitSleepProofAttempt(t, workSleep)
	eventually(t, func() bool { _, releases, _, _ := fake.counts(); return releases == 1 })
	close(connector.gate)
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not join cycle")
	}
}

func TestStandbySleepReleaseFailureIsDiagnosticAndRetriesAtNextWorkEnd(t *testing.T) {
	silenceSleepDiagnostics(t)
	h := newSleepHarness(t, StandbyConfig{})
	failure := errors.New("synthetic release failure")
	fake := &fakeSleepAssertion{releaseErr: failure, releaseFailures: 1}
	h.standby.sleep = fake
	first := newSleepTestSession()
	firstLease := submitSleepWork(t, h.standby, h.cycle, h.requests, first, 6, h.cycle.ctx, h.source)
	<-first.started
	close(first.finish)
	eventually(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	firstProof := &sleepProofLease{err: failure}
	h.standby.collect(firstProof, sleepBinding(6), nil, firstLease)
	waitSleepProofAttempt(t, firstLease)
	waitSleepReleases(t, h.standby, fake, 1)
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 1 || held != 1 {
		t.Fatalf("release failure state: takes=%d releases=%d held=%d", takes, releases, held)
	}
	h.standby.sleepMu.Lock()
	recorded := h.standby.sleepErr
	owners := h.standby.sleepOwners
	h.standby.sleepMu.Unlock()
	if !errors.Is(recorded, failure) || owners != 0 {
		t.Fatalf("release diagnostic/count not retained: error=%v owners=%d", recorded, owners)
	}
	second := newSleepTestSession()
	secondLease := submitSleepWork(t, h.standby, h.cycle, h.requests, second, 7, h.cycle.ctx, h.source)
	<-second.started
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 1 || held != 1 {
		t.Fatalf("next work duplicated a held OS assertion: takes=%d releases=%d held=%d", takes, releases, held)
	}
	close(second.finish)
	eventually(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	h.standby.collect(&sleepProofLease{}, sleepBinding(7), nil, secondLease)
	waitSleepProofAttempt(t, secondLease)
	waitSleepReleases(t, h.standby, fake, 2)
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 2 || held != 0 {
		t.Fatalf("next work end did not retry release: takes=%d releases=%d held=%d", takes, releases, held)
	}
	h.standby.sleepMu.Lock()
	recorded = h.standby.sleepErr
	h.standby.sleepMu.Unlock()
	if recorded != nil || h.standby.Snapshot().Cleanup != "confirmed" {
		t.Fatalf("release retry changed cleanup result: error=%v observation=%+v", recorded, h.standby.Snapshot())
	}
	h.cancelSource()
	if err := waitSleepOwner(t, h); err != nil {
		t.Fatal(err)
	}
}

func TestStandbySleepAllowedWorkAndIdleStandbyDoNotTakeAssertion(t *testing.T) {
	s, _, _ := idleStandby(t)
	fake := &fakeSleepAssertion{}
	s.sleep = fake
	if r := s.RequestOn(1, s.Snapshot().Revision, 2*time.Hour); r.Disposition != "accepted" {
		t.Fatal(r)
	}
	eventually(t, func() bool { return s.Snapshot().Standby == "on" })
	if takes, releases, held, _ := fake.counts(); takes != 0 || releases != 0 || held != 0 {
		t.Fatalf("idle standby took assertion: takes=%d releases=%d held=%d", takes, releases, held)
	}

	h := newSleepHarness(t, StandbyConfig{AllowSleepDuringWork: true})
	h.standby.sleep = fake
	session := newSleepTestSession()
	lease := submitSleepWork(t, h.standby, h.cycle, h.requests, session, 8, h.cycle.ctx, h.source)
	<-session.started
	close(session.finish)
	eventually(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	h.standby.collect(&sleepProofLease{}, sleepBinding(8), nil, lease)
	if takes, releases, held, _ := fake.counts(); takes != 0 || releases != 0 || held != 0 {
		t.Fatalf("allowed-sleep work took assertion: takes=%d releases=%d held=%d", takes, releases, held)
	}
	h.cancelSource()
	if err := waitSleepOwner(t, h); err != nil {
		t.Fatal(err)
	}
}

func TestStandbySleepCloseRetriesFailedReleaseWithoutChangingResult(t *testing.T) {
	silenceSleepDiagnostics(t)
	h := newSleepHarness(t, StandbyConfig{})
	failure := errors.New("synthetic release failure")
	fake := &fakeSleepAssertion{releaseErr: failure, releaseFailures: 1}
	h.standby.sleep = fake
	session := newSleepTestSession()
	lease := submitSleepWork(t, h.standby, h.cycle, h.requests, session, 9, h.cycle.ctx, h.source)
	<-session.started
	close(session.finish)
	eventually(t, func() bool { return h.cycle.owner.Snapshot().Owned == 0 })
	h.standby.collect(&sleepProofLease{err: failure}, sleepBinding(9), nil, lease)
	waitSleepProofAttempt(t, lease)
	waitSleepReleases(t, h.standby, fake, 1)
	h.cancelSource()
	if err := waitSleepOwner(t, h); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.standby.Close(ctx); err != nil {
		t.Fatalf("release failure changed Close result: %v", err)
	}
	if takes, releases, held, _ := fake.counts(); takes != 1 || releases != 2 || held != 0 {
		t.Fatalf("Close did not retry the failed release: takes=%d releases=%d held=%d", takes, releases, held)
	}
}
