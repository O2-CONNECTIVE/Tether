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

type getExecutionShapeBackend struct{ residentShapeBackend }

func (b *getExecutionShapeBackend) FixedGetExecution(context.Context, string) map[string]any {
	return b.value
}

func TestResidentGetExecutionSchemaAndSharedGolden(t *testing.T) {
	b := &getExecutionShapeBackend{}
	s, err := mcpadapter.NewResidentHTTPSession(b, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	request := func(method string, params any) map[string]any {
		t.Helper()
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
		if tool["name"] == "rescue_fixed_get_execution" {
			schema = tool["outputSchema"].(map[string]any)
			input = tool["inputSchema"].(map[string]any)
		}
	}
	if schema == nil || input == nil {
		t.Fatal("missing metadata schema")
	}
	raw, err := os.ReadFile("testdata/resident-get-execution.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]map[string]any
	if json.Unmarshal(raw, &golden) != nil {
		t.Fatal("bad golden")
	}
	values := []map[string]any{}
	for _, v := range golden {
		values = append(values, v)
	}
	for _, code := range []string{"not_read", "invalid", "not_found"} {
		values = append(values, map[string]any{"error": code})
	}
	for _, v := range values {
		b.value = v
		result := request("tools/call", map[string]any{"name": "rescue_fixed_get_execution", "arguments": map[string]any{"request_id": strings.Repeat("1", 32)}})
		if err := checkOutputShape(schema, result["structuredContent"]); err != nil {
			t.Fatal(err)
		}
		_, readError := v["error"]
		if result["isError"] != readError {
			t.Fatal("work outcome changed metadata read success", result)
		}
		var mirror any
		text := result["content"].([]any)[0].(map[string]any)["text"].(string)
		if json.Unmarshal([]byte(text), &mirror) != nil || !reflect.DeepEqual(mirror, result["structuredContent"]) {
			t.Fatal("text mirror differs")
		}
	}
	copyGolden := func(label string) map[string]any {
		var values map[string]map[string]any
		json.Unmarshal(raw, &values)
		return values[label]
	}
	for _, label := range []string{"pending", "released_before_command"} {
		for field := range golden[label] {
			v := copyGolden(label)
			delete(v, field)
			if checkOutputShape(schema, v) == nil {
				t.Fatal("missing field accepted", label, field)
			}
		}
	}
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { v["schema_version"] = 2.0 },
		func(v map[string]any) { v["request_id"] = "bad" },
		func(v map[string]any) { v["profile"] = "windows_guest" },
		func(v map[string]any) { v["command_id"] = strings.Repeat("A", 32) },
		func(v map[string]any) { v["command_phase"] = "invented" },
		func(v map[string]any) { v["command_cleanup"] = "collected" },
		func(v map[string]any) { v["stop"] = "raw error" },
		func(v map[string]any) { v["terminal"] = "true" },
		func(v map[string]any) { v["work_error"] = "raw error" },
		func(v map[string]any) { v["result_released"] = "true" },
		func(v map[string]any) { v["phase"] = "running" },
		func(v map[string]any) { v["owner_cleanup"] = "pending" },
		func(v map[string]any) { v["gateway_cleanup"] = "unknown" },
		func(v map[string]any) { v["collection_ended"] = false },
		func(v map[string]any) { v["exit"] = map[string]any{"known": false, "code": 0.0} },
		func(v map[string]any) { v["exit"] = map[string]any{"known": true} },
		func(v map[string]any) { v["exit"] = map[string]any{"known": true, "code": 2147483648.0} },
		func(v map[string]any) { v["exit"] = map[string]any{"known": true, "code": -2147483649.0} },
		func(v map[string]any) { v["exit"] = map[string]any{"known": true, "code": 1.5} },
	} {
		v := copyGolden("released_before_command")
		mutate(v)
		if checkOutputShape(schema, v) == nil {
			t.Fatal("invalid output accepted", v)
		}
	}
	for _, field := range []string{"chunks", "next_cursor", "source_output_end", "source_truncated", "source_dropped", "output_complete", "input_supported", "host", "generation", "error"} {
		v := copyGolden("finished")
		v[field] = true
		if checkOutputShape(schema, v) == nil {
			t.Fatal("extra output field accepted", field)
		}
	}
	for _, code := range []string{"result_released", "request_generation_mismatch", "raw error"} {
		if checkOutputShape(schema, map[string]any{"error": code}) == nil {
			t.Fatal("unexpected read error accepted", code)
		}
	}
	for _, code := range []float64{-2147483648, 2147483647} {
		v := copyGolden("finished")
		v["exit"] = map[string]any{"known": true, "code": code}
		if err := checkOutputShape(schema, v); err != nil {
			t.Fatal("known exit boundary rejected", err)
		}
	}
	if err := checkOutputShape(input, map[string]any{"request_id": strings.Repeat("1", 32)}); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{{}, {"request_id": nil}, {"request_id": "bad"}, {"request_id": strings.Repeat("1", 32), "host_id": "other"}} {
		if checkOutputShape(input, args) == nil {
			t.Fatal("invalid input accepted", args)
		}
	}
}
