package macagent

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

func TestNewRejectsUntrustedComposition(t *testing.T) {
	s := newFakeSpawner()
	for name, cfg := range map[string]Config{
		"relative cwd":   {WorkDir: "work", Home: "/Users/agent"},
		"root cwd":       {WorkDir: "/", Home: "/Users/agent"},
		"unclean cwd":    {WorkDir: "/Users/agent/../work", Home: "/Users/agent"},
		"empty home":     {WorkDir: "/Users/agent/work"},
		"newline home":   {WorkDir: "/Users/agent/work", Home: "/Users/agent\nPATH=/tmp"},
		"negative grace": {WorkDir: "/Users/agent/work", Home: "/Users/agent", TermGrace: -1},
	} {
		if r, err := newRuntime(cfg, s.spawn, s.checks(), time.Millisecond); r != nil || !errors.Is(err, ErrConfig) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if r, err := newRuntime(testTiming, nil, s.checks(), time.Millisecond); r != nil || !errors.Is(err, ErrConfig) {
		t.Fatalf("nil spawn: %v", err)
	}
	if r, err := newRuntime(testTiming, s.spawn, hostChecks{}, time.Millisecond); r != nil || !errors.Is(err, ErrConfig) {
		t.Fatalf("missing host checks: %v", err)
	}
	s.setHost(nil, errFake) // SIGCHLD ignored
	if r, err := newRuntime(testTiming, s.spawn, s.checks(), time.Millisecond); r != nil || !errors.Is(err, ErrHostState) {
		t.Fatalf("ignored SIGCHLD accepted: %v", err)
	}
	if !nativeSupported {
		if r, err := New(testTiming); r != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("non-darwin New = %v", err)
		}
	}
	if s.count() != 0 {
		t.Fatal("construction spawned a process")
	}
}

// unprofiled is a runtime without a declared profile, i.e. the original
// Windows-guest meaning of agentexec.GuestRuntime.
type unprofiled struct{ r *Runtime }

func (u unprofiled) StartShell(ctx context.Context, g supervisor.Binding, b []byte) (agentexec.GuestProcess, error) {
	return u.r.StartShell(ctx, g, b)
}

func TestPolicyProfilesAreSeparate(t *testing.T) {
	s := newFakeSpawner()
	mac := newTestRuntime(t, s)
	issuer, authority := grant(t, agentexec.FullPolicyDigest(), time.Hour)
	if sess, err := agentexec.New(agentexec.Config{Issuer: issuer, Authority: authority, Runtime: mac, Clock: time.Now, CleanupTimeout: time.Second}); sess != nil || !errors.Is(err, agentexec.ErrProfile) {
		t.Fatalf("Mac runtime served a Windows-guest grant: %v", err)
	}
	issuer, authority = grant(t, agentexec.MacCurrentUserPolicyDigest(), time.Hour)
	cfg := agentexec.Config{Issuer: issuer, Authority: authority, Runtime: unprofiled{mac}, Clock: time.Now, CleanupTimeout: time.Second}
	if sess, err := agentexec.New(cfg); sess != nil || !errors.Is(err, agentexec.ErrProfile) {
		t.Fatalf("unprofiled runtime served a Mac grant: %v", err)
	}
	cfg.Runtime = mac
	sess, err := agentexec.New(cfg)
	if err != nil || sess.Profile() != agentexec.ProfileMacCurrentUser {
		t.Fatalf("Mac grant with Mac runtime: %v", err)
	}
	if err := sess.Close(finite(t)); err != nil {
		t.Fatal(err)
	}
	if s.count() != 0 {
		t.Fatal("profile checks spawned a process")
	}
}

func TestFixedShellEnvironmentStdinOutputAndNaturalExit(t *testing.T) {
	t.Setenv("RESCUE_HOST_SIGNER_TOKEN", "host-secret")
	t.Setenv("BASH_ENV", "/tmp/evil")
	t.Setenv("ENV", "/tmp/evil")
	h := newHarness(t, time.Hour, agentexec.Limits{})
	st := h.start("req-1", "echo hi; echo warn >&2\n")
	c := h.spawner.child(t)
	if c.spec.path != "/bin/sh" || !slices.Equal(c.spec.argv, []string{"/bin/sh", "-s"}) || c.spec.dir != "/Users/agent/work" {
		t.Fatalf("spawn spec = %+v", c.spec)
	}
	want := []string{"HOME=/Users/agent", "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "SHELL=/bin/sh", "LANG=C"}
	if !slices.Equal(c.spec.env, want) {
		t.Fatalf("environment = %q", c.spec.env)
	}
	for _, kv := range c.spec.env {
		if strings.Contains(kv, "host-secret") || strings.HasPrefix(kv, "BASH_ENV=") || strings.HasPrefix(kv, "ENV=") {
			t.Fatalf("inherited variable reached the command: %q", kv)
		}
	}
	if os.Getenv("RESCUE_HOST_SIGNER_TOKEN") == "" {
		t.Fatal("fixture did not set the host variable")
	}
	if got := string(<-c.stdinData); got != "echo hi; echo warn >&2\n" {
		t.Fatalf("stdin = %q", got)
	}
	// Each write is observed before the next so the two pipes have one order.
	c.write(t, c.stdout, "hi\n")
	first := h.readUntil(st.CommandID, 0, 3)
	c.write(t, c.stderr, "warn\n")
	second := h.readUntil(st.CommandID, 3, 8)
	if len(first) != 1 || string(first[0].Data) != "hi\n" || first[0].Stream != agentexec.Stdout ||
		len(second) != 1 || string(second[0].Data) != "warn\n" || second[0].Stream != agentexec.Stderr || second[0].Offset != 3 {
		t.Fatalf("chunks = %+v %+v", first, second)
	}
	c.os.exit(0)
	c.closeOutputs()
	final := h.await(st.CommandID)
	if final.Phase != agentexec.PhaseExited || final.Exit != (agentexec.ExitStatus{Known: true, Code: 0}) || final.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	// The natural exit is reported, yet the group is still swept before the reap.
	if got := c.os.entries(); !before(got, "TERM", "reap") || !before(got, "KILL", "reap") {
		t.Fatalf("order = %v", got)
	}
	noViolation(t, c.os)
}

// M1: a same-group background member with closed stdio survives the shell's
// natural exit. It must be swept by group TERM/KILL before the single reap.
func TestNaturalExitSweepsSameGroupMemberBeforeReap(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	h.spawner.configure = func(f *fakeOS) { f.member, f.memberIgnoresTerm = true, true }
	st := h.start("req-1", "sleep 100 >/dev/null 2>&1 & exit 3\n")
	c := h.spawner.child(t)
	<-c.stdinData
	c.os.exit(3)
	c.closeOutputs()
	final := h.await(st.CommandID)
	if final.Phase != agentexec.PhaseExited || final.Exit != (agentexec.ExitStatus{Known: true, Code: 3}) || final.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	got := c.os.entries()
	if !before(got, "member-exit", "reap") || !before(got, "KILL", "reap") || strings.Count(strings.Join(got, ","), "reap") != 1 {
		t.Fatalf("background member outlived the reap: %v", got)
	}
	noViolation(t, c.os)
}

// A valid observed exit can omit status; this is not a watch-registration error.
func TestObservedExitWithoutStatusReportsStatusAfterSweep(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	h.spawner.configure = func(f *fakeOS) { f.exitBeforeWatch = true }
	st := h.start("req-1", "exit 5\n")
	c := h.spawner.child(t)
	<-c.stdinData
	c.os.exit(5)
	c.closeOutputs()
	final := h.await(st.CommandID)
	if final.Phase != agentexec.PhaseExited || final.Exit != (agentexec.ExitStatus{Known: true, Code: 5}) || final.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	if got := c.os.entries(); !before(got, "KILL", "reap") {
		t.Fatalf("status was taken by a reap before the sweep: %v", got)
	}
	noViolation(t, c.os)
}

func TestObserverSetupFailureIsStoppedAsPartialStart(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	h.spawner.configure = func(f *fakeOS) { f.observerErr = errFake }
	h.spawner.result = func(sp spawned) (spawned, error) { return sp, errors.Join(errObserver, errFake) }
	st := h.start("req-1", "id\n")
	c := h.spawner.child(t)
	final := h.await(st.CommandID)
	if final.Phase != agentexec.PhaseFailed || final.Stop != agentexec.StopRuntimeFault || final.Cleanup != agentexec.CleanupConfirmed || final.Exit.Known {
		t.Fatalf("final = %+v", final)
	}
	if got := c.os.entries(); !before(got, "KILL", "reap") {
		t.Fatalf("partial process was not swept before reap: %v", got)
	}
	noViolation(t, c.os)
}

// An observer failure while running never invents an exit status.
func TestObserverFailureDuringRunDoesNotInventExit(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	st := h.start("req-1", "exit 0\n")
	c := h.spawner.child(t)
	<-c.stdinData
	c.os.set(func(f *fakeOS) { f.observerErr = errFake })
	c.os.exit(0)
	c.closeOutputs()
	final := h.await(st.CommandID)
	if final.Exit.Known || final.Phase == agentexec.PhaseExited || final.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	if got := c.os.entries(); !before(got, "KILL", "reap") {
		t.Fatalf("order = %v", got)
	}
}

func TestCancelSendsTermThenKillBeforeReapAndJoinsIO(t *testing.T) {
	for _, exitOnTerm := range []bool{false, true} {
		t.Run(map[bool]string{false: "kill-needed", true: "exits-on-term"}[exitOnTerm], func(t *testing.T) {
			h := newHarness(t, time.Hour, agentexec.Limits{})
			h.spawner.configure = func(f *fakeOS) { f.exitOnTerm = exitOnTerm }
			st := h.start("req-1", "sleep 100\n")
			c := h.spawner.child(t)
			<-c.stdinData
			if got, err := h.session.Cancel(h.ref(st.CommandID)); err != nil || got.Stop != agentexec.StopCanceled {
				t.Fatalf("Cancel = %+v, %v", got, err)
			}
			final := h.await(st.CommandID)
			if final.Phase != agentexec.PhaseCanceled || final.Exit.Known || final.Cleanup != agentexec.CleanupConfirmed {
				t.Fatalf("final = %+v", final)
			}
			if got := c.os.entries(); !slices.Equal(got, []string{"stdin-closed", "TERM", "KILL", "reap", "observer-released"}) {
				t.Fatalf("order = %v", got)
			}
		})
	}
}

func TestBlockedStdinWriterIsReleasedByTerminate(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	h.spawner.holdStdin = true // the shell never reads, so the command write blocks
	st := h.start("req-1", strings.Repeat("x", 4096)+"\n")
	c := h.spawner.child(t)
	h.session.Cancel(h.ref(st.CommandID))
	final := h.await(st.CommandID)
	if final.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("blocked writer was not joined: %+v", final)
	}
	if got := c.os.entries(); !before(got, "stdin-closed", "TERM") || !before(got, "KILL", "reap") {
		t.Fatalf("order = %v", got)
	}
}

func TestMissingPipeProcessIsOwnedAndStopped(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	h.spawner.result = func(sp spawned) (spawned, error) {
		sp.stdout = nil
		return sp, nil
	}
	st := h.start("req-1", "id\n")
	c := h.spawner.child(t)
	final := h.await(st.CommandID)
	if final.Phase != agentexec.PhaseFailed || final.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	if got := c.os.entries(); !before(got, "stdin-closed", "TERM") || !before(got, "KILL", "reap") {
		t.Fatalf("process without full plumbing was not owned and stopped: %v", got)
	}
}

func TestHardDeadlineRevokesGrantBeforeTerm(t *testing.T) {
	h := newHarness(t, 300*time.Millisecond, agentexec.Limits{})
	st := h.start("req-1", "sleep 100\n")
	c := h.spawner.child(t)
	states := make(chan supervisor.State, 4)
	c.os.set(func(f *fakeOS) {
		f.onSignal = func(signal) { states <- h.authority.Snapshot(time.Now()).State }
	})
	final := h.await(st.CommandID)
	if final.Stop != agentexec.StopHardDeadline || final.Phase != agentexec.PhaseCanceled || final.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	if state := <-states; state != supervisor.Revoked {
		t.Fatalf("TERM sent while grant was %s", state)
	}
	noViolation(t, c.os)
}

func TestExternalRevokeStopsShellWithoutRequests(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	st := h.start("req-1", "sleep 100\n")
	c := h.spawner.child(t)
	states := make(chan supervisor.State, 4)
	c.os.set(func(f *fakeOS) {
		f.onSignal = func(signal) { states <- h.authority.Snapshot(time.Now()).State }
	})
	if err := h.issuer.Revoke(h.session.Binding()); err != nil {
		t.Fatal(err)
	}
	select {
	case state := <-states:
		if state != supervisor.Revoked {
			t.Fatalf("TERM sent while grant was %s", state)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("external revoke did not reach the shell")
	}
	if final := h.await(st.CommandID); final.Stop != agentexec.StopRevoked || final.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
}

func TestNoSpawnAfterRevokeOrCanceledContext(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	if err := h.issuer.Revoke(h.session.Binding()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.session.Start(h.startReq("req-1", "id\n")); !errors.Is(err, agentexec.ErrDenied) {
		t.Fatalf("Start after revoke = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := h.runtime.StartShell(ctx, h.session.Binding(), []byte("id\n")); p != nil || !errors.Is(err, agentexec.ErrNotStarted) {
		t.Fatalf("canceled StartShell = %v", err)
	}
	if n := h.spawner.count(); n != 0 {
		t.Fatalf("spawned %d processes", n)
	}
}

// M2/M3: an inheritable descriptor or an ignored SIGCHLD found right before the
// spawn refuses the command; nothing is spawned and nothing needs cleanup.
func TestHostChecksRefuseSpawn(t *testing.T) {
	for name, set := range map[string]func(*fakeSpawner){
		"inheritable fd":  func(s *fakeSpawner) { s.setHost(errFake, nil) },
		"SIGCHLD ignored": func(s *fakeSpawner) { s.setHost(nil, errFake) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, time.Hour, agentexec.Limits{})
			set(h.spawner)
			st := h.start("req-1", "id\n")
			final := h.await(st.CommandID)
			if final.Phase != agentexec.PhaseFailed || final.Cleanup != agentexec.CleanupNotRequired {
				t.Fatalf("final = %+v", final)
			}
			if n := h.spawner.count(); n != 0 {
				t.Fatalf("spawned despite unsafe host state: %d", n)
			}
			if _, err := h.runtime.StartShell(finite(t), h.session.Binding(), []byte("id\n")); !errors.Is(err, ErrHostState) || !errors.Is(err, agentexec.ErrNotStarted) {
				t.Fatalf("StartShell = %v", err)
			}
		})
	}
}

func TestPartialSpawnIsStoppedAndNoProcessIsNotStarted(t *testing.T) {
	t.Run("partial", func(t *testing.T) {
		h := newHarness(t, time.Hour, agentexec.Limits{})
		h.spawner.result = func(sp spawned) (spawned, error) { return sp, errFake }
		st := h.start("req-1", "id\n")
		c := h.spawner.child(t)
		final := h.await(st.CommandID)
		if final.Phase != agentexec.PhaseFailed || final.Stop != agentexec.StopRuntimeFault || final.Cleanup != agentexec.CleanupConfirmed {
			t.Fatalf("final = %+v", final)
		}
		if got := c.os.entries(); !before(got, "TERM", "reap") || !before(got, "KILL", "reap") {
			t.Fatalf("partial process was not stopped by its owner: %v", got)
		}
	})
	t.Run("no-process", func(t *testing.T) {
		h := newHarness(t, time.Hour, agentexec.Limits{})
		h.spawner.result = func(sp spawned) (spawned, error) {
			return spawned{stdin: sp.stdin, stdout: sp.stdout, stderr: sp.stderr}, errFake
		}
		st := h.start("req-1", "id\n")
		final := h.await(st.CommandID)
		if final.Phase != agentexec.PhaseFailed || final.Cleanup != agentexec.CleanupNotRequired {
			t.Fatalf("final = %+v", final)
		}
	})
}

func TestLateSpawnAfterCancelIsStoppedBySameOwner(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	h.spawner.gate, h.spawner.entered = make(chan struct{}), make(chan struct{})
	st := h.start("req-1", "id\n")
	<-h.spawner.entered
	if got, err := h.session.Cancel(h.ref(st.CommandID)); err != nil || got.Phase != agentexec.PhaseStarting {
		t.Fatalf("Cancel during spawn = %+v, %v", got, err)
	}
	close(h.spawner.gate)
	c := h.spawner.child(t)
	final := h.await(st.CommandID)
	if final.Phase != agentexec.PhaseCanceled || final.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	if got := c.os.entries(); !before(got, "KILL", "reap") {
		t.Fatalf("late process was not stopped: %v", got)
	}
	noViolation(t, c.os)
}

func TestEscapedStdioHolderKeepsCleanupUnknownUntilEOF(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	diag := &CleanupDiagnostics{}
	h.runtime.cleanupDiagnostics = diag
	defer func() {
		if !slices.Contains(diag.Failures(), "io_join") {
			t.Fatalf("missing cleanup class: %v", diag.Failures())
		}
	}()
	h.spawner.configure = func(f *fakeOS) { f.escaped = true }
	st := h.start("req-1", "setsid sleep 100 &\n")
	c := h.spawner.child(t)
	h.session.Cancel(h.ref(st.CommandID))
	final := h.await(st.CommandID)
	if final.Cleanup != agentexec.CleanupUnknown {
		t.Fatalf("escaped holder reported as cleaned: %+v", final)
	}
	if _, err := h.session.Start(h.startReq("req-2", "id\n")); !errors.Is(err, agentexec.ErrCleanupUnknown) {
		t.Fatalf("next Start = %v", err)
	}
	if _, err := h.session.Cleanup(finite(t), h.ref(st.CommandID)); !errors.Is(err, agentexec.ErrCleanupUnknown) {
		t.Fatalf("retry while still held = %v", err)
	}
	noViolation(t, c.os) // the retry sent no signal after the leader was reaped
	c.closeOutputs()     // the escaped holder exits
	if got, err := h.session.Cleanup(finite(t), h.ref(st.CommandID)); err != nil || got.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("retry after EOF = %+v, %v", got, err)
	}
	h.start("req-2", "id\n")
}

func TestIgnoredKillStaysUnknownAndRetriesOnlyBeforeReap(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	diag := &CleanupDiagnostics{}
	h.runtime.cleanupDiagnostics = diag
	defer func() {
		if !slices.Contains(diag.Failures(), "reap_timeout") {
			t.Fatalf("missing cleanup class: %v", diag.Failures())
		}
	}()
	h.spawner.configure = func(f *fakeOS) { f.ignoreKill = true }
	st := h.start("req-1", "sleep 100\n")
	c := h.spawner.child(t)
	h.session.Cancel(h.ref(st.CommandID))
	if final := h.await(st.CommandID); final.Cleanup != agentexec.CleanupUnknown {
		t.Fatalf("unexited leader reported as cleaned: %+v", final)
	}
	if _, err := h.session.Cleanup(finite(t), h.ref(st.CommandID)); !errors.Is(err, agentexec.ErrCleanupUnknown) {
		t.Fatalf("retry while leader lives = %v", err)
	}
	c.os.set(func(f *fakeOS) { f.ignoreKill = false })
	if got, err := h.session.Cleanup(finite(t), h.ref(st.CommandID)); err != nil || got.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("retry = %+v, %v", got, err)
	}
	got := c.os.entries()
	if !before(got, "KILL", "reap") || strings.Count(strings.Join(got, ","), "reap") != 1 {
		t.Fatalf("order = %v", got)
	}
	noViolation(t, c.os)
}

func TestFailedGroupSignalIsNeverConfirmed(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	diag := &CleanupDiagnostics{}
	h.runtime.cleanupDiagnostics = diag
	defer func() {
		if !slices.Contains(diag.Failures(), "signal_kill_other") {
			t.Fatalf("missing cleanup class: %v", diag.Failures())
		}
	}()
	h.spawner.configure = func(f *fakeOS) {
		f.exitOnTerm = true
		f.signalErr = map[signal]error{sigKill: errFake} // e.g. a member this user cannot signal
	}
	st := h.start("req-1", "sleep 100\n")
	h.spawner.child(t)
	h.session.Cancel(h.ref(st.CommandID))
	if final := h.await(st.CommandID); final.Cleanup != agentexec.CleanupUnknown {
		t.Fatalf("failed KILL reported as cleaned: %+v", final)
	}
	if _, err := h.session.Cleanup(finite(t), h.ref(st.CommandID)); !errors.Is(err, agentexec.ErrCleanupUnknown) {
		t.Fatalf("retry silently confirmed after a failed signal: %v", err)
	}
}

func TestReapFailureStopsSignalsAndStaysUnknown(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	diag := &CleanupDiagnostics{}
	h.runtime.cleanupDiagnostics = diag
	defer func() {
		if !slices.Contains(diag.Failures(), "reap_lost_other") {
			t.Fatalf("missing cleanup class: %v", diag.Failures())
		}
	}()
	st := h.start("req-1", "sleep 100\n")
	c := h.spawner.child(t)
	c.os.set(func(f *fakeOS) { f.reapErr = errFake })
	h.session.Cancel(h.ref(st.CommandID))
	if final := h.await(st.CommandID); final.Cleanup != agentexec.CleanupUnknown {
		t.Fatalf("lost reap reported as cleaned: %+v", final)
	}
	before := len(c.os.entries())
	if _, err := h.session.Cleanup(finite(t), h.ref(st.CommandID)); !errors.Is(err, agentexec.ErrCleanupUnknown) {
		t.Fatalf("retry = %v", err)
	}
	if after := c.os.entries(); len(after) != before {
		t.Fatalf("signals sent after the reap state was lost: %v", after)
	}
	noViolation(t, c.os)
}

// L2: a closer that certifies the descriptor is still owned is retried by the
// same owner; any other close error stays permanently unknown and the
// descriptor is never closed a second time.
func TestCloseRetryAndPermanentUncertainty(t *testing.T) {
	for name, tc := range map[string]struct {
		results []error
		retryOK bool
		calls   int
	}{
		"retryable": {results: []error{errCloseRetry, nil}, retryOK: true, calls: 2},
		"uncertain": {results: []error{errFake, nil}, retryOK: false, calls: 1},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, time.Hour, agentexec.Limits{})
			var stdout *scriptedCloser
			h.spawner.result = func(sp spawned) (spawned, error) {
				stdout = &scriptedCloser{Reader: sp.stdout, results: append([]error(nil), tc.results...)}
				sp.stdout = stdout
				return sp, nil
			}
			st := h.start("req-1", "id\n")
			c := h.spawner.child(t)
			<-c.stdinData
			c.os.exit(0)
			c.closeOutputs()
			if final := h.await(st.CommandID); final.Cleanup != agentexec.CleanupUnknown || final.Phase != agentexec.PhaseExited {
				t.Fatalf("first close error reported as cleaned: %+v", final)
			}
			got, err := h.session.Cleanup(finite(t), h.ref(st.CommandID))
			if tc.retryOK != (err == nil && got.Cleanup == agentexec.CleanupConfirmed) {
				t.Fatalf("retry = %+v, %v", got, err)
			}
			if n := stdout.attempts(); n != tc.calls {
				t.Fatalf("close attempts = %d, want %d", n, tc.calls)
			}
		})
	}
	t.Run("observer", func(t *testing.T) {
		h := newHarness(t, time.Hour, agentexec.Limits{})
		h.spawner.configure = func(f *fakeOS) { f.observerCloseErr = errors.Join(errCloseUncertain, errFake) }
		st := h.start("req-1", "id\n")
		c := h.spawner.child(t)
		<-c.stdinData
		c.os.exit(0)
		c.closeOutputs()
		if final := h.await(st.CommandID); final.Cleanup != agentexec.CleanupUnknown {
			t.Fatalf("observer close error reported as cleaned: %+v", final)
		}
		if _, err := h.session.Cleanup(finite(t), h.ref(st.CommandID)); !errors.Is(err, agentexec.ErrCleanupUnknown) {
			t.Fatalf("retry = %v", err)
		}
	})
}

// L3: Wait and Terminate racing on a leader whose status needs the reap. In
// every interleaving there is one reap, KILL precedes it, and nothing signals
// after it; Wait either reports the natural status or refuses a forced one.
func TestConcurrentWaitAndTerminateKillBeforeReap(t *testing.T) {
	for i := range 20 {
		s := newFakeSpawner()
		s.configure = func(f *fakeOS) { f.exitBeforeWatch = true }
		sp, _ := s.spawn(spawnSpec{})
		c := s.child(t)
		p := newProcess(sp, fakeTiming, nil)
		c.os.exit(7)
		c.closeOutputs()
		waitCtx, termCtx := finite(t), finite(t)
		var wg sync.WaitGroup
		var st agentexec.ExitStatus
		var waitErr, termErr error
		wg.Add(2)
		go func() { defer wg.Done(); st, waitErr = p.Wait(waitCtx) }()
		go func() { defer wg.Done(); termErr = p.Terminate(termCtx) }()
		wg.Wait()
		if termErr != nil {
			t.Fatalf("run %d: Terminate = %v", i, termErr)
		}
		if waitErr == nil && st.Code != 7 || waitErr != nil && !errors.Is(waitErr, errForcedExit) {
			t.Fatalf("run %d: Wait = %+v, %v", i, st, waitErr)
		}
		got := c.os.entries()
		if !before(got, "KILL", "reap") || strings.Count(strings.Join(got, ","), "reap") != 1 {
			t.Fatalf("run %d: order = %v", i, got)
		}
		if err := p.Release(finite(t)); err != nil {
			t.Fatalf("run %d: Release = %v", i, err)
		}
		n := len(c.os.entries())
		if err := p.Terminate(finite(t)); err != nil || len(c.os.entries()) != n {
			t.Fatalf("run %d: repeated Terminate acted after the reap: %v", i, c.os.entries())
		}
		noViolation(t, c.os)
	}
}

// countingSource is an endless stdout. Each Read checks the read-ahead bound at
// the moment it is called: read n can start only after n-1 chunks were queued,
// and the queue holds eventSlots, so n <= eventSlots+2+consumed, where the +2
// covers the chunk being sent and one receive not yet counted by the consumer.
type countingSource struct {
	reads, consumed atomic.Int32
	violated, eof   atomic.Bool
	atBound         chan struct{}
	boundOnce       sync.Once
}

func (s *countingSource) Read(b []byte) (int, error) {
	if s.eof.Load() {
		return 0, io.EOF
	}
	n := s.reads.Add(1)
	if n > eventSlots+2+s.consumed.Load() {
		s.violated.Store(true)
	}
	if n == eventSlots+1 {
		s.boundOnce.Do(func() { close(s.atBound) })
	}
	b[0] = byte('a' + (n-1)%26)
	return 1, nil
}

func (s *countingSource) Close() error { return nil }

func TestOutputReadAheadIsBounded(t *testing.T) {
	src := &countingSource{atBound: make(chan struct{})}
	f := &fakeOS{}
	p := newProcess(spawned{proc: f, stdin: nopWriteCloser{}, stdout: src, stderr: io.NopCloser(eofReader{})}, fakeTiming, nil)
	select {
	case <-src.atBound:
	case <-time.After(2 * time.Second):
		t.Fatal("reader did not fill the queue")
	}
	for i := range 20 {
		stream, data, err := p.Next(finite(t))
		if err != nil || stream != agentexec.Stdout || len(data) != 1 || data[0] != byte('a'+i%26) {
			t.Fatalf("chunk %d = %v %q %v", i, stream, data, err)
		}
		src.consumed.Add(1)
	}
	if src.violated.Load() {
		t.Fatalf("reader read ahead beyond the bound: reads=%d consumed=%d", src.reads.Load(), src.consumed.Load())
	}
	if err := p.Release(finite(t)); !errors.Is(err, errNotTerminated) {
		t.Fatalf("Release before Terminate = %v", err)
	}
	src.eof.Store(true)
	f.exit(0)
	if err := p.Terminate(finite(t)); err != nil {
		t.Fatalf("Terminate = %v", err)
	}
	if err := p.Release(finite(t)); err != nil {
		t.Fatalf("Release = %v", err)
	}
}

func TestSessionBackpressureDeliversAllOutputInOrder(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{MaxRetained: 64 + 8})
	st := h.start("req-1", "yes\n")
	c := h.spawner.child(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 6 {
			if _, err := c.stdout.Write([]byte(strings.Repeat(string(rune('a'+i)), 8))); err != nil {
				return
			}
		}
	}()
	chunks := h.readUntil(st.CommandID, 0, 48)
	var got strings.Builder
	for _, ch := range chunks {
		got.Write(ch.Data)
	}
	if got.String() != "aaaaaaaabbbbbbbbccccccccddddddddeeeeeeeeffffffff" {
		t.Fatalf("output = %q", got.String())
	}
	<-done
	c.os.exit(0)
	c.closeOutputs()
	if final := h.await(st.CommandID); final.Phase != agentexec.PhaseExited || final.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
}
