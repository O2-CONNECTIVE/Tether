package mcpadapter_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

type standbyShapeBackend struct {
	residentShapeBackend
	calls int
}

func (b *standbyShapeBackend) StandbyStatus(context.Context) map[string]any {
	b.calls++
	return b.value
}

func standbySession(t *testing.T, b mcpadapter.ResidentBackend) (*mcpadapter.HTTPSession, func(params any) (map[string]any, bool)) {
	t.Helper()
	s, err := mcpadapter.NewResidentHTTPSession(b, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	request := func(method string, params any) (map[string]any, bool) {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		reply := s.Handle(raw)
		var v map[string]any
		if json.Unmarshal(reply.Body, &v) != nil {
			t.Fatal("bad JSON")
		}
		if reply.Error {
			return v["error"].(map[string]any), false
		}
		return v["result"].(map[string]any), true
	}
	request("initialize", initParams(mcpadapter.ProtocolVersion))
	s.Handle([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	return s, func(params any) (map[string]any, bool) { return request("tools/call", params) }
}

func standbyTool(t *testing.T, s *mcpadapter.HTTPSession) map[string]any {
	t.Helper()
	reply := s.Handle([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	var v struct {
		Result struct{ Tools []map[string]any }
	}
	if reply.Error || json.Unmarshal(reply.Body, &v) != nil {
		t.Fatal(string(reply.Body))
	}
	for _, tool := range v.Result.Tools {
		if tool["name"] == "tether_standby_status" {
			return tool
		}
	}
	return nil
}

// The tool takes no argument at all, so no argument can ask for more standby.
func TestResidentStandbyToolTakesNoArgumentAndOnlyReads(t *testing.T) {
	ok := map[string]any{"schema_version": 1, "standby": "on", "standby_until": "2026-11-19T10:30:01.500Z", "remaining_ms": 5401500}
	b := &standbyShapeBackend{residentShapeBackend: residentShapeBackend{value: ok}}
	s, call := standbySession(t, b)
	tool := standbyTool(t, s)
	if tool == nil {
		t.Fatal("tether_standby_status not offered")
	}
	input, _ := json.Marshal(tool["inputSchema"])
	if string(input) != `{"additionalProperties":false,"properties":{},"required":[],"type":"object"}` {
		t.Fatal("input schema:", string(input))
	}
	schema, _ := tool["outputSchema"].(map[string]any)
	for _, args := range []any{map[string]any{}, nil} {
		params := map[string]any{"name": "tether_standby_status"}
		if args != nil {
			params["arguments"] = args
		}
		result, success := call(params)
		want, _ := json.Marshal(ok)
		got, _ := json.Marshal(result["structuredContent"])
		if !success || result["isError"] != false || string(got) != string(want) || checkOutputShape(schema, result["structuredContent"]) != nil {
			t.Fatal(args, result)
		}
	}
	if b.calls != 2 {
		t.Fatal("calls", b.calls)
	}
	for _, args := range []any{
		map[string]any{"hours": 24},
		map[string]any{"extend": true},
		map[string]any{"standby_until": "2099-01-01T00:00:00.000Z"},
		map[string]any{"remaining_ms": 86400000},
		map[string]any{"": nil},
		[]any{},
		"24h",
		24,
		json.RawMessage(`null`),
	} {
		e, success := call(map[string]any{"name": "tether_standby_status", "arguments": args})
		if success || e["code"] != -32602.0 {
			t.Fatal("argument accepted:", args, e)
		}
	}
	if b.calls != 2 {
		t.Fatal("a refused argument reached the backend", b.calls)
	}
}

func TestResidentStandbyToolOutputSchema(t *testing.T) {
	b := &standbyShapeBackend{}
	s, call := standbySession(t, b)
	schema, _ := standbyTool(t, s)["outputSchema"].(map[string]any)
	if schema == nil {
		t.Fatal("missing output schema")
	}
	for _, v := range []map[string]any{
		{"schema_version": 1, "standby": "on", "standby_until": "2026-11-19T10:30:01.500Z", "remaining_ms": 0},
		{"schema_version": 1, "standby": "stopping", "standby_until": nil, "remaining_ms": 0},
		{"error": "not_read"}, {"error": "not_reported"}, {"error": "control_ended", "reinitialize_required": true},
	} {
		b.value = v
		result, _ := call(map[string]any{"name": "tether_standby_status", "arguments": map[string]any{}})
		if err := checkOutputShape(schema, result["structuredContent"]); err != nil {
			t.Fatal(v, err)
		}
		_, hasError := v["error"]
		if result["isError"] != hasError {
			t.Fatal(result)
		}
	}
	for _, v := range []map[string]any{
		{"schema_version": 2, "standby": "on", "standby_until": nil, "remaining_ms": 0},
		{"schema_version": 1, "standby": "off", "standby_until": nil, "remaining_ms": 0},
		{"schema_version": 1, "standby": "on", "standby_until": nil, "remaining_ms": -1},
		{"schema_version": 1, "standby": "on", "standby_until": nil},
		{"schema_version": 1, "standby": "on", "standby_until": nil, "remaining_ms": 0, "host": "PRIVATE"},
		// The first draft's expired is not in the output (#412 review).
		{"schema_version": 1, "standby": "stopping", "standby_until": nil, "remaining_ms": 0, "expired": true},
		{"error": "raw OS error"},
	} {
		raw, _ := json.Marshal(v)
		var value any
		json.Unmarshal(raw, &value)
		if checkOutputShape(schema, value) == nil {
			t.Fatal("invalid standby accepted", v)
		}
	}
}

// A backend that cannot read standby offers no such tool.
func TestResidentStandbyToolAbsentWithoutBackend(t *testing.T) {
	s, call := standbySession(t, &residentShapeBackend{})
	if standbyTool(t, s) != nil {
		t.Fatal("offered without a backend")
	}
	if e, success := call(map[string]any{"name": "tether_standby_status", "arguments": map[string]any{}}); success || !reflect.DeepEqual(e["code"], -32602.0) {
		t.Fatal(e)
	}
}
