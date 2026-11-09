package agentgateway_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// Characterization of application-assertion expiry at the real HTTP handler.
// Synthetic identity, channel and process only; this is not edge OAuth or live
// provider revocation, and expiry does not assert physical work cleanup.
func TestAccessExpiryCannotCancelWorkOrDeleteOwnedSession(t *testing.T) {
	r := newAccessRig(t)
	m := r.approve(0, 60*time.Second)
	c := r.caller(0)
	st, bad := c.start("access-lifecycle", "fixed")
	if bad {
		t.Fatal("fixture start refused")
	}
	id := commandID(t, st)
	proc := m.proc(t)
	claims := accessClaims(subjects[0])
	claims["iat"] = time.Now().Add(-2 * time.Hour).Unix()
	claims["nbf"] = claims["iat"]
	claims["exp"] = time.Now().Add(-time.Hour).Unix()
	expired := assertion(t, claims)
	tool := func(name string) string {
		return rpc(4, "tools/call", map[string]any{"name": name, "arguments": map[string]any{"command_id": id}})
	}
	for _, q := range []struct{ method, body string }{
		{http.MethodPost, rpc(2, "tools/list", nil)},
		{http.MethodPost, tool("rescue_status")},
		{http.MethodPost, tool("rescue_cancel")},
		{http.MethodDelete, ""},
	} {
		res := r.do(q.method, expired, c.sid, q.body, nil)
		if res.status != http.StatusForbidden || res.header.Get(agentgateway.HeaderSession) != "" || res.header.Get("WWW-Authenticate") != "" {
			t.Fatalf("expired assertion admitted/challenged: %d", res.status)
		}
	}
	if m.runtime.startCount() != 1 || !m.issuer.Snapshot().HasAuthority {
		t.Fatal("expired requests changed work authority")
	}
	select {
	case <-proc.killed:
		t.Fatal("expired cancel reached runtime")
	default:
	}
	// A current assertion for another configured person cannot take the old SID.
	other := assertion(t, accessClaims(subjects[1]))
	if res := r.do(http.MethodDelete, other, c.sid, "", nil); res.status != http.StatusNotFound {
		t.Fatalf("other principal DELETE: %d", res.status)
	}
	// A fresh same-subject assertion is accepted by the server on the original
	// SID; this is not client refresh support or a policy/host rebind.
	freshClaims := accessClaims(subjects[0])
	freshClaims["iat"] = time.Now().Add(-10 * time.Second).Unix()
	freshClaims["nbf"] = freshClaims["iat"]
	fresh := assertion(t, freshClaims)
	if fresh == c.bearer {
		t.Fatal("replacement assertion must differ from initial fixture")
	}
	if res := r.do(http.MethodPost, fresh, c.sid, rpc(5, "tools/list", nil), nil); res.status != http.StatusOK {
		t.Fatalf("authentication refusal changed session: %d", res.status)
	}
	if res := r.do(http.MethodDelete, fresh, c.sid, "", nil); res.status != http.StatusNoContent {
		t.Fatalf("same principal DELETE: %d", res.status)
	}
	if res := r.do(http.MethodPost, fresh, c.sid, rpc(6, "tools/list", nil), nil); res.status != http.StatusNotFound {
		t.Fatalf("deleted SID reused: %d", res.status)
	}
	if !m.issuer.Snapshot().HasAuthority {
		t.Fatal("session DELETE revoked host grant")
	}
	select {
	case <-proc.killed:
		t.Fatal("session DELETE claimed work cancellation")
	default:
	}
}
