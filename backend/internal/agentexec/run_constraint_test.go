package agentexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

// dirRuntime is a fake runtime that can start the shell below its work root.
type dirRuntime struct {
	*fakeRuntime
	dirMu sync.Mutex
	dirs  []string
}

func (r *dirRuntime) StartShellIn(ctx context.Context, grant supervisor.Binding, dir string, stdin []byte) (GuestProcess, error) {
	r.dirMu.Lock()
	r.dirs = append(r.dirs, dir)
	r.dirMu.Unlock()
	return r.fakeRuntime.StartShell(ctx, grant, stdin)
}

func (r *dirRuntime) seen() []string {
	r.dirMu.Lock()
	defer r.dirMu.Unlock()
	return append([]string(nil), r.dirs...)
}

func runConstraint(command string) *FixedStartConstraint {
	sum := sha256.Sum256([]byte(command))
	return &FixedStartConstraint{RequestID: "run-v1", CommandSHA256: hex.EncodeToString(sum[:]), TimeoutMillis: 60_000, Dir: "src/app"}
}

func newDirEnv(t *testing.T, fixed *FixedStartConstraint) (*env, *dirRuntime) {
	t.Helper()
	e := &env{t: t, clock: &testClock{now: time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)}, timers: &fakeTimers{}, runtime: newFakeRuntime()}
	e.issuer, e.authority = newGrant(t, e.clock)
	runtime := &dirRuntime{fakeRuntime: e.runtime}
	s, err := newSession(Config{FixedStart: fixed, Issuer: e.issuer, Authority: e.authority, Runtime: runtime, Clock: e.clock.Now, CleanupTimeout: time.Second}, e.timers.after)
	if err != nil {
		t.Fatal(err)
	}
	e.session = s
	t.Cleanup(func() { e.closeAndCheck() })
	return e, runtime
}

// The gateway signs only the hash of a command. The command text arrives on
// another channel, so this comparison is what keeps a person's approval of one
// command from running a different one, with a different timeout, or twice.
func TestCommandHashConstraintRunsOnlyTheApprovedCommandOnceInItsDirectory(t *testing.T) {
	const command = "make test\n"
	fixed := runConstraint(command)
	e, runtime := newDirEnv(t, fixed)
	for name, edit := range map[string]func(*StartRequest){
		"one byte differs":   func(r *StartRequest) { r.Command = []byte("make test \n") },
		"prefix of command":  func(r *StartRequest) { r.Command = []byte("make test") },
		"shorter timeout":    func(r *StartRequest) { r.TimeoutMillis = 59_999 },
		"longer timeout":     func(r *StartRequest) { r.TimeoutMillis = 60_001 },
		"another request id": func(r *StartRequest) { r.RequestID = "run-v2" },
	} {
		req := e.startReq(fixed.RequestID, command)
		edit(&req)
		if _, err := e.session.Start(req); !errors.Is(err, ErrPolicy) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if n := e.runtime.startCount(); n != 0 {
		t.Fatalf("refused requests reached the runtime %d times", n)
	}
	st := e.mustStart(fixed.RequestID, command)
	e.proc()
	if dirs := runtime.seen(); len(dirs) != 1 || dirs[0] != "src/app" {
		t.Fatalf("approved directory was not passed to the runtime: %q", dirs)
	}
	e.runtime.mu.Lock()
	stdin := string(e.runtime.stdin[0])
	e.runtime.mu.Unlock()
	if stdin != command {
		t.Fatalf("runtime received %q", stdin)
	}
	// The same request is an idempotent status read, never a second run.
	again, err := e.session.Start(e.startReq(fixed.RequestID, command))
	if err != nil || again.CommandID != st.CommandID || e.runtime.startCount() != 1 {
		t.Fatalf("repeated request ran again: %v %v starts=%d", again.CommandID, err, e.runtime.startCount())
	}
	if _, err := e.session.Cancel(e.ref(st.CommandID)); err != nil {
		t.Fatal(err)
	}
	if final := e.await(st.CommandID); final.Cleanup != CleanupConfirmed {
		t.Fatal(final)
	}
}

// A constraint that could be read two ways, or that names a directory the
// runtime cannot honor, must fail before any grant is consumed. Otherwise a
// command could start in the default directory the approver never saw.
func TestCommandHashConstraintRefusesAmbiguousOrUnservableConfiguration(t *testing.T) {
	good := runConstraint("make test\n")
	for name, edit := range map[string]func(*FixedStartConstraint){
		"exact command and hash together": func(c *FixedStartConstraint) { c.Command, c.Dir = "make test\n", "" },
		"neither command nor hash":        func(c *FixedStartConstraint) { c.CommandSHA256 = "" },
		"uppercase hash":                  func(c *FixedStartConstraint) { c.CommandSHA256 = strings.ToUpper(c.CommandSHA256) },
		"short hash":                      func(c *FixedStartConstraint) { c.CommandSHA256 = c.CommandSHA256[:63] },
		"hash that is not hex":            func(c *FixedStartConstraint) { c.CommandSHA256 = strings.Repeat("zz", 32) },
		"hash without a directory":        func(c *FixedStartConstraint) { c.Dir = "" },
		"directory on the exact command":  func(c *FixedStartConstraint) { c.Command, c.CommandSHA256 = "make test\n", "" },
	} {
		c := *good
		edit(&c)
		if err := c.Validate(); !errors.Is(err, ErrUnconfigured) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (FixedStartConstraint{RequestID: "fixed-v1", Command: "fixed", TimeoutMillis: 15000}).Validate(); err != nil {
		t.Fatal("exact command constraint changed", err)
	}
	// A runtime without directory support keeps its meaning: one fixed directory.
	clock := &testClock{now: time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)}
	issuer, authority := newGrant(t, clock)
	cfg := Config{FixedStart: good, Issuer: issuer, Authority: authority, Runtime: newFakeRuntime(), Clock: clock.Now, CleanupTimeout: time.Second}
	if s, err := New(cfg); s != nil || !errors.Is(err, ErrUnconfigured) {
		t.Fatal("directory constraint accepted on a runtime that cannot honor it", err)
	}
	// The refusal came before the grant was claimed, so the same grant still serves.
	cfg.Runtime = &dirRuntime{fakeRuntime: newFakeRuntime()}
	s, err := New(cfg)
	if err != nil {
		t.Fatal("refused configuration consumed the grant", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The directory path is only for the hash constraint. A runtime that can
// change directory must still start every other command where it always did.
func TestExactCommandConstraintKeepsTheDefaultDirectoryOnDirectoryRuntime(t *testing.T) {
	fixed := &FixedStartConstraint{RequestID: "fixed-v1", Command: "fixed", TimeoutMillis: 60_000}
	e, runtime := newDirEnv(t, fixed)
	st := e.mustStart(fixed.RequestID, fixed.Command)
	e.proc()
	if dirs := runtime.seen(); len(dirs) != 0 || e.runtime.startCount() != 1 {
		t.Fatalf("exact command started through the directory path: %q starts=%d", dirs, e.runtime.startCount())
	}
	if _, err := e.session.Cancel(e.ref(st.CommandID)); err != nil {
		t.Fatal(err)
	}
	if final := e.await(st.CommandID); final.Cleanup != CleanupConfirmed {
		t.Fatal(final)
	}
}
