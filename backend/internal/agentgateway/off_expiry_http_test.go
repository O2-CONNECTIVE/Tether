package agentgateway_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// Same source lifecycle is exercised through the direct HTTP handler and the
// opt-in TCP/TLS lane. All identities, grants and runtime resources are fake.
func TestHTTPHostOffAndExpiredCredential(t *testing.T) {
	r := newRig(t, 2*time.Second)
	m := r.approve(0, 60*time.Second)
	checkOffAndExpiry(t, m, func(method, bearer, sid, body string) reply {
		return r.do(method, bearer, sid, body, nil)
	})
}

func checkOffAndExpiry(t *testing.T, m *mac, do func(string, string, string, string) reply) {
	t.Helper()
	var allowRelease atomic.Bool
	var releaseCalls atomic.Int32
	m.runtime.configure = func(p *fakeProc) {
		p.release = func(context.Context) error {
			releaseCalls.Add(1)
			if !allowRelease.Load() {
				return errors.New("synthetic resource still owned")
			}
			return nil
		}
	}
	bearer := token(t, subjects[0])
	init := do(http.MethodPost, bearer, "", rpc(1, "initialize", initParams()))
	sid := init.header.Get(agentgateway.HeaderSession)
	if init.status != http.StatusOK || sid == "" {
		t.Fatalf("initialize: %d %s", init.status, init.body)
	}
	if n := do(http.MethodPost, bearer, sid, rpc(nil, "notifications/initialized", nil)); n.status != http.StatusAccepted {
		t.Fatalf("initialized: %d", n.status)
	}
	start := func(requestID string) string {
		return rpc(2, "tools/call", map[string]any{"name": "rescue_start", "arguments": map[string]any{"request_id": requestID, "command": "synthetic pending command\n", "timeout_ms": 60000}})
	}
	first := do(http.MethodPost, bearer, sid, start("off-1"))
	result, ok := first.message["result"].(map[string]any)
	if first.status != http.StatusOK || !ok || result["isError"] != false {
		t.Fatalf("start: %d %s", first.status, first.body)
	}
	id := commandID(t, result["structuredContent"].(map[string]any))
	proc := m.proc(t)
	expiredClaims := claims(subjects[0])
	expiredClaims["iat"] = time.Now().Add(-time.Hour).Unix()
	expiredClaims["exp"] = time.Now().Add(-time.Minute).Unix() // beyond configured leeway
	expired := sign(t, edHeader(), expiredClaims, issuerKey)
	requests := []struct{ method, sid, body string }{
		{http.MethodPost, "", rpc(3, "initialize", initParams())},
		{http.MethodPost, sid, rpc(4, "tools/list", nil)},
		{http.MethodPost, sid, start("expired-new")},
		{http.MethodPost, sid, rpc(5, "tools/call", map[string]any{"name": "rescue_status", "arguments": map[string]any{"command_id": id}})},
		{http.MethodPost, sid, rpc(6, "tools/call", map[string]any{"name": "rescue_cancel", "arguments": map[string]any{"command_id": id}})},
		{http.MethodDelete, sid, ""},
	}
	for _, q := range requests {
		res := do(q.method, expired, q.sid, q.body)
		if res.status != http.StatusUnauthorized || res.header.Get(agentgateway.HeaderSession) != "" {
			t.Fatalf("expired credential: %d %s", res.status, res.body)
		}
	}
	if m.runtime.startCount() != 1 || !m.issuer.Snapshot().HasAuthority {
		t.Fatal("expired HTTP credential changed the approved work")
	}
	select {
	case <-proc.killed:
		t.Fatal("expired cancel reached runtime")
	default:
	}
	if res := do(http.MethodPost, bearer, sid, rpc(7, "tools/list", nil)); res.status != http.StatusOK {
		t.Fatalf("expired request destroyed valid session: %d", res.status)
	}

	// Trusted local host OFF, not a remote tool or GUI action. Revoke authority
	// first; Host Serve joins stop/release and must report the injected uncertainty.
	m.session.Revoke()
	if m.issuer.Snapshot().HasAuthority {
		t.Fatal("OFF retained authority")
	}
	if err := m.waitHost(t); err == nil {
		t.Fatal("host concealed unconfirmed cleanup")
	}
	select {
	case <-proc.killed:
	default:
		t.Fatal("OFF did not terminate fake process")
	}
	if releaseCalls.Load() == 0 {
		t.Fatal("OFF did not attempt owned release")
	}
	ref := agentexec.StatusRequest{Version: agentexec.Version, Binding: m.session.Binding(), CommandID: id}
	st, err := m.session.Status(ref)
	if err != nil || !st.Terminal || st.Stop != agentexec.StopRevoked || st.Cleanup != agentexec.CleanupUnknown {
		t.Fatalf("OFF owner result: %+v %v", st, err)
	}
	eventually(t, "OFF session removal", func() bool {
		return do(http.MethodPost, bearer, sid, rpc(8, "tools/list", nil)).status == http.StatusNotFound
	})
	if res := do(http.MethodPost, bearer, sid, start("after-off")); res.status != http.StatusNotFound {
		t.Fatalf("OFF old session accepted new work: %d %s", res.status, res.body)
	}
	// A fresh initialize is allowed only as permanently unbound discovery.
	fresh := do(http.MethodPost, bearer, "", rpc(9, "initialize", initParams()))
	freshID := fresh.header.Get(agentgateway.HeaderSession)
	if fresh.status != http.StatusOK || freshID == "" {
		t.Fatalf("fresh discovery: %d", fresh.status)
	}
	do(http.MethodPost, bearer, freshID, rpc(nil, "notifications/initialized", nil))
	denied := do(http.MethodPost, bearer, freshID, start("fresh-after-off"))
	if denied.status != http.StatusNotFound {
		t.Fatalf("fresh discovery admitted work: %d %s", denied.status, denied.body)
	}
	if m.runtime.startCount() != 1 {
		t.Fatal("OFF or expired request restarted runtime")
	}

	// Retry is owner-only; transport loss or a rejected request is not cleanup proof.
	allowRelease.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err = m.session.Cleanup(ctx, ref)
	if err != nil || st.Cleanup != agentexec.CleanupConfirmed || !st.Terminal {
		t.Fatalf("owner retry: %+v %v", st, err)
	}
	if m.issuer.Snapshot().HasAuthority || m.runtime.startCount() != 1 {
		t.Fatal("cleanup revived authority or work")
	}
}
