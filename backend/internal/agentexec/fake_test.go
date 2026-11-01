package agentexec

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// The fakes below are memory-only. They model the GuestRuntime contract and do
// not start processes, shells, accounts, pipes or any OS object.

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

type fakeTimers struct {
	mu   sync.Mutex
	list []*fakeTimer
}

func (ft *fakeTimers) after(d time.Duration, f func()) func() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	t := &fakeTimer{d: d, f: f}
	ft.list = append(ft.list, t)
	return func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		was := !t.stopped
		t.stopped = true
		return was
	}
}

func (ft *fakeTimers) timer(t *testing.T, i int) *fakeTimer {
	t.Helper()
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if i >= len(ft.list) {
		t.Fatalf("timer %d was not scheduled", i)
	}
	return ft.list[i]
}

func (ft *fakeTimers) fire(t *testing.T, i int) {
	timer := ft.timer(t, i)
	ft.mu.Lock()
	stopped := timer.stopped
	ft.mu.Unlock()
	if !stopped {
		timer.f()
	}
}

type fakeEvent struct {
	stream Stream
	data   []byte
}

type fakeProc struct {
	events   chan fakeEvent
	exited   chan struct{}
	killed   chan struct{}
	killOnce sync.Once
	code     int32

	mu            sync.Mutex
	terminates    int
	releases      int
	terminateErrs []error
	releaseErrs   []error
	onTerminate   func()
}

func newFakeProc() *fakeProc {
	return &fakeProc{events: make(chan fakeEvent), exited: make(chan struct{}), killed: make(chan struct{})}
}

func (p *fakeProc) Next(ctx context.Context) (Stream, []byte, error) {
	select {
	case ev, ok := <-p.events:
		if !ok {
			return 0, nil, io.EOF
		}
		return ev.stream, ev.data, nil
	case <-p.killed:
		return 0, nil, io.ErrClosedPipe
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

func (p *fakeProc) Wait(ctx context.Context) (ExitStatus, error) {
	select {
	case <-p.exited:
		return ExitStatus{Code: p.code}, nil
	case <-p.killed:
		return ExitStatus{Code: -1}, nil
	case <-ctx.Done():
		return ExitStatus{}, ctx.Err()
	}
}

func (p *fakeProc) Terminate(context.Context) error {
	p.mu.Lock()
	p.terminates++
	hook := p.onTerminate
	var err error
	if len(p.terminateErrs) > 0 {
		err, p.terminateErrs = p.terminateErrs[0], p.terminateErrs[1:]
	}
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err == nil {
		p.killOnce.Do(func() { close(p.killed) })
	}
	return err
}

func (p *fakeProc) Release(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.releases++
	if len(p.releaseErrs) > 0 {
		err := p.releaseErrs[0]
		p.releaseErrs = p.releaseErrs[1:]
		return err
	}
	return nil
}

func (p *fakeProc) counts() (terminates, releases int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.terminates, p.releases
}

func (p *fakeProc) setTerminateHook(f func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onTerminate = f
}

// awaitKilled waits for Terminate without issuing any Session request.
func (p *fakeProc) awaitKilled(t *testing.T) {
	t.Helper()
	select {
	case <-p.killed:
	case <-time.After(2 * time.Second):
		t.Fatal("guest workload was not terminated")
	}
}

type fakeRuntime struct {
	mu        sync.Mutex
	starts    int
	stdin     [][]byte
	grants    []supervisor.Binding
	start     func(context.Context) (GuestProcess, error)
	configure func(*fakeProc)
	procs     chan *fakeProc
}

func newFakeRuntime() *fakeRuntime { return &fakeRuntime{procs: make(chan *fakeProc, 8)} }

func (r *fakeRuntime) StartShell(ctx context.Context, grant supervisor.Binding, stdin []byte) (GuestProcess, error) {
	r.mu.Lock()
	r.starts++
	r.stdin = append(r.stdin, append([]byte(nil), stdin...))
	r.grants = append(r.grants, grant)
	start, configure := r.start, r.configure
	r.mu.Unlock()
	if start != nil {
		return start(ctx)
	}
	p := newFakeProc()
	if configure != nil {
		configure(p)
	}
	r.procs <- p
	return p, nil
}

func (r *fakeRuntime) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

type env struct {
	t         *testing.T
	clock     *testClock
	timers    *fakeTimers
	issuer    *ephemeral.Issuer
	authority *supervisor.Authority
	runtime   *fakeRuntime
	session   *Session
	mu        sync.Mutex
	ids       []string
}

// newGrantWith runs the real issuer Prepare/Launch with the given policy digest.
// The Mac current-user digest goes through the explicit current-user issuer;
// every other digest through the default guest issuer.
func newGrantWith(t *testing.T, clock func() time.Time, digest string, lifetime time.Duration, ceiling time.Duration) (*ephemeral.Issuer, *supervisor.Authority) {
	t.Helper()
	boundary := supervisor.SeparatedNonAdminGuest
	if digest == MacCurrentUserPolicyDigest() {
		boundary = supervisor.CurrentUserNoIsolation
	}
	return newGrantBoundary(t, clock, digest, boundary, lifetime, ceiling)
}

// newGrantBoundary selects the issuer kind by boundary, so a test can pair any
// policy digest with either explicit path.
func newGrantBoundary(t *testing.T, clock func() time.Time, digest string, boundary supervisor.GuestBoundary, lifetime, ceiling time.Duration) (*ephemeral.Issuer, *supervisor.Authority) {
	t.Helper()
	newIssuer := ephemeral.New
	if boundary == supervisor.CurrentUserNoIsolation {
		newIssuer = ephemeral.NewCurrentUser
	}
	issuer, err := newIssuer()
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := issuer.Prepare(ephemeral.ApprovedPolicy{
		PolicyDigest: digest, KeySetDigest: [32]byte{1}, GuestBoundary: boundary,
		HardDeadline: clock().Add(lifetime), BackendLeaseCeiling: ceiling,
	})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := issuer.Launch(context.Background(), challenge, clock, func(context.Context, *supervisor.Authority) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return issuer, authority
}

func newGrant(t *testing.T, clock *testClock) (*ephemeral.Issuer, *supervisor.Authority) {
	return newGrantWith(t, clock.Now, FullPolicyDigest(), time.Hour, time.Minute)
}

// newEnv uses the private constructor only to replace wall-clock timers with
// manually fired ones; the public New path has its own tests with real timers.
func newEnv(t *testing.T, limits Limits) *env {
	t.Helper()
	e := &env{t: t, clock: &testClock{now: time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)}, timers: &fakeTimers{}, runtime: newFakeRuntime()}
	e.issuer, e.authority = newGrant(t, e.clock)
	s, err := newSession(Config{Issuer: e.issuer, Authority: e.authority, Runtime: e.runtime, Clock: e.clock.Now, Limits: limits, CleanupTimeout: time.Second}, e.timers.after)
	if err != nil {
		t.Fatal(err)
	}
	e.session = s
	t.Cleanup(func() { e.closeAndCheck() })
	return e
}

// closeAndCheck ends the grant through the public Close and requires every
// command started by the test to report a terminal worker afterwards.
func (e *env) closeAndCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = e.session.Close(ctx)
	e.mu.Lock()
	ids := append([]string(nil), e.ids...)
	e.mu.Unlock()
	for _, id := range ids {
		if st, err := e.session.Status(e.ref(id)); err != nil || !st.Terminal {
			e.t.Errorf("command %s outlived the session: %+v, %v", id, st, err)
		}
	}
}

func (e *env) startReq(requestID, command string) StartRequest {
	return StartRequest{Version: Version, Binding: e.session.Binding(), RequestID: requestID, Mode: ModeFull, Command: []byte(command), TimeoutMillis: 60_000}
}

func (e *env) ref(id string) StatusRequest {
	return StatusRequest{Version: Version, Binding: e.session.Binding(), CommandID: id}
}

func (e *env) track(st Status) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ids = append(e.ids, st.CommandID)
}

func (e *env) mustStart(requestID, command string) Status {
	e.t.Helper()
	st, err := e.session.Start(e.startReq(requestID, command))
	if err != nil {
		e.t.Fatalf("Start: %v", err)
	}
	e.track(st)
	return st
}

func (e *env) proc() *fakeProc {
	e.t.Helper()
	select {
	case p := <-e.runtime.procs:
		return p
	case <-time.After(2 * time.Second):
		e.t.Fatal("guest shell was not started")
		return nil
	}
}

// await polls the public Status until the worker reports a terminal state.
func (e *env) await(id string) Status {
	e.t.Helper()
	return awaitTerminal(e.t, e.session, e.ref(id))
}

func awaitTerminal(t *testing.T, s *Session, ref StatusRequest) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		st, err := s.Status(ref)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if st.Terminal {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("command did not reach a terminal state: %+v", st)
		}
		time.Sleep(time.Millisecond)
	}
}

func (e *env) readUntil(id string, cursor, want uint64) []Chunk {
	e.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var chunks []Chunk
	for {
		out, err := e.session.Output(OutputRequest{Version: Version, Binding: e.session.Binding(), CommandID: id, Cursor: cursor, MaxBytes: 1024})
		if err != nil {
			e.t.Fatalf("Output: %v", err)
		}
		chunks = append(chunks, out.Chunks...)
		cursor = out.Next
		if cursor >= want {
			return chunks
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("output stalled at %d, want %d", cursor, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func send(t *testing.T, p *fakeProc, s Stream, data string) {
	t.Helper()
	select {
	case p.events <- fakeEvent{stream: s, data: []byte(data)}:
	case <-time.After(2 * time.Second):
		t.Fatal("guest output was not read")
	}
}

func finite(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}
