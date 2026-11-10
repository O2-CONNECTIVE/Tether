package agentgateway_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

func newDiscoveryRig(t *testing.T, access bool) *rig {
	t.Helper()
	cfg := agentgateway.Config{DiscoveryOnly: true, Verifier: verifier(t)}
	if access {
		cfg.Verifier = nil
		cfg.Access, cfg.Resource = accessVerifier(t), testResource
	}
	for i, subject := range subjects {
		cfg.Principals = append(cfg.Principals, agentgateway.Principal{Label: label(i), Subject: subject})
	}
	gw, err := agentgateway.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := gw.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return &rig{t: t, gw: gw, access: access}
}

func TestExplicitDiscoveryStaticCatalogAndAllCallsRefused(t *testing.T) {
	for _, access := range []bool{false, true} {
		r := newDiscoveryRig(t, access)
		a, b := r.caller(0), r.caller(1)
		x := r.do(http.MethodPost, a.bearer, a.sid, rpc(1, "tools/list", nil), nil)
		y := r.do(http.MethodPost, b.bearer, b.sid, rpc(1, "tools/list", nil), nil)
		if x.status != 200 || !bytes.Equal(x.body, y.body) {
			t.Fatal("catalog is not static")
		}
		result := x.message["result"].(map[string]any)
		if len(result["tools"].([]any)) != 4 {
			t.Fatal("discovery is the legacy static four, not resident tools")
		}
		for _, name := range []string{"rescue_start", "rescue_status", "rescue_output", "rescue_cancel", "tether_fixed_start", "tether_fixed_status", "tether_fixed_cancel", "tether_fixed_list", "tether_fixed_read_output", "tether_fixed_get_execution", "tether_fixed_release", "unknown"} {
			c := r.caller(0)
			args := map[string]any{"command_id": "0123456789abcdef0123456789abcdef"}
			switch name {
			case "rescue_start":
				args = map[string]any{"request_id": "discovery-test", "command": "fixed", "timeout_ms": 1000}
			case "rescue_output":
				args["cursor"], args["max_bytes"] = 0, 64
			}
			response := r.do(http.MethodPost, c.bearer, c.sid, rpc(2, "tools/call", map[string]any{"name": name, "arguments": args}), nil)
			if response.status == 200 && response.message["error"] == nil {
				t.Fatal("call returned a result", name)
			}
			r.do(http.MethodDelete, c.bearer, c.sid, "", nil)
		}
		if response := r.do(http.MethodPost, a.bearer, a.sid, startMessage(3), nil); response.status != 404 {
			t.Fatal("valid call admitted", response.status)
		}
	}
}

func TestExplicitDiscoveryRetainsAuthMembershipAndSessionIsolation(t *testing.T) {
	for _, access := range []bool{false, true} {
		r := newDiscoveryRig(t, access)
		a, b := r.caller(0), r.caller(1)
		want := 401
		if access {
			want = 403
		}
		if x := r.do(http.MethodPost, "", a.sid, rpc(1, "tools/list", nil), nil); x.status != want {
			t.Fatal("anonymous admitted")
		}
		if x := r.do(http.MethodPost, b.bearer, a.sid, rpc(1, "tools/list", nil), nil); x.status != 404 {
			t.Fatal("session crossed members")
		}
		c := claims(subjects[0])
		c["exp"] = time.Now().Add(-time.Hour).Unix()
		expired := sign(t, edHeader(), c, issuerKey)
		if access {
			c = accessClaims(subjects[0])
			c["exp"] = time.Now().Add(-time.Hour).Unix()
			expired = assertion(t, c)
		}
		if x := r.do(http.MethodPost, expired, a.sid, rpc(1, "tools/list", nil), nil); x.status != want {
			t.Fatal("expired auth admitted")
		}
		unknown := token(t, "not-a-member")
		if access {
			unknown = assertion(t, accessClaims("not-a-member"))
		}
		if x := r.do(http.MethodPost, unknown, "", rpc(0, "initialize", initParams()), nil); x.status != 403 {
			t.Fatal("unknown member admitted", x.status)
		}
	}
}

func TestExplicitDiscoveryForbidsSignersAndRegistration(t *testing.T) {
	cfg := agentgateway.Config{DiscoveryOnly: true, Verifier: verifier(t), Principals: []agentgateway.Principal{{Label: label(0), Subject: subjects[0], Signer: fixtureSigner(t, 0x21)}}}
	if gw, err := agentgateway.New(cfg); err == nil || gw != nil {
		t.Fatal("discovery accepted signer")
	}
	r := newDiscoveryRig(t, false)
	for _, resident := range []bool{false, true} {
		conn := &discoveryConn{}
		var err error
		if resident {
			err = r.gw.ConnectResident(label(0), agentgateway.Dispatch{}, agentgateway.ResidentWorkBinding{}, conn)
		} else {
			err = r.gw.Connect(label(0), agentgateway.Dispatch{}, conn)
		}
		if err == nil || conn.closes != 1 || conn.reads != 0 || conn.writes != 0 {
			t.Fatal("registration not refused before IO", err, conn)
		}
	}
	if err := r.gw.ConfigureResident("wss://gateway.example.invalid/agent-register", nil, nil); err == nil {
		t.Fatal("resident configuration admitted")
	}
}

// Embedding is only for unused net.Conn methods; any handshake IO is counted.
type discoveryConn struct {
	net.Conn
	reads, writes, closes int
}

func (c *discoveryConn) Read([]byte) (int, error)  { c.reads++; return 0, io.EOF }
func (c *discoveryConn) Write([]byte) (int, error) { c.writes++; return 0, io.ErrClosedPipe }
func (c *discoveryConn) Close() error              { c.closes++; return nil }
