package agentgateway

import (
	"bytes"
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

func TestInitializeFailsWhenSelectedRescueRouteEndsBeforeBinding(t *testing.T) {
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
	p := g.byLabel["owner"]
	oldRoute := &route{owner: p, rescue: true, client: &agentwire.Client{}, slots: make(chan struct{}, 1), ended: make(chan struct{}), cleanupDone: make(chan struct{})}
	newRoute := &route{owner: p, rescue: true, client: &agentwire.Client{}, slots: make(chan struct{}, 1), ended: make(chan struct{}), cleanupDone: make(chan struct{})}
	g.mu.Lock()
	p.route = oldRoute
	g.mu.Unlock()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 0, "method": "initialize",
		"params": map[string]any{"protocolVersion": mcpadapter.ProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}}})
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
	type result struct {
		rec *httptest.ResponseRecorder
	}
	finished := make(chan result, 1)
	go func() {
		rec := httptest.NewRecorder()
		g.initialize(rec, p, body)
		finished <- result{rec: rec}
	}()
	select {
	case <-barrier.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("initialize did not reach session binding barrier")
	}
	close(oldRoute.ended)
	g.mu.Lock()
	p.route = newRoute
	g.mu.Unlock()
	close(barrier.release)
	var rec *httptest.ResponseRecorder
	select {
	case result := <-finished:
		rec = result.rec
	case <-time.After(5 * time.Second):
		t.Fatal("initialize did not finish after route ended")
	}
	if rec.Code != 503 || rec.Header().Get(HeaderSession) != "" {
		t.Fatalf("initialize after route replacement = %d, session %q, body %s; want failure without fallback", rec.Code, rec.Header().Get(HeaderSession), rec.Body)
	}
	g.mu.Lock()
	p.route = nil
	gotSessions := len(g.sessions)
	g.mu.Unlock()
	if gotSessions != 0 {
		t.Fatalf("initialize left %d session(s) after selected route ended", gotSessions)
	}
}
