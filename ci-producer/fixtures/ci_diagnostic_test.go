//go:build darwin && cgo && ci_actual_producer

package residentpipe

import "testing"

// Fixed public enums only. Never include errors, paths, payloads or identities.
// The stage is set before an operation; failure records the first failing site.
type ciDiagnostic struct {
	stage, failure string
	toolOperation  string
	toolResult     map[string]string
}

func (d *ciDiagnostic) at(stage string) { d.stage = stage }
func (d *ciDiagnostic) note(code string) {
	if d.failure == "" {
		d.failure = code
	}
}
func (d *ciDiagnostic) fail(t *testing.T, code string) {
	t.Helper()
	d.note(code)
	t.Fatal("fixture assertion failed; see fixed outcome codes")
}
func (d *ciDiagnostic) fields() (string, string) {
	failure := d.failure
	if failure == "" {
		failure = "uncategorized"
	}
	return d.stage, failure
}

func ciChildFailure(p *ciChild, err error) string {
	if p != nil {
		p.mu.Lock()
		code := p.diagnosticCode
		if code == "" && p.waitErr != nil {
			code = "producer-wait-nonzero"
		}
		p.mu.Unlock()
		if code != "" {
			return code
		}
	}
	if err == nil {
		return "uncategorized"
	}
	switch err.Error() {
	case "no fresh producer snapshot":
		return "producer-stale-snapshot"
	case "producer ended before condition":
		return "producer-ended"
	case "producer observation deadline; original owner retained":
		return "producer-observation-deadline"
	case "applied transition missing accepted same-ID receipt":
		return "producer-receipt-mismatch"
	case "original producer EOF not joined":
		return "producer-eof-deadline"
	case "original child Wait not joined":
		return "producer-wait-deadline"
	}
	return "producer-operation"
}

// These are snapshots of the most recent tool call, not a fresh observation or
// cleanup proof. Starting another tool call clears every previous result field.
var ciResultEnums = map[string][]string{
	"result_error":    {"none", "admission_refused_or_uncertain", "start_uncertain", "registration_unobserved", "result_unobserved_or_uncertain", "output_limit", "not_found", "control_ended", "request_generation_mismatch", "busy_or_refused"},
	"result_phase":    {"pending", "running", "finished", "unknown"},
	"command_phase":   {"starting", "running", "exited", "canceled", "failed", "start_unknown"},
	"result_stop":     {"none", "canceled", "command_timeout", "revoked", "hard_deadline", "transport_lost", "runtime_fault"},
	"command_cleanup": {"pending", "confirmed", "unknown", "not_required"},
	"owner_cleanup":   {"pending", "confirmed", "unknown"},
	"gateway_cleanup": {"pending", "confirmed", "unknown"},
}

func (d *ciDiagnostic) beginTool(name string) {
	d.toolOperation = "unavailable"
	switch name {
	case "rescue_fixed_start":
		d.toolOperation = "start"
	case "rescue_fixed_status":
		d.toolOperation = "status"
	case "rescue_fixed_cancel":
		d.toolOperation = "cancel"
	}
	d.toolResult = nil
}
func (d *ciDiagnostic) observeTool(result map[string]any) {
	value, ok := result["structuredContent"].(map[string]any)
	if !ok {
		return
	}
	d.toolResult = make(map[string]string)
	for output, input := range map[string]string{"result_error": "error", "result_phase": "phase", "command_phase": "command_phase", "result_stop": "stop", "command_cleanup": "command_cleanup", "owner_cleanup": "owner_cleanup", "gateway_cleanup": "gateway_cleanup"} {
		candidate, ok := value[input].(string)
		if !ok {
			continue
		}
		if output == "result_error" && candidate == "" {
			candidate = "none"
		}
		for _, allowed := range ciResultEnums[output] {
			if candidate == allowed {
				d.toolResult[output] = allowed
				break
			}
		}
	}
}
func (d *ciDiagnostic) annotate(outcome map[string]any) {
	operation := d.toolOperation
	if operation == "" {
		operation = "unavailable"
	}
	outcome["tool_operation"] = operation
	for field := range ciResultEnums {
		value := d.toolResult[field]
		if value == "" {
			value = "unavailable"
		}
		outcome[field] = value
	}
}
