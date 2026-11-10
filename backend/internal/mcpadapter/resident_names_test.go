package mcpadapter

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

type residentNamesFixture struct {
	fixedBackendFixture
	calls []string
}

func (b *residentNamesFixture) record(name, id string) map[string]any {
	b.calls = append(b.calls, name)
	b.id = id
	return map[string]any{"error": "not_found"}
}
func (b *residentNamesFixture) FixedStart(_ context.Context, id string) map[string]any {
	return b.record("start", id)
}
func (b *residentNamesFixture) FixedStatus(_ context.Context, id string) map[string]any {
	return b.record("status", id)
}
func (b *residentNamesFixture) FixedCancel(_ context.Context, id string) map[string]any {
	return b.record("cancel", id)
}
func (b *residentNamesFixture) FixedRelease(_ context.Context, id string) map[string]any {
	return b.record("release", id)
}
func (b *residentNamesFixture) FixedList(_ context.Context, cursor string, limit int) map[string]any {
	if cursor != "" || limit != 1 {
		panic("changed list arguments")
	}
	return b.record("list", "")
}
func (b *residentNamesFixture) FixedReadOutput(_ context.Context, id string, cursor uint64, limit int) map[string]any {
	if cursor != 0 || limit != 1 {
		panic("changed output arguments")
	}
	return b.record("read_output", id)
}
func (b *residentNamesFixture) FixedGetExecution(_ context.Context, id string) map[string]any {
	return b.record("get_execution", id)
}

func TestResidentTetherCatalogAndDispatch(t *testing.T) {
	b := &residentNamesFixture{}
	s, err := NewResidentHTTPSession(b, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Handle([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`))
	s.Handle([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	listing := s.Handle([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	var catalog struct {
		Result struct {
			Tools []struct{ Name string }
		}
	}
	if listing.Error || json.Unmarshal(listing.Body, &catalog) != nil {
		t.Fatalf("catalog: %s", listing.Body)
	}
	want := []string{"tether_fixed_start", "tether_fixed_status", "tether_fixed_cancel", "tether_fixed_release", "tether_fixed_list", "tether_fixed_read_output", "tether_fixed_get_execution"}
	var names []string
	for _, tool := range catalog.Result.Tools {
		names = append(names, tool.Name)
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("catalog = %v; want %v", names, want)
	}
	const id = "0123456789abcdef0123456789abcdef"
	for n, name := range want {
		args := map[string]any{"request_id": id}
		if name == "tether_fixed_list" {
			args = map[string]any{"cursor": "", "limit": 1}
		} else if name == "tether_fixed_read_output" {
			args["cursor"], args["max_bytes"] = 0, 1
		}
		raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": n + 3, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		if err != nil {
			t.Fatal(err)
		}
		reply := s.Handle(raw)
		if reply.Error || len(b.calls) != n+1 || (name != "tether_fixed_list" && b.id != id) {
			t.Fatalf("%s did not reach the unchanged operation: %s", name, reply.Body)
		}
	}
	if !reflect.DeepEqual(b.calls, []string{"start", "status", "cancel", "release", "list", "read_output", "get_execution"}) {
		t.Fatalf("dispatch = %v", b.calls)
	}
}
