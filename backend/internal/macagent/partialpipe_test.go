package macagent

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"io"
	"strings"
	"testing"
	"time"
)

func TestPartialPipeCloseEvidenceRetainedByRuntime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		results []error
		retry   bool
	}{
		{"uncertain", []error{errFake, nil}, false},
		{"retryable", []error{errCloseRetry, nil}, true},
		{"confirmed", []error{nil}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, time.Hour, agentexec.Limits{})
			original := &scriptedCloser{Reader: strings.NewReader(""), results: tc.results}
			h.spawner.result = func(sp spawned) (spawned, error) {
				// Dispose only fake test streams before replacing their seam values.
				_ = sp.stdout.Close()
				_ = sp.stderr.Close()
				sp.stdout = original
				sp.stderr = nil
				return sp, nil
			}
			st := h.start("partial-pipes", "must-not-be-delivered")
			c := h.spawner.child(t)
			if sent := string(<-c.stdinData); sent != "" {
				t.Fatalf("failed plumbing sent %q", sent)
			}
			final := h.await(st.CommandID)
			if final.Phase != agentexec.PhaseFailed {
				t.Fatalf("%+v", final)
			}
			if tc.retry {
				if final.Cleanup != agentexec.CleanupConfirmed {
					t.Fatalf("%+v", final)
				}
				expected := 1
				if tc.name == "retryable" {
					expected = 2
				}
				if original.attempts() != expected {
					t.Fatalf("attempts %d", original.attempts())
				}
			} else {
				if final.Cleanup != agentexec.CleanupUnknown {
					t.Fatalf("%+v", final)
				}
				if _, err := h.session.Cleanup(finite(t), h.ref(st.CommandID)); !errors.Is(err, agentexec.ErrCleanupUnknown) {
					t.Fatal("unknown cleared", err)
				}
				if original.attempts() != 1 {
					t.Fatal("uncertain original close retried")
				}
			}
			noViolation(t, c.os)
		})
	}
}

func TestPartialPipeHelperKeepsErrorChainAndOwnership(t *testing.T) {
	out := &scriptedCloser{Reader: strings.NewReader(""), results: []error{errFake}}
	owned, err := retainPartialPipes(spawned{stdout: out})
	if !errors.Is(err, errCloseUncertain) || !errors.Is(err, errFake) {
		t.Fatal("close evidence lost", err)
	}
	if owned[0] != nil || owned[1] == nil || owned[2] != nil {
		t.Fatal("wrong stream mapping")
	}
	if !errors.Is(owned[1].close(), errFake) || out.attempts() != 1 {
		t.Fatal("uncertain close not cached")
	}
}

func TestPartialPipeReleaseWaitsForAllOriginalCloses(t *testing.T) {
	p, f := observedProcess(t, groupAnchorOnly, nil, nil)
	out := &scriptedCloser{Reader: strings.NewReader(""), results: []error{errCloseRetry, errCloseRetry, nil}}
	original, err := retainPartialPipes(spawned{stdout: out})
	if !errors.Is(err, errCloseRetry) {
		t.Fatal(err)
	}
	p.partialPipes = original
	observeNatural(t, p)
	if err := p.Terminate(finite(t)); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(p.Release(finite(t)), errCloseRetry) || p.released {
		t.Fatal("unclosed original released")
	}
	if err := p.Release(finite(t)); err != nil || !p.released {
		t.Fatal("same owner retry", err)
	}
	if out.attempts() != 3 {
		t.Fatal("wrong retry count")
	}
	assertNoSignals(t, f)
}

func TestPartialPipeUncertainObserverReleaseDoesNotRetryOriginal(t *testing.T) {
	p, f := observedProcess(t, groupAnchorOnly, nil, nil)
	out := &scriptedCloser{Reader: strings.NewReader(""), results: []error{errFake}}
	original, _ := retainPartialPipes(spawned{stdout: out})
	p.partialPipes = original
	observeNatural(t, p)
	if err := p.Terminate(finite(t)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if !errors.Is(p.Release(context.Background()), errFake) {
			t.Fatal("lost close error")
		}
	}
	if out.attempts() != 1 || p.released {
		t.Fatal("uncertain resource retried")
	}
	assertNoSignals(t, f)
}

var _ io.Closer = (*scriptedCloser)(nil)
