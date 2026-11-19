package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestServePairRejectsUncollectedWorkA7(t *testing.T) {
	g, s, host, _ := residentEvidenceRig(t, true)
	defer g.Close(context.Background())
	s.FixedStart(context.Background(), strings.Repeat("1", 32))
	pairAud := "https://example.test" + PairPath
	issuerKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize))
	now := time.Now().Unix()
	h, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": "test", "typ": "at+jwt"})
	c, _ := json.Marshal(map[string]any{"iss": "https://issuer.example.invalid/", "aud": pairAud, "sub": "subject", "iat": now, "exp": now + 60})
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	token := input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(issuerKey, []byte(input)))
	digest := sha256.Sum256([]byte(token))
	transcript := pairTranscriptBytesForTest(pairAud, host.PublicKey().Marshal(), digest)
	sig, err := host.Sign(nil, transcript)
	if err != nil {
		t.Fatal(err)
	}
	encoded := ssh.MarshalAuthorizedKey(host.PublicKey())
	body, _ := json.Marshal(map[string]string{"host_public_key": string(encoded[:len(encoded)-1]), "proof": base64.RawURLEncoding.EncodeToString(ssh.Marshal(sig))})
	req := httptest.NewRequest("POST", "http://gateway.internal"+PairPath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	g.ServePair(rr, req)
	// A host with work awaiting collection must retain its key and registry record.
	if rr.Code != 409 {
		t.Fatalf("uncollected work status = %d, want 409", rr.Code)
	}
}

func pairTranscriptBytesForTest(endpoint string, key []byte, hash [32]byte) []byte {
	var out bytes.Buffer
	for _, part := range [][]byte{[]byte("rescue-host.agentgateway.pair\x00v1\x00"), []byte(endpoint), key, hash[:]} {
		_ = binary.Write(&out, binary.BigEndian, uint32(len(part)))
		out.Write(part)
	}
	return out.Bytes()
}

func TestServePairAllowsPairingAfterCollectedWorkA7(t *testing.T) {
	g, s, host, control := residentEvidenceRig(t, true)
	defer g.Close(context.Background())
	id := strings.Repeat("2", 32)
	s.FixedStart(context.Background(), id)
	if err := g.ResidentCollected(signedCollectionFixture(t, g, s, host, id)); err != nil {
		t.Fatal(err)
	}
	close(control.begin)
	select {
	case <-s.entry.works[id].completed:
	case <-time.After(time.Second):
		t.Fatal("resident work did not settle after collection")
	}
	// Once signed collection clears the busy work, the pairing request may proceed.
	rr := residentPairAttempt(t, g, host)
	if rr.Code != 200 {
		t.Fatalf("collected work pairing status = %d, want 200", rr.Code)
	}
}

func residentPairAttempt(t *testing.T, g *Gateway, host ssh.Signer) *httptest.ResponseRecorder {
	t.Helper()
	pairAud := "https://example.test" + PairPath
	issuerKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize))
	now := time.Now().Unix()
	h, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": "test", "typ": "at+jwt"})
	c, _ := json.Marshal(map[string]any{"iss": "https://issuer.example.invalid/", "aud": pairAud, "sub": "subject", "iat": now, "exp": now + 60})
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	token := input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(issuerKey, []byte(input)))
	digest := sha256.Sum256([]byte(token))
	sig, err := host.Sign(nil, pairTranscriptBytesForTest(pairAud, host.PublicKey().Marshal(), digest))
	if err != nil {
		t.Fatal(err)
	}
	encoded := ssh.MarshalAuthorizedKey(host.PublicKey())
	body, _ := json.Marshal(map[string]string{"host_public_key": string(encoded[:len(encoded)-1]), "proof": base64.RawURLEncoding.EncodeToString(ssh.Marshal(sig))})
	req := httptest.NewRequest("POST", "http://gateway.internal"+PairPath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	g.ServePair(rr, req)
	return rr
}

// pairOnlyRig is a gateway with one empty pairing slot and no resident setup,
// so the generic session route is the only work the slot can have.
func pairOnlyRig(t *testing.T) (*Gateway, *principal) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize))
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "test", "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}}})
	verifier, err := NewVerifier(VerifierConfig{Issuer: "https://issuer.example.invalid/", Audience: "https://example.test/mcp", Scope: "scope", Types: []string{"at+jwt"}, JWKS: jwks})
	if err != nil {
		t.Fatal(err)
	}
	pairVerifier, err := NewVerifier(VerifierConfig{Issuer: "https://issuer.example.invalid/", Audience: "https://example.test" + PairPath, Types: []string{"at+jwt"}, JWKS: jwks})
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, 32)))
	g, err := New(Config{Verifier: verifier, Principals: []Principal{{Label: "member1", Subject: "subject", Roles: []Role{RoleRescueOperator, RoleResidentOwner}, Signer: signer}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close(context.Background()) })
	if err := g.ConfigureHosts([]HostEntry{{Label: "host1", Principals: []string{"member1"}, Paired: true}}); err != nil {
		t.Fatal(err)
	}
	if err := g.ConfigurePairing(PairingConfig{Verifier: pairVerifier, Endpoint: "https://example.test" + PairPath, RegistrationEndpoint: "wss://example.test/register"}); err != nil {
		t.Fatal(err)
	}
	return g, g.byLabel["member1"]
}

func pairTokenForTest() string {
	issuerKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize))
	now := time.Now().Unix()
	h, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": "test", "typ": "at+jwt"})
	c, _ := json.Marshal(map[string]any{"iss": "https://issuer.example.invalid/", "aud": "https://example.test" + PairPath, "sub": "subject", "iat": now, "exp": now + 60})
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(issuerKey, []byte(input)))
}

func servePairBodyForTest(g *Gateway, token string, body map[string]string) int {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "http://gateway.internal"+PairPath, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	g.ServePair(rr, req)
	return rr.Code
}

func servePairForTest(t *testing.T, g *Gateway, host ssh.Signer) int {
	t.Helper()
	token := pairTokenForTest()
	sig, err := host.Sign(nil, pairTranscriptBytesForTest("https://example.test"+PairPath, host.PublicKey().Marshal(), sha256.Sum256([]byte(token))))
	if err != nil {
		t.Fatal(err)
	}
	encoded := ssh.MarshalAuthorizedKey(host.PublicKey())
	return servePairBodyForTest(g, token, map[string]string{"host_public_key": string(encoded[:len(encoded)-1]), "proof": base64.RawURLEncoding.EncodeToString(ssh.Marshal(sig))})
}

func TestServePairRejectsLiveSessionRouteWithoutResidentA7(t *testing.T) {
	// Replacing the key would cut a host that is mid-connect or carrying a
	// session route, even when no resident setup exists for the slot.
	host, _ := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32)))
	for _, tc := range []struct {
		name string
		busy func(*principal)
	}{
		{"connecting", func(p *principal) { p.connecting = true }},
		{"live route", func(p *principal) { p.route = &route{owner: p} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, p := pairOnlyRig(t)
			g.mu.Lock()
			tc.busy(p)
			g.mu.Unlock()
			if code := servePairForTest(t, g, host); code != 409 {
				t.Fatalf("busy slot status = %d, want 409", code)
			}
			if key := g.HostKey("host1"); len(key) != 0 {
				t.Fatal("a refused pairing must leave the slot empty")
			}
			g.mu.Lock()
			p.connecting, p.route = false, nil
			g.mu.Unlock()
		})
	}
}

func TestConfigureHostsRejectsPairingSlotWithoutPrincipalKeyA3(t *testing.T) {
	// The pair response hands the host the principal's public key; a slot whose
	// principal has no signing key could never complete a registration.
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize))
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "test", "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}}})
	verifier, err := NewVerifier(VerifierConfig{Issuer: "https://issuer.example.invalid/", Audience: "https://example.test/mcp", Scope: "scope", Types: []string{"at+jwt"}, JWKS: jwks})
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(Config{Verifier: verifier, Principals: []Principal{{Label: "member1", Roles: []Role{RoleRescueOperator, RoleResidentOwner}, Subject: "subject"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close(context.Background())
	if err := g.ConfigureHosts([]HostEntry{{Label: "host1", Principals: []string{"member1"}, Paired: true}}); err == nil {
		t.Fatal("a pairing slot without a principal signing key was accepted")
	}
}

func TestServePairRejectsUnparsableHostKeyA4(t *testing.T) {
	// A malformed key from an authenticated caller is a bad request, not a
	// handler crash that drops the connection.
	g, _ := pairOnlyRig(t)
	if code := servePairBodyForTest(g, pairTokenForTest(), map[string]string{"host_public_key": "not-a-key", "proof": "AAAA"}); code != 400 {
		t.Fatalf("unparsable host key status = %d, want 400", code)
	}
}

func TestServePairHoldsSlotUntilOldHostIsCutA8(t *testing.T) {
	// Between committing the new key and cutting the old host's links, the old
	// session must not start work: it would reach the replaced host and stay
	// uncollectable once the new key rejects that host's proofs.
	g, s, _, _ := residentEvidenceRig(t, true)
	defer g.Close(context.Background())
	var during map[string]any
	g.mu.Lock()
	g.pairing.Disconnect = func(string) { during = s.FixedStart(context.Background(), strings.Repeat("3", 32)) }
	g.mu.Unlock()
	next, _ := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32)))
	if code := servePairForTest(t, g, next); code != 200 {
		t.Fatalf("replacement status = %d, want 200", code)
	}
	if refusal, _ := during["error"].(string); refusal == "" {
		t.Fatalf("old session started work while its host was being cut: %v", during)
	}
}

func TestConfigurePairingRequiresDedicatedVerifierA2(t *testing.T) {
	// The pair route must never accept tokens issued for the MCP entry.
	for _, tc := range []struct {
		name     string
		verifier func(*Gateway) *Verifier
	}{
		{"missing verifier", func(*Gateway) *Verifier { return nil }},
		{"MCP verifier", func(g *Gateway) *Verifier { return g.verifier }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := pairOnlyRig(t)
			g.mu.Lock()
			g.pairing = nil
			g.mu.Unlock()
			if err := g.ConfigurePairing(PairingConfig{Verifier: tc.verifier(g), Endpoint: "https://example.test" + PairPath, RegistrationEndpoint: "wss://example.test/register"}); err == nil {
				t.Fatal("pairing accepted a verifier that is not dedicated to the pair route")
			}
		})
	}
}
