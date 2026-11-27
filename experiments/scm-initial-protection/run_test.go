package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunCLIRefusesDefaultAndConfigurableModes(t *testing.T) {
	for _, args := range [][]string{nil, {"install"}, {"--service", "extra"}, {"--service=other"}} {
		called := false
		if got := RunCLI(args, func() int { called = true; return 0 }); got != 2 || called {
			t.Fatalf("unapproved mode entered service: exit=%d called=%v", got, called)
		}
	}
}

type fakeCollector struct {
	phases        []string
	closed        bool
	capture       func(context.Context, string) (Snapshot, []Failure)
	closeFailures []Failure
}

func (c *fakeCollector) Capture(ctx context.Context, phase string) (Snapshot, []Failure) {
	c.phases = append(c.phases, phase)
	if c.capture != nil {
		return c.capture(ctx, phase)
	}
	return Snapshot{Phase: phase}, nil
}
func (c *fakeCollector) Close() []Failure { c.closed = true; return c.closeFailures }

type fakeWriter struct {
	data     []byte
	failures []Failure
	write    func()
}

func (w *fakeWriter) WriteReport(data []byte) []Failure {
	w.data = append([]byte(nil), data...)
	if w.write != nil {
		w.write()
	}
	return w.failures
}

func TestRunCapturesTwoSamplesAndClosesBeforeReport(t *testing.T) {
	c, w := &fakeCollector{}, &fakeWriter{}
	var states []string
	r := Run(context.Background(), c, w, func(s string) { states = append(states, s) })
	if !reflect.DeepEqual(states, []string{"start_pending", "running", "stop_pending"}) || len(c.phases) != 2 || !c.closed {
		t.Fatalf("incomplete lifetime: states=%v captures=%d closed=%v", states, len(c.phases), c.closed)
	}
	var saved Report
	if err := json.Unmarshal(w.data, &saved); err != nil {
		t.Fatal(err)
	}
	if r.CaptureStatus != "complete" || saved.ProductVerdict != "NOT_EVALUATED" || saved.InitialProtection != "UNKNOWN" || len(saved.Snapshots) != 2 {
		t.Fatalf("observation was missing or promoted to protection proof: %#v", saved)
	}
}

func TestRunPreservesPartialCaptureAndCloseFailures(t *testing.T) {
	c := &fakeCollector{capture: func(context.Context, string) (Snapshot, []Failure) {
		return Snapshot{ProcessID: 42}, []Failure{{Stage: "token_groups", Win32: 5}}
	}, closeFailures: []Failure{{Stage: "close_process", Win32: 6}}}
	w := &fakeWriter{}
	r := Run(context.Background(), c, w, func(string) {})
	if r.CaptureStatus != "failed" || len(r.Failures) != 2 || len(c.phases) != 1 || !c.closed || len(r.Snapshots) != 1 || r.Snapshots[0].ProcessID != 42 {
		t.Fatalf("partial failure lost: status=%s failures=%v captures=%d closed=%v", r.CaptureStatus, r.Failures, len(c.phases), c.closed)
	}
}

func TestRunCancellationJoinsOwnedCaptureBeforeClosingAndWriting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	c := &fakeCollector{capture: func(ctx context.Context, _ string) (Snapshot, []Failure) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return Snapshot{}, nil
	}}
	w := &fakeWriter{}
	done := make(chan Report, 1)
	go func() { done <- Run(ctx, c, w, func(string) {}) }()
	<-entered
	cancel()
	<-canceled
	select {
	case <-done:
		t.Fatal("runner returned before collector joined")
	default:
	}
	close(release)
	select {
	case r := <-done:
		if r.CaptureStatus != "failed" || len(c.phases) != 1 || !c.closed || len(w.data) == 0 {
			t.Fatalf("cancellation lost: status=%s captures=%d closed=%v", r.CaptureStatus, len(c.phases), c.closed)
		}
	case <-time.After(time.Second):
		t.Fatal("collector was not joined")
	}
}

func TestRunExpiredContextRefusesCapture(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	c, w := &fakeCollector{}, &fakeWriter{}
	r := Run(ctx, c, w, func(string) {})
	if len(c.phases) != 0 || !c.closed || r.CaptureStatus != "failed" || len(r.Failures) != 1 || r.Failures[0].Stage != "capture_timeout" {
		t.Fatalf("expired context accepted: captures=%d status=%s failures=%v", len(c.phases), r.CaptureStatus, r.Failures)
	}
}

func TestRunWriteFailureIsCaptureFailure(t *testing.T) {
	w := &fakeWriter{failures: []Failure{{Stage: "report_close", Win32: 6}}}
	r := Run(context.Background(), &fakeCollector{}, w, func(string) {})
	if r.CaptureStatus != "failed" || len(r.Failures) != 1 || r.Failures[0].Stage != "report_close" {
		t.Fatalf("write failure promoted: status=%s failures=%v", r.CaptureStatus, r.Failures)
	}
}

func TestRunOversizedReportFailsWithoutTruncationOrWrite(t *testing.T) {
	c := &fakeCollector{capture: func(context.Context, string) (Snapshot, []Failure) {
		return Snapshot{Process: Security{SDDL: strings.Repeat("x", maxReportBytes)}}, nil
	}}
	w := &fakeWriter{}
	r := Run(context.Background(), c, w, func(string) {})
	if r.CaptureStatus != "failed" || len(w.data) != 0 || len(r.Failures) != 1 || r.Failures[0].Stage != "report_bound" {
		t.Fatalf("oversized report accepted: status=%s bytes=%d", r.CaptureStatus, len(w.data))
	}
}

func TestRunFailedFirstCaptureStillExposesDiagnosticRunning(t *testing.T) {
	c := &fakeCollector{capture: func(context.Context, string) (Snapshot, []Failure) {
		return Snapshot{}, []Failure{{Stage: "open_self_process", Win32: 5}}
	}}
	var states []string
	r := Run(context.Background(), c, &fakeWriter{}, func(s string) { states = append(states, s) })
	if !reflect.DeepEqual(states, []string{"start_pending", "running", "stop_pending"}) || r.CaptureStatus != "failed" || len(c.phases) != 1 {
		t.Fatalf("failed first observation lost control visibility: states=%v status=%s captures=%d", states, r.CaptureStatus, len(c.phases))
	}
}

func TestRunCancellationDuringWriteCannotComplete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := Run(ctx, &fakeCollector{}, &fakeWriter{write: cancel}, func(string) {})
	if r.CaptureStatus != "failed" || len(r.Failures) != 1 || r.Failures[0].Stage != "capture_canceled" {
		t.Fatalf("late cancellation promoted: status=%s failures=%v", r.CaptureStatus, r.Failures)
	}
}
