//go:build darwin && cgo && ci_actual_producer

package macagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// Pure cached-state checks: no helper, process, descriptor or socket is opened.
func TestCINativeCleanupCapture(t *testing.T) {
	p := &process{groupAttempted: true, groupState: groupMultiple, groupErr: errGroupObservation, termAttempted: true, ioWrite: ioNormal, ioStdout: ioNormal, ioStderr: ioNormal, diagnostics: &CleanupDiagnostics{}}
	p.term.Lock()
	p.ciCaptureCleanup("terminate", errors.Join(errGroupObservation, errors.New("SECRET /private/path pid=123")))
	p.term.Unlock()
	p.mu.Lock()
	p.groupState = groupAnchorOnly
	p.mu.Unlock()
	p.term.Lock()
	p.ciCaptureCleanup("terminate", errGroupObservation)
	p.term.Unlock()
	r, ok := p.ciFinalNativeRecord(strings.Repeat("a", 32), true, true)
	if !ok || r.Calls != 2 || r.FirstCall != 1 || r.First.Group != string(groupMultiple) || r.Final.Group != string(groupAnchorOnly) || r.Final.Cause != "group_observation" {
		t.Fatal("snapshot identity or immutability failed")
	}
	if _, ok = p.ciFinalNativeRecord(strings.Repeat("b", 32), true, true); ok {
		t.Fatal("record exported twice")
	}
	raw, err := json.Marshal(r)
	if err != nil || strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "123") {
		t.Fatal("raw evidence retained")
	}
	if ciNativeSinkEnabled.Load() || ciExportNativeRecord(r) {
		t.Fatal("pure tests must never enable/export to inherited descriptor")
	}
	wire, ok := CIEncodeNativeRecord(r)
	if !ok {
		t.Fatal("encoding rejected")
	}
	decoded, ok := CIDecodeNativeRecord(wire[:])
	if !ok || decoded.CommandID != r.CommandID || decoded.First.Group != r.First.Group || decoded.Final.Cause != r.Final.Cause {
		t.Fatal("wire changed evidence")
	}
	wire[63] = 1
	if _, ok := CIDecodeNativeRecord(wire[:]); ok {
		t.Fatal("reserved bytes accepted")
	}
	if _, ok := CIDecodeNativeRecord(wire[:63]); ok {
		t.Fatal("partial frame accepted")
	}
	// os is nil: any added poll, group query, signal, reap, or observer close would panic.
	if p.os != nil {
		t.Fatal("pure fixture must not own native process")
	}
}

type ciPanicOS struct{ osProcess }

func (ciPanicOS) signalGroup(signal) error { panic("original-panic") }

type ciNoopCloser struct{}

func (ciNoopCloser) Close() error { return nil }

var _ io.Closer = ciNoopCloser{}

func TestCINativeCleanupPanicPreserved(t *testing.T) {
	for _, prior := range []bool{false, true} {
		p := &process{os: ciPanicOS{}, stdin: newOwnedCloser(ciNoopCloser{}), discard: make(chan struct{})}
		if prior {
			p.term.Lock()
			p.ciCaptureCleanup("terminate", errGroupObservation)
			p.term.Unlock()
		}
		done := make(chan any, 1)
		go func() { defer func() { done <- recover() }(); _ = p.Terminate(context.Background()) }()
		select {
		case v := <-done:
			if v != "original-panic" {
				t.Fatal("original panic changed")
			}
		case <-time.After(time.Second):
			t.Fatal("diagnostic deadlocked original panic")
		}
		expected := uint32(0)
		if prior {
			expected = 1
		}
		if p.ciCapture.calls != expected {
			t.Fatal("panic invented successful capture")
		}
		if _, ok := p.ciFinalNativeRecord(strings.Repeat("a", 32), true, true); ok {
			t.Fatal("panic exported stale final snapshot")
		}
	}
}
