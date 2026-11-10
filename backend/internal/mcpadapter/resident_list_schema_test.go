package mcpadapter_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

type listShapeBackend struct{ residentShapeBackend }

func (b *listShapeBackend) FixedList(context.Context, string, int) map[string]any { return b.value }
func TestResidentListOutputSchemaAndBounds(t *testing.T) {
	b := &listShapeBackend{}
	s, err := mcpadapter.NewResidentHTTPSession(b, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	request := func(method string, params any) map[string]any {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		reply := s.Handle(raw)
		if reply.Error {
			t.Fatal(string(reply.Body))
		}
		var v map[string]any
		if json.Unmarshal(reply.Body, &v) != nil {
			t.Fatal("bad JSON")
		}
		return v["result"].(map[string]any)
	}
	request("initialize", initParams(mcpadapter.ProtocolVersion))
	s.Handle([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	var schema map[string]any
	for _, entry := range request("tools/list", map[string]any{})["tools"].([]any) {
		tool := entry.(map[string]any)
		if tool["name"] == "tether_fixed_list" {
			schema = tool["outputSchema"].(map[string]any)
		}
	}
	if schema == nil {
		t.Fatal("missing list schema")
	}
	row := map[string]any{"request_id": strings.Repeat("a", 32), "phase": "unknown", "terminal": false, "command_cleanup": "unknown", "owner_cleanup": "unknown", "gateway_cleanup": "unknown", "error": "start_uncertain", "result_released": false}
	rows := make([]any, 32)
	for i := range rows {
		rows[i] = row
	}
	valid := []map[string]any{
		{"schema_version": 1, "executions": []any{}, "next_cursor": ""},
		{"schema_version": 1, "executions": rows, "next_cursor": strings.Repeat("f", 32)},
		{"error": "not_read"}, {"error": "invalid"},
	}
	raw, err := os.ReadFile("testdata/resident-list-output.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]any
	if json.Unmarshal(raw, &golden) != nil {
		t.Fatal("bad golden")
	}
	valid = append(valid, golden)
	for _, v := range valid {
		b.value = v
		result := request("tools/call", map[string]any{"name": "tether_fixed_list", "arguments": map[string]any{"cursor": "", "limit": 32}})
		if err := checkOutputShape(schema, result["structuredContent"]); err != nil {
			t.Fatal(err)
		}
		_, hasError := v["error"]
		if result["isError"] != hasError {
			t.Fatal(result)
		}
	}
	for _, v := range []map[string]any{
		{"schema_version": 2.0, "executions": []any{}, "next_cursor": ""},
		{"schema_version": 1.0, "executions": append(rows, row), "next_cursor": ""},
		{"schema_version": 1.0, "executions": []any{}, "next_cursor": "bad"},
		{"schema_version": 1.0, "executions": []any{}, "next_cursor": "", "host": "PRIVATE"},
		{"error": "raw OS error"},
	} {
		raw, _ := json.Marshal(v)
		var jsonValue any
		json.Unmarshal(raw, &jsonValue)
		if checkOutputShape(schema, jsonValue) == nil {
			t.Fatal("invalid list accepted", v)
		}
	}
	row["chunks"] = []any{"PRIVATE"}
	raw, _ = json.Marshal(map[string]any{"schema_version": 1, "executions": []any{row}, "next_cursor": ""})
	var jsonValue any
	json.Unmarshal(raw, &jsonValue)
	if checkOutputShape(schema, jsonValue) == nil {
		t.Fatal("payload field accepted")
	}
}
