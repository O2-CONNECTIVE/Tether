//go:build darwin && cgo && ci_actual_producer

package residentpipe

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// In-memory protocol checks only; no producer, pipes, sockets or native work.
func TestCIProducerObservationValidation(t *testing.T) {
	ciCheckProducerDiagnostics(t)
	good := statusEnvelope{1, "status", 100, Snapshot{ProtocolVersion: 1, Source: "resident-owner", State: "source-observation", Revision: 1, ValidForMS: 3000, Standby: "off", Registration: "disconnected", Work: "idle", Cleanup: "confirmed", CanRequestOn: true}}
	raw, err := Encode(good)
	if err != nil {
		t.Fatal(err)
	}
	fresh := func() *ciChild { return &ciChild{receipts: make(map[uint64]Receipt)} }
	if err = fresh().acceptFrame(raw); err != nil {
		t.Fatal("valid actual schema refused", err)
	}
	for _, bad := range [][]byte{
		bytes.TrimSuffix(raw, []byte("\n")),
		append(append([]byte(nil), raw[:len(raw)-1]...), '\r', '\n'),
		bytes.Replace(raw, []byte(`"sourceContinuousMs":100`), []byte(`"sourceContinuousMs":null`), 1),
		bytes.Replace(raw, []byte(`"canRequestOn":true`), []byte(`"canRequestOn":null`), 1),
		bytes.Replace(raw, []byte(`"source":"resident-owner"`), []byte(`"source":"injected"`), 1),
		bytes.Replace(raw, []byte(`"protocolVersion":1`), []byte(`"protocolVersion":1,"protocolVersion":1`), 1),
		bytes.Replace(raw, []byte(`"validForMs":3000,`), nil, 1),
	} {
		if fresh().acceptFrame(bad) == nil {
			t.Fatal("invalid or incomplete original frame accepted")
		}
	}
	p := fresh()
	if p.acceptFrame(raw) != nil {
		t.Fatal("valid frame refused")
	}
	good.SourceContinuousMS = 99
	good.Snapshot.Revision = 2
	older, _ := Encode(good)
	if p.acceptFrame(older) == nil {
		t.Fatal("source time moved backward")
	}
	receipt, _ := Encode(receiptEnvelope{1, "receipt", Receipt{ProtocolVersion: 0, RequestID: 1, Intent: "on", Disposition: "accepted"}})
	if fresh().acceptFrame(receipt) == nil {
		t.Fatal("receipt protocol mismatch accepted")
	}
	// Connecting may legitimately be represented as standby unknown. Cleanup
	// and transition remain the authoritative sticky unknown criteria.
	good.SourceContinuousMS = 101
	good.Snapshot.Revision = 1
	good.Snapshot.Standby = "unknown"
	good.Snapshot.Registration = "connecting"
	connecting, _ := json.Marshal(good)
	connecting = append(connecting, '\n')
	p = fresh()
	if p.acceptFrame(connecting) != nil || p.unknown {
		t.Fatal("connecting standby unknown incorrectly latched")
	}
	d := &ciDiagnostic{stage: "natural-poll"}
	d.note("command-cleanup-unknown")
	d.note("output-mismatch")
	stage, code := d.fields()
	if stage != "natural-poll" || code != "command-cleanup-unknown" {
		t.Fatal("first fixed diagnostic overwritten")
	}
	d = &ciDiagnostic{stage: "initial-off"}
	_, code = d.fields()
	if code != "uncategorized" {
		t.Fatal("missing failure classification not explicit")
	}

	// Same rejection boundary, now distinguishing fixed application error enums.
	for _, tc := range []struct {
		errorValue any
		cleanup    string
		want       string
	}{
		{"admission_refused_or_uncertain", "pending", "mcp-tool-admission-uncertain"},
		{"result_unobserved_or_uncertain", "unknown", "mcp-tool-native-cleanup-unknown"},
		{"result_unobserved_or_uncertain", "confirmed", "mcp-tool-result-uncertain"},
		{"not_found", "", "mcp-tool-not-found"},
		{"arbitrary raw value must never be disclosed", "", "mcp-tool-error-other"},
	} {
		result := map[string]any{"isError": true, "structuredContent": map[string]any{"error": tc.errorValue, "command_cleanup": tc.cleanup}}
		if code := ciToolResultFailure(result); code != tc.want {
			t.Fatal("fixed application diagnostic mismatch")
		}
	}
	if ciToolResultFailure(map[string]any{"isError": true, "structuredContent": map[string]any{}}) != "mcp-tool-error-unspecified" {
		t.Fatal("unspecified error accepted")
	}
	if ciToolResultFailure(map[string]any{"isError": false, "structuredContent": map[string]any{}}) != "" {
		t.Fatal("prior successful shape rejected")
	}
	if ciToolResultFailure(map[string]any{"isError": false}) != "mcp-tool-result-shape" {
		t.Fatal("missing content accepted")
	}
	if ciToolResultFailure(map[string]any{"isError": "false", "structuredContent": map[string]any{}}) != "mcp-tool-result-flag" {
		t.Fatal("invalid flag accepted")
	}

	d = &ciDiagnostic{}
	d.beginTool("rescue_fixed_status")
	d.observeTool(map[string]any{"isError": true, "structuredContent": map[string]any{"error": "result_unobserved_or_uncertain", "phase": "running", "command_phase": "canceled", "stop": "canceled", "command_cleanup": "unknown", "owner_cleanup": "pending", "gateway_cleanup": "pending"}})
	safe := map[string]any{}
	d.annotate(safe)
	if safe["tool_operation"] != "status" || safe["result_error"] != "result_unobserved_or_uncertain" || safe["command_cleanup"] != "unknown" {
		t.Fatal("fixed result state lost")
	}
	d.beginTool("rescue_fixed_cancel")
	safe = map[string]any{}
	d.annotate(safe)
	if safe["tool_operation"] != "cancel" || safe["result_error"] != "unavailable" || safe["command_cleanup"] != "unavailable" {
		t.Fatal("old tool observation reused")
	}
	d.observeTool(map[string]any{"structuredContent": map[string]any{"error": "RAW_PRIVATE_SENTINEL", "phase": "RAW_PRIVATE_SENTINEL", "stop": []string{"RAW_PRIVATE_SENTINEL"}, "command_cleanup": "RAW_PRIVATE_SENTINEL"}})
	safe = map[string]any{}
	d.annotate(safe)
	encoded, _ := json.Marshal(safe)
	if bytes.Contains(encoded, []byte("RAW_PRIVATE_SENTINEL")) || safe["result_error"] != "unavailable" {
		t.Fatal("unlisted tool value escaped diagnostic allowlist")
	}

}

// Called inside the existing exact-selected pure test: no new live test or I/O.
func ciCheckProducerDiagnostics(t *testing.T) {
	t.Helper()
	now := time.Unix(100, 0)
	d := &ciDiagnostic{}
	p := &ciChild{receipts: make(map[uint64]Receipt), observed: now}
	p.snapshot = Snapshot{Standby: "on", Registration: "authenticated", Work: "idle", Cleanup: "confirmed"}
	check := func(field, want string) {
		t.Helper()
		out := map[string]any{}
		d.annotateProducer(out, p, now)
		if out[field] != want {
			t.Fatalf("fixed producer diagnostic mismatch: %s", field)
		}
	}
	d.beginProducerRequest("off")
	check("producer_receipt", "unavailable")
	d.producerRequestID = MaxInteger + 1
	check("producer_receipt", "unavailable")
	check("producer_transition", "unavailable")
	d.producerRequestID = 2
	check("producer_receipt", "missing")
	for _, disposition := range []string{"accepted", "rejected", "stale", "unknown"} {
		p.receipts[2] = Receipt{RequestID: 2, Intent: "off", Disposition: disposition}
		check("producer_receipt", disposition)
	}
	p.receipts[2] = Receipt{RequestID: 1, Intent: "off", Disposition: "accepted"}
	check("producer_receipt", "mismatch")
	p.receipts[2] = Receipt{RequestID: 2, Intent: "on", Disposition: "accepted"}
	check("producer_receipt", "mismatch")
	p.receipts[2] = Receipt{RequestID: 2, Intent: "off", Disposition: "accepted"}
	check("producer_transition", "missing")
	p.snapshot.Transition = &Transition{RequestID: 1, Intent: "on", Outcome: "applied"}
	check("producer_transition", "other-request")
	for _, outcome := range []string{"pending", "applied", "rejected", "unknown"} {
		p.snapshot.Transition = &Transition{RequestID: 2, Intent: "off", Outcome: outcome}
		check("producer_transition", outcome)
	}
	p.snapshot.Transition.Intent = "on"
	check("producer_transition", "other-request")
	check("producer_snapshot", "within-window")
	p.observed = now.Add(-3 * time.Second)
	check("producer_snapshot", "within-window")
	p.observed = p.observed.Add(-time.Nanosecond)
	check("producer_snapshot", "expired")
	check("producer_cleanup", "confirmed") // retained value, not renewed proof
	p.unknown = true
	check("producer_snapshot", "observation-unknown")
	p.unknown = false
	p.readErr = errors.New("RAW_PRIVATE_SENTINEL")
	check("producer_snapshot", "observation-unknown")
	p.readErr = nil
	p.observed = now.Add(time.Second)
	check("producer_snapshot", "unavailable")
	p.observed = time.Time{}
	check("producer_snapshot", "missing")
	check("producer_cleanup", "unavailable")
	check("producer_transition", "unavailable")
	p.observed = now
	p.snapshot.Standby = "RAW_PRIVATE_SENTINEL"
	p.snapshot.Registration = "RAW_PRIVATE_SENTINEL"
	p.snapshot.Work = "RAW_PRIVATE_SENTINEL"
	p.snapshot.Cleanup = "RAW_PRIVATE_SENTINEL"
	p.receipts[2] = Receipt{RequestID: 2, Intent: "off", Disposition: "RAW_PRIVATE_SENTINEL"}
	p.snapshot.Transition = &Transition{RequestID: 2, Intent: "off", Outcome: "RAW_PRIVATE_SENTINEL"}
	out := map[string]any{}
	d.annotateProducer(out, p, now)
	encoded, _ := json.Marshal(out)
	if bytes.Contains(encoded, []byte("RAW_PRIVATE_SENTINEL")) || len(out) != len(ciProducerEnums) {
		t.Fatal("producer diagnostic escaped its fixed field boundary")
	}
	for _, field := range []string{"producer_standby", "producer_registration", "producer_work", "producer_cleanup", "producer_receipt", "producer_transition"} {
		check(field, "unavailable")
	}
	d.beginProducerRequest("on")
	check("producer_receipt", "unavailable")
	check("producer_transition", "unavailable")
	d.producerRequestID = 3
	check("producer_receipt", "missing")
	check("producer_transition", "other-request")
	d.beginProducerRequest("RAW_PRIVATE_SENTINEL")
	d.producerRequestID = 2
	check("producer_request_intent", "unavailable")
	check("producer_receipt", "unavailable")
	d.annotateProducer(out, nil, now)
	for _, value := range out {
		if value != "unavailable" {
			t.Fatal("missing producer reused previous diagnostic")
		}
	}
}
