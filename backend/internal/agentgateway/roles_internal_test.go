package agentgateway

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"

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
