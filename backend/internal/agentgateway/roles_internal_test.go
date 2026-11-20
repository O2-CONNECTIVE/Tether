package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
	"golang.org/x/crypto/ssh"
)

// A route opened by ConnectResident carries one resident work. If an MCP
// initialize bound the generic tools to it (for example while the resident
// control is between generations), the owner would run arbitrary commands
// on its resident host through the rescue tools, and a rescue-only path
// would read output before the resident collector does (#408 review).
func TestRoleResidentRouteNeverBindsAGenericSession(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, 32))
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "test", "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}}})
	verifier, err := NewVerifier(VerifierConfig{Issuer: "https://issuer.example.invalid/", Audience: "https://example.test/mcp", Scope: "scope", Types: []string{"at+jwt"}, JWKS: jwks})
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, 32)))
	// Both roles: holding rescue_operator does not turn a resident route
	// into a rescue one.
	g, err := New(Config{Verifier: verifier, Principals: []Principal{{Label: "owner", Subject: "subject", Roles: []Role{RoleRescueOperator, RoleResidentOwner}, Signer: signer}}})
	if err != nil {
		t.Fatal(err)
	}
	p := g.byLabel["owner"]
	g.mu.Lock()
	p.route = &route{owner: p, ended: make(chan struct{}), cleanupDone: make(chan struct{})} // as connect(..., rescue=false) leaves it
	g.mu.Unlock()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 0, "method": "initialize",
		"params": map[string]any{"protocolVersion": mcpadapter.ProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}}})
	rec := httptest.NewRecorder()
	g.initialize(rec, p, body)
	g.mu.Lock()
	defer g.mu.Unlock()
	if rec.Code != 200 || len(g.sessions) != 1 {
		t.Fatalf("initialize = %d with %d sessions, want one discovery session", rec.Code, len(g.sessions))
	}
	for _, s := range g.sessions {
		if s.route != nil {
			t.Fatal("a generic MCP session was bound to a resident work route")
		}
	}
}

type initializeBarrierReader struct {
	entered  chan struct{}
	release  chan struct{}
	fallback io.Reader
	once     sync.Once
}

func (r *initializeBarrierReader) Read(p []byte) (int, error) {
	r.once.Do(func() {
		close(r.entered)
		<-r.release
	})
	return r.fallback.Read(p)
}

func rolesGatewayWithOnlineResident(t *testing.T) (*Gateway, *principal) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, 32))
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "test", "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}}})
	verifier, err := NewVerifier(VerifierConfig{Issuer: "https://issuer.example.invalid/", Audience: "https://example.test/mcp", Scope: "scope", Types: []string{"at+jwt"}, JWKS: jwks})
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, 32)))
	g, err := New(Config{Verifier: verifier, Principals: []Principal{{Label: "owner", Subject: "subject", Roles: []Role{RoleRescueOperator, RoleResidentOwner}, Signer: signer}}})
	if err != nil {
		t.Fatal(err)
	}
	host, _ := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x73}, 32)))
	if err := g.ConfigureHosts([]HostEntry{{Label: "resident-host", Key: host.PublicKey().Marshal(), Principals: []string{"owner"}}}); err != nil {
		t.Fatal(err)
	}
	control := &residentControlFixture{begin: make(chan struct{}), runs: make(chan residentRunCall, 1), rotated: make(chan string, 1)}
	if err := g.ConfigureResident("wss://example.test/register", control, []ResidentEnrollment{{HostLabel: "resident-host", PrincipalLabel: "owner"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = g.Close(ctx)
	})
	return g, g.byLabel["owner"]
}

func rolesLiveRoute(p *principal, deadline time.Time) *route {
	return &route{
		owner: p, rescue: true, client: &agentwire.Client{}, deadline: deadline,
		channelDone: make(chan struct{}), clientDone: make(chan struct{}), slots: make(chan struct{}, 1),
		ended: make(chan struct{}), cleanupDone: make(chan struct{}),
	}
}

func rolesClosedSignal() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func rolesInitializeBody() []byte {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 0, "method": "initialize",
		"params": map[string]any{"protocolVersion": mcpadapter.ProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}}})
	return body
}

func TestInitializeSkipsRescueRouteThatIsNotLiveAtSelection(t *testing.T) {
	cases := []struct {
		name string
		end  func(*route, *time.Time)
	}{
		{name: "deadline", end: func(r *route, now *time.Time) { r.deadline = now.Add(-time.Second) }},
		{name: "channel", end: func(r *route, _ *time.Time) { r.channelDone = rolesClosedSignal() }},
		{name: "wire client", end: func(r *route, _ *time.Time) { r.clientDone = rolesClosedSignal() }},
		{name: "retired", end: func(r *route, _ *time.Time) { close(r.ended) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, p := rolesGatewayWithOnlineResident(t)
			now := time.Now()
			g.clock = func() time.Time { return now }
			rt := rolesLiveRoute(p, now.Add(time.Hour))
			tc.end(rt, &now)
			g.mu.Lock()
			p.route = rt
			g.mu.Unlock()
			rec := httptest.NewRecorder()
			g.initialize(rec, p, rolesInitializeBody())
			id := rec.Header().Get(HeaderSession)
			if rec.Code != 200 || id == "" {
				t.Fatalf("initialize with dead rescue route = %d, session %q, body %s; want resident session", rec.Code, id, rec.Body)
			}
			g.mu.Lock()
			s := g.sessions[id]
			g.mu.Unlock()
			if s == nil || s.route != nil || s.resident == nil {
				t.Fatalf("session selected after dead rescue route = %+v, want resident", s)
			}
		})
	}
}

func TestInitializeFailsWhenSelectedRescueRouteEndsBeforeBinding(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Gateway, *principal, *route, *time.Time)
	}{
		{name: "ended signal", change: func(_ *Gateway, _ *principal, r *route, _ *time.Time) { close(r.ended) }},
		{name: "channel signal", change: func(g *Gateway, _ *principal, r *route, _ *time.Time) {
			g.mu.Lock()
			r.channelDone = rolesClosedSignal()
			g.mu.Unlock()
		}},
		{name: "wire client signal", change: func(g *Gateway, _ *principal, r *route, _ *time.Time) {
			g.mu.Lock()
			r.clientDone = rolesClosedSignal()
			g.mu.Unlock()
		}},
		{name: "deadline", change: func(_ *Gateway, _ *principal, r *route, now *time.Time) { *now = r.deadline }},
		{name: "pointer replacement", change: func(g *Gateway, p *principal, _ *route, now *time.Time) {
			g.mu.Lock()
			p.route = rolesLiveRoute(p, now.Add(time.Hour))
			g.mu.Unlock()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, p := rolesGatewayWithOnlineResident(t)
			now := time.Now()
			g.clock = func() time.Time { return now }
			oldRoute := rolesLiveRoute(p, now.Add(time.Hour))
			g.mu.Lock()
			p.route = oldRoute
			g.mu.Unlock()
			barrier := &initializeBarrierReader{entered: make(chan struct{}), release: make(chan struct{}), fallback: rand.Reader}
			previousReader := rand.Reader
			rand.Reader = barrier
			defer func() {
				rand.Reader = previousReader
				select {
				case <-barrier.release:
				default:
					close(barrier.release)
				}
			}()
			finished := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				rec := httptest.NewRecorder()
				g.initialize(rec, p, rolesInitializeBody())
				finished <- rec
			}()
			select {
			case <-barrier.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("initialize did not reach session binding barrier")
			}
			tc.change(g, p, oldRoute, &now)
			close(barrier.release)
			var rec *httptest.ResponseRecorder
			select {
			case rec = <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("initialize did not finish after route change")
			}
			if rec.Code != 503 || rec.Header().Get(HeaderSession) != "" {
				t.Fatalf("initialize after selected route changed = %d, session %q, body %s; want failure without fallback", rec.Code, rec.Header().Get(HeaderSession), rec.Body)
			}
			g.mu.Lock()
			p.route = nil
			gotSessions, gotCount := len(g.sessions), p.sessions
			g.mu.Unlock()
			if gotSessions != 0 || gotCount != 0 {
				t.Fatalf("initialize left %d session(s), principal count %d after selected route changed", gotSessions, gotCount)
			}
		})
	}
}

func TestInitializeDoesNotReplaceResidentWhenRescuePublishesBeforeBinding(t *testing.T) {
	g, p := rolesGatewayWithOnlineResident(t)
	var ids []string
	for i := 0; i < MaxSessionsPerPrincipal; i++ {
		rec := httptest.NewRecorder()
		g.initialize(rec, p, rolesInitializeBody())
		id := rec.Header().Get(HeaderSession)
		if rec.Code != 200 || id == "" {
			t.Fatalf("resident initialize %d = %d, session %q", i, rec.Code, id)
		}
		ids = append(ids, id)
	}

	barrier := &initializeBarrierReader{entered: make(chan struct{}), release: make(chan struct{}), fallback: rand.Reader}
	previousReader := rand.Reader
	rand.Reader = barrier
	defer func() {
		rand.Reader = previousReader
		select {
		case <-barrier.release:
		default:
			close(barrier.release)
		}
	}()
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		g.initialize(rec, p, rolesInitializeBody())
		finished <- rec
	}()
	select {
	case <-barrier.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("initialize did not reach resident replacement barrier")
	}
	g.mu.Lock()
	p.route = rolesLiveRoute(p, time.Now().Add(time.Hour))
	g.mu.Unlock()
	close(barrier.release)
	var rec *httptest.ResponseRecorder
	select {
	case rec = <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("initialize did not finish after rescue publication")
	}
	if rec.Code != 503 || rec.Header().Get(HeaderSession) != "" {
		t.Fatalf("initialize after rescue publication = %d, session %q, body %s; want failure without eviction", rec.Code, rec.Header().Get(HeaderSession), rec.Body)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.sessions) != MaxSessionsPerPrincipal || p.sessions != MaxSessionsPerPrincipal {
		t.Fatalf("rescue publication changed session capacity: %d sessions, principal count %d", len(g.sessions), p.sessions)
	}
	for _, id := range ids {
		if g.sessions[id] == nil {
			t.Fatalf("resident session %q was evicted before failed initialize", id)
		}
	}
}
