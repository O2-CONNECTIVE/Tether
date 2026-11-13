package mcpadapter

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

type residentRunFixture struct {
	residentNamesFixture
	command, cwd string
	timeout      uint32
}

func (b *residentRunFixture) RunCommand(_ context.Context, id, command, cwd string, timeout uint32) map[string]any {
	b.command, b.cwd, b.timeout = command, cwd, timeout
	return b.record("run", id)
}

func residentSessionFixture(t *testing.T, b ResidentBackend) *HTTPSession {
	t.Helper()
	s, err := NewResidentHTTPSession(b, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.Handle([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`))
	s.Handle([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	return s
}

func callRunCommand(t *testing.T, s *HTTPSession, args map[string]any) HTTPReply {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 9, "method": "tools/call", "params": map[string]any{"name": "tether_run_command", "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handle(raw)
}

// The model chooses these arguments, so this is the first trust boundary for a
// command: every value that reaches the gateway must be the caller's own,
// complete and of the declared type. Nothing is guessed except the documented
// default timeout.
func TestResidentRunCommandReachesBackendOnlyWithACompleteCommandScope(t *testing.T) {
	b := &residentRunFixture{}
	s := residentSessionFixture(t, b)
	var catalog struct {
		Result struct {
			Tools []struct {
				Name        string
				InputSchema struct {
					Required             []string
					AdditionalProperties *bool
				}
			}
		}
	}
	listing := s.Handle([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	if listing.Error || json.Unmarshal(listing.Body, &catalog) != nil || len(catalog.Result.Tools) != 8 {
		t.Fatalf("catalog: %s", listing.Body)
	}
	tool := catalog.Result.Tools[7]
	if tool.Name != "tether_run_command" || !reflect.DeepEqual(tool.InputSchema.Required, []string{"request_id", "command", "cwd"}) || tool.InputSchema.AdditionalProperties == nil || *tool.InputSchema.AdditionalProperties {
		t.Fatalf("run command tool is not declared with a closed schema: %+v", tool)
	}
	const id = "0123456789abcdef0123456789abcdef"
	good := func() map[string]any {
		return map[string]any{"request_id": id, "command": "make test\n", "cwd": "src/app", "timeout_ms": 90000}
	}
	if reply := callRunCommand(t, s, good()); reply.Error || !reflect.DeepEqual(b.calls, []string{"run"}) || b.id != id || b.command != "make test\n" || b.cwd != "src/app" || b.timeout != 90000 {
		t.Fatalf("arguments changed on the way to the gateway: %q %q %d %s", b.command, b.cwd, b.timeout, reply.Body)
	}
	args := good()
	delete(args, "timeout_ms")
	if reply := callRunCommand(t, s, args); reply.Error || b.timeout != 60000 {
		t.Fatalf("default timeout = %d: %s", b.timeout, reply.Body)
	}
	for _, bound := range []uint32{15000, 240000} {
		args["timeout_ms"] = bound
		if reply := callRunCommand(t, s, args); reply.Error || b.timeout != bound {
			t.Fatalf("timeout bound %d refused: %s", bound, reply.Body)
		}
	}
	calls := len(b.calls)
	for name, edit := range map[string]func(map[string]any){
		"missing command":       func(a map[string]any) { delete(a, "command") },
		"empty command":         func(a map[string]any) { a["command"] = "" },
		"command not a string":  func(a map[string]any) { a["command"] = []string{"make", "test"} },
		"oversized command":     func(a map[string]any) { a["command"] = strings.Repeat("a", agentwire.MaxCommandBytes+1) },
		"missing directory":     func(a map[string]any) { delete(a, "cwd") },
		"directory not string":  func(a map[string]any) { a["cwd"] = 1 },
		"zero timeout":          func(a map[string]any) { a["timeout_ms"] = 0 },
		"timeout below floor":   func(a map[string]any) { a["timeout_ms"] = 14999 },
		"timeout above limit":   func(a map[string]any) { a["timeout_ms"] = 240001 },
		"fractional timeout":    func(a map[string]any) { a["timeout_ms"] = 1.5 },
		"timeout as a string":   func(a map[string]any) { a["timeout_ms"] = "60000" },
		"negative timeout":      func(a map[string]any) { a["timeout_ms"] = -1 },
		"unknown argument":      func(a map[string]any) { a["env"] = "X=1" },
		"malformed request id":  func(a map[string]any) { a["request_id"] = "1" },
		"missing request id":    func(a map[string]any) { delete(a, "request_id") },
		"null timeout":          func(a map[string]any) { a["timeout_ms"] = nil },
		"timeout beyond uint32": func(a map[string]any) { a["timeout_ms"] = 4294967296 + 60000 },
	} {
		a := good()
		edit(a)
		if reply := callRunCommand(t, s, a); !reply.Error || len(b.calls) != calls {
			t.Fatalf("%s reached the gateway: %s", name, reply.Body)
		}
	}
}

// A backend that cannot run commands must not advertise the tool or accept it.
func TestResidentRunCommandIsAbsentWithoutARunBackend(t *testing.T) {
	b := &residentNamesFixture{}
	s := residentSessionFixture(t, b)
	listing := s.Handle([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	if listing.Error || strings.Contains(string(listing.Body), "tether_run_command") {
		t.Fatalf("catalog advertises a tool the backend cannot serve: %s", listing.Body)
	}
	reply := callRunCommand(t, s, map[string]any{"request_id": "0123456789abcdef0123456789abcdef", "command": "true\n", "cwd": "."})
	if !reply.Error || len(b.calls) != 0 {
		t.Fatalf("run command reached a backend without run support: %s", reply.Body)
	}
}
