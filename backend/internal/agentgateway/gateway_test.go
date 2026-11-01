package agentgateway_test

import (
	"errors"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// The four tools reach the one approved Mac Session through the gateway:
// start with request_id idempotency and mismatch refusal, output, status and
// explicit cancel, whose acceptance is reported apart from cleanup.
func TestFourToolsOverHTTPReachTheApprovedMacSession(t *testing.T) {
	r := newRig(t, 0)
	m := r.approve(0, 30*time.Second)
	c := r.caller(0)

	list := r.do(http.MethodPost, c.bearer, c.sid, rpc(1, "tools/list", nil), nil)
	if list.status != http.StatusOK || list.header.Get("Content-Type") != "application/json" || list.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("tools/list = %d %v", list.status, list.header)
	}
	var names []string
	for _, tool := range list.message["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	sort.Strings(names)
	if len(names) != 4 || names[0] != "rescue_cancel" || names[1] != "rescue_output" || names[2] != "rescue_start" || names[3] != "rescue_status" {
		t.Fatalf("tools = %v", names)
	}

	st, isErr := c.start("req-1", "printf 'hello\\n'\n")
	if isErr || st["profile"] != "mac_current_user" {
		t.Fatalf("rescue_start = %v", st)
	}
	id := commandID(t, st)
	proc := m.proc(t)
	m.runtime.mu.Lock()
	stdin := string(m.runtime.stdin[0])
	m.runtime.mu.Unlock()
	if stdin != "printf 'hello\\n'\n" {
		t.Fatalf("shell stdin = %q", stdin)
	}
	if again, isErr := c.start("req-1", "printf 'hello\\n'\n"); isErr || commandID(t, again) != id {
		t.Fatalf("same request_id replay = %v", again)
	}
	if res, isErr := c.start("req-1", "id -un\n"); !isErr || res["error"] != "request_mismatch" {
		t.Fatalf("mismatched replay = %v", res)
	}
	emit(t, proc, "hello\n")
	close(proc.events)
	close(proc.exited)
	final := c.awaitTerminal(id)
	if final["phase"] != "exited" || final["cleanup"] != "confirmed" || final["exit"].(map[string]any)["code"] != 0.0 {
		t.Fatalf("final = %v", final)
	}
	out, isErr := c.tool("rescue_output", map[string]any{"command_id": id, "cursor": 0, "max_bytes": 1024})
	chunks, _ := out["chunks"].([]any)
	if isErr || len(chunks) != 1 || chunks[0].(map[string]any)["data"] != "hello\n" || out["complete"] != true {
		t.Fatalf("rescue_output = %v", out)
	}

	st, _ = c.start("req-2", "sleep 60\n")
	second := commandID(t, st)
	m.proc(t)
	canceled, isErr := c.tool("rescue_cancel", map[string]any{"command_id": second})
	if _, reported := canceled["cleanup"]; isErr || canceled["stop"] != "canceled" || !reported {
		t.Fatalf("cancel accepted = %v", canceled)
	}
	if final := c.awaitTerminal(second); final["phase"] != "canceled" || final["cleanup"] != "confirmed" {
		t.Fatalf("owned cleanup after cancel = %v", final)
	}
	if m.runtime.startCount() != 2 || !m.issuer.Snapshot().HasAuthority {
		t.Fatal("unexpected shell starts or the grant ended")
	}
}

// Sessions and command IDs never cross principals; session IDs and identity
// headers are not authentication; a configured principal without a route
// gets only a permanently unbound discovery session.
func TestPrincipalsAreBoundToTheirOwnSessionAndGrant(t *testing.T) {
	r := newRig(t, 0)
	ma, mb := r.approve(0, 30*time.Second), r.approve(1, 30*time.Second)
	a, b := r.caller(0), r.caller(1)

	if res := r.do(http.MethodPost, b.bearer, a.sid, rpc(5, "tools/list", nil), func(q *http.Request) {
		q.Header.Set("X-Forwarded-User", subjects[0])
		q.Header.Set("X-Auth-Request-User", subjects[0])
	}); res.status != http.StatusNotFound {
		t.Fatalf("other principal's session = %d %s", res.status, res.body)
	}
	if res := r.do(http.MethodPost, "", a.sid, rpc(5, "tools/list", nil), nil); res.status != http.StatusUnauthorized {
		t.Fatalf("session without bearer = %d", res.status)
	}
	unrouted := token(t, subjects[2])
	if res := r.do(http.MethodPost, unrouted, a.sid, rpc(5, "tools/list", nil), nil); res.status != http.StatusNotFound {
		t.Fatalf("unrouted principal with another session = %d", res.status)
	}
	if res := r.do(http.MethodPost, unrouted, "", rpc(0, "initialize", initParams()), nil); res.status != http.StatusOK || res.header.Get(agentgateway.HeaderSession) == "" {
		t.Fatalf("initialize without an approved grant = %d %s", res.status, res.body)
	}
	if res := r.do(http.MethodPost, token(t, "synthetic-outsider"), "", rpc(0, "initialize", initParams()), nil); res.status != http.StatusForbidden {
		t.Fatalf("unconfigured subject = %d", res.status)
	}

	stA, _ := a.start("same-id", "echo a\n")
	idA := commandID(t, stA)
	ma.proc(t)
	stB, isErr := b.start("same-id", "echo a\n")
	idB := commandID(t, stB)
	mb.proc(t)
	if isErr || idA == idB {
		t.Fatalf("the same request_id on another grant = %v", stB)
	}
	if res, isErr := b.tool("rescue_status", map[string]any{"command_id": idA}); !isErr || res["error"] != "not_found" {
		t.Fatalf("other principal's command = %v", res)
	}
	if ma.runtime.startCount() != 1 || mb.runtime.startCount() != 1 {
		t.Fatal("a call reached another principal's Mac")
	}
	if res := r.do(http.MethodPost, a.bearer, a.sid, rpc(6, "tools/list", nil), nil); res.status != http.StatusOK {
		t.Fatalf("own session after cross attempts = %d", res.status)
	}
}

// Losing the agent channel ends the route: its sessions are unknown, a new
// command session cannot resume (new discovery is allowed); the Mac grant is revoked, not even
// with the same dispatch on a fresh stream.
func TestLostBackendEndsSessionsWithoutResume(t *testing.T) {
	r := newRig(t, 0)
	m := r.approve(0, 30*time.Second)
	c := r.caller(0)
	st, _ := c.start("req-1", "sleep 60\n")
	commandID(t, st)
	m.proc(t)

	_ = m.hostConn.Close()
	if err := m.waitHost(t); err == nil {
		t.Fatal("host Serve ended without reporting the lost channel")
	}
	eventually(t, "session end", func() bool {
		return r.do(http.MethodPost, c.bearer, c.sid, rpc(9, "tools/list", nil), nil).status == http.StatusNotFound
	})
	if res := r.do(http.MethodPost, c.bearer, "", rpc(0, "initialize", initParams()), nil); res.status != http.StatusOK {
		t.Fatalf("initialize after loss = %d", res.status)
	}
	if m.issuer.Snapshot().HasAuthority {
		t.Fatal("the Mac grant survived the lost channel")
	}
	_, fresh := memPair()
	if err := r.gw.Connect(label(0), m.dispatch, fresh); !errors.Is(err, agentgateway.ErrRefused) || !fresh.isClosed() {
		t.Fatalf("re-registering the same approval = %v", err)
	}
	if m.runtime.startCount() != 1 {
		t.Fatal("the shell was restarted")
	}
}

// The grant's hard deadline ends the route without any request.
func TestExpiredGrantEndsTheRoute(t *testing.T) {
	r := newRig(t, 0)
	m := r.approve(0, 1500*time.Millisecond)
	c := r.caller(0)
	st, _ := c.start("req-1", "sleep 60\n")
	commandID(t, st)
	m.proc(t)
	eventually(t, "deadline", func() bool {
		return r.do(http.MethodPost, c.bearer, c.sid, rpc(9, "tools/list", nil), nil).status == http.StatusNotFound
	})
	if res := r.do(http.MethodPost, c.bearer, "", rpc(0, "initialize", initParams()), nil); res.status != http.StatusOK {
		t.Fatalf("initialize after expiry = %d", res.status)
	}
	_ = m.waitHost(t)
	if m.issuer.Snapshot().HasAuthority {
		t.Fatal("the grant outlived its deadline")
	}
}

// A restarted gateway is a fresh memory: old session IDs are unknown and no
// route exists until a new recipient approval is registered.
func TestRestartDoesNotResume(t *testing.T) {
	before := newRig(t, 0)
	before.approve(0, 30*time.Second)
	c := before.caller(0)
	after := newRig(t, 0)
	if res := after.do(http.MethodPost, c.bearer, c.sid, rpc(1, "tools/list", nil), nil); res.status != http.StatusNotFound {
		t.Fatalf("old session after restart = %d", res.status)
	}
	if res := after.do(http.MethodPost, c.bearer, "", rpc(0, "initialize", initParams()), nil); res.status != http.StatusOK {
		t.Fatalf("initialize after restart = %d", res.status)
	}
}

// Registration accepts only the principal's own new approval of the Mac
// profile; every refusal closes the offered stream and leaves the live route.
func TestRegistrationRefusesAmbiguousOrReusedBindings(t *testing.T) {
	r := newRig(t, 0)
	m := r.approve(0, 30*time.Second)
	c := r.caller(0)
	other := m.dispatch
	other.Binding.Grant.Identity.LaunchID = "synthetic-fresh-launch"
	windows := other
	windows.Binding.Profile = agentexec.ProfileWindowsGuest
	for _, tc := range []struct {
		name  string
		label string
		d     agentgateway.Dispatch
		want  error
	}{
		{"second route for the same principal", label(0), other, agentgateway.ErrRefused},
		{"same approval for another principal", label(1), m.dispatch, agentgateway.ErrRefused},
		{"unknown label", "member-9", other, agentgateway.ErrConfig},
		{"Windows profile", label(2), windows, agentgateway.ErrConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, conn := memPair()
			if err := r.gw.Connect(tc.label, tc.d, conn); !errors.Is(err, tc.want) || !conn.isClosed() {
				t.Fatalf("Connect = %v, closed %v", err, conn.isClosed())
			}
		})
	}
	if err := r.gw.Connect(label(1), other, nil); !errors.Is(err, agentgateway.ErrConfig) {
		t.Fatalf("Connect without a stream = %v", err)
	}
	if res := r.do(http.MethodPost, c.bearer, c.sid, rpc(1, "tools/list", nil), nil); res.status != http.StatusOK || !m.issuer.Snapshot().HasAuthority {
		t.Fatal("a refused registration disturbed the live route")
	}

	unsigned, err := agentgateway.New(agentgateway.Config{Verifier: verifier(t), Principals: []agentgateway.Principal{{Label: "member-1", Subject: subjects[0]}}, Budgets: budgets()})
	if err != nil {
		t.Fatal(err)
	}
	_, conn := memPair()
	if err := unsigned.Connect("member-1", other, conn); !errors.Is(err, agentgateway.ErrConfig) || !conn.isClosed() {
		t.Fatalf("Connect without an enrolled signer = %v", err)
	}
}

// A possibly delivered start is uncertain, never retried; a colliding
// JSON-RPC ID is refused without executing; the same request_id later shows
// the one execution.
func TestUncertainStartAndRequestCollision(t *testing.T) {
	r := newRig(t, time.Second)
	m := r.approve(0, 30*time.Second)
	c := r.caller(0)
	args := map[string]any{"request_id": "req-u", "command": "sleep 60\n", "timeout_ms": 60000}

	m.hostConn.Hold()
	first := make(chan reply, 1)
	go func() {
		first <- r.do(http.MethodPost, c.bearer, c.sid, rpc(7, "tools/call", map[string]any{"name": "rescue_start", "arguments": args}), nil)
	}()
	proc := m.proc(t)
	dup := r.do(http.MethodPost, c.bearer, c.sid, rpc(7, "tools/call", map[string]any{"name": "rescue_status", "arguments": map[string]any{"command_id": "00000000000000000000000000000000"}}), nil)
	if e, ok := dup.message["error"].(map[string]any); dup.status != http.StatusOK || !ok || e["code"] != -32600.0 {
		t.Fatalf("colliding request id = %d %s", dup.status, dup.body)
	}
	res := <-first
	result, _ := res.message["result"].(map[string]any)
	if res.status != http.StatusOK || result == nil || result["isError"] != true || result["structuredContent"].(map[string]any)["error"] != "start_uncertain" {
		t.Fatalf("held start = %d %s", res.status, res.body)
	}
	m.hostConn.Release()
	again, isErr := c.toolWithID(8, "rescue_start", args)
	if isErr {
		t.Fatalf("same request_id after uncertainty = %v", again)
	}
	id := commandID(t, again)
	if m.runtime.startCount() != 1 {
		t.Fatal("the uncertain start was executed twice")
	}
	close(proc.events)
	close(proc.exited)
	c.awaitTerminal(id)
}

// A cancelled MCP request gets a cancellation error and stops nothing; a
// stalled output read is uncertain, not empty; only rescue_cancel stops.
func TestStalledOutputAndCancelledRequest(t *testing.T) {
	r := newRig(t, time.Second)
	m := r.approve(0, 30*time.Second)
	c := r.caller(0)
	st, _ := c.start("req-1", "sleep 60\n")
	id := commandID(t, st)
	m.proc(t)
	outArgs := map[string]any{"command_id": id, "cursor": 0, "max_bytes": 1024}

	m.hostConn.Hold()
	first := make(chan reply, 1)
	go func() {
		first <- r.do(http.MethodPost, c.bearer, c.sid, rpc(20, "tools/call", map[string]any{"name": "rescue_output", "arguments": outArgs}), nil)
	}()
	var got reply
wait:
	for {
		select {
		case got = <-first:
			break wait
		case <-time.After(20 * time.Millisecond):
			if n := r.do(http.MethodPost, c.bearer, c.sid, rpc(nil, "notifications/cancelled", map[string]any{"requestId": 20, "reason": "test"}), nil); n.status != http.StatusAccepted {
				t.Fatalf("cancelled notification = %d", n.status)
			}
		}
	}
	if got.status != http.StatusOK || got.message["error"] == nil || got.message["error"].(map[string]any)["code"] != -32800.0 {
		t.Fatalf("cancelled request = %d %s", got.status, got.body)
	}
	if out, isErr := c.tool("rescue_output", outArgs); !isErr || out["error"] != "uncertain" {
		t.Fatalf("stalled output = %v", out)
	}
	m.hostConn.Release()
	if st, isErr := c.tool("rescue_status", map[string]any{"command_id": id}); isErr || st["terminal"] == true {
		t.Fatalf("an MCP cancellation stopped the command: %v", st)
	}
	canceled, isErr := c.tool("rescue_cancel", map[string]any{"command_id": id})
	if isErr || canceled["stop"] != "canceled" {
		t.Fatalf("rescue_cancel = %v", canceled)
	}
	if final := c.awaitTerminal(id); final["phase"] != "canceled" || final["cleanup"] != "confirmed" {
		t.Fatalf("after cancel = %v", final)
	}
	if !m.issuer.Snapshot().HasAuthority {
		t.Fatal("command cancel revoked the grant")
	}
}

// DELETE ends one MCP session only; the per-principal session bound holds.
func TestSessionDeleteAndBound(t *testing.T) {
	r := newRig(t, 0)
	m := r.approve(0, 30*time.Second)
	c := r.caller(0)
	for i := 1; i < agentgateway.MaxSessionsPerPrincipal; i++ {
		r.open(c.bearer)
	}
	if res := r.do(http.MethodPost, c.bearer, "", rpc(0, "initialize", initParams()), nil); res.status != http.StatusTooManyRequests {
		t.Fatalf("session over the bound = %d", res.status)
	}
	if res := r.do(http.MethodDelete, c.bearer, c.sid, "", nil); res.status != http.StatusNoContent {
		t.Fatalf("DELETE = %d", res.status)
	}
	if res := r.do(http.MethodPost, c.bearer, c.sid, rpc(1, "tools/list", nil), nil); res.status != http.StatusNotFound {
		t.Fatalf("deleted session = %d", res.status)
	}
	r.open(c.bearer)
	if !m.issuer.Snapshot().HasAuthority {
		t.Fatal("ending an MCP session revoked the grant")
	}
}
