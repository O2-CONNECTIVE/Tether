package agentgateway_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

// Unsupported transports and malformed HTTP or JSON are refused with the
// Streamable HTTP status codes; nothing creates a session or reaches the Mac.
func TestMalformedHTTPAndUnsupportedTransport(t *testing.T) {
	r := newRig(t, 0)
	m := r.approve(0, 30*time.Second)
	c := r.caller(0)
	list := rpc(1, "tools/list", nil)

	for _, tc := range []struct {
		name    string
		method  string
		session string
		body    string
		mutate  func(*http.Request)
		status  int
		rpcCode float64
	}{
		{"GET event stream", http.MethodGet, c.sid, "", func(q *http.Request) { q.Header.Set("Accept", "text/event-stream") }, http.StatusMethodNotAllowed, 0},
		{"PUT", http.MethodPut, c.sid, list, nil, http.StatusMethodNotAllowed, 0},
		{"text body", http.MethodPost, c.sid, list, func(q *http.Request) { q.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType, 0},
		{"foreign charset", http.MethodPost, c.sid, list, func(q *http.Request) { q.Header.Set("Content-Type", "application/json; charset=latin1") }, http.StatusUnsupportedMediaType, 0},
		{"event stream only", http.MethodPost, c.sid, list, func(q *http.Request) { q.Header.Set("Accept", "text/event-stream") }, http.StatusNotAcceptable, 0},
		{"json refused by q=0", http.MethodPost, c.sid, list, func(q *http.Request) { q.Header.Set("Accept", "application/json;q=0") }, http.StatusNotAcceptable, 0},
		{"oversized body", http.MethodPost, c.sid, `{"x":"` + strings.Repeat("a", mcpadapter.MaxLine) + `"}`, nil, http.StatusRequestEntityTooLarge, 0},
		{"truncated JSON", http.MethodPost, c.sid, `{"jsonrpc":"2.0","id":1,`, nil, http.StatusBadRequest, -32700},
		{"duplicate member", http.MethodPost, c.sid, `{"jsonrpc":"2.0","id":1,"id":2,"method":"tools/list"}`, nil, http.StatusBadRequest, -32700},
		{"batch", http.MethodPost, c.sid, "[" + list + "]", nil, http.StatusBadRequest, -32600},
		{"null id", http.MethodPost, c.sid, `{"jsonrpc":"2.0","id":null,"method":"tools/list"}`, nil, http.StatusBadRequest, -32600},
		{"foreign origin", http.MethodPost, c.sid, list, func(q *http.Request) { q.Header.Set("Origin", "https://attacker.example.invalid") }, http.StatusForbidden, 0},
		{"token in query", http.MethodPost, c.sid, list, func(q *http.Request) { q.URL.RawQuery = "access_token=" + c.bearer }, http.StatusBadRequest, 0},
		{"older protocol version", http.MethodPost, c.sid, list, func(q *http.Request) { q.Header.Set(agentgateway.HeaderProtocol, "2025-03-26") }, http.StatusBadRequest, 0},
		{"missing protocol version", http.MethodPost, c.sid, list, func(q *http.Request) { q.Header.Del(agentgateway.HeaderProtocol) }, http.StatusBadRequest, 0},
		{"two session headers", http.MethodPost, c.sid, list, func(q *http.Request) { q.Header.Add(agentgateway.HeaderSession, c.sid) }, http.StatusBadRequest, 0},
		{"unknown session", http.MethodPost, strings.Repeat("0", 32), list, nil, http.StatusNotFound, 0},
		{"malformed session", http.MethodPost, "../" + c.sid, list, nil, http.StatusNotFound, 0},
		{"no session for tools", http.MethodPost, "", list, nil, http.StatusBadRequest, -32600},
		{"no session for a notification", http.MethodPost, "", rpc(nil, "notifications/initialized", nil), nil, http.StatusBadRequest, -32001},
		{"incomplete initialize", http.MethodPost, "", rpc(0, "initialize", map[string]any{"protocolVersion": mcpadapter.ProtocolVersion}), nil, http.StatusBadRequest, -32602},
		{"second initialize", http.MethodPost, c.sid, rpc(0, "initialize", initParams()), nil, http.StatusOK, -32600},
		{"unknown tool", http.MethodPost, c.sid, rpc(2, "tools/call", map[string]any{"name": "rescue_cleanup", "arguments": map[string]any{}}), nil, http.StatusOK, -32602},
		{"host selection argument", http.MethodPost, c.sid, rpc(3, "tools/call", map[string]any{"name": "rescue_status", "arguments": map[string]any{
			"command_id": strings.Repeat("0", 32), "host": "other.example.invalid"}}), nil, http.StatusOK, -32602},
		{"resources method", http.MethodPost, c.sid, rpc(4, "resources/list", nil), nil, http.StatusOK, -32601},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := r.do(tc.method, c.bearer, tc.session, tc.body, tc.mutate)
			if res.status != tc.status {
				t.Fatalf("status = %d %s", res.status, res.body)
			}
			if tc.status == http.StatusMethodNotAllowed && res.header.Get("Allow") != "POST, DELETE" {
				t.Fatalf("Allow = %q", res.header.Get("Allow"))
			}
			if tc.rpcCode != 0 {
				e, ok := res.message["error"].(map[string]any)
				if !ok || e["code"] != tc.rpcCode {
					t.Fatalf("JSON-RPC error = %s", res.body)
				}
			}
			if tc.session == "" && res.header.Get(agentgateway.HeaderSession) != "" {
				t.Fatal("a refused message created a session")
			}
		})
	}
	if ok := r.do(http.MethodPost, c.bearer, c.sid, list, func(q *http.Request) { q.Header.Set("Origin", "https://client.example.invalid") }); ok.status != http.StatusOK {
		t.Fatalf("allowed origin = %d", ok.status)
	}
	if res := r.do(http.MethodPost, c.bearer, c.sid, `{"jsonrpc":"2.0","id":99,"result":{}}`, nil); res.status != http.StatusAccepted || len(res.body) != 0 {
		t.Fatalf("client response = %d %s", res.status, res.body)
	}
	if res := r.do(http.MethodDelete, c.bearer, c.sid, "", func(q *http.Request) { q.Header.Del(agentgateway.HeaderProtocol) }); res.status != http.StatusBadRequest {
		t.Fatalf("DELETE without protocol version = %d", res.status)
	}
	if m.runtime.startCount() != 0 {
		t.Fatal("a refused message reached the Mac")
	}
}

// Before initialized, tools stay refused; the lifecycle is per session.
func TestLifecycleIsPerSession(t *testing.T) {
	r := newRig(t, 0)
	r.approve(0, 30*time.Second)
	bearer := token(t, subjects[0])
	res := r.do(http.MethodPost, bearer, "", rpc(0, "initialize", initParams()), nil)
	sid := res.header.Get(agentgateway.HeaderSession)
	if res.status != http.StatusOK || len(sid) != 32 {
		t.Fatalf("initialize = %d %q", res.status, sid)
	}
	early := r.do(http.MethodPost, bearer, sid, rpc(1, "tools/list", nil), nil)
	if e, ok := early.message["error"].(map[string]any); !ok || e["code"] != -32600.0 {
		t.Fatalf("tools before initialized = %s", early.body)
	}
	if ping := r.do(http.MethodPost, bearer, sid, rpc(2, "ping", nil), nil); ping.status != http.StatusOK || ping.message["result"] == nil {
		t.Fatalf("ping = %s", ping.body)
	}
	r.do(http.MethodPost, bearer, sid, rpc(nil, "notifications/initialized", nil), nil)
	if ready := r.do(http.MethodPost, bearer, sid, rpc(3, "tools/list", nil), nil); ready.message["result"] == nil {
		t.Fatalf("tools after initialized = %s", ready.body)
	}
}

func TestProtectedResourceMetadata(t *testing.T) {
	r := newRig(t, 0)
	for _, path := range []string{agentgateway.MetadataPrefix + "/mcp", agentgateway.MetadataPrefix} {
		rec := httptest.NewRecorder()
		r.gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://gateway.example.invalid"+path, nil))
		var doc map[string]any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &doc) != nil || doc["resource"] != testResource ||
			doc["authorization_servers"].([]any)[0] != testIssuer || doc["scopes_supported"].([]any)[0] != testScope {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body.Bytes())
		}
	}
	rec := httptest.NewRecorder()
	r.gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://gateway.example.invalid/other", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other path = %d", rec.Code)
	}
}
