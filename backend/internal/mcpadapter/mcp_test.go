package mcpadapter_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

const foreignID = "0123456789abcdef0123456789abcdef"

func TestInitializeAndExactlyFourToolSchemas(t *testing.T) {
	h := newHarness(t, options{})
	resp := h.request("initialize", initParams(mcpadapter.ProtocolVersion))
	result := resp["result"].(map[string]any)
	if result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
	want := map[string]any{"tools": map[string]any{"listChanged": false}}
	if !reflect.DeepEqual(result["capabilities"], want) || result["serverInfo"].(map[string]any)["name"] != "rescue-mcp" {
		t.Fatalf("initialize result = %v", result)
	}
	h.notify("notifications/initialized", nil)
	tools := h.request("tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	required := []struct {
		name string
		keys []any
	}{
		{"rescue_start", []any{"request_id", "command", "timeout_ms"}},
		{"rescue_status", []any{"command_id"}},
		{"rescue_output", []any{"command_id", "cursor", "max_bytes"}},
		{"rescue_cancel", []any{"command_id"}},
	}
	if len(tools) != len(required) {
		t.Fatalf("tools = %v", tools)
	}
	for i, w := range required {
		tool := tools[i].(map[string]any)
		schema := tool["inputSchema"].(map[string]any)
		props := schema["properties"].(map[string]any)
		if tool["name"] != w.name || schema["type"] != "object" || schema["additionalProperties"] != false ||
			!reflect.DeepEqual(schema["required"], w.keys) || len(props) != len(w.keys) {
			t.Fatalf("tool %d = %v", i, tool)
		}
		for _, k := range w.keys {
			if props[k.(string)] == nil {
				t.Fatalf("%s lacks property %v", w.name, k)
			}
		}
	}
}

func TestOtherRequestedVersionGetsThePinnedVersion(t *testing.T) {
	h := newHarness(t, options{})
	result := h.request("initialize", initParams("2024-11-05"))["result"].(map[string]any)
	if result["protocolVersion"] != mcpadapter.ProtocolVersion {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
}

func TestLifecycleOrderingIsEnforced(t *testing.T) {
	h := newHarness(t, options{})
	if resp := h.request("ping", nil); !reflect.DeepEqual(resp["result"], map[string]any{}) {
		t.Fatalf("ping before initialize = %v", resp)
	}
	for _, method := range []string{"tools/list", "tools/call"} {
		if code := errorCode(t, h.request(method, map[string]any{"name": "rescue_status", "arguments": map[string]any{"command_id": foreignID}})); code != -32600 {
			t.Fatalf("%s before initialize = %v", method, code)
		}
	}
	h.request("initialize", initParams(mcpadapter.ProtocolVersion))
	if code := errorCode(t, h.request("tools/list", nil)); code != -32600 {
		t.Fatalf("tools/list before initialized notification = %v", code)
	}
	if code := errorCode(t, h.request("initialize", initParams(mcpadapter.ProtocolVersion))); code != -32600 {
		t.Fatalf("second initialize = %v", code)
	}
	h.notify("notifications/initialized", nil)
	if resp := h.request("tools/list", nil); resp["result"] == nil {
		t.Fatalf("tools/list after initialization = %v", resp)
	}
	if h.runtime.startCount() != 0 {
		t.Fatal("lifecycle traffic started the guest")
	}
}

func TestNotificationsAndStrayResponsesGetNoReply(t *testing.T) {
	h := newHarness(t, options{})
	h.initialize()
	h.notify("notifications/initialized", nil)
	h.notify("notifications/progress", map[string]any{"progressToken": 1, "progress": 1})
	h.notify("notifications/cancelled", map[string]any{"requestId": 12345})
	h.raw(`{"jsonrpc":"2.0","id":99,"result":{}}`)
	if resp := h.request("ping", nil); resp["result"] == nil {
		t.Fatalf("ping = %v", resp)
	}
	h.noMessage(50 * time.Millisecond)
}

func call(id int, args string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"rescue_start","arguments":%s}}`, id, args)
}

func TestMalformedMessagesAreRefusedAndServingContinues(t *testing.T) {
	h := newHarness(t, options{})
	h.initialize()
	statusArgs := `{"jsonrpc":"2.0","id":30,"method":"tools/call","params":{"name":"rescue_status","arguments":{"command_id":"%s"}}}`
	cases := []struct {
		name, line string
		code       float64
		id         any
	}{
		{"parse", `{`, -32700, nil},
		{"blank", ``, -32700, nil},
		{"invalid utf8", "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\",\"params\":{\"x\":\"\xff\"}}", -32700, nil},
		{"lone surrogate", `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"x":"\udc00"}}`, -32700, nil},
		{"duplicate member", `{"jsonrpc":"2.0","id":1,"id":2,"method":"ping"}`, -32700, nil},
		{"duplicate argument", call(2, `{"request_id":"r","request_id":"s","command":"x","timeout_ms":1}`), -32700, nil},
		{"batch", `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, -32600, nil},
		{"null id", `{"jsonrpc":"2.0","id":null,"method":"ping"}`, -32600, nil},
		{"fraction id", `{"jsonrpc":"2.0","id":1.5,"method":"ping"}`, -32600, nil},
		{"other jsonrpc", `{"jsonrpc":"1.0","id":3,"method":"ping"}`, -32600, 3.0},
		{"extra member", `{"jsonrpc":"2.0","id":4,"method":"ping","extra":1}`, -32600, nil},
		{"case member", `{"jsonrpc":"2.0","id":4,"Method":"ping"}`, -32600, nil},
		{"params array", `{"jsonrpc":"2.0","id":5,"method":"ping","params":[]}`, -32600, 5.0},
		{"unknown method", `{"jsonrpc":"2.0","id":"m","method":"resources/list"}`, -32601, "m"},
		{"fifth tool", `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"rescue_cleanup","arguments":{"command_id":"` + foreignID + `"}}}`, -32602, 6.0},
		{"params member", `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"rescue_status","arguments":{"command_id":"` + foreignID + `"},"binding":{}}}`, -32602, 7.0},
		{"no arguments", `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"rescue_status"}}`, -32602, 8.0},
		{"mode argument", call(9, `{"request_id":"r","command":"x","timeout_ms":1,"mode":"full"}`), -32602, 9.0},
		{"host argument", call(10, `{"request_id":"r","command":"x","timeout_ms":1,"host":"h"}`), -32602, 10.0},
		{"null argument", call(11, `{"request_id":"r","command":null,"timeout_ms":1}`), -32602, 11.0},
		{"case argument", call(12, `{"Request_ID":"r","command":"x","timeout_ms":1}`), -32602, 12.0},
		{"missing argument", call(13, `{"request_id":"r","command":"x"}`), -32602, 13.0},
		{"string timeout", call(14, `{"request_id":"r","command":"x","timeout_ms":"1"}`), -32602, 14.0},
		{"fraction timeout", call(15, `{"request_id":"r","command":"x","timeout_ms":1.5}`), -32602, 15.0},
		{"zero timeout", call(16, `{"request_id":"r","command":"x","timeout_ms":0}`), -32602, 16.0},
		{"overflow timeout", call(17, `{"request_id":"r","command":"x","timeout_ms":4294967296}`), -32602, 17.0},
		{"long request id", call(18, `{"request_id":"`+strings.Repeat("r", 49)+`","command":"x","timeout_ms":1}`), -32602, 18.0},
		{"space request id", call(19, `{"request_id":"a b","command":"x","timeout_ms":1}`), -32602, 19.0},
		{"empty command", call(20, `{"request_id":"r","command":"","timeout_ms":1}`), -32602, 20.0},
		{"large command", call(21, `{"request_id":"r","command":"`+strings.Repeat("x", agentwire.MaxCommandBytes+1)+`","timeout_ms":1}`), -32602, 21.0},
		{"arguments array", call(22, `[]`), -32602, 22.0},
		{"upper command id", fmt.Sprintf(statusArgs, strings.ToUpper(foreignID)), -32602, 30.0},
		{"short command id", fmt.Sprintf(statusArgs, "abc"), -32602, 30.0},
		{"large cursor", `{"jsonrpc":"2.0","id":31,"method":"tools/call","params":{"name":"rescue_output","arguments":{"command_id":"` + foreignID + `","cursor":9007199254740992,"max_bytes":1}}}`, -32602, 31.0},
		{"negative cursor", `{"jsonrpc":"2.0","id":32,"method":"tools/call","params":{"name":"rescue_output","arguments":{"command_id":"` + foreignID + `","cursor":-1,"max_bytes":1}}}`, -32602, 32.0},
		{"large max_bytes", `{"jsonrpc":"2.0","id":33,"method":"tools/call","params":{"name":"rescue_output","arguments":{"command_id":"` + foreignID + `","cursor":0,"max_bytes":49153}}}`, -32602, 33.0},
		{"oversize line", `{"jsonrpc":"2.0","id":34,"method":"ping","params":{"pad":"` + strings.Repeat("a", mcpadapter.MaxLine) + `"}}`, -32600, nil},
	}
	for _, tc := range cases {
		h.raw(tc.line)
		resp := h.recv()
		if code := errorCode(t, resp); code != tc.code || resp["id"] != tc.id {
			t.Fatalf("%s: code=%v id=%v, want %v %v", tc.name, code, resp["id"], tc.code, tc.id)
		}
	}
	if resp := h.request("ping", nil); resp["result"] == nil {
		t.Fatalf("ping after malformed input = %v", resp)
	}
	if h.runtime.startCount() != 0 {
		t.Fatal("malformed input started the guest")
	}
}
