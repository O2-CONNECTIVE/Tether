package mcpadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type readOutputBackendFixture struct {
	fixedBackendFixture
	calls    int
	cursor   uint64
	maxBytes int
}

func (b *readOutputBackendFixture) FixedReadOutput(_ context.Context, id string, cursor uint64, maxBytes int) map[string]any {
	b.calls++
	b.id, b.cursor, b.maxBytes = id, cursor, maxBytes
	return map[string]any{"error": "not_found"}
}
func TestResidentReadOutputOptionalAndStrictArguments(t *testing.T) {
	b := &readOutputBackendFixture{}
	for _, backend := range []ResidentBackend{b, &fixedBackendFixture{}} {
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
			return s.Handle([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"tether_fixed_read_output","arguments":` + args + `}}`))
		}
		valid := `{"request_id":"11111111111111111111111111111111","cursor":0,"max_bytes":4096}`
		if backend != b {
			if len(catalog) != 3 || !call(valid).Error {
				t.Fatal("unsupported read advertised or accepted")
			}
			continue
		}
		if len(catalog) != 4 || catalog[3].(map[string]any)["name"] != "tether_fixed_read_output" {
			t.Fatal(catalog)
		}
		invalid := []string{`{}`, `null`, `[]`, `{"request_id":"11111111111111111111111111111111"}`, strings.Replace(valid, `"cursor":0`, `"cursor":0,"cursor":1`, 1), strings.Replace(valid, `"max_bytes":4096`, `"max_bytes":1,"host_id":"other"`, 1)}
		for _, field := range []string{"request_id", "cursor", "max_bytes"} {
			old := map[string]string{"request_id": `"11111111111111111111111111111111"`, "cursor": "0", "max_bytes": "4096"}[field]
			for _, bad := range []string{`null`, `true`, `{}`, `[]`} {
				invalid = append(invalid, strings.Replace(valid, `"`+field+`":`+old, `"`+field+`":`+bad, 1))
			}
		}
		for _, cursor := range []string{`-1`, `1.5`, `"0"`, `9007199254740992`, `18446744073709551616`, `1e300`} {
			invalid = append(invalid, strings.Replace(valid, `"cursor":0`, `"cursor":`+cursor, 1))
		}
		for _, limit := range []string{`-1`, `0`, `4097`, `1.5`, `"1"`, `9223372036854775808`} {
			invalid = append(invalid, strings.Replace(valid, `"max_bytes":4096`, `"max_bytes":`+limit, 1))
		}
		for _, id := range []string{`"bad"`, `"` + strings.Repeat("A", 32) + `"`, `"` + strings.Repeat("a", 10000) + `"`} {
			invalid = append(invalid, strings.Replace(valid, `"11111111111111111111111111111111"`, id, 1))
		}
		for _, args := range invalid {
			if reply := call(args); !reply.Error || b.calls != 0 {
				t.Fatalf("invalid args dispatched: %s", reply.Body)
			}
		}
		for _, cursor := range []uint64{0, 1, MaxResidentReadCursor} {
			for _, limit := range []int{1, 4096} {
				args := fmt.Sprintf(`{"request_id":"11111111111111111111111111111111","cursor":%d,"max_bytes":%d}`, cursor, limit)
				reply := call(args)
				if reply.Error || b.cursor != cursor || b.maxBytes != limit || b.starts != 0 {
					t.Fatal(string(reply.Body))
				}
			}
		}
		if b.calls != 6 {
			t.Fatal(b.calls)
		}
	}
}
