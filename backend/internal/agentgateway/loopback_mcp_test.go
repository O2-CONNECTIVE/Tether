//go:build ci_mcp_loopback

package agentgateway_test

import (
	"crypto/x509"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestLoopbackTLSMCPConnection(t *testing.T) {
	r := newRig(t, 2*time.Second)
	m := r.approve(0, 60*time.Second)
	c := newLoopbackMCP(t, r.gw)

	// A real TLS handshake must reject this server without its private test trust.
	untrusted := c.transport.Clone()
	untrusted.TLSClientConfig.RootCAs = x509.NewCertPool()
	defer untrusted.CloseIdleConnections()
	negative := &http.Client{Transport: untrusted, Timeout: 5 * time.Second}
	res, err := negative.Get(c.url)
	if res != nil {
		res.Body.Close()
	}
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Fatalf("untrusted certificate: %v", err)
	}

	// Invalid synthetic JWT and absent bearer must not create sessions or dispatch.
	for _, bearer := range []string{"", sign(t, edHeader(), claims(subjects[0]), otherKey)} {
		res := c.do(http.MethodPost, bearer, "", rpc(0, "initialize", initParams()))
		if res.status != http.StatusUnauthorized || res.header.Get("Mcp-Session-Id") != "" {
			t.Fatalf("unauthenticated initialize = %d %s", res.status, res.body)
		}
	}
	if m.runtime.startCount() != 0 {
		t.Fatal("unauthenticated request dispatched")
	}
	c.call("initialize", initParams())
	if res := c.do(http.MethodPost, c.bearer, c.sid, rpc(nil, "notifications/initialized", nil)); res.status != http.StatusAccepted || len(res.body) != 0 {
		t.Fatalf("initialized = %d %s", res.status, res.body)
	}
	result := c.call("tools/list", nil)
	var names []string
	for _, tool := range result["tools"].([]any) {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"rescue_cancel", "rescue_output", "rescue_start", "rescue_status"}) {
		t.Fatalf("tools = %v", names)
	}
	// Even a valid other principal cannot use this MCP session over the socket.
	if res := c.do(http.MethodPost, token(t, subjects[1]), c.sid, rpc(9, "tools/list", nil)); res.status != http.StatusNotFound {
		t.Fatalf("cross-principal session = %d", res.status)
	}
	start := c.tool("rescue_start", map[string]any{"request_id": "socket-1", "command": "synthetic command\n", "timeout_ms": 60000})
	id := commandID(t, start)
	proc := m.proc(t)
	m.runtime.mu.Lock()
	stdin := string(m.runtime.stdin[0])
	m.runtime.mu.Unlock()
	if stdin != "synthetic command\n" {
		t.Fatalf("fake runtime input = %q", stdin)
	}
	emit(t, proc, "loopback output\n")
	close(proc.events)
	close(proc.exited)
	final := c.terminal(id)
	if final["phase"] != "exited" || final["cleanup"] != "confirmed" || final["exit"].(map[string]any)["code"] != 0.0 {
		t.Fatalf("completed command = %v", final)
	}
	output := c.tool("rescue_output", map[string]any{"command_id": id, "cursor": 0, "max_bytes": 1024})
	chunks, _ := output["chunks"].([]any)
	if len(chunks) != 1 || chunks[0].(map[string]any)["data"] != "loopback output\n" || output["complete"] != true {
		t.Fatalf("output = %v", output)
	}
	second := commandID(t, c.tool("rescue_start", map[string]any{"request_id": "socket-2", "command": "synthetic pending command\n", "timeout_ms": 60000}))
	pending := m.proc(t)
	canceled := c.tool("rescue_cancel", map[string]any{"command_id": second})
	if canceled["stop"] != "canceled" {
		t.Fatalf("cancel = %v", canceled)
	}
	if final := c.terminal(second); final["phase"] != "canceled" || final["cleanup"] != "confirmed" {
		t.Fatalf("canceled status = %v", final)
	}
	select {
	case <-pending.killed:
	default:
		t.Fatal("fake process was not terminated")
	}
	if m.runtime.startCount() != 2 {
		t.Fatal("unexpected dispatch count")
	}
	if res := c.do(http.MethodDelete, c.bearer, c.sid, ""); res.status != http.StatusNoContent {
		t.Fatalf("delete = %d", res.status)
	}
	if res := c.do(http.MethodPost, c.bearer, c.sid, rpc(99, "tools/list", nil)); res.status != http.StatusNotFound {
		t.Fatalf("deleted session reused: %d", res.status)
	}
}
