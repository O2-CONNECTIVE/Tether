package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryStatusSink struct {
	mu     sync.Mutex
	frames [][]byte
	closed bool
	refuse bool
}

func (s *memoryStatusSink) writeFrame(b []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuse {
		return false
	}
	s.frames = append(s.frames, append([]byte(nil), b...))
	return true
}
func (s *memoryStatusSink) close() { s.mu.Lock(); defer s.mu.Unlock(); s.closed = true }
func (s *memoryStatusSink) last(t *testing.T) statusFrame {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.frames) == 0 {
		t.Fatal("no frames")
	}
	var v statusFrame
	if json.Unmarshal(s.frames[len(s.frames)-1], &v) != nil {
		t.Fatal("invalid frame")
	}
	return v
}

type summaryOwner struct {
	v             agentexec.LocalSummary
	connected, ok bool
}

func (s summaryOwner) LocalSummary() (agentexec.LocalSummary, bool, bool) {
	return s.v, s.connected, s.ok
}
func TestStatusFrameOnlySafeAggregate(t *testing.T) {
	sink := &memoryStatusSink{}
	f := newTestStatusFeed(sink)
	f.attach(summaryOwner{agentexec.LocalSummary{Deadline: time.Now().Add(time.Minute), Running: 1, Cleanup: agentexec.CleanupPending}, true, true})
	v, ok := f.snapshot(1, 1234)
	if !ok || v.Phase != "connected" || v.Running != 1 || v.Remaining == nil || *v.Remaining <= 0 {
		t.Fatal(v)
	}
	f.unknown()
	f.update("closed", "confirmed", true)
	f.end()
	v = sink.last(t)
	if !v.Unknown || v.Cleanup != "unknown" || !v.Terminal || v.Running != 1 || v.Reason != "terminal_cleanup_unknown" {
		t.Fatal(v)
	}
	for _, b := range sink.frames {
		if len(b) > 2048 || bytes.Contains(b, []byte("command_id")) {
			t.Fatal("unsafe frame")
		}
	}
}
func TestStatusContentionNoHeartbeatAndSinkLossBounded(t *testing.T) {
	f := &statusFeed{frame: statusFrame{Version: 1}, owner: summaryOwner{ok: false}}
	if _, ok := f.snapshot(1, 1234); ok {
		t.Fatal("stale emitted")
	}
	f.mu.Lock()
	if _, ok := f.snapshot(1, 1234); ok {
		t.Fatal("lock contention emitted")
	}
	f.mu.Unlock()
	sink := &memoryStatusSink{refuse: true}
	f = newTestStatusFeed(sink)
	select {
	case <-f.done:
	case <-time.After(time.Second):
		t.Fatal("sink blocked shutdown")
	}
	f.end()
	if !sink.closed {
		t.Fatal("not closed")
	}
}
func TestStatusConcurrentTerminalUpdate(t *testing.T) {
	sink := &memoryStatusSink{}
	f := newTestStatusFeed(sink)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				f.update("connecting", "pending", false)
			}
		}()
	}
	wg.Wait()
	f.unknown()
	f.end()
	if !sink.last(t).Unknown {
		t.Fatal("unknown lost")
	}
	var prev uint64
	for _, b := range sink.frames {
		var v statusFrame
		json.Unmarshal(b, &v)
		if v.Seq <= prev {
			t.Fatal("nonincreasing")
		}
		prev = v.Seq
	}
}
func TestEntryStatusCallerWiring(t *testing.T) {
	for _, name := range []string{"clean", "close-unknown", "approval-partial", "new-client-failure", "denied", "sink-refuses"} {
		t.Run(name, func(t *testing.T) {
			r := newRecorder(t, encode(t, macDoc(t)))
			sink := &memoryStatusSink{}
			d := r.deps()
			d.statusClock = func() (uint64, bool) { return 1234, true }
			d.statusSink = func() (statusSink, error) { return sink, nil }
			switch name {
			case "close-unknown":
				r.session.closeErr = agentexec.ErrCleanupUnknown
			case "approval-partial":
				r.approval.err = errors.New("denied")
				r.owner.closeErr = agentexec.ErrCleanupUnknown
			case "new-client-failure":
				r.clientErr = errors.New("unconfigured")
				r.owner.closeErr = agentexec.ErrCleanupUnknown
			case "denied":
				r.approval.owner = nil
				r.approval.err = errors.New("denied")
			case "sink-refuses":
				sink.refuse = true
			}
			var diag bytes.Buffer
			code := execute(context.Background(), []string{"-config", configPath, "-status-stdout"}, &diag, d)
			if code != 1 {
				t.Fatal(code)
			}
			if !sink.closed {
				t.Fatal("exporter not joined")
			}
			if name == "sink-refuses" {
				if !strings.Contains(r.trace(), "client-close") {
					t.Fatal("cleanup impeded")
				}
				return
			}
			v := sink.last(t)
			if !v.Terminal {
				t.Fatal(v)
			}
			if strings.Contains(name, "unknown") || name == "approval-partial" || name == "new-client-failure" {
				if !v.Unknown || v.Cleanup != "unknown" {
					t.Fatal(v)
				}
			}
			if name == "clean" && v.Cleanup != "confirmed" {
				t.Fatal(v)
			}
			if name == "denied" && (v.Remaining != nil || v.Cleanup != "not_started") {
				t.Fatal(v)
			}
		})
	}
}
func TestEntryStatusInvalidSinkNeverReadsConfig(t *testing.T) {
	r := newRecorder(t, nil)
	d := r.deps()
	d.statusClock = func() (uint64, bool) { return 1234, true }
	d.statusSink = func() (statusSink, error) { return nil, errors.New("closed") }
	if execute(context.Background(), []string{"-config", configPath, "-status-stdout"}, nil, d) != 2 || r.trace() != "" {
		t.Fatal("unsafe input")
	}
}

type fragmentSink struct {
	calls  int
	prefix []byte
}

func (s *fragmentSink) writeFrame(b []byte) bool {
	s.calls++
	s.prefix = append(s.prefix, b[:3]...)
	return false
}
func (s *fragmentSink) close() {}
func TestPartialStatusFrameEndsStream(t *testing.T) {
	s := &fragmentSink{}
	f := newTestStatusFeed(s)
	<-f.done
	f.unknown()
	f.end()
	if s.calls != 1 || len(s.prefix) != 3 {
		t.Fatal("continued corrupt frame")
	}
}
func TestStatusRevokedDeadlineAndContention(t *testing.T) {
	f := &statusFeed{frame: statusFrame{Version: 1, Phase: "connecting", Cleanup: "pending"}, owner: summaryOwner{agentexec.LocalSummary{Deadline: time.Now().Add(time.Hour), Revoked: true, Reason: agentexec.StopHardDeadline}, true, true}}
	v, ok := f.snapshot(1, 1234)
	if !ok || v.Phase != "stopping" || v.Reason != "deadline" || v.Remaining == nil || *v.Remaining != 0 {
		t.Fatal(v)
	}
}

// This fixture exercises the production writer/encoder with a synthetic owner.
func TestStatusInteropProductionFrames(t *testing.T) {
	sink := &memoryStatusSink{}
	f := newTestStatusFeed(sink)
	f.attach(summaryOwner{agentexec.LocalSummary{Deadline: time.Now().Add(time.Minute), Running: 1, Cleanup: agentexec.CleanupPending}, true, true})
	wait := func(phase string) {
		t.Helper()
		until := time.Now().Add(time.Second)
		for {
			sink.mu.Lock()
			found := false
			for _, b := range sink.frames {
				var v statusFrame
				json.Unmarshal(b, &v)
				if v.Phase == phase {
					found = true
				}
			}
			sink.mu.Unlock()
			if found {
				return
			}
			if time.Now().After(until) {
				t.Fatal("missing phase", phase)
			}
			time.Sleep(time.Millisecond)
		}
	}
	wait("connected")
	f.update("stopping", "pending", false)
	wait("stopping")
	f.unknown()
	f.end()
	seen := map[string]bool{}
	for _, b := range sink.frames {
		var v statusFrame
		json.Unmarshal(b, &v)
		if (v.Phase == "connected" || v.Phase == "stopping" || v.Unknown) && !seen[v.Phase] {
			t.Logf("STATUS_FIXTURE %s", bytes.TrimSpace(b))
			seen[v.Phase] = true
		}
	}
}

func TestTerminalFrameSentExactlyOnceBeforeEnd(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		sink := &memoryStatusSink{}
		f := newTestStatusFeed(sink)
		if unknown {
			f.unknown()
		} else {
			f.update("closed", "confirmed", true)
		}
		select {
		case <-f.done:
		case <-time.After(time.Second):
			t.Fatal("terminal did not end stream")
		}
		f.end()
		terminals := 0
		for _, b := range sink.frames {
			var v statusFrame
			json.Unmarshal(b, &v)
			if v.Terminal {
				terminals++
			}
		}
		if terminals != 1 {
			t.Fatal("terminal count", terminals)
		}
	}
}

func newTestStatusFeed(s statusSink) *statusFeed {
	return newStatusFeedClock(s, func() (uint64, bool) { return 1234, true })
}
func TestStatusClockFailureStopsOnlyDisplay(t *testing.T) {
	s := &memoryStatusSink{}
	f := newStatusFeedClock(s, func() (uint64, bool) { return 0, false })
	<-f.done
	f.end()
	if len(s.frames) != 0 || !s.closed {
		t.Fatal("clock fallback")
	}
}
func TestStatusTimestampIsNotRefreshedBySerialization(t *testing.T) {
	f := &statusFeed{frame: statusFrame{Version: 1}}
	v, ok := f.snapshot(1, 1234)
	if !ok || v.SourceContinuousMS != 1234 {
		t.Fatal(v)
	}
}
func TestDisplayRemainingDoesNotExtend(t *testing.T) {
	now := time.Now()
	end := now.Add(time.Minute)
	if displayRemaining(end, now) != 60000 || displayRemaining(end, now.Add(2*time.Minute)) != 0 {
		t.Fatal("wrong remaining")
	}
	wallForward := now.Round(0).Add(30 * time.Second)
	if displayRemaining(end, wallForward) != 30000 {
		t.Fatal("wall forward ignored")
	}
}
