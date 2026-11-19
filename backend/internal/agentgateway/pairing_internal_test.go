package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// A subject is unique only within its issuer. ConfigurePairing refuses a
// pairing verifier of another issuer (#417), but the pair route must not lean
// on that alone: it matches the principal on issuer and subject, so a token
// from another issuer that carries the owner's subject never replaces the
// owner's pair.
func TestServePairRefusesSameSubjectFromAnotherIssuer(t *testing.T) {
	const issuer, other = "https://issuer.example.invalid/", "https://other.example.invalid/"
	const endpoint = "https://example.test" + PairPath
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, 32))
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "test", "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}}})
	newVerifier := func(iss, aud, scope string) *Verifier {
		v, err := NewVerifier(VerifierConfig{Issuer: iss, Audience: aud, Scope: scope, Types: []string{"at+jwt"}, JWKS: jwks})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	signer := func(seed byte) ssh.Signer {
		s, _ := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32)))
		return s
	}
	oldHost, newHost := signer(7), signer(9)
	g, err := New(Config{Verifier: newVerifier(issuer, "https://example.test/mcp", "scope"), Principals: []Principal{{Label: "member1", Subject: "subject", Roles: []Role{RoleRescueOperator, RoleResidentOwner}, Signer: signer(8)}}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close(context.Background())
	if err := g.ConfigureHosts([]HostEntry{{Label: "host1", Principals: []string{"member1"}, Paired: true}}); err != nil {
		t.Fatal(err)
	}
	saves := 0
	if err := g.ConfigurePairing(PairingConfig{Verifier: newVerifier(issuer, endpoint, ""), Endpoint: endpoint, RegistrationEndpoint: "wss://example.test/register",
		Records: []PairRecord{{HostLabel: "host1", HostKey: oldHost.PublicKey().Marshal(), Issuer: issuer, Subject: "subject"}},
		Save:    func([]PairRecord) error { saves++; return nil }}); err != nil {
		t.Fatal(err)
	}
	pair := func(iss string) int {
		now := time.Now()
		h, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": "test", "typ": "at+jwt"})
		c, _ := json.Marshal(map[string]any{"iss": iss, "aud": endpoint, "sub": "subject", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()})
		input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
		bearer := input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
		proof, err := newHost.Sign(nil, PairTranscript(endpoint, newHost.PublicKey().Marshal(), sha256.Sum256([]byte(bearer))))
		if err != nil {
			t.Fatal(err)
		}
		authorized := ssh.MarshalAuthorizedKey(newHost.PublicKey())
		body, _ := json.Marshal(map[string]string{"host_public_key": string(authorized[:len(authorized)-1]), "proof": base64.RawURLEncoding.EncodeToString(ssh.Marshal(proof))})
		req := httptest.NewRequest(http.MethodPost, "http://gateway.internal"+PairPath, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		g.ServePair(rec, req)
		return rec.Code
	}
	// Stand in for a library caller whose pairing verifier trusts another issuer.
	g.mu.Lock()
	trusted := g.pairing.Verifier
	g.pairing.Verifier = newVerifier(other, endpoint, "")
	g.mu.Unlock()
	if code := pair(other); code != http.StatusForbidden || saves != 0 || !bytes.Equal(g.HostKey("host1"), oldHost.PublicKey().Marshal()) {
		t.Fatalf("another issuer's token: status=%d saves=%d, want 403 with the old pair kept", code, saves)
	}
	// Control: the same request from the configured issuer replaces the pair,
	// so the refusal above is the issuer match and not a broken request.
	g.mu.Lock()
	g.pairing.Verifier = trusted
	g.mu.Unlock()
	if code := pair(issuer); code != http.StatusOK || saves != 1 || !bytes.Equal(g.HostKey("host1"), newHost.PublicKey().Marshal()) {
		t.Fatalf("configured issuer's token: status=%d saves=%d, want 200 with the new pair", code, saves)
	}
}
