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

type residentShapeBackend struct{ value map[string]any }

func (b *residentShapeBackend) FixedStart(context.Context, string) map[string]any  { return b.value }
func (b *residentShapeBackend) FixedStatus(context.Context, string) map[string]any { return b.value }
func (b *residentShapeBackend) FixedCancel(context.Context, string) map[string]any { return b.value }

type residentReleaseShapeBackend struct{ *residentShapeBackend }

func (b *residentReleaseShapeBackend) FixedRelease(context.Context, string) map[string]any {
	return b.value
}

func residentGolden(t *testing.T) map[string]map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/resident-output.json")
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]map[string]any
	if err = json.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	return values
}

func TestResidentOutputSchemasMatchGatewayGoldenResults(t *testing.T) {
	values := residentGolden(t)
	for _, release := range []bool{false, true} {
		b := &residentShapeBackend{}
		var backend mcpadapter.ResidentBackend = b
		if release {
			backend = &residentReleaseShapeBackend{b}
		}
		s, err := mcpadapter.NewResidentHTTPSession(backend, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		request := func(method string, params any) map[string]any {
			t.Helper()
			message := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
			if params != nil {
				message["params"] = params
			}
			raw, _ := json.Marshal(message)
			reply := s.Handle(raw)
			if reply.Error {
				t.Fatalf("%s: %s", method, reply.Body)
			}
			var value map[string]any
			if json.Unmarshal(reply.Body, &value) != nil {
				t.Fatal("invalid reply")
			}
			return value["result"].(map[string]any)
		}
		request("initialize", initParams(mcpadapter.ProtocolVersion))
		s.Handle([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
		catalog := request("tools/list", nil)["tools"].([]any)
		want := 3
		if release {
			want = 4
		}
		if len(catalog) != want {
			t.Fatal("optional release changed", catalog)
		}
		for _, entry := range catalog {
			tool := entry.(map[string]any)
			name := tool["name"].(string)
			schema, ok := tool["outputSchema"].(map[string]any)
			if !ok || schema["type"] != "object" {
				t.Fatalf("%s lacks object outputSchema", name)
			}
			for label, value := range values {
				if name == "rescue_fixed_release" && value["request_id"] != nil && value["result_released"] != true {
					continue
				}
				b.value = value
				result := request("tools/call", map[string]any{"name": name, "arguments": map[string]any{"request_id": strings.Repeat("1", 32)}})
				if err := checkOutputShape(schema, result["structuredContent"]); err != nil {
					t.Fatalf("%s/%s: %v", name, label, err)
				}
				content := result["content"].([]any)[0].(map[string]any)
				var mirrored map[string]any
				if json.Unmarshal([]byte(content["text"].(string)), &mirrored) != nil || !reflect.DeepEqual(mirrored, result["structuredContent"]) {
					t.Fatal("text mirror changed")
				}
				if result["isError"] != (value["error"] != "") {
					t.Fatal("error meaning changed", label, result)
				}
			}
			if name != "rescue_fixed_release" {
				assertResidentSchemaBounds(t, schema)
			}
			for _, mutation := range []func(map[string]any){
				func(v map[string]any) { delete(v, "owner_cleanup") },
				func(v map[string]any) { v["endpoint"] = "PRIVATE" },
				func(v map[string]any) { v["profile"] = "sandbox" },
				func(v map[string]any) { v["command_cleanup"] = "collected" },
				func(v map[string]any) { v["exit"] = map[string]any{"known": false, "code": 0.0} },
				func(v map[string]any) { v["next_cursor"] = -1.0 },
				func(v map[string]any) { v["result_released"] = false },
				func(v map[string]any) { v["phase"] = "pending" },
				func(v map[string]any) { v["owner_cleanup"] = "unknown" },
				func(v map[string]any) { v["chunks"] = residentGolden(t)["running"]["chunks"] },
			} {
				v := residentGolden(t)["released_before_command"]
				mutation(v)
				if checkOutputShape(schema, v) == nil {
					t.Fatalf("%s accepted malformed result %v", name, v)
				}
			}
		}
	}
}

func assertResidentSchemaBounds(t *testing.T, schema map[string]any) {
	t.Helper()
	value := residentGolden(t)["running"]
	chunk := value["chunks"].([]any)[0].(map[string]any)
	chunk["data"] = strings.Repeat("AAAA", 5461) + "AA==" // base64 of exactly 16384 bytes
	value["chunks"] = []any{chunk}
	if err := checkOutputShape(schema, value); err != nil {
		t.Fatal("boundary rejected", err)
	}
	chunk["data"] = strings.Repeat("AAAA", 5463)
	if checkOutputShape(schema, value) == nil {
		t.Fatal("oversized chunk accepted")
	}
	chunk["data"] = "%%%="
	if checkOutputShape(schema, value) == nil {
		t.Fatal("invalid base64 shape accepted")
	}
	chunk["data"] = ""
	chunks := make([]any, 128)
	for i := range chunks {
		chunks[i] = chunk
	}
	value["chunks"] = chunks
	if err := checkOutputShape(schema, value); err != nil {
		t.Fatal("chunk-count boundary rejected", err)
	}
	value["chunks"] = append(chunks, chunk)
	if checkOutputShape(schema, value) == nil {
		t.Fatal("too many chunks accepted")
	}
	for _, label := range []string{"busy_or_refused", "control_ended"} {
		value := residentGolden(t)[label]
		delete(value, "owner_cleanup")
		if checkOutputShape(schema, value) == nil {
			t.Fatal("incomplete error accepted", label)
		}
	}
	value = residentGolden(t)["control_ended"]
	value["reinitialize_required"] = false
	if checkOutputShape(schema, value) == nil {
		t.Fatal("false reinitialize flag accepted")
	}
}
