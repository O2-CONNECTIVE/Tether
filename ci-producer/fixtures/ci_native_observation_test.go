//go:build darwin && cgo && ci_actual_producer

package residentpipe

import (
	"encoding/hex"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/macagent"
	"io"
	"reflect"
	"testing"
	"time"
)

var ciNativeEnums = map[string][]string{
	"native_capture": {"matched", "unavailable", "invalid"},
}

func init() {
	for _, prefix := range []string{"native_first_", "native_final_"} {
		for k, v := range map[string][]string{
			"method":   {"terminate", "release"},
			"cause":    {"none", "terminal_unknown", "group_observation", "observer", "signal", "exit_unconfirmed", "io_unconfirmed", "not_terminated", "close_uncertain", "close_retry", "other"},
			"group":    {"not_attempted", "owned_group_quiescent_snapshot", "unknown_anchor", "unknown_permission", "unknown_query", "unknown_truncated", "unknown_anchor_missing", "unknown_invalid_list", "unknown_pid_pgid_changed", "unknown_query_race", "observed_live_member_unknown", "unknown_multiple_members"},
			"io_stdin": {"pending", "normal", "expected_local_close", "abnormal"}, "io_stdout": {"pending", "normal", "expected_local_close", "abnormal"}, "io_stderr": {"pending", "normal", "expected_local_close", "abnormal"},
		} {
			ciNativeEnums[prefix+k] = v
		}
		for _, k := range []string{"diag_available", "group_attempted", "observer_error", "signal_error", "reap_lost", "reaped", "term_attempted", "kill_attempted", "io_errored", "terminal_unknown", "diag_signal_term", "diag_signal_kill", "diag_reap_lost", "diag_reap_timeout", "diag_io_join", "diag_stdin_close", "diag_stdout_close", "diag_stderr_close", "diag_observer_close", "diag_not_terminated"} {
			ciNativeEnums[prefix+k] = []string{"yes", "no"}
		}
	}
	for _, k := range []string{"native_terminate_failed", "native_cleanup_failed"} {
		ciNativeEnums[k] = []string{"yes", "no"}
	}
}
func ciBool(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
func ciNativeFields(r macagent.CINativeRecord) map[string]string {
	out := map[string]string{"native_capture": "matched", "native_terminate_failed": ciBool(r.TerminateFailed), "native_cleanup_failed": ciBool(r.CleanupFailed)}
	for prefix, s := range map[string]macagent.CINativeSnapshot{"native_first_": r.First, "native_final_": r.Final} {
		if prefix == "native_first_" && r.FirstCall == 0 {
			continue
		}
		out[prefix+"method"] = s.Method
		out[prefix+"cause"] = s.Cause
		out[prefix+"group"] = s.Group
		out[prefix+"io_stdin"] = s.IOStdin
		out[prefix+"io_stdout"] = s.IOStdout
		out[prefix+"io_stderr"] = s.IOStderr
		for k, v := range map[string]bool{"diag_available": s.DiagnosticsAvailable, "group_attempted": s.GroupAttempted, "observer_error": s.ObserverError, "signal_error": s.SignalError, "reap_lost": s.ReapLost, "reaped": s.Reaped, "term_attempted": s.TermAttempted, "kill_attempted": s.KillAttempted, "io_errored": s.IOErrored, "terminal_unknown": s.TerminalUnknown} {
			out[prefix+k] = ciBool(v)
		}
		for _, k := range []string{"signal_term", "signal_kill", "reap_lost", "reap_timeout", "io_join", "stdin_close", "stdout_close", "stderr_close", "observer_close", "not_terminated"} {
			found := false
			for _, v := range s.Failures {
				if v == k {
					found = true
				}
			}
			out[prefix+"diag_"+k] = "unavailable"
			if s.DiagnosticsAvailable {
				out[prefix+"diag_"+k] = ciBool(found)
			}
		}
	}
	return out
}
func ciValidNativeRecord(r macagent.CINativeRecord) bool {
	id, err := hex.DecodeString(r.CommandID)
	if err != nil || len(id) != 16 || hex.EncodeToString(id) != r.CommandID || r.Version != 1 || r.Calls < 1 || r.Calls > 16 || r.FirstCall > r.Calls {
		return false
	}
	if r.FirstCall == 0 && !reflect.DeepEqual(r.First, macagent.CINativeSnapshot{}) {
		return false
	}
	if r.FirstCall > 0 && (r.First.Cause == "" || r.First.Cause == "none") {
		return false
	}
	if r.CleanupFailed != (r.Final.Cause != "none") {
		return false
	}
	if r.CleanupFailed && r.FirstCall == 0 {
		return false
	}
	if r.TerminateFailed && (r.Final.Method != "terminate" || !r.CleanupFailed) {
		return false
	}
	for key, v := range ciNativeFields(r) {
		valid := false
		for _, allowed := range ciNativeEnums[key] {
			if v == allowed || v == "unavailable" {
				valid = true
			}
		}
		if !valid {
			return false
		}
	}
	for _, s := range []macagent.CINativeSnapshot{r.First, r.Final} {
		for _, v := range s.Failures {
			valid := false
			for _, base := range []string{"signal_term", "signal_kill", "reap_lost", "reap_timeout", "io_join", "stdin_close", "stdout_close", "stderr_close", "observer_close", "not_terminated"} {
				for _, suffix := range []string{"", "_already_closed", "_bad_descriptor", "_permission", "_no_child", "_interrupted", "_other"} {
					if v == base+suffix {
						valid = true
					}
				}
			}
			if !valid {
				return false
			}
		}
	}
	return true
}

// Bounded private read only, never asks native runtime to query/retry cleanup.
func ciAnnotateNative(outcome map[string]any, p *ciChild, commandID string) {
	for key := range ciNativeEnums {
		outcome[key] = "unavailable"
	}
	if p == nil || p.nativeEvidence == nil || commandID == "" {
		return
	}
	if p.nativeEvidence.SetReadDeadline(time.Now().Add(2*time.Second)) != nil {
		return
	}
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		var raw [64]byte
		if _, err := io.ReadFull(p.nativeEvidence, raw[:]); err != nil {
			return
		}
		r, ok := macagent.CIDecodeNativeRecord(raw[:])
		if !ok || !ciValidNativeRecord(r) || seen[r.CommandID] {
			outcome["native_capture"] = "invalid"
			return
		}
		seen[r.CommandID] = true
		if r.CommandID == commandID {
			if !r.CleanupFailed {
				outcome["native_capture"] = "invalid"
				return
			}
			for k, v := range ciNativeFields(r) {
				outcome[k] = v
			}
			return
		}
	}

}
func (p *ciChild) finishNativeEvidence() error {
	if p.nativeEvidence == nil {
		return errors.New("native evidence missing")
	}
	if err := p.nativeEvidence.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(p.nativeEvidence, 129))
	if err != nil || len(raw) > 128 {
		return errors.New("native evidence EOF unconfirmed")
	}
	// Evidence is optional on successful original cleanup; never weakens prior gates.
	return p.nativeEvidence.Close()
}

func TestCINativeEvidenceValidation(t *testing.T) {
	s := macagent.CINativeSnapshot{Method: "terminate", Cause: "group_observation", Group: "unknown_multiple_members", IOStdin: "normal", IOStdout: "normal", IOStderr: "normal"}
	r := macagent.CINativeRecord{Version: 1, CommandID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Calls: 2, FirstCall: 1, TerminateFailed: true, CleanupFailed: true, First: s, Final: s}
	if !ciValidNativeRecord(r) {
		t.Fatal("valid record rejected")
	}
	for k, v := range ciNativeFields(r) {
		if k == "command_id" || v == r.CommandID {
			t.Fatal("private identity leaked")
		}
	}
	if ciNativeFields(r)["native_final_diag_signal_term"] != "unavailable" {
		t.Fatal("absent recorder fabricated negative")
	}
	r.Final.DiagnosticsAvailable = true
	if ciNativeFields(r)["native_final_diag_signal_term"] != "no" {
		t.Fatal("present recorder missing negative")
	}
	r.Final.Failures = []string{"secret /path 123"}
	if ciValidNativeRecord(r) {
		t.Fatal("raw value accepted")
	}
	r.Final.Failures = nil
	r.Final.Group = "invented"
	if ciValidNativeRecord(r) {
		t.Fatal("unknown enum accepted")
	}
}
