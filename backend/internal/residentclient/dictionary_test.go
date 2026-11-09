package residentclient

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

func metadataFixture() map[string]any {
	v := fixtureValue(testID)
	delete(v, "chunks")
	delete(v, "next_cursor")
	delete(v, "error")
	v["schema_version"] = 1
	v["collection_ended"] = false
	v["result_released"] = false
	v["work_error"] = "start_uncertain"
	return v
}
func outputFixture() map[string]any {
	return map[string]any{"schema_version": 1, "request_id": testID, "cursor": 0, "next_cursor": 4, "observed_end": 7, "gap_bytes": 2, "chunks": []any{map[string]any{"stream": "stdout", "offset": 2, "encoding": "base64", "data": "AP8="}}, "has_more": true, "collection_ended": true, "phase": "unknown", "command_phase": "start_unknown", "terminal": false, "command_cleanup": "unknown", "owner_cleanup": "unknown", "gateway_cleanup": "unknown", "source_output_end": uint64(maxInteger + 1), "source_truncated": true, "source_dropped": uint64(2), "work_error": "output_limit"}
}
func TestDictionaryMetadataAndReadPreserveIndependentFacts(t *testing.T) {
	for _, pair := range []struct {
		op string
		v  map[string]any
	}{{"get_execution", metadataFixture()}, {"read_output", outputFixture()}} {
		out, e := decodeDictionary(fixtureTool(pair.v), pair.op, testID, "", 0, 2)
		if e != nil || !strings.Contains(string(out), `"work_error"`) {
			t.Fatal(pair.op, e)
		}
	}
	v := metadataFixture()
	v["result_released"] = true
	v["collection_ended"] = true
	v["phase"] = "finished"
	v["owner_cleanup"] = "confirmed"
	v["gateway_cleanup"] = "confirmed"
	if _, e := decodeDictionary(fixtureTool(v), "get_execution", testID, "", 0, 0); e != nil {
		t.Fatal("precommand release must not invent terminal", e)
	}
	for _, code := range []string{"not_found", "result_released", "cursor_ahead", "output_unavailable", "not_read", "invalid"} {
		if _, e := decodeDictionary(fixtureTool(map[string]any{"error": code}), "read_output", testID, "", 0, 2); e != nil {
			t.Fatal(code)
		}
	}
}
func TestDictionaryRejectsInvalidOutputAndMetadata(t *testing.T) {
	for _, change := range []func(map[string]any){
		func(v map[string]any) { v["gap_bytes"] = 1 }, func(v map[string]any) { v["has_more"] = false }, func(v map[string]any) { v["cursor"] = 1 }, func(v map[string]any) { v["next_cursor"] = 8 }, func(v map[string]any) { v["request_id"] = strings.Repeat("a", 32) }, func(v map[string]any) { v["chunks"] = nil }, func(v map[string]any) { v["private"] = "secret" }, func(v map[string]any) { v["source_dropped"] = -1 },
	} {
		v := outputFixture()
		change(v)
		if _, e := decodeDictionary(fixtureTool(v), "read_output", testID, "", 0, 2); e == nil {
			t.Fatal("bad output accepted")
		}
	}
	if _, e := decodeDictionary(fixtureTool(outputFixture()), "read_output", testID, "", 0, 1); e == nil {
		t.Fatal("byte budget exceeded")
	}
	for _, change := range []func(map[string]any){func(v map[string]any) { v["result_released"] = true }, func(v map[string]any) { v["exit"] = map[string]any{"known": false, "code": 0} }, func(v map[string]any) { v["chunks"] = []any{} }} {
		v := metadataFixture()
		change(v)
		if _, e := decodeDictionary(fixtureTool(v), "get_execution", testID, "", 0, 0); e == nil {
			t.Fatal("bad metadata accepted")
		}
	}
	raw := strings.Replace(string(fixtureTool(metadataFixture())), `"isError":false`, `"isError":true`, 1)
	if _, e := decodeDictionary([]byte(raw), "get_execution", testID, "", 0, 0); e == nil {
		t.Fatal("work failure became read failure")
	}
	raw = strings.Replace(string(fixtureTool(metadataFixture())), `start_uncertain`, `output_limit`, 1)
	if _, e := decodeDictionary([]byte(raw), "get_execution", testID, "", 0, 0); e == nil {
		t.Fatal("mirror mismatch")
	}
}
func TestDictionaryListOrderingAndPagination(t *testing.T) {
	item := map[string]any{"request_id": testID, "phase": "unknown", "terminal": false, "command_cleanup": "unknown", "owner_cleanup": "unknown", "gateway_cleanup": "unknown", "error": "start_uncertain", "result_released": false}
	v := map[string]any{"schema_version": 1, "executions": []any{item}, "next_cursor": testID}
	if _, e := decodeDictionary(fixtureTool(v), "list", "", "", 0, 1); e != nil {
		t.Fatal(e)
	}
	for _, after := range []string{testID, strings.Repeat("f", 32)} {
		if _, e := decodeDictionary(fixtureTool(v), "list", "", after, 0, 1); e == nil {
			t.Fatal("nonadvancing page")
		}
	}
	v["executions"] = []any{item, item}
	if _, e := decodeDictionary(fixtureTool(v), "list", "", "", 0, 2); e == nil {
		t.Fatal("duplicate IDs")
	}
	v["executions"] = []any{}
	v["next_cursor"] = ""
	if _, e := decodeDictionary(fixtureTool(v), "list", "", "", 0, 32); e != nil {
		t.Fatal("empty live view")
	}
}

func TestDictionaryHTTPUsesOriginalSIDAndDiscoversOnce(t *testing.T) {
	for _, supported := range []bool{true, false} {
		t.Run(map[bool]string{true: "supported", false: "legacy"}[supported], func(t *testing.T) {
			var mu sync.Mutex
			methods := []string{}
			calls := 0
			bad := false
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Header.Get("Authorization") != "Bearer anonymous-fixture" {
					bad = true
				}
				if r.Method == http.MethodDelete {
					methods = append(methods, "DELETE")
					if r.Header.Get(agentgateway.HeaderSession) != testSID {
						bad = true
					}
					w.WriteHeader(204)
					return
				}
				var msg struct {
					ID     uint64          `json:"id"`
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if json.NewDecoder(r.Body).Decode(&msg) != nil {
					bad = true
					w.WriteHeader(400)
					return
				}
				methods = append(methods, msg.Method)
				if msg.Method != "initialize" && r.Header.Get(agentgateway.HeaderSession) != testSID {
					bad = true
				}
				if msg.Method == "notifications/initialized" {
					w.WriteHeader(202)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				var value any
				switch msg.Method {
				case "initialize":
					w.Header().Set(agentgateway.HeaderSession, testSID)
					value = map[string]any{"protocolVersion": mcpadapter.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]any{"name": "rescue-mcp", "version": "0.0.0-unreleased"}, "instructions": "fixture"}
				case "tools/list":
					tools := []any{}
					if supported {
						for _, op := range []string{"list", "get_execution", "read_output"} {
							tools = append(tools, map[string]any{"name": "rescue_fixed_" + op, "inputSchema": map[string]any{"type": "object"}})
						}
					}
					value = map[string]any{"tools": tools}
				case "tools/call":
					calls++
					var p struct {
						Name      string                     `json:"name"`
						Arguments map[string]json.RawMessage `json:"arguments"`
					}
					if json.Unmarshal(msg.Params, &p) != nil {
						bad = true
					}
					switch p.Name {
					case "rescue_fixed_list":
						if len(p.Arguments) != 2 || string(p.Arguments["cursor"]) != `""` || string(p.Arguments["limit"]) != "1" {
							bad = true
						}
						value = json.RawMessage(fixtureTool(map[string]any{"schema_version": 1, "executions": []any{}, "next_cursor": ""}))
					case "rescue_fixed_get_execution":
						if len(p.Arguments) != 1 || text(p.Arguments["request_id"]) != testID {
							bad = true
						}
						value = json.RawMessage(fixtureTool(metadataFixture()))
					case "rescue_fixed_read_output":
						if len(p.Arguments) != 3 || text(p.Arguments["request_id"]) != testID || string(p.Arguments["cursor"]) != "0" || string(p.Arguments["max_bytes"]) != "2" {
							bad = true
						}
						value = json.RawMessage(fixtureTool(outputFixture()))
					default:
						bad = true
					}
				default:
					bad = true
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": value})
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			c, e := newClient(server.URL, []byte("anonymous-fixture"), roots)
			if e != nil || c.open(context.Background()) != nil {
				t.Fatal("open")
			}
			for _, call := range []func() (json.RawMessage, error){func() (json.RawMessage, error) { return c.List(context.Background(), "", 1) }, func() (json.RawMessage, error) { return c.GetExecution(context.Background(), testID) }, func() (json.RawMessage, error) { return c.ReadOutput(context.Background(), testID, 0, 2) }} {
				v, e := call()
				if e != nil {
					t.Fatal(e)
				}
				if !supported && string(v) != `{"error":"unsupported"}` {
					t.Fatal("missing optional tool")
				}
			}
			if c.Close(context.Background()) != nil {
				t.Fatal("close")
			}
			mu.Lock()
			defer mu.Unlock()
			if bad || (supported && calls != 3) || (!supported && calls != 0) || strings.Count(strings.Join(methods, ","), "tools/list") != 1 {
				t.Fatal("binding/discovery/call mismatch", methods, calls)
			}
		})
	}
}

func TestDictionarySharedServiceFixtures(t *testing.T) {
	raw, e := os.ReadFile("../mcpadapter/testdata/resident-get-execution.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases map[string]map[string]any
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("metadata fixture")
	}
	for name, v := range cases {
		id, _ := v["request_id"].(string)
		if id == "" {
			id = testID
		}
		if _, e := decodeDictionary(fixtureTool(v), "get_execution", id, "", 0, 0); e != nil {
			t.Fatal(name, e)
		}
	}
	raw, e = os.ReadFile("../mcpadapter/testdata/resident-read-output.json")
	if e != nil {
		t.Fatal(e)
	}
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		t.Fatal("output fixture")
	}
	if _, e := decodeDictionary(fixtureTool(v), "read_output", v["request_id"].(string), "", 0, 4096); e != nil {
		t.Fatal(e)
	}
}
