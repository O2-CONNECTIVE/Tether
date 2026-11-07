//go:build darwin && cgo && ci_actual_producer

package macagent

import (
	"encoding/hex"
	"errors"
	"golang.org/x/sys/unix"
	"sync"
	"sync/atomic"
)

// Compiler-only evidence. No error objects, pointers, PIDs, paths or payloads
// survive capture. These values are NOT a fresh OS observation or cleanup proof.
type CINativeSnapshot struct {
	DiagnosticsAvailable bool     `json:"diagnostics_available"`
	Method               string   `json:"method"`
	Cause                string   `json:"cause"`
	Group                string   `json:"group"`
	GroupAttempted       bool     `json:"group_attempted"`
	ObserverError        bool     `json:"observer_error"`
	SignalError          bool     `json:"signal_error"`
	ReapLost             bool     `json:"reap_lost"`
	Reaped               bool     `json:"reaped"`
	TermAttempted        bool     `json:"term_attempted"`
	KillAttempted        bool     `json:"kill_attempted"`
	IOStdin              string   `json:"io_stdin"`
	IOStdout             string   `json:"io_stdout"`
	IOStderr             string   `json:"io_stderr"`
	IOErrored            bool     `json:"io_errored"`
	TerminalUnknown      bool     `json:"terminal_unknown"`
	Failures             []string `json:"failures"`
}
type ciNativeCapture struct {
	calls     uint32
	first     CINativeSnapshot
	last      CINativeSnapshot
	firstCall uint32
	exported  bool
}
type CINativeRecord struct {
	Version int `json:"version"`
	// Only on private inherited pipe. Never included in public outcome.
	CommandID       string           `json:"command_id"`
	Calls           uint32           `json:"calls"`
	FirstCall       uint32           `json:"first_call"`
	TerminateFailed bool             `json:"terminate_failed"`
	CleanupFailed   bool             `json:"cleanup_failed"`
	First           CINativeSnapshot `json:"first"`
	Final           CINativeSnapshot `json:"final"`
}

func ciIO(v ioCompletion) string {
	switch v {
	case ioPending:
		return "pending"
	case ioNormal:
		return "normal"
	case ioExpectedLocalClose:
		return "expected_local_close"
	case ioAbnormal:
		return "abnormal"
	}
	return "unavailable"
}
func ciGroup(v groupObservation, attempted bool) string {
	if !attempted {
		return "not_attempted"
	}
	switch v {
	case groupAnchorOnly, groupInvalidAnchor, groupPermission, groupQueryFailed, groupTruncated, groupMissingAnchor, groupInvalidList, groupChanged, groupRace, groupLive, groupMultiple:
		return string(v)
	}
	return "unavailable"
}
func ciCause(err error) string {
	if err == nil {
		return "none"
	}
	// Independent flags preserve additional joined causes without raw text.
	for _, v := range []struct {
		err  error
		name string
	}{
		{ErrTerminalCleanupUnknown, "terminal_unknown"}, {errGroupObservation, "group_observation"}, {errObserver, "observer"}, {errSignal, "signal"}, {errUnconfirmed, "exit_unconfirmed"}, {errIOUnconfirmed, "io_unconfirmed"}, {errNotTerminated, "not_terminated"}, {errCloseUncertain, "close_uncertain"}, {errCloseRetry, "close_retry"},
	} {
		if errors.Is(err, v.err) {
			return v.name
		}
	}
	return "other"
}

// Called by a defer while original p.term is held, after original return value
// selection. p.mu is acquired only to copy cached state. No OS method or export.
func (p *process) ciCaptureCleanup(method string, err error) {
	p.mu.Lock()
	s := CINativeSnapshot{DiagnosticsAvailable: p.diagnostics != nil, Method: method, Cause: ciCause(err), Group: ciGroup(p.groupState, p.groupAttempted), GroupAttempted: p.groupAttempted, ObserverError: p.obsErr != nil, SignalError: p.sigErr != nil, ReapLost: p.lost != nil, Reaped: p.reaped, TermAttempted: p.termAttempted, KillAttempted: p.killAttempted, IOStdin: ciIO(p.ioWrite), IOStdout: ciIO(p.ioStdout), IOStderr: ciIO(p.ioStderr), IOErrored: p.ioErr != nil, TerminalUnknown: p.terminalState.Load() == 1, Failures: p.diagnostics.Failures()}
	p.mu.Unlock()
	p.ciCapture.calls++
	p.ciCapture.last = s
	if err != nil && p.ciCapture.firstCall == 0 {
		p.ciCapture.first = s
		p.ciCapture.firstCall = p.ciCapture.calls
	}
}

// Invoked ONLY after Session.run's original final Terminate/Release completed,
// outside Session/native locks. Exact proc and command correlation, no reassessment.
func (p *process) CIFinalCleanupDiagnostic(commandID string, terminateFailed, cleanupFailed bool) {
	r, ok := p.ciFinalNativeRecord(commandID, terminateFailed, cleanupFailed)
	if ok {
		ciExportNativeRecord(r)
	}
}
func (p *process) ciFinalNativeRecord(commandID string, terminateFailed, cleanupFailed bool) (CINativeRecord, bool) {
	p.term.Lock()
	if p.ciCapture.exported || p.ciCapture.last.Method == "" {
		p.term.Unlock()
		return CINativeRecord{}, false
	}
	p.ciCapture.exported = true
	r := CINativeRecord{Version: 1, CommandID: commandID, Calls: p.ciCapture.calls, FirstCall: p.ciCapture.firstCall, TerminateFailed: terminateFailed, CleanupFailed: cleanupFailed, First: p.ciCapture.first, Final: p.ciCapture.last}
	p.term.Unlock()
	return r, true
}

// Fixed 64-byte wire record is below POSIX minimum PIPE_BUF (512).
// Encoding admits only fixed classes; no raw errors or extra identities.
var ciMethods = []string{"", "terminate", "release"}
var ciCauses = []string{"", "none", "terminal_unknown", "group_observation", "observer", "signal", "exit_unconfirmed", "io_unconfirmed", "not_terminated", "close_uncertain", "close_retry", "other"}
var ciGroups = []string{"", "not_attempted", "owned_group_quiescent_snapshot", "unknown_anchor", "unknown_permission", "unknown_query", "unknown_truncated", "unknown_anchor_missing", "unknown_invalid_list", "unknown_pid_pgid_changed", "unknown_query_race", "observed_live_member_unknown", "unknown_multiple_members", "unavailable"}
var ciIOs = []string{"", "pending", "normal", "expected_local_close", "abnormal", "unavailable"}
var ciFailures = []string{"signal_term", "signal_kill", "reap_lost", "reap_timeout", "io_join", "stdin_close", "stdout_close", "stderr_close", "observer_close", "not_terminated"}

func ciIndex(values []string, value string) (byte, bool) {
	for i, v := range values {
		if v == value {
			return byte(i), true
		}
	}
	return 0, false
}
func CIEncodeNativeRecord(r CINativeRecord) ([64]byte, bool) {
	var out [64]byte
	id, err := hex.DecodeString(r.CommandID)
	if err != nil || len(id) != 16 || r.Version != 1 || r.Calls < 1 || r.Calls > 16 || r.FirstCall > r.Calls {
		return out, false
	}
	out[0] = 1
	copy(out[1:17], id)
	out[17] = byte(r.Calls)
	out[18] = byte(r.FirstCall)
	if r.TerminateFailed {
		out[19] |= 1
	}
	if r.CleanupFailed {
		out[19] |= 2
	}
	for n, s := range []CINativeSnapshot{r.First, r.Final} {
		offset := 20 + n*16
		for i, v := range []struct {
			values []string
			value  string
		}{{ciMethods, s.Method}, {ciCauses, s.Cause}, {ciGroups, s.Group}, {ciIOs, s.IOStdin}, {ciIOs, s.IOStdout}, {ciIOs, s.IOStderr}} {
			b, ok := ciIndex(v.values, v.value)
			if !ok {
				return out, false
			}
			out[offset+i] = b
		}
		for i, v := range []bool{s.GroupAttempted, s.ObserverError, s.SignalError, s.ReapLost, s.Reaped, s.TermAttempted, s.KillAttempted, s.IOErrored, s.TerminalUnknown, s.DiagnosticsAvailable} {
			if v {
				out[offset+6+i/8] |= 1 << uint(i%8)
			}
		}
		for i, base := range ciFailures {
			for _, v := range s.Failures {
				if v == base {
					out[offset+8+i/8] |= 1 << uint(i%8)
				}
			}
		}
	}
	return out, true
}
func CIDecodeNativeRecord(raw []byte) (CINativeRecord, bool) {
	var r CINativeRecord
	if len(raw) != 64 || raw[0] != 1 || raw[17] < 1 || raw[17] > 16 || raw[18] > raw[17] || raw[19] > 3 {
		return r, false
	}
	r.Version = 1
	r.CommandID = hex.EncodeToString(raw[1:17])
	r.Calls = uint32(raw[17])
	r.FirstCall = uint32(raw[18])
	r.TerminateFailed = raw[19]&1 != 0
	r.CleanupFailed = raw[19]&2 != 0
	snapshots := []*CINativeSnapshot{&r.First, &r.Final}
	for n, s := range snapshots {
		offset := 20 + n*16
		for i, v := range []struct {
			values []string
			target *string
		}{{ciMethods, &s.Method}, {ciCauses, &s.Cause}, {ciGroups, &s.Group}, {ciIOs, &s.IOStdin}, {ciIOs, &s.IOStdout}, {ciIOs, &s.IOStderr}} {
			b := int(raw[offset+i])
			if b >= len(v.values) {
				return CINativeRecord{}, false
			}
			*v.target = v.values[b]
		}
		for i, v := range []*bool{&s.GroupAttempted, &s.ObserverError, &s.SignalError, &s.ReapLost, &s.Reaped, &s.TermAttempted, &s.KillAttempted, &s.IOErrored, &s.TerminalUnknown, &s.DiagnosticsAvailable} {
			*v = raw[offset+6+i/8]&(1<<uint(i%8)) != 0
		}
		if raw[offset+7] > 3 || raw[offset+9] > 3 {
			return CINativeRecord{}, false
		}
		for i, base := range ciFailures {
			if raw[offset+8+i/8]&(1<<uint(i%8)) != 0 {
				s.Failures = append(s.Failures, base)
			}
		}
		for _, b := range raw[offset+10 : offset+16] {
			if b != 0 {
				return CINativeRecord{}, false
			}
		}
	}
	for _, b := range raw[52:] {
		if b != 0 {
			return CINativeRecord{}, false
		}
	}
	return r, true
}

var ciNativeSinkEnabled atomic.Bool
var ciNativeSinkMu sync.Mutex
var ciNativeSinkEntered bool

// Only the fixture helper calls enable after adopting/validating owned FD4.
// Tagged unit/test binaries remain disabled; never write an arbitrary FD4.
func CIEnableNativeDiagnosticSink() bool {
	ciNativeSinkMu.Lock()
	defer ciNativeSinkMu.Unlock()
	if ciNativeSinkEntered {
		return false
	}
	ciNativeSinkEntered = true
	ciNativeSinkEnabled.Store(true)
	return true
}
func CIDisableNativeDiagnosticSink() {
	ciNativeSinkMu.Lock()
	defer ciNativeSinkMu.Unlock()
	ciNativeSinkEnabled.Store(false)
}
func ciExportNativeRecord(r CINativeRecord) bool {
	ciNativeSinkMu.Lock()
	defer ciNativeSinkMu.Unlock()
	if !ciNativeSinkEnabled.Load() {
		return false
	}
	raw, ok := CIEncodeNativeRecord(r)
	if !ok {
		return false
	}
	// No file poller, blocking write, retries, or export while lifecycle locks held.
	n, err := unix.Write(4, raw[:])
	return err == nil && n == len(raw)
}
