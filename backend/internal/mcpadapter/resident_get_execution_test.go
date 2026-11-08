package mcpadapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type getExecutionBackendFixture struct {
	fixedBackendFixture
	calls int
}

func (b *getExecutionBackendFixture) FixedGetExecution(_ context.Context, id string) map[string]any {
	b.calls++
	b.id = id
	return map[string]any{"error": "not_found"}
}

func TestResidentGetExecutionOptionalAndStrictArguments(t *testing.T) {
	b := &getExecutionBackendFixture{}
	for _, backend := range []ResidentBackend{b, &fixedBackendFixture{}, &releaseBackendFixture{}} {
		s, err := NewResidentHTTPSession(backend, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		s.Handle([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`))
		s.Handle([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
		listing := s.Handle([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
		var v map[string]any
		if json.Unmarshal(listing.Body, &v) != nil {
			t.Fatal(string(listing.Body))
		}
		catalog := v["result"].(map[string]any)["tools"].([]any)
		call := func(args string) HTTPReply {
			return s.Handle([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"rescue_fixed_get_execution","arguments":` + args + `}}`))
		}
		valid := `{"request_id":"11111111111111111111111111111111"}`
		if backend != b {
			want := 3
			if _, release := backend.(ResidentReleaseBackend); release {
				want = 4
			}
			if len(catalog) != want || !call(valid).Error {
				t.Fatal("unsupported metadata read advertised or accepted")
			}
			continue
		}
		if len(catalog) != 4 || catalog[3].(map[string]any)["name"] != "rescue_fixed_get_execution" {
			t.Fatal(catalog)
		}
		invalid := []string{`{}`, `null`, `[]`, `{"request_id":"11111111111111111111111111111111","request_id":"22222222222222222222222222222222"}`}
		for _, field := range []string{"host_id", "principal", "generation", "command", "cursor", "approved"} {
			invalid = append(invalid, strings.TrimSuffix(valid, "}")+`,"`+field+`":"other"}`)
		}
		for _, bad := range []string{`null`, `true`, `0`, `{}`, `[]`, `""`, `"bad"`, `"` + strings.Repeat("A", 32) + `"`, `"` + strings.Repeat("a", 10000) + `"`} {
			invalid = append(invalid, `{"request_id":`+bad+`}`)
		}
		for _, args := range invalid {
			if reply := call(args); !reply.Error || b.calls != 0 {
				t.Fatalf("invalid args dispatched: %s", reply.Body)
			}
		}
		reply := call(valid)
		if reply.Error || b.calls != 1 || b.id != strings.Repeat("1", 32) || b.starts != 0 {
			t.Fatal(string(reply.Body), b)
		}
	}
}
