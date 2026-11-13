package mcpadapter

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fixedBackendFixture struct {
	starts int
	id     string
}

func (b *fixedBackendFixture) FixedStart(_ context.Context, id string) map[string]any {
	b.starts++
	b.id = id
	return fixedResultFixture(id)
}
func (b *fixedBackendFixture) FixedStatus(_ context.Context, id string) map[string]any {
	return fixedResultFixture(id)
}
func (b *fixedBackendFixture) FixedCancel(_ context.Context, id string) map[string]any {
	return fixedResultFixture(id)
}
func TestResidentHTTPOnlyFixedScopeAndOriginalCorrelation(t *testing.T) {
	b := &fixedBackendFixture{}
	s, e := NewResidentHTTPSession(b, time.Second, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	for _, raw := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	} {
		s.Handle([]byte(raw))
	}
	listing := s.Handle([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	var result map[string]any
	if e = json.Unmarshal(listing.Body, &result); e != nil || listing.Error {
		t.Fatal(string(listing.Body), e)
	}
	tools := result["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 3 || tools[0].(map[string]any)["name"] != "tether_fixed_start" {
		t.Fatal(result)
	}
	for _, args := range []string{`{"request_id":"0123456789abcdef0123456789abcdef","command":"true"}`, `{"request_id":"changed"}`, `{"request_id":null}`} {
		reply := s.Handle([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"tether_fixed_start","arguments":` + args + `}}`))
		if !reply.Error {
			t.Fatal("expanded scope accepted", string(reply.Body))
		}
	}
	if b.starts != 0 {
		t.Fatal("invalid args dispatched")
	}
	reply := s.Handle([]byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"tether_fixed_start","arguments":{"request_id":"0123456789abcdef0123456789abcdef"}}}`))
	if reply.Error || reply.Kind != HTTPResponse || b.starts != 1 || b.id != "0123456789abcdef0123456789abcdef" {
		t.Fatal(string(reply.Body), b)
	}
}

// Match the concrete pending gateway result, including command fields that
// have not been observed yet. Cancellation requests do not invent a new phase.
func fixedResultFixture(id string) map[string]any {
	return map[string]any{"request_id": id, "phase": "pending", "profile": "", "command_id": "", "command_phase": "", "exit": map[string]any{"known": false}, "stop": "none", "terminal": false, "command_cleanup": "", "owner_cleanup": "pending", "gateway_cleanup": "pending", "chunks": []any{}, "next_cursor": 0, "error": ""}
}

func TestResidentDiscoveryHasCatalogButNoAuthority(t *testing.T) {
	s, err := NewResidentDiscoveryHTTPSession(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.backend != nil || s.resident != nil {
		t.Fatal("discovery retained backend")
	}
	if !s.Handle([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)).Error {
		t.Fatal("premature catalog")
	}
	reply := s.Handle([]byte(`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`))
	if !reply.Initialized || !strings.Contains(string(reply.Body), "Resident catalog only") {
		t.Fatal(string(reply.Body))
	}
	s.Handle([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	reply = s.Handle([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`))
	var result map[string]any
	if json.Unmarshal(reply.Body, &result) != nil || reply.Error {
		t.Fatal(string(reply.Body))
	}
	tools := result["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 8 {
		t.Fatal(len(tools))
	}
	expected, _ := json.Marshal(residentCatalog(true, true, true, true, true))
	actual, _ := json.Marshal(tools)
	if !reflect.DeepEqual(expected, actual) {
		t.Fatal("catalog drift")
	}
	for _, tool := range tools {
		name := tool.(map[string]any)["name"].(string)
		reply = s.Handle([]byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`))
		if !reply.Error || !strings.Contains(string(reply.Body), "Resident catalog only") {
			t.Fatal(name, string(reply.Body))
		}
	}
	if s.backend != nil || s.resident != nil {
		t.Fatal("discovery acquired backend")
	}
	s.Close()
	if s.ctx.Err() == nil || s.backend != nil || s.resident != nil {
		t.Fatal("catalog close did not cancel or acquired authority")
	}
}
