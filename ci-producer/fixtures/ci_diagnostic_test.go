//go:build darwin && cgo && ci_actual_producer

package residentpipe

import (
	"testing"
	"time"
)

// Fixed public enums only. Never include errors, paths, payloads or identities.
// The stage is set before an operation; failure records the first failing site.
type ciDiagnostic struct {
	stage, failure    string
	failureSubstep    string
	failureElapsedMS  *int64
	failureDeadlineMS *int64
	failureIteration  *int
	toolOperation     string
	commandID         string
	toolResult        map[string]string
	producerRequestID uint64 // private correlation only, never published
	producerIntent    string
}

func (d *ciDiagnostic) at(stage string) { d.stage = stage }

var ciFailureSubsteps = map[string][]string{
	"run-allow": {
		"producer-state-wait", "allow-request", "allow-receipt-wait",
		"extend-request", "extend-receipt-wait",
	},
	"gateway-setup": {
		"root-stat", "ca-key-generate", "ca-certificate-create", "ca-certificate-parse",
		"server-key-generate", "server-certificate-create", "host-key-generate", "host-signer-create",
		"principal-key-generate", "principal-signer-create", "other-principal-key-generate",
		"other-principal-signer-create", "jwt-key-generate", "listener-bind", "jwks-encode",
		"verifier-create", "gateway-create", "handler-create", "hosts-configure", "resident-configure",
		"host-key-encode", "host-key-file-write", "policy-encode", "policy-file-write",
	},
	"work-cli-response": {"timeout", "eof", "invalid-json", "shape-mismatch"},
}

var ciFailureWaitDeadlines = map[string]int64{
	"producer-state-wait": 10000,
	"allow-receipt-wait":  10000,
	"extend-receipt-wait": 10000,
	"timeout":             40000,
	"eof":                 40000,
	"invalid-json":        40000,
	"shape-mismatch":      40000,
}

func ciFailureSubstepAllowed(code, substep string) bool {
	if substep == "unavailable" {
		return true
	}
	for _, allowed := range ciFailureSubsteps[code] {
		if substep == allowed {
			return true
		}
	}
	return false
}

func (d *ciDiagnostic) note(code string) {
	d.noteAt(code, "unavailable", nil, nil, nil)
}
func (d *ciDiagnostic) noteAt(code, substep string, elapsedMS, deadlineMS *int64, iteration *int) {
	if d.failure == "" {
		d.failure = code
		if !ciFailureSubstepAllowed(code, substep) {
			substep, elapsedMS, deadlineMS, iteration = "unavailable", nil, nil, nil
		}
		if expected, timed := ciFailureWaitDeadlines[substep]; timed {
			if elapsedMS == nil || deadlineMS == nil || *deadlineMS != expected {
				substep, elapsedMS, deadlineMS = "unavailable", nil, nil
			}
		} else if elapsedMS != nil || deadlineMS != nil {
			substep, elapsedMS, deadlineMS = "unavailable", nil, nil
		}
		if code != "work-cli-response" || d.toolOperation != "list" || iteration == nil || *iteration < 1 {
			iteration = nil
		}
		d.failureSubstep = substep
		d.failureElapsedMS = elapsedMS
		d.failureDeadlineMS = deadlineMS
		d.failureIteration = iteration
	}
}
func (d *ciDiagnostic) fail(t *testing.T, code string) {
	d.failAt(t, code, "unavailable", nil, nil, nil)
}
func (d *ciDiagnostic) failAt(t *testing.T, code, substep string, elapsedMS, deadlineMS *int64, iteration *int) {
	t.Helper()
	d.noteAt(code, substep, elapsedMS, deadlineMS, iteration)
	t.Fatal("fixture assertion failed; see fixed outcome codes")
}
func (d *ciDiagnostic) failWait(t *testing.T, code, substep string, started time.Time, deadline time.Duration) {
	elapsedMS, deadlineMS := time.Since(started).Milliseconds(), deadline.Milliseconds()
	d.failAt(t, code, substep, &elapsedMS, &deadlineMS, nil)
}
func (d *ciDiagnostic) failWorkResponse(t *testing.T, substep string, elapsed int64, iteration *int) {
	var elapsedMS, deadlineMS *int64
	if deadline, timed := ciFailureWaitDeadlines[substep]; timed {
		elapsedMS = &elapsed
		deadlineMS = &deadline
	}
	d.failAt(t, "work-cli-response", substep, elapsedMS, deadlineMS, iteration)
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
	case "tether_fixed_start":
		d.toolOperation = "start"
	case "tether_fixed_status":
		d.toolOperation = "status"
	case "tether_fixed_cancel":
		d.toolOperation = "cancel"
	case "tether_fixed_release":
		d.toolOperation = "release"
	case "tether_fixed_list":
		d.toolOperation = "list"
	case "tether_fixed_get_execution":
		d.toolOperation = "get_execution"
	case "tether_fixed_read_output":
		d.toolOperation = "read_output"
	}
	d.toolResult = nil
	d.commandID = ""
}
func (d *ciDiagnostic) observeTool(result map[string]any) {
	value, ok := result["structuredContent"].(map[string]any)
	if !ok {
		return
	}
	d.commandID, _ = value["command_id"].(string)
	d.toolResult = make(map[string]string)
	for output, input := range map[string]string{"result_error": "error", "result_phase": "phase", "command_phase": "command_phase", "result_stop": "stop", "command_cleanup": "command_cleanup", "owner_cleanup": "owner_cleanup", "gateway_cleanup": "gateway_cleanup"} {
		candidate, ok := value[input].(string)
		if output == "result_error" && !ok {
			candidate, ok = value["work_error"].(string)
		}
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
	substep := d.failureSubstep
	if !ciFailureSubstepAllowed(d.failure, substep) {
		substep = "unavailable"
	}
	outcome["failure_substep"] = substep
	if d.failureElapsedMS == nil {
		outcome["failure_elapsed_ms"] = "unavailable"
	} else {
		outcome["failure_elapsed_ms"] = *d.failureElapsedMS
	}
	if d.failureDeadlineMS == nil {
		outcome["failure_deadline_ms"] = "unavailable"
	} else {
		outcome["failure_deadline_ms"] = *d.failureDeadlineMS
	}
	if d.failureIteration == nil {
		outcome["failure_iteration"] = "unavailable"
	} else {
		outcome["failure_iteration"] = *d.failureIteration
	}
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

// The ID and intent identify the one request attempted by this fixture. Clearing
// before submission prevents an OFF submission failure from reusing ON evidence.
func (d *ciDiagnostic) beginProducerRequest(intent string) {
	d.producerRequestID = 0
	d.producerIntent = intent
}

var ciProducerEnums = map[string][]string{
	"producer_request_intent": {"on", "off"},
	"producer_receipt":        {"missing", "accepted", "rejected", "stale", "unknown", "mismatch"},
	"producer_snapshot":       {"missing", "within-window", "expired", "observation-unknown"},
	"producer_standby":        {"off", "on", "stopping", "unknown"},
	"producer_registration":   {"disconnected", "connecting", "authenticated", "unknown"},
	"producer_work":           {"idle", "preparing", "running", "stopping", "unknown"},
	"producer_cleanup":        {"confirmed", "pending", "unknown"},
	"producer_transition":     {"missing", "other-request", "pending", "applied", "rejected", "unknown"},
}

func ciProducerEnum(field, value string) string {
	for _, allowed := range ciProducerEnums[field] {
		if value == allowed {
			return allowed
		}
	}
	return "unavailable"
}

// Copy only the latest cached state under the existing lock. Receive age is not
// a new producer observation, complete transition history, or cleanup proof.
// No status request, wait, retry, native query or owner intervention is added.
func (d *ciDiagnostic) annotateProducer(outcome map[string]any, p *ciChild, now time.Time) {
	for field := range ciProducerEnums {
		outcome[field] = "unavailable"
	}
	intent := ciProducerEnum("producer_request_intent", d.producerIntent)
	outcome["producer_request_intent"] = intent
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	requestKnown := d.producerRequestID != 0 && d.producerRequestID <= MaxInteger && intent != "unavailable"
	if requestKnown {
		outcome["producer_receipt"] = "missing"
		if r, ok := p.receipts[d.producerRequestID]; ok {
			outcome["producer_receipt"] = "mismatch"
			if r.RequestID == d.producerRequestID && r.Intent == intent {
				outcome["producer_receipt"] = ciProducerEnum("producer_receipt", r.Disposition)
			}
		}
	}
	outcome["producer_snapshot"] = "missing"
	if p.unknown || p.readErr != nil {
		outcome["producer_snapshot"] = "observation-unknown"
	} else if !p.observed.IsZero() {
		age := now.Sub(p.observed)
		switch {
		case age < 0:
			outcome["producer_snapshot"] = "unavailable"
		case age > 3*time.Second:
			outcome["producer_snapshot"] = "expired"
		default:
			outcome["producer_snapshot"] = "within-window"
		}
	}
	if p.observed.IsZero() {
		return
	}
	s := p.snapshot
	for field, value := range map[string]string{
		"producer_standby": s.Standby, "producer_registration": s.Registration,
		"producer_work": s.Work, "producer_cleanup": s.Cleanup,
	} {
		outcome[field] = ciProducerEnum(field, value)
	}
	if requestKnown {
		outcome["producer_transition"] = "missing"
		if tr := s.Transition; tr != nil {
			outcome["producer_transition"] = "other-request"
			if tr.RequestID == d.producerRequestID && tr.Intent == intent {
				outcome["producer_transition"] = ciProducerEnum("producer_transition", tr.Outcome)
			}
		}
	}
}
