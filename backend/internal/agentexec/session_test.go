package agentexec

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

func TestAuthorizedCommandOutputCancelAndSameOwnerCleanup(t *testing.T) {
	e := newEnv(t, Limits{})
	st := e.mustStart("req-1", "Get-Date\n")
	if st.Phase != PhaseStarting || st.Cleanup != CleanupPending || len(st.CommandID) != 32 {
		t.Fatalf("start status = %+v", st)
	}
	p := e.proc()
	e.runtime.mu.Lock()
	stdin, grant := e.runtime.stdin[0], e.runtime.grants[0]
	e.runtime.mu.Unlock()
	if !bytes.Equal(stdin, []byte("Get-Date\n")) || grant != e.session.Binding() {
		t.Fatalf("runtime got stdin %q grant %+v", stdin, grant)
	}
	send(t, p, Stdout, "out1")
	send(t, p, Stderr, "err1")
	send(t, p, Stdout, "out2")
	chunks := e.readUntil(st.CommandID, 0, 12)
	want := []Chunk{{Stdout, 0, []byte("out1")}, {Stderr, 4, []byte("err1")}, {Stdout, 8, []byte("out2")}}
	if len(chunks) != len(want) {
		t.Fatalf("chunks = %+v", chunks)
	}
	for i := range want {
		if chunks[i].Stream != want[i].Stream || chunks[i].Offset != want[i].Offset || !bytes.Equal(chunks[i].Data, want[i].Data) {
			t.Fatalf("chunk %d = %+v, want %+v", i, chunks[i], want[i])
		}
	}
	if st, _ := e.session.Status(e.ref(st.CommandID)); st.Phase != PhaseRunning || st.Terminal || st.OutputEnd != 12 {
		t.Fatalf("running status = %+v", st)
	}

	if st, err := e.session.Cancel(e.ref(st.CommandID)); err != nil || st.Stop != StopCanceled {
		t.Fatalf("Cancel = %+v, %v", st, err)
	}
	final := e.await(st.CommandID)
	// A forced stop reports Stop, not the kill code as the command's exit.
	if final.Phase != PhaseCanceled || final.Stop != StopCanceled || final.Cleanup != CleanupConfirmed || final.Exit.Known {
		t.Fatalf("final status = %+v", final)
	}
	terminates, releases := p.counts()
	if terminates < 1 || releases != 1 {
		t.Fatalf("terminate=%d release=%d", terminates, releases)
	}
	if again, err := e.session.Cancel(e.ref(st.CommandID)); err != nil || again != final {
		t.Fatalf("repeated Cancel = %+v, %v", again, err)
	}
	if again, err := e.session.Cleanup(finite(t), e.ref(st.CommandID)); err != nil || again.Cleanup != CleanupConfirmed {
		t.Fatalf("repeated Cleanup = %+v, %v", again, err)
	}
	if t2, r2 := p.counts(); t2 != terminates || r2 != 1 {
		t.Fatalf("idempotent calls repeated side effects: terminate=%d release=%d", t2, r2)
	}
	out, err := e.session.Output(OutputRequest{Version: Version, Binding: e.session.Binding(), CommandID: st.CommandID, Cursor: 12, MaxBytes: 16})
	if err != nil || !out.Complete || len(out.Chunks) != 0 {
		t.Fatalf("terminal output = %+v, %v", out, err)
	}
	// Command cancel is not a grant revoke; the same grant admits a new command.
	if !e.issuer.Snapshot().HasAuthority {
		t.Fatal("command cancel revoked the grant")
	}
	next := e.mustStart("req-2", "whoami\n")
	if next.CommandID == st.CommandID {
		t.Fatal("new request reused the command ID")
	}
	e.proc()
	released, err := e.session.Output(OutputRequest{Version: Version, Binding: e.session.Binding(), CommandID: st.CommandID, Cursor: 0, MaxBytes: 16})
	if err != nil || released.Gap != 12 || !released.Complete {
		t.Fatalf("released output = %+v, %v", released, err)
	}
}

func TestNaturalExitKeepsExitStatusSeparateFromCleanup(t *testing.T) {
	e := newEnv(t, Limits{})
	e.runtime.configure = func(p *fakeProc) {
		p.code = 3
		p.releaseErrs = []error{errors.New("job still active")}
	}
	st := e.mustStart("req-1", "exit 3\n")
	p := e.proc()
	send(t, p, Stdout, "x")
	close(p.events)
	close(p.exited)
	final := e.await(st.CommandID)
	if final.Phase != PhaseExited || final.Exit != (ExitStatus{Known: true, Code: 3}) || final.Stop != StopNone || final.Cleanup != CleanupUnknown {
		t.Fatalf("final = %+v", final)
	}
	if terminates, releases := p.counts(); terminates != 1 || releases != 1 {
		t.Fatalf("whole workload was not terminated before release: terminate=%d release=%d", terminates, releases)
	}
	// A cancel after the natural exit does not relabel the outcome.
	if again, err := e.session.Cancel(e.ref(st.CommandID)); err != nil || again.Phase != PhaseExited || again.Stop != StopNone {
		t.Fatalf("late Cancel = %+v, %v", again, err)
	}
	if _, err := e.session.Start(e.startReq("req-2", "whoami\n")); !errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("Start with unknown cleanup = %v", err)
	}
	retried, err := e.session.Cleanup(finite(t), e.ref(st.CommandID))
	if err != nil || retried.Cleanup != CleanupConfirmed || retried.Exit.Code != 3 || retried.Phase != PhaseExited {
		t.Fatalf("Cleanup retry = %+v, %v", retried, err)
	}
	if terminates, releases := p.counts(); terminates != 1 || releases != 2 {
		t.Fatalf("retry did not resume at Release: terminate=%d release=%d", terminates, releases)
	}
	e.mustStart("req-2", "whoami\n")
	e.proc()
}

func TestCloseReportsUnknownCleanupAndNeverReleasesClaim(t *testing.T) {
	e := newEnv(t, Limits{})
	e.runtime.configure = func(p *fakeProc) { p.releaseErrs = []error{errors.New("job still active")} }
	st := e.mustStart("req-1", "dir\n")
	p := e.proc()
	close(p.events)
	close(p.exited)
	e.await(st.CommandID)
	if err := e.session.Close(finite(t)); !errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("Close with unknown cleanup = %v", err)
	}
	if st, err := e.session.Cleanup(finite(t), e.ref(st.CommandID)); err != nil || st.Cleanup != CleanupConfirmed {
		t.Fatalf("Cleanup after revoke = %+v, %v", st, err)
	}
	if err := e.session.Close(finite(t)); err != nil {
		t.Fatalf("Close after retry = %v", err)
	}
	cfg := Config{Issuer: e.issuer, Authority: e.authority, Runtime: e.runtime, Clock: e.clock.Now, CleanupTimeout: time.Second}
	if s, err := New(cfg); s != nil || err == nil {
		t.Fatalf("closed grant produced another session: %v", err)
	}
}

func TestCleanupRetryRepeatsFailedTerminateBeforeRelease(t *testing.T) {
	e := newEnv(t, Limits{})
	// Both the Cancel request and the worker's own Terminate fail.
	e.runtime.configure = func(p *fakeProc) {
		p.terminateErrs = []error{errors.New("terminate failed"), errors.New("terminate failed")}
	}
	st := e.mustStart("req-1", "sleep\n")
	p := e.proc()
	e.session.Cancel(e.ref(st.CommandID))
	final := e.await(st.CommandID)
	if final.Cleanup != CleanupUnknown || final.Phase != PhaseCanceled {
		t.Fatalf("final = %+v", final)
	}
	if _, releases := p.counts(); releases != 0 {
		t.Fatal("Release ran after a failed Terminate")
	}
	if st, err := e.session.Cleanup(finite(t), e.ref(st.CommandID)); err != nil || st.Cleanup != CleanupConfirmed {
		t.Fatalf("Cleanup = %+v, %v", st, err)
	}
	if _, releases := p.counts(); releases != 1 {
		t.Fatalf("release=%d", releases)
	}
	if _, err := e.session.Cleanup(context.Background(), e.ref(st.CommandID)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Cleanup without deadline = %v", err)
	}
}

func TestRequestsForOtherGrantVersionOrRestrictedModeAreRejected(t *testing.T) {
	e := newEnv(t, Limits{MaxCommandBytes: 8})
	_, other := newGrant(t, e.clock)
	otherBinding := other.Snapshot(e.clock.Now()).Grant.Binding
	cases := []struct {
		name string
		edit func(*StartRequest)
		want error
	}{
		{"version", func(r *StartRequest) { r.Version = 2 }, ErrVersion},
		{"other grant", func(r *StartRequest) { r.Binding = otherBinding }, ErrBinding},
		{"generation", func(r *StartRequest) { r.Binding.Generation++ }, ErrBinding},
		{"policy digest", func(r *StartRequest) { r.Binding.Identity.PolicyDigest = strings.Repeat("ab", 32) }, ErrBinding},
		{"restricted", func(r *StartRequest) { r.Mode = ModeRestricted }, ErrRestrictedUndefined},
		{"unknown mode", func(r *StartRequest) { r.Mode = 9 }, ErrInvalid},
		{"empty", func(r *StartRequest) { r.Command = nil }, ErrInvalid},
		{"oversized", func(r *StartRequest) { r.Command = []byte("123456789") }, ErrInvalid},
		{"request id", func(r *StartRequest) { r.RequestID = "a b" }, ErrInvalid},
		{"timeout", func(r *StartRequest) { r.TimeoutMillis = 0 }, ErrInvalid},
	}
	for _, tc := range cases {
		r := e.startReq("req-1", "dir\n")
		tc.edit(&r)
		if _, err := e.session.Start(r); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := e.session.Status(StatusRequest{Version: Version, Binding: otherBinding, CommandID: "x"}); !errors.Is(err, ErrBinding) {
		t.Fatalf("Status other grant = %v", err)
	}
	if _, err := e.session.Status(e.ref("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Status missing = %v", err)
	}
	if n := e.runtime.startCount(); n != 0 {
		t.Fatalf("rejected requests reached the runtime %d times", n)
	}
	// A rejected restricted request does not reserve its request ID.
	e.mustStart("req-1", "dir\n")
	e.proc()
}

func TestNewRequiresTheIssuersCurrentAuthorityObject(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)}
	issuer, authority := newGrant(t, clock)
	otherIssuer, otherAuthority := newGrant(t, clock)
	lookalike, err := supervisor.New(authority.Snapshot(clock.Now()).Grant, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	base := Config{Issuer: issuer, Authority: authority, Runtime: newFakeRuntime(), Clock: clock.Now, CleanupTimeout: time.Second}
	for name, edit := range map[string]func(*Config){
		"lookalike authority":    func(c *Config) { c.Authority = lookalike },
		"other issuer":           func(c *Config) { c.Issuer = otherIssuer },
		"other issuer authority": func(c *Config) { c.Authority = otherAuthority },
		"no runtime":             func(c *Config) { c.Runtime = nil },
		"no cleanup budget":      func(c *Config) { c.CleanupTimeout = 0 },
		"tiny retained window":   func(c *Config) { c.Limits.MaxRetained = recordOverhead },
	} {
		cfg := base
		edit(&cfg)
		if s, err := New(cfg); s != nil || !errors.Is(err, ErrUnconfigured) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// None of the rejected attempts consumed the single-use claim.
	s, err := New(base)
	if err != nil {
		t.Fatalf("valid New after rejections = %v", err)
	}
	if err := s.Close(finite(t)); err != nil {
		t.Fatal(err)
	}
	if again, err := New(base); again != nil || !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked authority: err = %v", err)
	}
}

func TestNewRejectsGrantWithoutAgentFullPolicy(t *testing.T) {
	if d := FullPolicyDigest(); len(d) != 64 || strings.ToLower(d) != d || d != FullPolicyDigest() {
		t.Fatalf("policy digest = %q", d)
	}
	clock := &testClock{now: time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)}
	for _, digest := range []string{strings.Repeat("ab", 32), strings.Repeat("0", 64)} {
		issuer, authority := newGrantWith(t, clock.Now, digest, time.Hour, time.Minute)
		runtime := newFakeRuntime()
		if s, err := New(Config{Issuer: issuer, Authority: authority, Runtime: runtime, Clock: clock.Now, CleanupTimeout: time.Second}); s != nil || !errors.Is(err, ErrPolicy) {
			t.Fatalf("digest %s: err = %v", digest, err)
		}
		if n := runtime.startCount(); n != 0 {
			t.Fatalf("runtime starts = %d", n)
		}
	}
}

type macProfiledRuntime struct{ *fakeRuntime }

func (macProfiledRuntime) AgentProfile() Profile { return ProfileMacCurrentUser }

func TestRuntimeProfileMustMatchGrantPolicy(t *testing.T) {
	if FullPolicyDigest() == MacCurrentUserPolicyDigest() || len(MacCurrentUserPolicyDigest()) != 64 {
		t.Fatal("policy digests are not separate")
	}
	clock := &testClock{now: time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)}
	newCfg := func(digest string, runtime GuestRuntime) Config {
		issuer, authority := newGrantWith(t, clock.Now, digest, time.Hour, time.Minute)
		return Config{Issuer: issuer, Authority: authority, Runtime: runtime, Clock: clock.Now, CleanupTimeout: time.Second}
	}
	mac := macProfiledRuntime{newFakeRuntime()}

	windowsGrant := newCfg(FullPolicyDigest(), mac)
	if s, err := New(windowsGrant); s != nil || !errors.Is(err, ErrProfile) {
		t.Fatalf("Mac runtime served a Windows-guest grant: %v", err)
	}
	windowsGrant.Runtime = newFakeRuntime() // the rejection did not consume the claim
	s, err := New(windowsGrant)
	if err != nil || s.Profile() != ProfileWindowsGuest {
		t.Fatalf("Windows-guest grant: %v", err)
	}
	if err := s.Close(finite(t)); err != nil {
		t.Fatal(err)
	}

	macGrant := newCfg(MacCurrentUserPolicyDigest(), newFakeRuntime())
	if s, err := New(macGrant); s != nil || !errors.Is(err, ErrProfile) {
		t.Fatalf("unprofiled runtime served a Mac grant: %v", err)
	}
	macGrant.Runtime = mac
	s, err = New(macGrant)
	if err != nil || s.Profile() != ProfileMacCurrentUser {
		t.Fatalf("Mac grant: %v", err)
	}
	if err := s.Close(finite(t)); err != nil {
		t.Fatal(err)
	}
	if n := mac.startCount(); n != 0 {
		t.Fatalf("runtime starts = %d", n)
	}
}

// The current-user boundary is accepted only when the Mac policy, the Mac
// runtime, the current-user grant boundary and the explicit current-user issuer
// all agree. Every mismatch is refused before the single-use claim.
func TestCurrentUserBoundaryNeedsTheExplicitPathAndMatchingProfile(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)}
	mac := macProfiledRuntime{newFakeRuntime()}
	cases := []struct {
		name     string
		digest   string
		boundary supervisor.GuestBoundary
		runtime  GuestRuntime
	}{
		{"Mac policy on a guest grant", MacCurrentUserPolicyDigest(), supervisor.SeparatedNonAdminGuest, mac},
		{"Windows policy on a current-user grant", FullPolicyDigest(), supervisor.CurrentUserNoIsolation, newFakeRuntime()},
		{"Windows policy and Mac runtime on a current-user grant", FullPolicyDigest(), supervisor.CurrentUserNoIsolation, mac},
		{"Mac policy and guest runtime on a current-user grant", MacCurrentUserPolicyDigest(), supervisor.CurrentUserNoIsolation, newFakeRuntime()},
	}
	for _, tc := range cases {
		issuer, authority := newGrantBoundary(t, clock.Now, tc.digest, tc.boundary, time.Hour, time.Minute)
		if s, err := New(Config{Issuer: issuer, Authority: authority, Runtime: tc.runtime, Clock: clock.Now, CleanupTimeout: time.Second}); s != nil || !errors.Is(err, ErrProfile) {
			t.Fatalf("%s: err = %v", tc.name, err)
		}
		// No consumption on mismatch: the claim is still available.
		if err := authority.ClaimConsumer(authority.Grant().Binding); err != nil {
			t.Fatalf("%s: mismatch consumed the claim: %v", tc.name, err)
		}
	}

	issuer, authority := newGrantBoundary(t, clock.Now, MacCurrentUserPolicyDigest(), supervisor.CurrentUserNoIsolation, time.Hour, time.Minute)
	if issuer.Boundary() != supervisor.CurrentUserNoIsolation || authority.Grant().GuestBoundary != supervisor.CurrentUserNoIsolation {
		t.Fatal("explicit current-user path did not produce a current-user grant")
	}
	s, err := New(Config{Issuer: issuer, Authority: authority, Runtime: mac, Clock: clock.Now, CleanupTimeout: time.Second})
	if err != nil || s.Profile() != ProfileMacCurrentUser || s.Mode() != ModeFull || s.KeySetDigest() != [32]byte{1} {
		t.Fatalf("explicit Mac path: %v", err)
	}
	if err := s.Claim(); err != nil {
		t.Fatalf("first adapter claim: %v", err)
	}
	if err := s.Claim(); !errors.Is(err, ErrSessionClaimed) {
		t.Fatalf("second adapter claim: %v", err)
	}
	// Revoke and lifetime semantics are those of every other grant.
	s.Revoke()
	if _, err := issuer.Lookup(authority.Grant().Binding, clock.Now); err == nil {
		t.Fatal("revoked current-user grant is still current")
	}
	if err := s.Close(finite(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Issuer: issuer, Authority: authority, Runtime: mac, Clock: clock.Now, CleanupTimeout: time.Second}); err == nil {
		t.Fatal("a closed current-user grant got a second Session")
	}
}

func TestPublicNewRejectsSecondSessionConcurrentlyAndAfterClose(t *testing.T) {
	issuer, authority := newGrantWith(t, time.Now, FullPolicyDigest(), time.Hour, time.Minute)
	cfg := Config{Issuer: issuer, Authority: authority, Runtime: newFakeRuntime(), Clock: time.Now, CleanupTimeout: time.Second}
	var wg sync.WaitGroup
	sessions := make(chan *Session, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s, err := New(cfg); err != nil {
				failures <- err
			} else {
				sessions <- s
			}
		}()
	}
	wg.Wait()
	close(sessions)
	close(failures)
	var won []*Session
	for s := range sessions {
		won = append(won, s)
	}
	for err := range failures {
		if !errors.Is(err, ErrSessionClaimed) {
			t.Errorf("duplicate New = %v", err)
		}
	}
	if len(won) != 1 {
		t.Fatalf("sessions created = %d", len(won))
	}
	if err := won[0].Close(finite(t)); err != nil {
		t.Fatal(err)
	}
	if s, err := New(cfg); s != nil || err == nil {
		t.Fatal("Close reset the claim on the same grant")
	}
}

func TestPublicNewEnforcesShortRealDeadline(t *testing.T) {
	issuer, authority := newGrantWith(t, time.Now, FullPolicyDigest(), 300*time.Millisecond, 10*time.Millisecond)
	runtime := newFakeRuntime()
	s, err := New(Config{Issuer: issuer, Authority: authority, Runtime: runtime, Clock: time.Now, CleanupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	st, err := s.Start(StartRequest{Version: Version, Binding: s.Binding(), RequestID: "req-1", Mode: ModeFull, Command: []byte("dir\n"), TimeoutMillis: 60_000})
	if err != nil {
		t.Fatal(err)
	}
	var p *fakeProc
	select {
	case p = <-runtime.procs:
	case <-time.After(2 * time.Second):
		t.Fatal("guest shell was not started")
	}
	observed := make(chan supervisor.State, 4)
	p.setTerminateHook(func() { observed <- authority.Snapshot(time.Now()).State })
	p.awaitKilled(t) // the wall-clock timer alone stops it; no request is issued
	final := awaitTerminal(t, s, StatusRequest{Version: Version, Binding: s.Binding(), CommandID: st.CommandID})
	if final.Phase != PhaseCanceled || final.Stop != StopHardDeadline || final.Cleanup != CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	if state := <-observed; state != supervisor.Revoked {
		t.Fatalf("guest terminated while grant was %s", state)
	}
}

func TestSameRequestReplaysAndMismatchedPayloadIsRejected(t *testing.T) {
	e := newEnv(t, Limits{})
	first := e.mustStart("req-1", "dir\n")
	e.proc()
	replay := e.mustStart("req-1", "dir\n")
	if replay.CommandID != first.CommandID {
		t.Fatal("replay created another execution")
	}
	changed := e.startReq("req-1", "del x\n")
	if _, err := e.session.Start(changed); !errors.Is(err, ErrRequestMismatch) {
		t.Fatalf("altered command = %v", err)
	}
	changed = e.startReq("req-1", "dir\n")
	changed.TimeoutMillis++
	if _, err := e.session.Start(changed); !errors.Is(err, ErrRequestMismatch) {
		t.Fatalf("altered timeout = %v", err)
	}
	if _, err := e.session.Start(e.startReq("req-2", "dir\n")); !errors.Is(err, ErrBusy) {
		t.Fatalf("second active command = %v", err)
	}
	e.session.Cancel(e.ref(first.CommandID))
	e.await(first.CommandID)
	late := e.mustStart("req-1", "dir\n")
	if late.CommandID != first.CommandID || late.Phase != PhaseCanceled || !late.Terminal {
		t.Fatalf("terminal replay = %+v", late)
	}
	if n := e.runtime.startCount(); n != 1 {
		t.Fatalf("runtime starts = %d", n)
	}
}

// Request IDs are scoped to one grant: a later grant neither replays nor
// recognizes them, so adapters must surface an uncertain Start instead of
// resending it under a new approval.
func TestRequestIDIsScopedToOneGrant(t *testing.T) {
	old := newEnv(t, Limits{})
	first := old.mustStart("req-1", "dir\n")
	old.proc()
	old.session.TransportLost()
	old.await(first.CommandID)
	fresh := newEnv(t, Limits{})
	if _, err := fresh.session.Start(old.startReq("req-1", "dir\n")); !errors.Is(err, ErrBinding) {
		t.Fatalf("old binding on new grant = %v", err)
	}
	if _, err := fresh.session.Status(fresh.ref(first.CommandID)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old command visible in new grant = %v", err)
	}
}

func TestUncertainStartIsRetainedAndNeverRepeated(t *testing.T) {
	e := newEnv(t, Limits{})
	e.runtime.start = func(context.Context) (GuestProcess, error) { return nil, errors.New("pipe broke after create") }
	st := e.mustStart("req-1", "dir\n")
	final := e.await(st.CommandID)
	if final.Phase != PhaseStartUnknown || final.Cleanup != CleanupUnknown {
		t.Fatalf("final = %+v", final)
	}
	if replay := e.mustStart("req-1", "dir\n"); replay.CommandID != st.CommandID {
		t.Fatal("replay after uncertain start created another execution")
	}
	if _, err := e.session.Start(e.startReq("req-2", "dir\n")); !errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("new start = %v", err)
	}
	if _, err := e.session.Cleanup(finite(t), e.ref(st.CommandID)); !errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("Cleanup without a retained process = %v", err)
	}
	if n := e.runtime.startCount(); n != 1 {
		t.Fatalf("runtime starts = %d", n)
	}
}

func TestProvenNotStartedNeedsNoCleanupAndCommandLimitHolds(t *testing.T) {
	e := newEnv(t, Limits{MaxCommands: 1})
	e.runtime.start = func(context.Context) (GuestProcess, error) { return nil, ErrNotStarted }
	st := e.mustStart("req-1", "dir\n")
	final := e.await(st.CommandID)
	if final.Phase != PhaseFailed || final.Cleanup != CleanupNotRequired {
		t.Fatalf("final = %+v", final)
	}
	if _, err := e.session.Start(e.startReq("req-2", "dir\n")); !errors.Is(err, ErrExhausted) {
		t.Fatalf("limit = %v", err)
	}
}

func TestCommandIDCollisionIsRejected(t *testing.T) {
	e := newEnv(t, Limits{})
	e.session.random = bytes.NewReader(make([]byte, 64)) // every read yields the same ID
	e.runtime.start = func(context.Context) (GuestProcess, error) { return nil, ErrNotStarted }
	st := e.mustStart("req-1", "dir\n")
	e.await(st.CommandID)
	if _, err := e.session.Start(e.startReq("req-2", "dir\n")); !errors.Is(err, ErrIdentifier) {
		t.Fatalf("colliding ID = %v", err)
	}
	if n := e.runtime.startCount(); n != 1 {
		t.Fatalf("runtime starts = %d", n)
	}
}

func TestCancelBeforeRuntimeCallSkipsRuntime(t *testing.T) {
	e := newEnv(t, Limits{})
	entered, release := make(chan struct{}), make(chan struct{})
	e.session.beforeStart = func() {
		close(entered)
		<-release
	}
	st := e.mustStart("req-1", "dir\n")
	<-entered
	canceled, err := e.session.Cancel(e.ref(st.CommandID))
	if err != nil || canceled.Phase != PhaseStarting || canceled.Stop != StopCanceled || canceled.Terminal {
		t.Fatalf("Cancel before runtime = %+v, %v", canceled, err)
	}
	close(release)
	final := e.await(st.CommandID)
	if final.Phase != PhaseCanceled || final.Cleanup != CleanupNotRequired || final.Stop != StopCanceled {
		t.Fatalf("final = %+v", final)
	}
	if n := e.runtime.startCount(); n != 0 {
		t.Fatalf("runtime called after cancel: %d", n)
	}
	if !e.issuer.Snapshot().HasAuthority {
		t.Fatal("command cancel revoked the grant")
	}
}

func TestExternalRevokeBeforeRuntimeCallSkipsRuntime(t *testing.T) {
	e := newEnv(t, Limits{})
	e.session.beforeStart = func() { _ = e.issuer.Revoke(e.session.Binding()) }
	st := e.mustStart("req-1", "dir\n")
	final := e.await(st.CommandID)
	if final.Phase != PhaseCanceled || final.Cleanup != CleanupNotRequired || final.Stop != StopRevoked {
		t.Fatalf("final = %+v", final)
	}
	if n := e.runtime.startCount(); n != 0 {
		t.Fatalf("runtime called after revoke: %d", n)
	}
}

func TestCancelDuringStartKeepsLateProcessForCleanup(t *testing.T) {
	e := newEnv(t, Limits{})
	entered, gate := make(chan struct{}), make(chan struct{})
	late := newFakeProc()
	e.runtime.start = func(context.Context) (GuestProcess, error) {
		close(entered)
		<-gate // models a native call that ignores cancellation
		return late, nil
	}
	st := e.mustStart("req-1", "dir\n")
	<-entered
	canceled, err := e.session.Cancel(e.ref(st.CommandID))
	if err != nil || canceled.Phase != PhaseStarting || canceled.Stop != StopCanceled || canceled.Terminal {
		t.Fatalf("Cancel while starting = %+v, %v", canceled, err)
	}
	if _, err := e.session.Start(e.startReq("req-2", "dir\n")); !errors.Is(err, ErrBusy) {
		t.Fatalf("Start while late launch is pending = %v", err)
	}
	close(gate)
	final := e.await(st.CommandID)
	if final.Phase != PhaseCanceled || final.Cleanup != CleanupConfirmed || final.Exit.Known {
		t.Fatalf("final = %+v", final)
	}
	if terminates, releases := late.counts(); terminates < 1 || releases != 1 {
		t.Fatalf("late process: terminate=%d release=%d", terminates, releases)
	}
}

func TestPartialStartIsCleanedBySameOwner(t *testing.T) {
	e := newEnv(t, Limits{})
	partial := newFakeProc()
	e.runtime.start = func(context.Context) (GuestProcess, error) { return partial, errors.New("job assignment failed") }
	st := e.mustStart("req-1", "dir\n")
	final := e.await(st.CommandID)
	if final.Phase != PhaseFailed || final.Stop != StopRuntimeFault || final.Cleanup != CleanupConfirmed || final.Exit.Known {
		t.Fatalf("final = %+v", final)
	}
	if terminates, releases := partial.counts(); terminates != 1 || releases != 1 {
		t.Fatalf("partial process: terminate=%d release=%d", terminates, releases)
	}
}

func TestBackpressureResumesAfterAcknowledgementAndTruncatesAtCap(t *testing.T) {
	e := newEnv(t, Limits{MaxRetained: recordOverhead + 8, MaxTotalOutput: 20})
	st := e.mustStart("req-1", "type big.txt\n")
	p := e.proc()
	send(t, p, Stdout, "aaaaaaaa")
	send(t, p, Stdout, "bbbbbbbb") // received by the worker, which cannot retain it yet
	// Deterministic part: the received bytes never get offsets before an ack.
	// The timed send below is only supplementary evidence that reads paused.
	select {
	case p.events <- fakeEvent{stream: Stderr, data: []byte("cccccccc")}:
		t.Fatal("pump kept reading while the retained window was full")
	case <-time.After(50 * time.Millisecond):
	}
	if st, _ := e.session.Status(e.ref(st.CommandID)); st.OutputEnd != 8 {
		t.Fatalf("window overflowed without acknowledgement: %+v", st)
	}
	first := e.readUntil(st.CommandID, 0, 8)
	if len(first) != 1 || string(first[0].Data) != "aaaaaaaa" {
		t.Fatalf("first window = %+v", first)
	}
	second := e.readUntil(st.CommandID, 8, 16)
	if len(second) != 1 || string(second[0].Data) != "bbbbbbbb" || second[0].Offset != 8 {
		t.Fatalf("second window = %+v", second)
	}
	send(t, p, Stderr, "cccccccc")
	third := e.readUntil(st.CommandID, 16, 20)
	if len(third) != 1 || string(third[0].Data) != "cccc" || third[0].Stream != Stderr {
		t.Fatalf("third window = %+v", third)
	}
	close(p.events)
	close(p.exited)
	final := e.await(st.CommandID)
	if final.Phase != PhaseExited || !final.Truncated || final.Dropped != 4 || final.OutputEnd != 20 {
		t.Fatalf("final = %+v", final)
	}
	out, err := e.session.Output(OutputRequest{Version: Version, Binding: e.session.Binding(), CommandID: st.CommandID, Cursor: 20, MaxBytes: 8})
	if err != nil || !out.Complete || !out.Truncated || out.Dropped != 4 {
		t.Fatalf("terminal output = %+v, %v", out, err)
	}
	if _, err := e.session.Output(OutputRequest{Version: Version, Binding: e.session.Binding(), CommandID: st.CommandID, Cursor: 21, MaxBytes: 8}); !errors.Is(err, ErrCursor) {
		t.Fatalf("cursor beyond end = %v", err)
	}
	if _, err := e.session.Output(OutputRequest{Version: Version, Binding: e.session.Binding(), CommandID: st.CommandID, Cursor: 20}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero MaxBytes = %v", err)
	}
}

func TestHardDeadlineRevokesGrantBeforeStoppingBackpressuredWorker(t *testing.T) {
	e := newEnv(t, Limits{MaxRetained: recordOverhead + 8})
	if d := e.timers.timer(t, 0).d; d != DeadlineRecheck {
		t.Fatalf("deadline timer = %v, want the bounded recheck", d)
	}
	st := e.mustStart("req-1", "type big.txt\n")
	p := e.proc()
	observed := make(chan supervisor.State, 4)
	p.setTerminateHook(func() { observed <- e.authority.Snapshot(e.clock.Now()).State })
	send(t, p, Stdout, "aaaaaaaa")
	send(t, p, Stdout, "bbbbbbbb") // worker is now held by the full window
	e.clock.Advance(time.Hour)
	e.timers.fire(t, 0)
	final := e.await(st.CommandID)
	if final.Phase != PhaseCanceled || final.Stop != StopHardDeadline || final.Cleanup != CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	if state := <-observed; state != supervisor.Revoked {
		t.Fatalf("guest terminated while grant was %s", state)
	}
	if e.issuer.Snapshot().HasAuthority {
		t.Fatal("issuer still reports authority")
	}
	if _, err := e.session.Output(OutputRequest{Version: Version, Binding: e.session.Binding(), CommandID: st.CommandID, MaxBytes: 8}); !errors.Is(err, ErrDenied) {
		t.Fatalf("Output after deadline = %v", err)
	}
	if _, err := e.session.Start(e.startReq("req-2", "dir\n")); !errors.Is(err, ErrDenied) {
		t.Fatalf("Start after deadline = %v", err)
	}
}

func TestObservedExpiryDeniesAndRevokesBeforeTermination(t *testing.T) {
	e := newEnv(t, Limits{})
	st := e.mustStart("req-1", "dir\n")
	p := e.proc()
	observed := make(chan supervisor.State, 4)
	p.setTerminateHook(func() { observed <- e.authority.Snapshot(e.clock.Now()).State })
	e.clock.Advance(2 * time.Hour) // timer not fired: the next observation must enforce
	if _, err := e.session.Output(OutputRequest{Version: Version, Binding: e.session.Binding(), CommandID: st.CommandID, MaxBytes: 8}); !errors.Is(err, ErrDenied) {
		t.Fatalf("Output after expiry = %v", err)
	}
	final := e.await(st.CommandID)
	if final.Stop != StopHardDeadline || final.Phase != PhaseCanceled || final.Cleanup != CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	if state := <-observed; state != supervisor.Revoked {
		t.Fatalf("guest terminated while grant was %s", state)
	}
	if n := e.runtime.startCount(); n != 1 {
		t.Fatalf("runtime starts = %d", n)
	}
}

func TestExternalIssuerRevokeStopsRunningCommandWithoutRequests(t *testing.T) {
	e := newEnv(t, Limits{})
	st := e.mustStart("req-1", "sleep\n")
	p := e.proc()
	observed := make(chan supervisor.State, 4)
	p.setTerminateHook(func() { observed <- e.authority.Snapshot(e.clock.Now()).State })
	if err := e.issuer.Revoke(e.session.Binding()); err != nil {
		t.Fatal(err)
	}
	p.awaitKilled(t) // only the Authority notification can reach the worker here
	if state := <-observed; state != supervisor.Revoked {
		t.Fatalf("guest terminated while grant was %s", state)
	}
	final := e.await(st.CommandID)
	if final.Phase != PhaseCanceled || final.Stop != StopRevoked || final.Cleanup != CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	if _, err := e.session.Start(e.startReq("req-2", "dir\n")); !errors.Is(err, ErrDenied) {
		t.Fatalf("Start after external revoke = %v", err)
	}
}

func TestStatusObservesExternalRevokeImmediately(t *testing.T) {
	e := newEnv(t, Limits{})
	st := e.mustStart("req-1", "sleep\n")
	e.proc()
	if err := e.issuer.Revoke(e.session.Binding()); err != nil {
		t.Fatal(err)
	}
	got, err := e.session.Status(e.ref(st.CommandID))
	if err != nil || got.Stop != StopRevoked {
		t.Fatalf("Status after external revoke = %+v, %v", got, err)
	}
	if got, err := e.session.Cancel(e.ref(st.CommandID)); err != nil || got.Stop != StopRevoked {
		t.Fatalf("Cancel keeps the first stop reason: %+v, %v", got, err)
	}
}

func TestOwnerStopStopsRunningCommand(t *testing.T) {
	e := newEnv(t, Limits{})
	_, owner, err := serviceauthowner.New(e.issuer, &sync.Mutex{})
	if err != nil {
		t.Fatal(err)
	}
	st := e.mustStart("req-1", "sleep\n")
	p := e.proc()
	observed := make(chan supervisor.State, 4)
	p.setTerminateHook(func() { observed <- e.authority.Snapshot(e.clock.Now()).State })
	if err := owner.Revoke(); err != nil {
		t.Fatal(err)
	}
	p.awaitKilled(t)
	if state := <-observed; state != supervisor.Revoked {
		t.Fatalf("guest terminated while grant was %s", state)
	}
	if final := e.await(st.CommandID); final.Stop != StopRevoked || final.Cleanup != CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	if !e.issuer.Snapshot().Closed {
		t.Fatal("owner stop did not close the issuer")
	}
}

func TestTransportLossRevokesWithoutReconnect(t *testing.T) {
	e := newEnv(t, Limits{})
	st := e.mustStart("req-1", "dir\n")
	p := e.proc()
	observed := make(chan supervisor.State, 4)
	p.setTerminateHook(func() { observed <- e.authority.Snapshot(e.clock.Now()).State })
	e.session.TransportLost()
	final := e.await(st.CommandID)
	if final.Stop != StopTransportLost || final.Phase != PhaseCanceled || final.Cleanup != CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	if state := <-observed; state != supervisor.Revoked {
		t.Fatalf("guest terminated while grant was %s", state)
	}
	if _, err := e.session.Start(e.startReq("req-2", "dir\n")); !errors.Is(err, ErrDenied) {
		t.Fatalf("Start after transport loss = %v", err)
	}
	if _, err := e.session.Output(OutputRequest{Version: Version, Binding: e.session.Binding(), CommandID: st.CommandID, MaxBytes: 8}); !errors.Is(err, ErrDenied) {
		t.Fatalf("Output after transport loss = %v", err)
	}
	if err := e.session.Close(finite(t)); err != nil {
		t.Fatalf("Close = %v", err)
	}
}

func TestCommandTimeoutStopsOnlyThatCommand(t *testing.T) {
	e := newEnv(t, Limits{})
	st := e.mustStart("req-1", "dir\n") // 60s, well before the 1h grant deadline
	e.proc()
	if d := e.timers.timer(t, 1).d; d != time.Minute {
		t.Fatalf("command timer = %v", d)
	}
	e.timers.fire(t, 1)
	final := e.await(st.CommandID)
	if final.Stop != StopCommandTimeout || final.Phase != PhaseCanceled || final.Cleanup != CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	if !e.issuer.Snapshot().HasAuthority {
		t.Fatal("command timeout revoked the grant")
	}
}

// A command timeout reaching the grant deadline is the grant deadline. Firing
// that timer first (as a real scheduler may) must revoke the grant before the
// guest stops and report StopHardDeadline, not a command timeout.
func TestCommandDeadlineCappedByGrantIsHardDeadline(t *testing.T) {
	e := newEnv(t, Limits{MaxTimeout: uint32((2 * time.Hour).Milliseconds())})
	r := e.startReq("req-1", "dir\n")
	r.TimeoutMillis = uint32((2 * time.Hour).Milliseconds())
	st, err := e.session.Start(r)
	if err != nil {
		t.Fatal(err)
	}
	e.track(st)
	p := e.proc()
	observed := make(chan supervisor.State, 4)
	p.setTerminateHook(func() { observed <- e.authority.Snapshot(e.clock.Now()).State })
	if d := e.timers.timer(t, 1).d; d != time.Hour {
		t.Fatalf("command timer = %v, want capped to grant deadline", d)
	}
	e.clock.Advance(time.Hour)
	e.timers.fire(t, 1) // the command timer wins the race with the Session timer
	final := e.await(st.CommandID)
	if final.Stop != StopHardDeadline || final.Phase != PhaseCanceled || final.Cleanup != CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	if state := <-observed; state != supervisor.Revoked {
		t.Fatalf("guest terminated while grant was %s", state)
	}
	if e.issuer.Snapshot().HasAuthority {
		t.Fatal("capped deadline left the grant live")
	}
}

// timerCount is the number of timers scheduled so far.
func (ft *fakeTimers) timerCount() int {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return len(ft.list)
}

// A deadline callback that runs before both the wall deadline and the elapsed
// budget have passed re-arms the single owner instead of revoking. The capped
// command timer firing early likewise leaves the grant and command alone.
func TestDeadlineCallbackBeforeTheDeadlineDoesNotRevoke(t *testing.T) {
	e := newEnv(t, Limits{MaxTimeout: uint32((2 * time.Hour).Milliseconds())})
	r := e.startReq("req-1", "dir\n")
	r.TimeoutMillis = uint32((2 * time.Hour).Milliseconds())
	st, err := e.session.Start(r)
	if err != nil {
		t.Fatal(err)
	}
	e.track(st)
	e.proc()
	for _, i := range []int{0, 1} { // the recheck timer, then the capped command timer
		e.timers.fire(t, i)
		if !e.issuer.Snapshot().HasAuthority {
			t.Fatalf("timer %d revoked a live grant before its deadline", i)
		}
	}
	if n := e.timers.timerCount(); n != 4 {
		t.Fatalf("timers scheduled = %d, want one re-arm per early callback", n)
	}
	// Timer 1 is the command timer; 0 and 2 were the owner's earlier timers.
	for _, i := range []int{0, 2} {
		if !e.timers.timer(t, i).stopped {
			t.Fatalf("deadline timer %d was not stopped when the owner re-armed", i)
		}
	}
	if d := e.timers.timer(t, 3).d; d != DeadlineRecheck {
		t.Fatalf("re-armed wait = %v", d)
	}
	if got, _ := e.session.Status(e.ref(st.CommandID)); got.Terminal || got.Stop != StopNone {
		t.Fatalf("early callback stopped the command: %+v", got)
	}
}

// A forward wall-clock jump (or a sleep the monotonic clock did not count)
// with no requests at all ends the grant at the next bounded recheck: the
// primary wait was never more than DeadlineRecheck, and the grant is revoked
// before the guest is stopped.
func TestForwardWallJumpWithoutRequestsEndsAtTheNextRecheck(t *testing.T) {
	e := newEnv(t, Limits{})
	st := e.mustStart("req-1", "dir\n")
	p := e.proc()
	observed := make(chan supervisor.State, 4)
	p.setTerminateHook(func() { observed <- e.authority.Snapshot(e.clock.Now()).State })
	if d := e.timers.timer(t, 0).d; d > DeadlineRecheck {
		t.Fatalf("primary wait %v exceeds the recheck bound", d)
	}
	e.clock.Advance(2 * time.Hour)
	e.timers.fire(t, 0)
	final := e.await(st.CommandID)
	if final.Stop != StopHardDeadline || final.Cleanup != CleanupConfirmed {
		t.Fatalf("final = %+v", final)
	}
	if state := <-observed; state != supervisor.Revoked {
		t.Fatalf("guest terminated while grant was %s", state)
	}
	if n := e.timers.timerCount(); n != 2 {
		t.Fatalf("a revoked grant re-armed its deadline: %d timers", n)
	}
}

// The wall time left at New is also an elapsed budget. If the wall clock lags
// (for example it was set back by less than an observable regression), the
// budget still ends the grant; nothing extends it.
func TestElapsedBudgetEndsTheGrantWhenTheWallClockLags(t *testing.T) {
	e := newEnv(t, Limits{})
	e.session.mu.Lock()
	e.session.elapsed = func() time.Duration { return time.Hour }
	e.session.mu.Unlock()
	e.timers.fire(t, 0)
	if e.issuer.Snapshot().HasAuthority {
		t.Fatal("an exhausted elapsed budget left the grant live")
	}
	if _, err := e.session.Start(e.startReq("req-1", "dir\n")); !errors.Is(err, ErrDenied) {
		t.Fatalf("Start after the budget = %v", err)
	}
}

// A backward or unavailable wall clock is a revocation through the canonical
// issuer, never a longer wait.
func TestBackwardClockRevokesInsteadOfExtending(t *testing.T) {
	e := newEnv(t, Limits{})
	e.clock.Advance(-time.Minute)
	e.timers.fire(t, 0)
	if e.issuer.Snapshot().HasAuthority {
		t.Fatal("a regressed clock left the grant live")
	}
	if _, r := e.authority.Status(); r != supervisor.ClockRegressed {
		t.Fatalf("authority reason = %v", r)
	}
	if n := e.timers.timerCount(); n != 1 {
		t.Fatalf("a regressed clock re-armed the deadline: %d timers", n)
	}
}

func TestElapsedCapRefusesStartEvenWhenTimerCallbackIsDelayed(t *testing.T) {
	e := newEnv(t, Limits{})
	e.session.mu.Lock()
	e.session.elapsed = func() time.Duration { return e.session.budget }
	e.session.mu.Unlock()
	if _, err := e.session.Start(e.startReq("late", "echo fake")); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	e.runtime.mu.Lock()
	started := len(e.runtime.stdin)
	e.runtime.mu.Unlock()
	if started != 0 || e.issuer.Snapshot().HasAuthority {
		t.Fatal("elapsed cap admitted a start")
	}
}
