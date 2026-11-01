// schemafixture is a test-only stdio peer. It has no backend, authentication,
// token reader, transport, authority, process launcher or runtime configuration.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
	"io"
	"os"
	"reflect"
)

func main() {
	if len(os.Args) != 1 {
		os.Exit(2)
	}
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr))
}

func run(in io.Reader, out, evidence io.Writer) int {
	s, err := mcpadapter.NewDiscoveryHTTPSession(0)
	if err != nil {
		return 1
	}
	defer s.Close()
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), mcpadapter.MaxLine+1)
	frames := 0
	for scan.Scan() {
		frames++
		if frames > 64 {
			fmt.Fprintln(evidence, "SCHEMA_FIXTURE frame_limit dispatch=0")
			return 1
		}
		raw := scan.Bytes()
		r := s.Handle(raw)
		switch r.Kind {
		case mcpadapter.HTTPResponse, mcpadapter.HTTPRejected:
			if _, err := fmt.Fprintf(out, "%s\n", r.Body); err != nil {
				return 1
			}
			if r.Initialized {
				if _, err := fmt.Fprintln(evidence, "SCHEMA_FIXTURE initialize dispatch=0"); err != nil {
					return 1
				}
			}
		case mcpadapter.HTTPRequiresBinding:
			// Handle already strictly validated request ID, tool name and arguments.
			// No invoker exists: this session was constructed permanently unbound.
			var request struct {
				ID     json.RawMessage `json:"id"`
				Params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			if json.Unmarshal(raw, &request) != nil {
				return 1
			}
			matched := fixedArguments(request.Params.Name, request.Params.Arguments)
			event := "SCHEMA_FIXTURE rejected_fixed_arguments dispatch=0"
			if matched {
				event = "SCHEMA_FIXTURE valid_" + request.Params.Name + " dispatch=0"
			}
			if _, err := fmt.Fprintln(evidence, event); err != nil {
				return 1
			}
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{
				"isError": true, "content": []any{map[string]any{"type": "text", "text": "schema_only_no_execution"}},
				"structuredContent": map[string]any{"error": "schema_only_no_execution", "fixed_arguments": matched, "dispatch_count": 0}}})
			if _, err := fmt.Fprintf(out, "%s\n", body); err != nil {
				return 1
			}
		case mcpadapter.HTTPAccepted: // notifications have no stdio response
		default:
			return 1
		}
	}
	if scan.Err() != nil {
		fmt.Fprintln(evidence, "SCHEMA_FIXTURE input_failed dispatch=0")
		return 1
	}
	if _, err := fmt.Fprintln(evidence, "SCHEMA_FIXTURE ended dispatch=0"); err != nil {
		return 1
	}
	return 0
}
func fixedArguments(name string, got map[string]any) bool {
	var raw string
	switch name {
	case "rescue_start":
		raw = `{"request_id":"schema-check-1","command":"printf 'tether-schema-only\\n'","timeout_ms":1000}`
	case "rescue_status", "rescue_cancel":
		raw = `{"command_id":"00000000000000000000000000000000"}`
	case "rescue_output":
		raw = `{"command_id":"00000000000000000000000000000000","cursor":0,"max_bytes":1024}`
	default:
		return false
	}
	var want map[string]any
	return json.Unmarshal([]byte(raw), &want) == nil && reflect.DeepEqual(got, want)
}
