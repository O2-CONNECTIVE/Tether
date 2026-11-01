package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
	"strings"
	"testing"
)

const initLine = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`
const readyLine = `{"jsonrpc":"2.0","method":"notifications/initialized"}`

func msg(name string, args any) string {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	return string(b)
}
func TestFixtureSchemasMatchProductDiscovery(t *testing.T) {
	list := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	var out, ev bytes.Buffer
	if run(strings.NewReader(initLine+"\n"+readyLine+"\n"+list+"\n"), &out, &ev) != 0 {
		t.Fatal("run")
	}
	s, _ := mcpadapter.NewDiscoveryHTTPSession(0)
	defer s.Close()
	a := s.Handle([]byte(initLine))
	s.Handle([]byte(readyLine))
	b := s.Handle([]byte(list))
	if out.String() != string(a.Body)+"\n"+string(b.Body)+"\n" {
		t.Fatal("schema drift")
	}
	if strings.Contains(ev.String(), "token") || !strings.Contains(ev.String(), "dispatch=0") {
		t.Fatal("bad evidence")
	}
}
func TestEveryFixedToolIsSchemaCheckedButNeverExecuted(t *testing.T) {
	cases := map[string]string{"rescue_start": `{"request_id":"schema-check-1","command":"printf 'tether-schema-only\\n'","timeout_ms":1000}`, "rescue_status": `{"command_id":"00000000000000000000000000000000"}`, "rescue_cancel": `{"command_id":"00000000000000000000000000000000"}`, "rescue_output": `{"command_id":"00000000000000000000000000000000","cursor":0,"max_bytes":1024}`}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			var args map[string]any
			json.Unmarshal([]byte(raw), &args)
			var out, ev bytes.Buffer
			if run(strings.NewReader(initLine+"\n"+readyLine+"\n"+msg(name, args)+"\n"), &out, &ev) != 0 {
				t.Fatal("run")
			}
			if !strings.Contains(out.String(), `"isError":true`) || !strings.Contains(out.String(), `"fixed_arguments":true`) || !strings.Contains(ev.String(), "valid_"+name+" dispatch=0") {
				t.Fatal(out.String(), ev.String())
			}
		})
	}
}
func TestMismatchedAndInvalidArgumentsNeverEscapeIntoEvidence(t *testing.T) {
	for _, args := range []any{map[string]any{"request_id": "other", "command": "SENSITIVE_FIXTURE_MARKER", "timeout_ms": 1000}, map[string]any{"arbitrary_host": "SENSITIVE_FIXTURE_MARKER"}} {
		var out, ev bytes.Buffer
		run(strings.NewReader(initLine+"\n"+readyLine+"\n"+msg("rescue_start", args)+"\n"), &out, &ev)
		if strings.Contains(ev.String(), "SENSITIVE_FIXTURE_MARKER") || strings.Contains(out.String(), "SENSITIVE_FIXTURE_MARKER") || strings.Contains(ev.String(), "valid_rescue_start") {
			t.Fatal("raw input exposed or accepted")
		}
	}
}
func TestFixtureRejectsEarlyCallAndMalformedInput(t *testing.T) {
	for _, line := range []string{msg("rescue_status", map[string]any{"command_id": strings.Repeat("0", 32)}), `{"jsonrpc":"2.0","id":1,"id":2,"method":"tools/list"}`, `[]`} {
		var out, ev bytes.Buffer
		run(strings.NewReader(line+"\n"), &out, &ev)
		if !strings.Contains(out.String(), `"error"`) || strings.Contains(ev.String(), "valid_") {
			t.Fatal("invalid accepted")
		}
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("synthetic write failure") }
func TestFixtureOutputAndOversizedInputFailClosed(t *testing.T) {
	var ev bytes.Buffer
	if run(strings.NewReader(initLine+"\n"), failedWriter{}, &ev) != 1 {
		t.Fatal("write accepted")
	}
	var out bytes.Buffer
	if run(strings.NewReader(strings.Repeat("x", mcpadapter.MaxLine+2)), &out, &ev) != 1 {
		t.Fatal("oversize accepted")
	}
}
