package macagent

import (
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"os"
	"sync"
	"testing"
	"time"
)

func preKillFixture(t *testing.T, state groupObservation) (*process, *observedFakeOS) {
	p, f := observedProcess(t, state, nil, nil)
	<-p.ioDone
	f.set(func(o *fakeOS) { o.exited = false; o.exitOnTerm = true })
	return p, f
}
func countEntry(f *observedFakeOS, want string) int {
	n := 0
	for _, s := range f.entries() {
		if s == want {
			n++
		}
	}
	return n
}
func TestPreKillKnownTermExitAnchorOnly(t *testing.T) {
	p, f := preKillFixture(t, groupAnchorOnly)
	if err := p.Terminate(finite(t)); err != nil {
		t.Fatal(err)
	}
	if p.natural || !p.signaled || !p.reaped || p.killAttempted {
		t.Fatal("cancellation reclassified or KILL attempted")
	}
	if countEntry(f, "TERM") != 1 || countEntry(f, "KILL") != 0 || f.observations.Load() != 1 || !before(f.entries(), "snapshot", "reap") {
		t.Fatal(f.entries())
	}
	if _, err := p.Wait(finite(t)); !errors.Is(err, errForcedExit) {
		t.Fatal("forced exit became natural", err)
	}
	if err := p.Release(finite(t)); err != nil {
		t.Fatal(err)
	}
}
func TestPreKillNegativeSnapshotStickyNoFurtherSignal(t *testing.T) {
	for _, state := range []groupObservation{groupPermission, groupQueryFailed, groupTruncated, groupMissingAnchor, groupInvalidList, groupChanged, groupRace, groupLive, groupMultiple, groupInvalidAnchor} {
		t.Run(string(state), func(t *testing.T) {
			p, f := preKillFixture(t, state)
			for i := 0; i < 2; i++ {
				if !errors.Is(p.Terminate(finite(t)), errGroupObservation) {
					t.Fatal("negative rescued")
				}
			}
			if f.observations.Load() != 1 || countEntry(f, "KILL") != 0 || countEntry(f, "TERM") != 1 || p.reaped || p.Release(finite(t)) == nil {
				t.Fatal(f.entries())
			}
		})
	}
}
func TestPreKillLiveUsesExistingKillWithoutQuery(t *testing.T) {
	p, f := preKillFixture(t, groupAnchorOnly)
	f.set(func(o *fakeOS) { o.exitOnTerm = false })
	if err := p.Terminate(finite(t)); err != nil {
		t.Fatal(err)
	}
	if f.observations.Load() != 0 || countEntry(f, "KILL") != 1 || !p.killAttempted {
		t.Fatal(f.entries())
	}
	p.signal(sigKill)
	if countEntry(f, "KILL") != 1 {
		t.Fatal("duplicate kill")
	}
}
func TestPreKillExitObservationErrorsAreSticky(t *testing.T) {
	for _, kind := range []string{"permission", "other", "unknown-status", "lost"} {
		t.Run(kind, func(t *testing.T) {
			p, f := preKillFixture(t, groupAnchorOnly)
			switch kind {
			case "permission":
				f.observerErr = os.ErrPermission
			case "other":
				f.observerErr = errFake
			case "unknown-status":
				f.exitBeforeWatch = true
			case "lost":
				p.lost = errFake
			}
			if p.Terminate(finite(t)) == nil || p.Terminate(finite(t)) == nil {
				t.Fatal("observation failure rescued")
			}
			if f.observations.Load() != 0 || countEntry(f, "KILL") != 0 || p.reaped {
				t.Fatal(f.entries())
			}
		})
	}
}
func TestPreKillPriorSignalAndSweepCannotEnterFastPath(t *testing.T) {
	for _, kind := range []string{"term-permission", "prior-error", "prior-kill", "prior-sweep"} {
		t.Run(kind, func(t *testing.T) {
			p, f := preKillFixture(t, groupAnchorOnly)
			switch kind {
			case "term-permission":
				f.signalErr = map[signal]error{sigTerm: os.ErrPermission}
			case "prior-error":
				p.sigErr = errFake
			case "prior-kill":
				p.killAttempted = true
			case "prior-sweep":
				p.stopStarted = true
			}
			_ = p.Terminate(finite(t))
			if f.observations.Load() != 0 {
				t.Fatal("prior stop reused as fresh")
			}

			if (kind == "term-permission" || kind == "prior-error") && p.sigErr == nil {
				t.Fatal("permission evidence lost")
			}
		})
	}
}
func TestPreKillConcurrentCancelWaitReleaseOnce(t *testing.T) {
	for i := 0; i < 50; i++ {
		p, f := preKillFixture(t, groupAnchorOnly)
		waitResult := make(chan error, 1)
		go func() { _, err := p.Wait(finite(t)); waitResult <- err }()
		var wg sync.WaitGroup
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func() { defer wg.Done(); _ = p.Terminate(finite(t)); _ = p.Release(finite(t)) }()
		}
		joined := make(chan struct{})
		go func() { wg.Wait(); close(joined) }()
		select {
		case <-joined:
		case <-time.After(time.Second):
			t.Fatal("concurrent stop/release did not finish")
		}
		select {
		case err := <-waitResult:
			if !errors.Is(err, errForcedExit) {
				t.Fatal("Wait lost canceled classification", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Wait did not finish")
		}
		if countEntry(f, "TERM") != 1 || countEntry(f, "KILL") != 0 || f.observations.Load() != 1 || countEntry(f, "reap") != 1 || !p.reaped {
			t.Fatal(f.entries())
		}
		noViolation(t, f.fakeOS) // no signal after reap/identity loss
	}
}
func TestPreKillPreservesIOAndReleaseFailures(t *testing.T) {
	for _, kind := range []string{"io", "observer-close"} {
		p, f := preKillFixture(t, groupAnchorOnly)
		if kind == "io" {
			p.ioErr = errStreamRead
			p.ioStdout = ioAbnormal
		} else {
			f.observerCloseErr = errFake
		}
		err := p.Terminate(finite(t))
		if kind == "io" && err == nil {
			t.Fatal("IO failure erased")
		}
		if p.Release(finite(t)) == nil {
			t.Fatal("release failure erased")
		}
		if countEntry(f, "KILL") != 0 {
			t.Fatal("KILL despite anchor-only")
		}
	}
}

func TestPreKillSnapshotPreservesAgentexecCanceledResult(t *testing.T) {
	h := newHarness(t, time.Hour, agentexec.Limits{})
	var observed *observedFakeOS
	h.spawner.configure = func(f *fakeOS) { f.exitOnTerm = true }
	h.spawner.result = func(sp spawned) (spawned, error) {
		observed = &observedFakeOS{fakeOS: sp.proc.(*fakeOS), observation: groupAnchorOnly}
		sp.proc = observed
		return sp, nil
	}
	st := h.start("cancel-prekill", "fixed")
	c := h.spawner.child(t)
	<-c.stdinData
	h.session.Cancel(h.ref(st.CommandID))
	final := h.await(st.CommandID)
	if final.Phase != agentexec.PhaseCanceled || final.Stop != agentexec.StopCanceled || final.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("%+v", final)
	}
	if observed.observations.Load() != 1 || countEntry(observed, "KILL") != 0 {
		t.Fatal(observed.entries())
	}
}

func TestPreKillPriorSuccessfulKillRetainsExplicitCleanupRetry(t *testing.T) {
	p, f := preKillFixture(t, groupAnchorOnly)
	f.set(func(o *fakeOS) { o.exitOnTerm = false; o.ignoreKill = true })
	if p.Terminate(finite(t)) == nil {
		t.Fatal("live accepted")
	}
	if !p.killAttempted || p.reaped || p.sigErr != nil {
		t.Fatal("fixture")
	}
	f.set(func(o *fakeOS) { o.ignoreKill = false; o.exitOnTerm = true })
	if err := p.Terminate(finite(t)); err != nil {
		t.Fatal(err)
	}
	if f.observations.Load() != 0 || countEntry(f, "KILL") != 2 || !p.reaped {
		t.Fatal("prior KILL reused fastpath or explicit retry lost", f.entries())
	}
	noViolation(t, f.fakeOS)
}
