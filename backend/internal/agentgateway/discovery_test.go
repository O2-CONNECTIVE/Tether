package agentgateway_test

import (
	"bytes"
	"context"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func startMessage(id int) string {
	return rpc(id, "tools/call", map[string]any{"name": "rescue_start", "arguments": map[string]any{"request_id": "discovery-same-request", "command": "fixed", "timeout_ms": 1000}})
}
func TestOfflineDiscoveryAuthenticatedStaticBothModes(t *testing.T) {
	for _, access := range []bool{false, true} {
		t.Run(map[bool]string{false: "bearer", true: "access"}[access], func(t *testing.T) {
			r := newRig(t, 0)
			if access {
				r = newAccessRig(t)
			}
			a, b := r.caller(0), r.caller(1)
			x := r.do(http.MethodPost, a.bearer, a.sid, rpc(1, "tools/list", nil), nil)
			y := r.do(http.MethodPost, b.bearer, b.sid, rpc(1, "tools/list", nil), nil)
			if x.status != 200 || !bytes.Equal(x.body, y.body) {
				t.Fatal("catalog is not static", x.status, y.status)
			}
			for _, secret := range append(append([]string{}, subjects...), "synthetic-nonce", testAUD, syntheticEmail, label(0), "host_key", "launch_id") {
				if strings.Contains(string(x.body), secret) {
					t.Fatal("dynamic data in catalog", secret)
				}
			}
			if res := r.do(http.MethodPost, b.bearer, a.sid, rpc(2, "tools/list", nil), nil); res.status != 404 {
				t.Fatal("cross-member session accepted")
			}
			want := 401
			if access {
				want = 403
			}
			if res := r.do(http.MethodPost, "", a.sid, rpc(2, "tools/list", nil), nil); res.status != want {
				t.Fatal("anonymous catalog", res.status)
			}
			if res := r.do(http.MethodPost, a.bearer, a.sid, startMessage(3), nil); res.status != 404 {
				t.Fatal("offline command admitted", res.status)
			}
			if res := r.do(http.MethodPost, a.bearer, a.sid, rpc(4, "tools/list", nil), nil); res.status != 404 {
				t.Fatal("discovery call did not end session")
			}
		})
	}
}
func TestDiscoveryNeverBindsAfterRouteAttachOrConcurrentCalls(t *testing.T) {
	r := newRig(t, 0)
	old := r.caller(0)
	m := r.approve(0, 30*time.Second)
	var wg sync.WaitGroup
	statuses := make(chan int, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- r.do(http.MethodPost, old.bearer, old.sid, startMessage(7), nil).status
		}()
	}
	wg.Wait()
	close(statuses)
	for n := range statuses {
		if n != 404 {
			t.Fatal("old discovery executed", n)
		}
	}
	if m.runtime.startCount() != 0 {
		t.Fatal("backend reached")
	}
	live := r.caller(0)
	st, fail := live.start("discovery-same-request", "fixed")
	if fail {
		t.Fatal(st)
	}
	id := commandID(t, st)
	m.proc(t)
	again, fail := live.start("discovery-same-request", "fixed")
	if fail || commandID(t, again) != id {
		t.Fatal("idempotency changed")
	}
	if _, fail = live.start("discovery-same-request", "different"); !fail {
		t.Fatal("mismatch accepted")
	}
	if m.runtime.startCount() != 1 {
		t.Fatal("unexpected starts")
	}
}
func TestDiscoveryRetainsParsingAndLifecycleBeforeCallRefusal(t *testing.T) {
	r := newRig(t, 0)
	bearer := token(t, subjects[0])
	res := r.do(http.MethodPost, bearer, "", rpc(0, "initialize", initParams()), nil)
	sid := res.header.Get(agentgateway.HeaderSession)
	if early := r.do(http.MethodPost, bearer, sid, startMessage(1), nil); early.message["error"] == nil || early.status != 200 {
		t.Fatal("early call not lifecycle error")
	}
	r.do(http.MethodPost, bearer, sid, rpc(nil, "notifications/initialized", nil), nil)
	bad := r.do(http.MethodPost, bearer, sid, rpc(2, "tools/call", map[string]any{"name": "rescue_start", "arguments": map[string]any{"host": "other"}}), nil)
	if bad.status != 200 || bad.message["error"] == nil {
		t.Fatal("bad params not refused")
	}
	if list := r.do(http.MethodPost, bearer, sid, rpc(3, "tools/list", nil), nil); list.status != 200 {
		t.Fatal("invalid call destroyed session")
	}
	if good := r.do(http.MethodPost, bearer, sid, startMessage(4), nil); good.status != 404 {
		t.Fatal("valid call did not end unbound session")
	}
}
func TestDiscoveryQuotaIdleDeleteAndClose(t *testing.T) {
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	r := newModeRig(t, agentgateway.Config{Verifier: verifier(t), Clock: clock})
	bearer := token(t, subjects[0])
	var ids []string
	for i := 0; i < agentgateway.MaxSessionsPerPrincipal; i++ {
		ids = append(ids, r.open(bearer))
	}
	if res := r.do(http.MethodPost, bearer, "", rpc(0, "initialize", initParams()), nil); res.status != 429 {
		t.Fatal("cap bypass", res.status)
	}
	if res := r.do(http.MethodDelete, bearer, ids[0], "", nil); res.status != 204 {
		t.Fatal("nil route DELETE", res.status)
	}
	fresh := r.open(bearer)
	mu.Lock()
	now = now.Add(agentgateway.SessionIdle + time.Second)
	mu.Unlock()
	if res := r.do(http.MethodPost, bearer, fresh, rpc(1, "tools/list", nil), nil); res.status != 404 {
		t.Fatal("idle session live")
	}
	r.open(bearer) // expired siblings are reclaimed on initialize
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.gw.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if res := r.do(http.MethodPost, bearer, "", rpc(0, "initialize", initParams()), nil); res.status != 503 {
		t.Fatal("closed gateway discovery", res.status)
	}
}
func TestRetiredBoundSessionNeverTurnsIntoDiscoveryOrRebinds(t *testing.T) {
	r := newRig(t, 0)
	m := r.approve(0, 30*time.Second)
	old := r.caller(0)
	_ = m.hostConn.Close()
	_ = m.waitHost(t)
	eventually(t, "old session retired", func() bool {
		return r.do(http.MethodPost, old.bearer, old.sid, rpc(1, "tools/list", nil), nil).status == 404
	})
	discovery := r.caller(0)
	next := r.approve(0, 30*time.Second)
	for _, sid := range []string{old.sid, discovery.sid} {
		if res := r.do(http.MethodPost, old.bearer, sid, startMessage(2), nil); res.status != 404 {
			t.Fatal("old session rebound", res.status)
		}
	}
	if next.runtime.startCount() != 0 {
		t.Fatal("old session reached new epoch")
	}
	fresh := r.caller(0)
	if st, fail := fresh.start("discovery-same-request", "fixed"); fail {
		t.Fatal(st)
	}
	next.proc(t)
}

func TestOfflineDiscoveryRejectsExpiredCredentialsOnEveryRequest(t *testing.T) {
	for _, access := range []bool{false, true} {
		r := newRig(t, 0)
		if access {
			r = newAccessRig(t)
		}
		c := r.caller(0)
		var expired string
		want := 401
		if access {
			x := accessClaims(subjects[0])
			x["iat"] = time.Now().Add(-2 * time.Hour).Unix()
			x["nbf"] = x["iat"]
			x["exp"] = time.Now().Add(-time.Hour).Unix()
			expired = assertion(t, x)
			want = 403
		} else {
			x := claims(subjects[0])
			x["iat"] = time.Now().Add(-time.Hour).Unix()
			x["exp"] = time.Now().Add(-time.Minute).Unix()
			expired = sign(t, edHeader(), x, issuerKey)
		}
		for _, body := range []string{rpc(1, "tools/list", nil), startMessage(2)} {
			if res := r.do(http.MethodPost, expired, c.sid, body, nil); res.status != want {
				t.Fatal("expired credential admitted", res.status)
			}
		}
		if res := r.do(http.MethodPost, c.bearer, c.sid, rpc(3, "tools/list", nil), nil); res.status != 200 {
			t.Fatal("failed authentication mutated session")
		}
	}
}
func TestDiscoveryCallsRaceRouteRetirementWithoutDispatch(t *testing.T) {
	r := newRig(t, 0)
	old := r.caller(0)
	m := r.approve(0, 30*time.Second)
	var wg sync.WaitGroup
	statuses := make(chan int, 8)
	wg.Add(1)
	go func() { defer wg.Done(); _ = m.hostConn.Close() }()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses <- r.do(http.MethodPost, old.bearer, old.sid, startMessage(8), nil).status
		}()
	}
	wg.Wait()
	close(statuses)
	for s := range statuses {
		if s != 404 {
			t.Fatal("old discovery call accepted", s)
		}
	}
	_ = m.waitHost(t)
	if m.runtime.startCount() != 0 || m.issuer.Snapshot().HasAuthority {
		t.Fatal("dispatch or authority survived")
	}
}
