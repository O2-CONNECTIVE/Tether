package mcpadapter

import (
	"testing"
	"time"
)

func TestDiscoveryIsExplicitAndNeverHasBackend(t *testing.T) {
	if _, err := NewHTTPSession(HTTPConfig{}); err == nil {
		t.Fatal("implicit nil backend allowed")
	}
	if _, err := NewDiscoveryHTTPSession(-time.Second); err == nil {
		t.Fatal("invalid timeout allowed")
	}
	s, err := NewDiscoveryHTTPSession(0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := s.Handle([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`))
	if !r.Initialized {
		t.Fatal("initialize refused")
	}
	s.Handle([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if r = s.Handle([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)); r.Kind != HTTPResponse || r.Error {
		t.Fatal("catalog refused")
	}
	r = s.Handle([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"rescue_start","arguments":{"request_id":"fixture","command":"fixed","timeout_ms":1000}}}`))
	if r.Kind != HTTPRequiresBinding || s.backend != nil || len(s.calls) != 0 || len(s.slots) != 0 {
		t.Fatal("unbound request reached backend state")
	}
}
