package mcpadapter_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

type readOutputShapeBackend struct{ residentShapeBackend }

func (b *readOutputShapeBackend) FixedReadOutput(context.Context, string, uint64, int) map[string]any {
	return b.value
}
func TestResidentReadOutputSchemaAndSharedGolden(t *testing.T) {
	b := &readOutputShapeBackend{}
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
	var schema, input map[string]any
	for _, entry := range request("tools/list", map[string]any{})["tools"].([]any) {
		tool := entry.(map[string]any)
		if tool["name"] == "rescue_fixed_read_output" {
			schema = tool["outputSchema"].(map[string]any)
			input = tool["inputSchema"].(map[string]any)
		}
	}
	if schema == nil || input == nil {
		t.Fatal("missing read schema")
	}
	raw, err := os.ReadFile("testdata/resident-read-output.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]any
	if json.Unmarshal(raw, &golden) != nil {
		t.Fatal("bad golden")
	}
	values := []map[string]any{golden}
	for _, code := range []string{"not_read", "invalid", "not_found", "result_released", "cursor_ahead", "output_unavailable"} {
		values = append(values, map[string]any{"error": code})
	}
	for _, v := range values {
		b.value = v
		result := request("tools/call", map[string]any{"name": "rescue_fixed_read_output", "arguments": map[string]any{"request_id": strings.Repeat("1", 32), "cursor": 0, "max_bytes": 4096}})
		if err := checkOutputShape(schema, result["structuredContent"]); err != nil {
			t.Fatal(err)
		}
		_, hasError := v["error"]
		if result["isError"] != hasError {
			t.Fatal(result)
		}
		var mirror any
		text := result["content"].([]any)[0].(map[string]any)["text"].(string)
		if json.Unmarshal([]byte(text), &mirror) != nil || !reflect.DeepEqual(mirror, result["structuredContent"]) {
			t.Fatal("text mirror differs")
		}
	}
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { delete(v, "collection_ended") },
		func(v map[string]any) { v["schema_version"] = 2.0 },
		func(v map[string]any) { v["next_cursor"] = -1.0 },
		func(v map[string]any) { v["cursor"] = 9007199254740992.0 },
		func(v map[string]any) { v["has_more"] = "false" },
		func(v map[string]any) { v["source_dropped"] = -1.0 },
		func(v map[string]any) { v["source_truncated"] = "unknown" },
		func(v map[string]any) { v["work_error"] = "raw error" },
		func(v map[string]any) { v["command_phase"] = "invented" },
		func(v map[string]any) { v["output_complete"] = true },
		func(v map[string]any) { v["chunks"].([]any)[0].(map[string]any)["data"] = "not base64!" },
		func(v map[string]any) { v["chunks"].([]any)[0].(map[string]any)["data"] = strings.Repeat("A", 5468) },
		func(v map[string]any) {
			row := v["chunks"].([]any)[0]
			rows := make([]any, 129)
			for i := range rows {
				rows[i] = row
			}
			v["chunks"] = rows
		},
	} {
		var v map[string]any
		json.Unmarshal(raw, &v)
		mutate(v)
		if checkOutputShape(schema, v) == nil {
			t.Fatal("invalid output accepted", v)
		}
	}
	for _, cursor := range []float64{0, 9007199254740991} {
		args := map[string]any{"request_id": strings.Repeat("1", 32), "cursor": cursor, "max_bytes": 4096.0}
		if err := checkOutputShape(input, args); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range []map[string]any{
		{"request_id": strings.Repeat("1", 32), "cursor": -1.0, "max_bytes": 1.0},
		{"request_id": strings.Repeat("1", 32), "cursor": 9007199254740992.0, "max_bytes": 1.0},
		{"request_id": strings.Repeat("1", 32), "cursor": 0.0, "max_bytes": 4097.0},
		{"request_id": strings.Repeat("1", 32), "cursor": 0.0, "max_bytes": 1.0, "command": "x"},
	} {
		if checkOutputShape(input, args) == nil {
			t.Fatal("invalid input accepted", args)
		}
	}
}
