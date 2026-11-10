package mcpadapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type listBackendFixture struct {
	fixedBackendFixture
	calls  int
	cursor string
	limit  int
}

func (b *listBackendFixture) FixedList(_ context.Context, cursor string, limit int) map[string]any {
	b.calls++
	b.cursor, b.limit = cursor, limit
	return map[string]any{"schema_version": 1, "executions": []any{}, "next_cursor": ""}
}
func TestResidentListIsOptionalBoundedAndReadOnly(t *testing.T) {
	b := &listBackendFixture{}
	s, err := NewResidentHTTPSession(b, time.Second, nil)
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
	if len(catalog) != 4 || catalog[3].(map[string]any)["name"] != "tether_fixed_list" {
		t.Fatal("missing optional list", v)
	}
	call := func(args string) HTTPReply {
		return s.Handle([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"tether_fixed_list","arguments":` + args + `}}`))
	}
	for _, args := range []string{`{}`, `{"cursor":"","limit":0}`, `{"cursor":"","limit":33}`, `{"cursor":"","limit":1.5}`, `{"cursor":"","limit":null}`, `{"cursor":null,"limit":1}`, `{"cursor":"invalid","limit":1}`, `{"cursor":"","limit":1,"host_id":"other"}`, `{"cursor":"","limit":1,"limit":2}`, `{"Cursor":"","limit":1}`} {
		if reply := call(args); !reply.Error || b.calls != 0 {
			t.Fatalf("invalid list dispatched %s: %s", args, reply.Body)
		}
	}
	for _, cursor := range []string{"", strings.Repeat("a", 32)} {
		raw, _ := json.Marshal(map[string]any{"cursor": cursor, "limit": 32})
		reply := call(string(raw))
		if reply.Error || b.cursor != cursor || b.limit != 32 || b.starts != 0 {
			t.Fatalf("list: %s", reply.Body)
		}
	}
	if b.calls != 2 {
		t.Fatal(b.calls)
	}
}
