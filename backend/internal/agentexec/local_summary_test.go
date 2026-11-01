package agentexec

import (
	"sync"
	"testing"
	"time"
)

func TestLocalSummaryPureAndBounded(t *testing.T) {
	s := &Session{deadline: time.Unix(100, 0), order: []*command{{phase: PhaseRunning, cleanup: CleanupPending}}}
	v, ok := s.LocalSummary()
	if !ok || v.Running != 1 || v.Cleanup != CleanupPending || s.revoked {
		t.Fatal(v, ok)
	}
	s.mu.Lock()
	if _, ok = s.LocalSummary(); ok {
		t.Fatal("contended snapshot")
	}
	s.mu.Unlock()
	s.order[0].stop = StopCanceled
	v, _ = s.LocalSummary()
	if v.Stopping != 1 || v.Running != 0 {
		t.Fatal(v)
	}
	s.order[0].terminal = true
	s.order[0].cleanup = CleanupUnknown
	v, _ = s.LocalSummary()
	if v.Cleanup != CleanupUnknown || v.Stopping != 0 {
		t.Fatal(v)
	}
	if s.revoked {
		t.Fatal("observation changed authority")
	}
}
func TestLocalSummaryConcurrentRead(t *testing.T) {
	s := &Session{order: []*command{{phase: PhaseStarting, cleanup: CleanupPending}}}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				s.LocalSummary()
			}
		}()
	}
	for j := 0; j < 500; j++ {
		s.mu.Lock()
		s.order[0].phase = PhaseRunning
		s.mu.Unlock()
	}
	wg.Wait()
}
