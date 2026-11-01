package macagent

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

// These fakes model only the osProcess seam, the host checks and the three
// pipes in memory. Nothing here executes a shell, creates a process, sends a
// signal, inspects descriptors or opens a socket; native_darwin.go is covered
// by compile only.

type fakeOS struct {
	mu                sync.Mutex
	log               []string
	exited            bool
	reaped            bool
	lost              bool
	code              int32
	exitOnTerm        bool
	ignoreKill        bool
	escaped           bool  // a holder outside the group keeps stdout/stderr open
	exitBeforeWatch   bool  // the observer saw the exit but not its status
	observerErr       error // exit observation is unavailable
	member            bool  // a live same-group member that already closed its stdio
	memberIgnoresTerm bool
	signalErr         map[signal]error
	reapErr           error
	observerCloseErr  error
	observerAttempts  int
	closeIO           func() // the group's stdio writers, closed when the group dies
	onSignal          func(signal)
}

func (f *fakeOS) record(entry string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, entry)
}

func (f *fakeOS) entries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

func (f *fakeOS) exit(code int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exited, f.code = true, code
}

func (f *fakeOS) set(edit func(*fakeOS)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edit(f)
}

func (f *fakeOS) signalGroup(s signal) error {
	f.mu.Lock()
	name := map[signal]string{sigTerm: "TERM", sigKill: "KILL"}[s]
	if f.reaped || f.lost {
		f.log = append(f.log, "VIOLATION:"+name+"-after-reap-or-loss")
		f.mu.Unlock()
		return errFake
	}
	f.log = append(f.log, name)
	err := f.signalErr[s]
	if err == nil && f.member && (s == sigKill || !f.memberIgnoresTerm) {
		f.member = false
		f.log = append(f.log, "member-exit")
	}
	dies := err == nil && !f.exited && ((s == sigTerm && f.exitOnTerm) || (s == sigKill && !f.ignoreKill))
	if dies {
		f.exited, f.code = true, 128+map[signal]int32{sigTerm: 15, sigKill: 9}[s]
	}
	closeIO, hook, escaped := f.closeIO, f.onSignal, f.escaped
	f.mu.Unlock()
	if hook != nil {
		hook(s)
	}
	if dies && !escaped && closeIO != nil {
		closeIO()
	}
	return err
}

func (f *fakeOS) pollExit() (bool, exitInfo, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observerErr != nil {
		return false, exitInfo{}, false, f.observerErr
	}
	if !f.exited {
		return false, exitInfo{}, false, nil
	}
	if f.exitBeforeWatch {
		return true, exitInfo{}, false, nil
	}
	return true, exitInfo{code: f.code}, true, nil
}

func (f *fakeOS) tryReap() (bool, exitInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reapErr != nil {
		f.lost = true
		return false, exitInfo{}, f.reapErr
	}
	if f.reaped || f.lost {
		f.log = append(f.log, "VIOLATION:second-reap")
		return false, exitInfo{}, errFake
	}
	if !f.exited {
		return false, exitInfo{}, nil
	}
	f.reaped = true
	f.log = append(f.log, "reap")
	return true, exitInfo{code: f.code}, nil
}

// releaseObserver models the native single close attempt.
func (f *fakeOS) releaseObserver() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.observerAttempts == 0 {
		f.log = append(f.log, "observer-released")
	}
	f.observerAttempts++
	return f.observerCloseErr
}

// recordingStdin logs its close into the same ordered log as the signals.
type recordingStdin struct {
	*io.PipeWriter
	os *fakeOS
}

func (w recordingStdin) Close() error {
	w.os.record("stdin-closed")
	return w.PipeWriter.Close()
}

// scriptedCloser wraps a stream whose Close results are scripted per attempt.
type scriptedCloser struct {
	io.Reader
	mu      sync.Mutex
	results []error
	calls   int
}

func (c *scriptedCloser) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if len(c.results) == 0 {
		return nil
	}
	err := c.results[0]
	c.results = c.results[1:]
	return err
}

func (c *scriptedCloser) attempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type fakeChild struct {
	os               *fakeOS
	spec             spawnSpec
	stdin            *io.PipeReader
	stdout, stderr   *io.PipeWriter
	stdinData        chan []byte
	closeOutputsOnce sync.Once
}

func (c *fakeChild) closeOutputs() {
	c.closeOutputsOnce.Do(func() {
		_ = c.stdout.Close()
		_ = c.stderr.Close()
	})
}

func (c *fakeChild) write(t *testing.T, w *io.PipeWriter, data string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := w.Write([]byte(data))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("guest write: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("guest output was not read")
	}
}

type fakeSpawner struct {
	mu        sync.Mutex
	spawns    int
	children  chan *fakeChild
	configure func(*fakeOS)
	result    func(spawned) (spawned, error) // optional partial/no-process override
	gate      chan struct{}                  // optional: blocks spawn (late return)
	entered   chan struct{}
	holdStdin bool // the shell never reads stdin
	fdErr     error
	sigErr    error
}

func newFakeSpawner() *fakeSpawner { return &fakeSpawner{children: make(chan *fakeChild, 8)} }

func (s *fakeSpawner) checks() hostChecks {
	return hostChecks{
		inheritableFDs: func() error { s.mu.Lock(); defer s.mu.Unlock(); return s.fdErr },
		childReaping:   func() error { s.mu.Lock(); defer s.mu.Unlock(); return s.sigErr },
	}
}

func (s *fakeSpawner) setHost(fdErr, sigErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fdErr, s.sigErr = fdErr, sigErr
}

func (s *fakeSpawner) spawn(spec spawnSpec) (spawned, error) {
	s.mu.Lock()
	s.spawns++
	configure, result, gate, entered, holdStdin := s.configure, s.result, s.gate, s.entered, s.holdStdin
	s.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if gate != nil {
		<-gate // models a native call that does not observe cancellation
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	f := &fakeOS{}
	c := &fakeChild{os: f, spec: spec, stdin: inR, stdout: outW, stderr: errW, stdinData: make(chan []byte, 1)}
	f.closeIO = c.closeOutputs
	if configure != nil {
		configure(f)
	}
	if !holdStdin {
		go func() {
			data, _ := io.ReadAll(inR)
			c.stdinData <- data
		}()
	}
	s.children <- c
	sp := spawned{proc: f, stdin: recordingStdin{inW, f}, stdout: outR, stderr: errR}
	if result != nil {
		return result(sp)
	}
	return sp, nil
}

func (s *fakeSpawner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spawns
}

func (s *fakeSpawner) child(t *testing.T) *fakeChild {
	t.Helper()
	select {
	case c := <-s.children:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("shell was not spawned")
		return nil
	}
}

var testTiming = Config{WorkDir: "/Users/agent/work", Home: "/Users/agent", TermGrace: 20 * time.Millisecond, KillWait: 200 * time.Millisecond, IOWait: 200 * time.Millisecond}

var fakeTiming = timing{termGrace: 20 * time.Millisecond, killWait: 200 * time.Millisecond, ioWait: 200 * time.Millisecond, poll: time.Millisecond}

func newTestRuntime(t *testing.T, s *fakeSpawner) *Runtime {
	t.Helper()
	r, err := newRuntime(testTiming, s.spawn, s.checks(), time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// grant runs the real issuer Prepare/Launch. A Mac current-user policy uses the
// explicit current-user issuer and boundary; any other digest uses the default
// guest issuer, so profile and boundary mismatches stay observable.
func grant(t *testing.T, digest string, lifetime time.Duration) (*ephemeral.Issuer, *supervisor.Authority) {
	t.Helper()
	newIssuer, boundary := ephemeral.New, supervisor.SeparatedNonAdminGuest
	if digest == agentexec.MacCurrentUserPolicyDigest() {
		newIssuer, boundary = ephemeral.NewCurrentUser, supervisor.CurrentUserNoIsolation
	}
	issuer, err := newIssuer()
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := issuer.Prepare(ephemeral.ApprovedPolicy{
		PolicyDigest: digest, KeySetDigest: [32]byte{1}, GuestBoundary: boundary,
		HardDeadline: time.Now().Add(lifetime), BackendLeaseCeiling: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := issuer.Launch(context.Background(), challenge, time.Now, func(context.Context, *supervisor.Authority) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return issuer, authority
}

type harness struct {
	t         *testing.T
	spawner   *fakeSpawner
	runtime   *Runtime
	issuer    *ephemeral.Issuer
	authority *supervisor.Authority
	session   *agentexec.Session
}

// newHarness composes the public agentexec.New with the real current-user
// issuer and a Mac runtime whose only fake parts are the spawn and host seams.
func newHarness(t *testing.T, lifetime time.Duration, limits agentexec.Limits) *harness {
	t.Helper()
	h := &harness{t: t, spawner: newFakeSpawner()}
	h.runtime = newTestRuntime(t, h.spawner)
	h.issuer, h.authority = grant(t, agentexec.MacCurrentUserPolicyDigest(), lifetime)
	s, err := agentexec.New(agentexec.Config{Issuer: h.issuer, Authority: h.authority, Runtime: h.runtime, Clock: time.Now, Limits: limits, CleanupTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	h.session = s
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	return h
}

func (h *harness) startReq(requestID, command string) agentexec.StartRequest {
	return agentexec.StartRequest{Version: agentexec.Version, Binding: h.session.Binding(), RequestID: requestID, Mode: agentexec.ModeFull, Command: []byte(command), TimeoutMillis: 60_000}
}

func (h *harness) start(requestID, command string) agentexec.Status {
	h.t.Helper()
	st, err := h.session.Start(h.startReq(requestID, command))
	if err != nil {
		h.t.Fatalf("Start: %v", err)
	}
	return st
}

func (h *harness) ref(id string) agentexec.StatusRequest {
	return agentexec.StatusRequest{Version: agentexec.Version, Binding: h.session.Binding(), CommandID: id}
}

func (h *harness) await(id string) agentexec.Status {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st, err := h.session.Status(h.ref(id))
		if err != nil {
			h.t.Fatalf("Status: %v", err)
		}
		if st.Terminal {
			return st
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("command did not finish: %+v", st)
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *harness) readUntil(id string, cursor, want uint64) []agentexec.Chunk {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var chunks []agentexec.Chunk
	for cursor < want {
		out, err := h.session.Output(agentexec.OutputRequest{Version: agentexec.Version, Binding: h.session.Binding(), CommandID: id, Cursor: cursor, MaxBytes: 1024})
		if err != nil {
			h.t.Fatalf("Output: %v", err)
		}
		chunks, cursor = append(chunks, out.Chunks...), out.Next
		if time.Now().After(deadline) {
			h.t.Fatalf("output stalled at %d", cursor)
		}
		time.Sleep(time.Millisecond)
	}
	return chunks
}

func finite(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func noViolation(t *testing.T, f *fakeOS) {
	t.Helper()
	for _, e := range f.entries() {
		if strings.HasPrefix(e, "VIOLATION") {
			t.Fatalf("unsafe signal or reap: %v", f.entries())
		}
	}
}

// before reports whether every occurrence of a precedes the first b.
func before(log []string, a, b string) bool {
	ib := len(log)
	for i, e := range log {
		if e == b {
			ib = i
			break
		}
	}
	found := false
	for i, e := range log {
		if e == a {
			if i > ib {
				return false
			}
			found = true
		}
	}
	return found && ib < len(log)
}

var errFake = errors.New("fake failure")
