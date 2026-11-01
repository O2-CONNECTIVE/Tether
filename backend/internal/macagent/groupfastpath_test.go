package macagent

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
)

type observedFakeOS struct {
	*fakeOS
	observation  groupObservation
	observations atomic.Int32
	onObserve    func()
}

func (f *observedFakeOS) observeOwnedGroup() groupObservation {
	f.observations.Add(1)
	f.record("snapshot")
	if f.onObserve != nil {
		f.onObserve()
	}
	return f.observation
}

type testInput struct {
	writeErr error
	short    bool
}

func (w testInput) Write(b []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	if w.short {
		return 0, nil
	}
	return len(b), nil
}
func (testInput) Close() error { return nil }

type faultReader struct{ sent bool }

func (r *faultReader) Read(b []byte) (int, error) {
	if !r.sent {
		r.sent = true
		copy(b, "x")
		return 1, errFake
	}
	return 0, errFake
}
func (*faultReader) Close() error { return nil }

func observedProcess(t *testing.T, state groupObservation, out io.ReadCloser, in io.WriteCloser) (*process, *observedFakeOS) {
	t.Helper()
	if out == nil {
		out = io.NopCloser(strings.NewReader(""))
	}
	if in == nil {
		in = testInput{}
	}
	f := &observedFakeOS{fakeOS: &fakeOS{exited: true}, observation: state}
	p := newProcess(spawned{proc: f, stdin: in, stdout: out, stderr: io.NopCloser(strings.NewReader(""))}, fakeTiming, []byte("fixed"))
	p.diagnostics = &CleanupDiagnostics{}
	return p, f
}
func observeNatural(t *testing.T, p *process) {
	t.Helper()
	if st, err := p.Wait(finite(t)); err != nil || st.Code != 0 {
		t.Fatalf("wait: %v %v", st, err)
	}
}
func assertNoSignals(t *testing.T, f *observedFakeOS) {
	t.Helper()
	for _, e := range f.entries() {
		if e == "TERM" || e == "KILL" {
			t.Fatalf("unexpected signal %v", f.entries())
		}
	}
	noViolation(t, f.fakeOS)
}
func TestNaturalAnchorSnapshotReapIORelease(t *testing.T) {
	p, f := observedProcess(t, groupAnchorOnly, nil, nil)
	observeNatural(t, p)
	if err := p.Terminate(finite(t)); err != nil {
		t.Fatal(err)
	}
	if err := p.Release(finite(t)); err != nil {
		t.Fatal(err)
	}
	if !before(f.entries(), "snapshot", "reap") || !before(f.entries(), "reap", "observer-released") {
		t.Fatal(f.entries())
	}
	if f.observations.Load() != 1 || !p.reaped || !p.released {
		t.Fatal("not once-owned")
	}
	assertNoSignals(t, f)
}
func TestNaturalSnapshotUnknownNeverRetriedOrReaped(t *testing.T) {
	for _, state := range []groupObservation{groupPermission, groupQueryFailed, groupTruncated, groupMissingAnchor, groupInvalidList, groupChanged, groupRace, groupLive, groupMultiple, groupInvalidAnchor} {
		t.Run(string(state), func(t *testing.T) {
			p, f := observedProcess(t, state, nil, nil)
			observeNatural(t, p)
			if !errors.Is(p.Terminate(finite(t)), errGroupObservation) {
				t.Fatal("unknown accepted")
			}
			f.observation = groupAnchorOnly
			if !errors.Is(p.Terminate(finite(t)), errGroupObservation) {
				t.Fatal("unknown rescued")
			}
			if !errors.Is(p.Release(finite(t)), errNotTerminated) {
				t.Fatal("released unknown")
			}
			if f.observations.Load() != 1 || p.reaped || p.released {
				t.Fatal("ownership lost/retried")
			}
			assertNoSignals(t, f)
		})
	}
}
func TestNaturalSnapshotReapLossStaysUnknown(t *testing.T) {
	p, f := observedProcess(t, groupAnchorOnly, nil, nil)
	observeNatural(t, p)
	f.set(func(f *fakeOS) { f.reapErr = errFake })
	if p.Terminate(finite(t)) == nil {
		t.Fatal("lost reap accepted")
	}
	if p.Terminate(finite(t)) == nil || p.Release(finite(t)) == nil {
		t.Fatal("lost reap rescued")
	}
	if f.observations.Load() != 1 || p.lost == nil {
		t.Fatal("loss not retained")
	}
	assertNoSignals(t, f)
}
func TestNaturalSnapshotAndReapUnderOneOwnerLock(t *testing.T) {
	p, f := observedProcess(t, groupAnchorOnly, nil, nil)
	observeNatural(t, p)
	entered, allow := make(chan struct{}), make(chan struct{})
	f.onObserve = func() { close(entered); <-allow }
	done := make(chan error, 1)
	go func() { done <- p.Terminate(context.Background()) }()
	<-entered
	locked := make(chan struct{})
	go func() { p.mu.Lock(); close(locked); p.mu.Unlock() }()
	select {
	case <-locked:
		t.Fatal("owner lock released during snapshot")
	default:
	}
	close(allow)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-locked
	if err := p.Release(finite(t)); err != nil {
		t.Fatal(err)
	}
	assertNoSignals(t, f)
}
func TestNaturalSnapshotConcurrentWaitTerminateRelease(t *testing.T) {
	p, f := observedProcess(t, groupAnchorOnly, nil, nil)
	observeNatural(t, p)
	var wg sync.WaitGroup
	errs := make(chan error, 60)
	for i := 0; i < 20; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); _, e := p.Wait(context.Background()); errs <- e }()
		go func() { defer wg.Done(); errs <- p.Terminate(context.Background()) }()
		go func() {
			defer wg.Done()
			e := p.Release(context.Background())
			if !errors.Is(e, errNotTerminated) {
				errs <- e
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Release(finite(t)); err != nil {
		t.Fatal(err)
	}
	if f.observations.Load() != 1 {
		t.Fatal("duplicate snapshot")
	}
	assertNoSignals(t, f)
}
func TestSignalPermissionIsNeverRescuedBySnapshot(t *testing.T) {
	p, f := observedProcess(t, groupAnchorOnly, nil, nil)
	f.set(func(f *fakeOS) { f.signalErr = map[signal]error{sigTerm: os.ErrPermission, sigKill: os.ErrPermission} })
	p.signal(sigTerm) // Models an already-attempted stop, not an OS signal.
	if p.Terminate(finite(t)) == nil {
		t.Fatal("permission erased")
	}
	if p.Terminate(finite(t)) == nil {
		t.Fatal("past unknown rescued")
	}
	if f.observations.Load() != 0 {
		t.Fatal("permission fallback observation")
	}
	if p.sigErr == nil {
		t.Fatal("lost evidence")
	}
	noViolation(t, f.fakeOS)
}
func TestUnknownExitStatusDoesNotUseSnapshot(t *testing.T) {
	p, f := observedProcess(t, groupAnchorOnly, nil, nil)
	f.set(func(f *fakeOS) { f.exitBeforeWatch = true })
	// Known-exited without a known status is not a fresh anchor proof. Do not
	// convert this ambiguous observation to a live-child KILL fallback.
	if _, err := p.Wait(finite(t)); !errors.Is(err, errGroupObservation) {
		t.Fatal(err)
	}
	if !errors.Is(p.Terminate(finite(t)), errGroupObservation) {
		t.Fatal("unknown rescued")
	}
	if f.observations.Load() != 0 || countEntry(f, "KILL") != 0 || p.reaped {
		t.Fatal(f.entries())
	}
	if p.Release(finite(t)) == nil {
		t.Fatal("unknown became released")
	}
}
func TestAbnormalOutputIsNotEOFOrCleanSuccess(t *testing.T) {
	p, f := observedProcess(t, groupAnchorOnly, &faultReader{}, nil)
	stream, b, err := p.Next(finite(t))
	if err != nil || stream != agentexec.Stdout || string(b) != "x" {
		t.Fatal(stream, string(b), err)
	}
	if _, _, err = p.Next(finite(t)); !errors.Is(err, errStreamRead) || errors.Is(err, io.EOF) {
		t.Fatal("read fault hidden", err)
	}
	observeNatural(t, p)
	if !errors.Is(p.Terminate(finite(t)), errIOUnconfirmed) || p.Release(finite(t)) == nil {
		t.Fatal("read fault confirmed")
	}
	if p.ioStdout != ioAbnormal || p.ioStderr != ioNormal {
		t.Fatal("completion states")
	}
	assertNoSignals(t, f)
}
func TestAbnormalInputIsNotCleanSuccess(t *testing.T) {
	for _, in := range []testInput{{writeErr: errFake}, {short: true}, {writeErr: io.ErrClosedPipe}} {
		p, f := observedProcess(t, groupAnchorOnly, nil, in)
		<-p.ioDone // Error happens before a Terminate-local close.
		observeNatural(t, p)
		if !errors.Is(p.Terminate(finite(t)), errIOUnconfirmed) {
			t.Fatal("input fault confirmed")
		}
		if p.ioWrite != ioAbnormal {
			t.Fatal("input error misclassified")
		}
		assertNoSignals(t, f)
	}
}
func TestExpectedLocalInputCloseIsSeparateFromEOF(t *testing.T) {
	inR, inW := io.Pipe()
	p, f := observedProcess(t, groupAnchorOnly, nil, inW)
	observeNatural(t, p)
	if err := p.Terminate(finite(t)); err != nil {
		t.Fatal(err)
	}
	if p.ioWrite != ioExpectedLocalClose {
		t.Fatalf("state %d", p.ioWrite)
	}
	if err := p.Release(finite(t)); err != nil {
		t.Fatal(err)
	}
	_ = inR.Close()
	assertNoSignals(t, f)
}
func TestSnapshotReapUnexpectedlyPendingIsSticky(t *testing.T) {
	p, f := observedProcess(t, groupAnchorOnly, nil, nil)
	observeNatural(t, p)
	f.onObserve = func() { f.set(func(f *fakeOS) { f.exited = false }) }
	if p.Terminate(finite(t)) == nil {
		t.Fatal("pending reap accepted")
	}
	f.set(func(f *fakeOS) { f.exited = true })
	if p.Terminate(finite(t)) == nil || f.observations.Load() != 1 {
		t.Fatal("pending rescued")
	}
	assertNoSignals(t, f)
}
func TestCanceledContextDoesNotBecomeSnapshotAuthority(t *testing.T) {
	p, f := observedProcess(t, groupAnchorOnly, nil, nil)
	f.set(func(f *fakeOS) { f.exited = false; f.exitOnTerm = false })
	// Context cancellation alone cannot mint an anchor. A normally live child
	// uses the original KILL path; the separate pre-KILL test supplies actual
	// fake owner exit evidence when TERM has ended the child.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = p.Terminate(ctx)
	if f.observations.Load() != 0 || countEntry(f, "KILL") != 1 {
		t.Fatal(f.entries())
	}
	noViolation(t, f.fakeOS)
	_ = p.Terminate(finite(t))
	_ = p.Release(finite(t))
}

func TestNaturalSnapshotThroughAgentexecCaller(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	var observed *observedFakeOS
	h.spawner.result = func(sp spawned) (spawned, error) {
		observed = &observedFakeOS{fakeOS: sp.proc.(*fakeOS), observation: groupAnchorOnly}
		sp.proc = observed
		return sp, nil
	}
	st := h.start("snapshot-caller", "fixed")
	c := h.spawner.child(t)
	<-c.stdinData
	c.write(t, c.stdout, "ok")
	h.readUntil(st.CommandID, 0, 2)
	c.os.exit(0)
	c.closeOutputs()
	final := h.await(st.CommandID)
	if final.Phase != agentexec.PhaseExited || final.Cleanup != agentexec.CleanupConfirmed || !final.Exit.Known {
		t.Fatalf("%+v", final)
	}
	assertNoSignals(t, observed)
	if observed.observations.Load() != 1 || !before(observed.entries(), "snapshot", "reap") {
		t.Fatal(observed.entries())
	}
}
func TestAbnormalOutputThroughAgentexecCaller(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	h.spawner.result = func(sp spawned) (spawned, error) {
		_ = sp.stdout.Close() // Fake stream only.
		sp.stdout = &faultReader{}
		return sp, nil
	}
	st := h.start("fault-caller", "fixed")
	c := h.spawner.child(t)
	<-c.stdinData
	c.closeOutputs()
	final := h.await(st.CommandID)
	if final.Phase != agentexec.PhaseFailed || final.Stop != agentexec.StopRuntimeFault || final.Cleanup != agentexec.CleanupUnknown || final.Exit.Known {
		t.Fatalf("%+v", final)
	}
}
