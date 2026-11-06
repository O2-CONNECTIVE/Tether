package mcpadapter

import (
	"context"
	"encoding/json"
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
	return map[string]any{"request_id": id, "phase": "pending", "owner_cleanup": "pending"}
}
func (b *fixedBackendFixture) FixedStatus(_ context.Context, id string) map[string]any {
	return map[string]any{"request_id": id, "phase": "pending"}
}
func (b *fixedBackendFixture) FixedCancel(_ context.Context, id string) map[string]any {
	return map[string]any{"request_id": id, "phase": "stopping"}
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
	if len(tools) != 3 || tools[0].(map[string]any)["name"] != "rescue_fixed_start" {
		t.Fatal(result)
	}
	for _, args := range []string{`{"request_id":"0123456789abcdef0123456789abcdef","command":"true"}`, `{"request_id":"changed"}`, `{"request_id":null}`} {
		reply := s.Handle([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"rescue_fixed_start","arguments":` + args + `}}`))
		if !reply.Error {
			t.Fatal("expanded scope accepted", string(reply.Body))
		}
	}
	if b.starts != 0 {
		t.Fatal("invalid args dispatched")
	}
	reply := s.Handle([]byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"rescue_fixed_start","arguments":{"request_id":"0123456789abcdef0123456789abcdef"}}}`))
	if reply.Error || reply.Kind != HTTPResponse || b.starts != 1 || b.id != "0123456789abcdef0123456789abcdef" {
		t.Fatal(string(reply.Body), b)
	}
}
