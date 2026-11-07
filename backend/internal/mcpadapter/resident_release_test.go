package mcpadapter

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type releaseBackendFixture struct {
	fixedBackendFixture
	releases int
}

func (b *releaseBackendFixture) FixedRelease(_ context.Context, id string) map[string]any {
	b.releases++
	v := fixedResultFixture(id)
	v["phase"], v["owner_cleanup"], v["gateway_cleanup"] = "finished", "confirmed", "confirmed"
	v["result_released"] = true
	return v
}
func TestResidentReleaseToolIsExplicitOptionalAndStrict(t *testing.T) {
	b := &releaseBackendFixture{}
	s, e := NewResidentHTTPSession(b, time.Second, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.Handle([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`))
	s.Handle([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	list := s.Handle([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	var v map[string]any
	if e = json.Unmarshal(list.Body, &v); e != nil {
		t.Fatal(e)
	}
	tools := v["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 4 || tools[3].(map[string]any)["name"] != "rescue_fixed_release" {
		t.Fatal(v)
	}
	for _, args := range []string{`{"request_id":"0123456789abcdef0123456789abcdef","confirmed":true}`, `{"request_id":"invalid"}`} {
		r := s.Handle([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"rescue_fixed_release","arguments":` + args + `}}`))
		if !r.Error || b.releases != 0 {
			t.Fatal(string(r.Body), b.releases)
		}
	}
	r := s.Handle([]byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"rescue_fixed_release","arguments":{"request_id":"0123456789abcdef0123456789abcdef"}}}`))
	if r.Error || b.releases != 1 || b.starts != 0 {
		t.Fatal(string(r.Body), b.releases, b.starts)
	}
}
