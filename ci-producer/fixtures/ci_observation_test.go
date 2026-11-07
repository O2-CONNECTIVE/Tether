//go:build darwin && cgo && ci_actual_producer

package residentpipe

import (
	"bytes"
	"encoding/json"
	"testing"
)

// In-memory protocol checks only; no producer, pipes, sockets or native work.
func TestCIProducerObservationValidation(t *testing.T) {
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
