package agentgateway_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// Cloudflare Access assertion mode. Every key, assertion, issuer, AUD tag,
// subject and address is a synthetic placeholder; no Access team, certs
// endpoint or account is contacted or named.

const (
	testAccessIssuer = "https://synthetic-team.example.invalid"
	accessKid        = "synthetic-access"
	accessKid2       = "synthetic-access-next"
	syntheticEmail   = "member@example.invalid"
)

var (
	testAUD     = strings.Repeat("5a", 32) // the shape of an Access AUD tag
	rsaNextOnce sync.Once
	rsaNext     *rsa.PrivateKey
)

// rsaNextKey is a second synthetic RSA key: the configured rotation key, or,
// under accessKid, an unconfigured signer.
func rsaNextKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	rsaNextOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		rsaNext = k
	})
	return rsaNext
}

func rsaJWK(kid string, k *rsa.PrivateKey) map[string]any {
	return map[string]any{"kty": "RSA", "kid": kid, "use": "sig", "n": enc.EncodeToString(k.N.Bytes()), "e": enc.EncodeToString(big.NewInt(int64(k.E)).Bytes())}
}

// accessKeys has one key without "alg" and one with "alg": "RS256"; both are
// the shape of the "keys" member of an Access certs document.
func accessKeys(t *testing.T) []byte {
	t.Helper()
	next := rsaJWK(accessKid2, rsaNextKey(t))
	next["alg"] = "RS256"
	b, _ := json.Marshal(map[string]any{"keys": []any{rsaJWK(accessKid, rsaKey(t)), next}})
	return b
}

func accessHeader() map[string]any {
	return map[string]any{"alg": "RS256", "kid": accessKid, "typ": "JWT"}
}

func accessClaims(subject string) map[string]any {
	now := time.Now()
	return map[string]any{"iss": testAccessIssuer, "aud": []string{testAUD}, "sub": subject, "email": syntheticEmail, "type": "app",
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(), "identity_nonce": "synthetic-nonce", "country": "XX"}
}

// signRaw signs exact header and payload bytes with RS256.
func signRaw(t *testing.T, header, payload []byte, key *rsa.PrivateKey) string {
	t.Helper()
	input := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + enc.EncodeToString(sig)
}

func signRS(t *testing.T, header, body map[string]any, key *rsa.PrivateKey) string {
	t.Helper()
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(body)
	return signRaw(t, h, c, key)
}

func assertion(t *testing.T, body map[string]any) string {
	t.Helper()
	return signRS(t, accessHeader(), body, rsaKey(t))
}

func accessVerifier(t *testing.T) *agentgateway.AccessVerifier {
	t.Helper()
	v, err := agentgateway.NewAccessVerifier(agentgateway.AccessVerifierConfig{Issuer: testAccessIssuer, AUD: testAUD, Keys: accessKeys(t), Leeway: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func newAccessRig(t *testing.T) *rig {
	t.Helper()
	return newModeRig(t, agentgateway.Config{Access: accessVerifier(t), Resource: testResource})
}

// Every refusal is 403 with no WWW-Authenticate challenge and no session; an
// accepted assertion reaches initialize, which answers 503 without a route.
func TestAccessAssertionChecks(t *testing.T) {
	r := newAccessRig(t)
	now := time.Now()
	with := func(edit func(map[string]any)) map[string]any {
		c := accessClaims(subjects[0])
		edit(c)
		return c
	}
	header := func(edit func(map[string]any)) map[string]any {
		h := accessHeader()
		edit(h)
		return h
	}
	signed := assertion(t, accessClaims(subjects[0]))
	parts := strings.Split(signed, ".")
	forged, _ := json.Marshal(accessClaims(subjects[1]))
	tampered := parts[0] + "." + enc.EncodeToString(forged) + "." + parts[2]
	noneHeader, _ := json.Marshal(map[string]any{"alg": "none", "kid": accessKid, "typ": "JWT"})
	unsigned := enc.EncodeToString(noneHeader) + "." + parts[1] + "."
	claims0, _ := json.Marshal(accessClaims(subjects[0]))
	dupClaim := signRaw(t, []byte(`{"alg":"RS256","kid":"`+accessKid+`","typ":"JWT"}`),
		append(claims0[:len(claims0)-1:len(claims0)-1], `,"sub":"`+subjects[1]+`"}`...), rsaKey(t))
	dupHeader := signRaw(t, []byte(`{"alg":"RS256","kid":"`+accessKid+`","typ":"JWT","kid":"`+accessKid2+`"}`), claims0, rsaKey(t))

	for _, tc := range []struct {
		name      string
		assertion string
		status    int
	}{
		{"accepted control", signed, http.StatusOK},
		{"accepted rotation key with alg", signRS(t, header(func(h map[string]any) { h["kid"] = accessKid2 }), accessClaims(subjects[0]), rsaNextKey(t)), http.StatusOK},
		{"accepted second audience", assertion(t, with(func(c map[string]any) { c["aud"] = []string{"other-tag", testAUD} })), http.StatusOK},
		{"missing", "", http.StatusForbidden},
		{"wrong signature", signRS(t, accessHeader(), accessClaims(subjects[0]), rsaNextKey(t)), http.StatusForbidden},
		{"tampered subject", tampered, http.StatusForbidden},
		{"alg none", unsigned, http.StatusForbidden},
		{"unknown kid", signRS(t, header(func(h map[string]any) { h["kid"] = "unknown" }), accessClaims(subjects[0]), rsaKey(t)), http.StatusForbidden},
		{"alg HS256", signRS(t, header(func(h map[string]any) { h["alg"] = "HS256" }), accessClaims(subjects[0]), rsaKey(t)), http.StatusForbidden},
		{"OAuth provider key", sign(t, map[string]any{"alg": "EdDSA", "kid": edKid, "typ": "JWT"}, accessClaims(subjects[0]), issuerKey), http.StatusForbidden},
		{"OAuth access token", token(t, subjects[0]), http.StatusForbidden},
		{"header jku", signRS(t, header(func(h map[string]any) { h["jku"] = "https://synthetic-team.example.invalid/certs" }), accessClaims(subjects[0]), rsaKey(t)), http.StatusForbidden},
		{"header x5u", signRS(t, header(func(h map[string]any) { h["x5u"] = "https://synthetic-team.example.invalid/cert" }), accessClaims(subjects[0]), rsaKey(t)), http.StatusForbidden},
		{"header jwk", signRS(t, header(func(h map[string]any) { h["jwk"] = rsaJWK(accessKid, rsaNextKey(t)) }), accessClaims(subjects[0]), rsaNextKey(t)), http.StatusForbidden},
		{"header x5t", signRS(t, header(func(h map[string]any) { h["x5t"] = "synthetic" }), accessClaims(subjects[0]), rsaKey(t)), http.StatusForbidden},
		{"duplicate header member", dupHeader, http.StatusForbidden},
		{"typ at+jwt", signRS(t, header(func(h map[string]any) { h["typ"] = "at+jwt" }), accessClaims(subjects[0]), rsaKey(t)), http.StatusForbidden},
		{"typ missing", signRS(t, header(func(h map[string]any) { delete(h, "typ") }), accessClaims(subjects[0]), rsaKey(t)), http.StatusOK},
		{"typ empty", signRS(t, header(func(h map[string]any) { h["typ"] = "" }), accessClaims(subjects[0]), rsaKey(t)), http.StatusForbidden},
		{"typ null", signRS(t, header(func(h map[string]any) { h["typ"] = nil }), accessClaims(subjects[0]), rsaKey(t)), http.StatusForbidden},
		{"typ number", signRS(t, header(func(h map[string]any) { h["typ"] = 1 }), accessClaims(subjects[0]), rsaKey(t)), http.StatusForbidden},
		{"typ JWS", signRS(t, header(func(h map[string]any) { h["typ"] = "JWS" }), accessClaims(subjects[0]), rsaKey(t)), http.StatusForbidden},
		{"typ absent wrong signature", signRS(t, header(func(h map[string]any) { delete(h, "typ") }), accessClaims(subjects[0]), rsaNextKey(t)), http.StatusForbidden},
		{"typ absent wrong issuer", signRS(t, header(func(h map[string]any) { delete(h, "typ") }), with(func(c map[string]any) { c["iss"] = "https://other.example.invalid" }), rsaKey(t)), http.StatusForbidden},
		{"issuer with slash", assertion(t, with(func(c map[string]any) { c["iss"] = testAccessIssuer + "/" })), http.StatusForbidden},
		{"other issuer", assertion(t, with(func(c map[string]any) { c["iss"] = "https://other-team.example.invalid" })), http.StatusForbidden},
		{"OAuth issuer", assertion(t, with(func(c map[string]any) { c["iss"] = testIssuer })), http.StatusForbidden},
		{"audience string", assertion(t, with(func(c map[string]any) { c["aud"] = testAUD })), http.StatusForbidden},
		{"audience is the resource URL", assertion(t, with(func(c map[string]any) { c["aud"] = []string{testResource} })), http.StatusForbidden},
		{"other application", assertion(t, with(func(c map[string]any) { c["aud"] = []string{strings.Repeat("5b", 32)} })), http.StatusForbidden},
		{"empty audience", assertion(t, with(func(c map[string]any) { c["aud"] = []string{} })), http.StatusForbidden},
		{"audience non-string", assertion(t, with(func(c map[string]any) { c["aud"] = []any{testAUD, 1} })), http.StatusForbidden},
		{"missing audience", assertion(t, with(func(c map[string]any) { delete(c, "aud") })), http.StatusForbidden},
		{"organization token", assertion(t, with(func(c map[string]any) { c["type"] = "org" })), http.StatusForbidden},
		{"missing type", assertion(t, with(func(c map[string]any) { delete(c, "type") })), http.StatusForbidden},
		{"service token", assertion(t, with(func(c map[string]any) {
			c["sub"], c["common_name"] = "", "synthetic-client.access"
			delete(c, "email")
		})), http.StatusForbidden},
		{"service identity with user sub", assertion(t, with(func(c map[string]any) { c["common_name"] = "synthetic-client.access" })), http.StatusForbidden},
		{"empty subject", assertion(t, with(func(c map[string]any) { c["sub"] = "" })), http.StatusForbidden},
		{"missing subject", assertion(t, with(func(c map[string]any) { delete(c, "sub") })), http.StatusForbidden},
		{"numeric subject", assertion(t, with(func(c map[string]any) { c["sub"] = 1 })), http.StatusForbidden},
		{"duplicate subject member", dupClaim, http.StatusForbidden},
		{"missing email", assertion(t, with(func(c map[string]any) { delete(c, "email") })), http.StatusForbidden},
		{"email without at", assertion(t, with(func(c map[string]any) { c["email"] = "member" })), http.StatusForbidden},
		{"valid email cannot admit an unconfigured subject", assertion(t, with(func(c map[string]any) {
			c["sub"], c["email"] = "synthetic-outsider", subjects[0]+"@example.invalid"
		})), http.StatusForbidden},
		{"expired", assertion(t, with(func(c map[string]any) {
			c["iat"], c["nbf"], c["exp"] = now.Add(-11*time.Minute).Unix(), now.Add(-11*time.Minute).Unix(), now.Add(-time.Minute).Unix()
		})), http.StatusForbidden},
		{"not yet valid", assertion(t, with(func(c map[string]any) { c["nbf"] = now.Add(time.Minute).Unix() })), http.StatusForbidden},
		{"issued in the future", assertion(t, with(func(c map[string]any) { c["iat"] = now.Add(time.Minute).Unix() })), http.StatusForbidden},
		{"lifetime too long", assertion(t, with(func(c map[string]any) { c["exp"] = now.Add(25 * time.Hour).Unix() })), http.StatusForbidden},
		{"exp before iat", assertion(t, with(func(c map[string]any) { c["exp"] = now.Add(-time.Second).Unix() })), http.StatusForbidden},
		{"missing exp", assertion(t, with(func(c map[string]any) { delete(c, "exp") })), http.StatusForbidden},
		{"missing iat", assertion(t, with(func(c map[string]any) { delete(c, "iat") })), http.StatusForbidden},
		{"missing nbf", assertion(t, with(func(c map[string]any) { delete(c, "nbf") })), http.StatusForbidden},
		{"fractional exp", assertion(t, with(func(c map[string]any) { c["exp"] = float64(now.Add(time.Minute).Unix()) + 0.5 })), http.StatusForbidden},
		{"string exp", assertion(t, with(func(c map[string]any) { c["exp"] = "4102444800" })), http.StatusForbidden},
		{"oversized", strings.Repeat("a", agentgateway.MaxTokenBytes+1), http.StatusForbidden},
		{"unconfigured subject", assertion(t, accessClaims("synthetic-outsider")), http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := r.do(http.MethodPost, tc.assertion, "", rpc(0, "initialize", initParams()), nil)
			if tc.status == http.StatusOK && res.status == http.StatusOK {
				if res.header.Get(agentgateway.HeaderSession) == "" {
					t.Fatal("missing discovery session")
				}
				r.do(http.MethodDelete, tc.assertion, res.header.Get(agentgateway.HeaderSession), "", nil)
			}
			if res.status != tc.status || (tc.status != http.StatusOK && res.header.Get(agentgateway.HeaderSession) != "") || res.header.Get("WWW-Authenticate") != "" {
				t.Fatalf("status = %d %v %s", res.status, res.header, res.body)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		mutate func(*http.Request)
		status int
	}{
		{"two assertion headers", func(q *http.Request) {
			q.Header.Add(agentgateway.HeaderAccessAssertion, signed)
			q.Header.Add(agentgateway.HeaderAccessAssertion, signed)
		}, http.StatusForbidden},
		{"two assertions in one header", func(q *http.Request) { q.Header.Set(agentgateway.HeaderAccessAssertion, signed+","+signed) }, http.StatusForbidden},
		{"bearer only, no fallback", func(q *http.Request) { q.Header.Set("Authorization", "Bearer "+token(t, subjects[0])) }, http.StatusForbidden},
		{"assertion as bearer", func(q *http.Request) { q.Header.Set("Authorization", "Bearer "+signed) }, http.StatusForbidden},
		{"assertion cookie only", func(q *http.Request) { q.Header.Set("Cookie", "CF_Authorization="+signed) }, http.StatusForbidden},
		{"identity headers only", func(q *http.Request) {
			q.Header.Set("Cf-Access-Authenticated-User-Email", syntheticEmail)
			q.Header.Set("X-Forwarded-User", subjects[0])
		}, http.StatusForbidden},
		{"opaque edge token beside a valid assertion", func(q *http.Request) {
			q.Header.Set(agentgateway.HeaderAccessAssertion, signed)
			q.Header.Set("Authorization", "Bearer synthetic-opaque-edge-token")
		}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := r.do(http.MethodPost, "", "", rpc(0, "initialize", initParams()), tc.mutate)
			if res.status != tc.status || res.header.Get("WWW-Authenticate") != "" {
				t.Fatalf("status = %d %v %s", res.status, res.header, res.body)
			}
		})
	}
}

// In Access mode discovery and the OAuth challenge belong to the Access edge:
// the origin serves no protected resource metadata and never challenges.
func TestAccessModeServesNoOriginOAuthMetadata(t *testing.T) {
	r := newAccessRig(t)
	for _, path := range []string{agentgateway.MetadataPrefix + "/mcp", agentgateway.MetadataPrefix, "/.well-known/oauth-authorization-server"} {
		rec := httptest.NewRecorder()
		r.gw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://gateway.example.invalid"+path, nil))
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), testAccessIssuer) {
			t.Fatalf("%s = %d %s", path, rec.Code, rec.Body.Bytes())
		}
	}
	if res := r.do(http.MethodPost, "", "", rpc(0, "initialize", initParams()), nil); res.status != http.StatusForbidden ||
		res.header.Get("WWW-Authenticate") != "" || strings.Contains(string(res.body), testAccessIssuer) {
		t.Fatalf("unauthenticated = %d %v %s", res.status, res.header, res.body)
	}
}

// OAuth bearer mode never reads the Access assertion header.
func TestOAuthModeIgnoresAccessAssertion(t *testing.T) {
	r := newRig(t, 0)
	signed := assertion(t, accessClaims(subjects[0]))
	res := r.do(http.MethodPost, "", "", rpc(0, "initialize", initParams()), func(q *http.Request) {
		q.Header.Set(agentgateway.HeaderAccessAssertion, signed)
	})
	if res.status != http.StatusUnauthorized || !strings.Contains(res.header.Get("WWW-Authenticate"), "resource_metadata=") {
		t.Fatalf("assertion without bearer = %d %v", res.status, res.header)
	}
	if res := r.do(http.MethodPost, "", "", rpc(0, "initialize", initParams()), func(q *http.Request) {
		q.Header.Set("Authorization", "Bearer "+signed)
	}); res.status != http.StatusUnauthorized {
		t.Fatalf("assertion as bearer = %d", res.status)
	}
	if res := r.do(http.MethodPost, token(t, subjects[0]), "", rpc(0, "initialize", initParams()), func(q *http.Request) {
		q.Header.Set(agentgateway.HeaderAccessAssertion, "synthetic-garbage")
	}); res.status != http.StatusOK {
		t.Fatalf("bearer beside a garbage assertion = %d", res.status)
	}
}

// A verified Access subject reaches only its own principal's session and
// grant; the session survives a fresh assertion for the same subject, and the
// existing route loss, revocation and non-resume rules hold in this mode.
func TestAccessCallerReachesOnlyItsOwnGrant(t *testing.T) {
	r := newAccessRig(t)
	ma, mb := r.approve(0, 30*time.Second), r.approve(1, 30*time.Second)
	a, b := r.caller(0), r.caller(1)

	if res := r.do(http.MethodPost, a.bearer, a.sid, rpc(1, "tools/list", nil), nil); res.status != http.StatusOK {
		t.Fatalf("tools/list = %d %s", res.status, res.body)
	}
	if res := r.do(http.MethodPost, b.bearer, a.sid, rpc(2, "tools/list", nil), nil); res.status != http.StatusNotFound {
		t.Fatalf("other principal's session = %d", res.status)
	}
	if res := r.do(http.MethodPost, "", a.sid, rpc(3, "tools/list", nil), func(q *http.Request) {
		q.Header.Set("Authorization", "Bearer "+token(t, subjects[0]))
	}); res.status != http.StatusForbidden {
		t.Fatalf("session with an OAuth bearer in Access mode = %d", res.status)
	}
	fresh := assertion(t, edited(accessClaims(subjects[0]), func(c map[string]any) { c["identity_nonce"] = "synthetic-nonce-2" }))
	if res := r.do(http.MethodPost, fresh, a.sid, rpc(4, "tools/list", nil), nil); res.status != http.StatusOK {
		t.Fatalf("own session with a fresh assertion = %d", res.status)
	}

	st, isErr := a.start("access-1", "echo a\n")
	if isErr {
		t.Fatalf("rescue_start = %v", st)
	}
	commandID(t, st)
	ma.proc(t)
	if ma.runtime.startCount() != 1 || mb.runtime.startCount() != 0 {
		t.Fatal("a call reached another principal's Mac")
	}

	_ = ma.hostConn.Close()
	if err := ma.waitHost(t); err == nil {
		t.Fatal("host Serve ended without reporting the lost channel")
	}
	eventually(t, "session end", func() bool {
		return r.do(http.MethodPost, a.bearer, a.sid, rpc(9, "tools/list", nil), nil).status == http.StatusNotFound
	})
	if res := r.do(http.MethodPost, a.bearer, "", rpc(0, "initialize", initParams()), nil); res.status != http.StatusOK {
		t.Fatalf("initialize after loss = %d", res.status)
	}
	if ma.issuer.Snapshot().HasAuthority {
		t.Fatal("the Mac grant survived the lost channel")
	}
	if res := r.do(http.MethodDelete, b.bearer, b.sid, "", nil); res.status != http.StatusNoContent {
		t.Fatalf("DELETE own session = %d", res.status)
	}
	if !mb.issuer.Snapshot().HasAuthority {
		t.Fatal("ending an MCP session ended the grant")
	}
}

func edited(c map[string]any, edit func(map[string]any)) map[string]any {
	edit(c)
	return c
}

func TestAccessVerifierAndGatewayConfigRefusals(t *testing.T) {
	good := agentgateway.AccessVerifierConfig{Issuer: testAccessIssuer, AUD: testAUD, Keys: accessKeys(t)}
	if _, err := agentgateway.NewAccessVerifier(good); err != nil {
		t.Fatal(err)
	}
	keySet := func(keys ...map[string]any) []byte {
		list := []any{}
		for _, k := range keys {
			list = append(list, k)
		}
		b, _ := json.Marshal(map[string]any{"keys": list})
		return b
	}
	rsaWith := func(edit func(map[string]any)) []byte {
		k := rsaJWK(accessKid, rsaKey(t))
		edit(k)
		return keySet(k)
	}
	small := new(big.Int).Lsh(big.NewInt(1), 1023)
	for _, tc := range []struct {
		name string
		edit func(*agentgateway.AccessVerifierConfig)
	}{
		{"http issuer", func(c *agentgateway.AccessVerifierConfig) { c.Issuer = "http://synthetic-team.example.invalid" }},
		{"issuer with slash", func(c *agentgateway.AccessVerifierConfig) { c.Issuer = testAccessIssuer + "/" }},
		{"issuer with path", func(c *agentgateway.AccessVerifierConfig) { c.Issuer = testAccessIssuer + "/cdn-cgi/access/certs" }},
		{"issuer with port", func(c *agentgateway.AccessVerifierConfig) { c.Issuer = testAccessIssuer + ":443" }},
		{"issuer with query", func(c *agentgateway.AccessVerifierConfig) { c.Issuer = testAccessIssuer + "?x=1" }},
		{"upper-case issuer", func(c *agentgateway.AccessVerifierConfig) { c.Issuer = "https://Synthetic-team.example.invalid" }},
		{"empty issuer", func(c *agentgateway.AccessVerifierConfig) { c.Issuer = "" }},
		{"empty AUD", func(c *agentgateway.AccessVerifierConfig) { c.AUD = "" }},
		{"AUD is the resource URL", func(c *agentgateway.AccessVerifierConfig) { c.AUD = testResource }},
		{"AUD with space", func(c *agentgateway.AccessVerifierConfig) { c.AUD = "tag one" }},
		{"AUD too long", func(c *agentgateway.AccessVerifierConfig) { c.AUD = strings.Repeat("a", 257) }},
		{"leeway too long", func(c *agentgateway.AccessVerifierConfig) { c.Leeway = 2 * time.Minute }},
		{"negative leeway", func(c *agentgateway.AccessVerifierConfig) { c.Leeway = -time.Second }},
		{"no keys", func(c *agentgateway.AccessVerifierConfig) { c.Keys = []byte(`{"keys":[]}`) }},
		{"key set not an object", func(c *agentgateway.AccessVerifierConfig) { c.Keys = []byte(`[]`) }},
		{"whole certs document", func(c *agentgateway.AccessVerifierConfig) {
			c.Keys, _ = json.Marshal(map[string]any{"keys": []any{rsaJWK(accessKid, rsaKey(t))}, "public_cert": map[string]any{"kid": accessKid, "cert": "synthetic"}})
		}},
		{"Ed25519 key", func(c *agentgateway.AccessVerifierConfig) {
			c.Keys = keySet(map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "k", "alg": "EdDSA", "x": enc.EncodeToString(make([]byte, 32))})
		}},
		{"Ed25519 key without alg", func(c *agentgateway.AccessVerifierConfig) {
			c.Keys = keySet(map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "k", "x": enc.EncodeToString(make([]byte, 32))})
		}},
		{"RSA key for RS512", func(c *agentgateway.AccessVerifierConfig) {
			c.Keys = rsaWith(func(k map[string]any) { k["alg"] = "RS512" })
		}},
		{"RSA key for HS256", func(c *agentgateway.AccessVerifierConfig) {
			c.Keys = rsaWith(func(k map[string]any) { k["alg"] = "HS256" })
		}},
		{"symmetric key", func(c *agentgateway.AccessVerifierConfig) {
			c.Keys = keySet(map[string]any{"kty": "oct", "kid": "k", "k": "c3ludGhldGlj"})
		}},
		{"private member", func(c *agentgateway.AccessVerifierConfig) {
			c.Keys = rsaWith(func(k map[string]any) { k["d"] = k["n"] })
		}},
		{"encryption key", func(c *agentgateway.AccessVerifierConfig) {
			c.Keys = rsaWith(func(k map[string]any) { k["use"] = "enc" })
		}},
		{"key_ops member", func(c *agentgateway.AccessVerifierConfig) {
			c.Keys = rsaWith(func(k map[string]any) { k["key_ops"] = []string{"verify"} })
		}},
		{"short RSA", func(c *agentgateway.AccessVerifierConfig) {
			c.Keys = rsaWith(func(k map[string]any) { k["n"] = enc.EncodeToString(small.Bytes()) })
		}},
		{"duplicate kid", func(c *agentgateway.AccessVerifierConfig) {
			c.Keys = keySet(rsaJWK(accessKid, rsaKey(t)), rsaJWK(accessKid, rsaNextKey(t)))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := good
			tc.edit(&cfg)
			if _, err := agentgateway.NewAccessVerifier(cfg); !errors.Is(err, agentgateway.ErrConfig) {
				t.Fatalf("NewAccessVerifier = %v", err)
			}
		})
	}

	access, oauth := accessVerifier(t), verifier(t)
	ps := []agentgateway.Principal{{Label: "member-1", Subject: subjects[0]}}
	for _, tc := range []struct {
		name string
		cfg  agentgateway.Config
	}{
		{"both modes", agentgateway.Config{Verifier: oauth, Access: access, Resource: testResource}},
		{"both modes without resource", agentgateway.Config{Verifier: oauth, Access: access}},
		{"neither mode", agentgateway.Config{Resource: testResource}},
		{"Access without resource", agentgateway.Config{Access: access}},
		{"Access resource without path", agentgateway.Config{Access: access, Resource: "https://gateway.example.invalid/"}},
		{"Access http resource", agentgateway.Config{Access: access, Resource: "http://gateway.example.invalid/mcp"}},
		{"OAuth with a separate resource", agentgateway.Config{Verifier: oauth, Resource: testResource}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.Principals = ps
			if _, err := agentgateway.New(tc.cfg); !errors.Is(err, agentgateway.ErrConfig) {
				t.Fatalf("New = %v", err)
			}
		})
	}
}

func accessConfigDoc(t *testing.T, edit func(map[string]any)) []byte {
	t.Helper()
	var keys any
	_ = json.Unmarshal(accessKeys(t), &keys)
	var ps []any
	for i, s := range subjects {
		ps = append(ps, map[string]any{"label": label(i), "subject": s})
	}
	doc := map[string]any{
		"listen": "127.0.0.1:8787", "auth_mode": "cloudflare_access", "resource": testResource,
		"access_issuer": testAccessIssuer, "access_aud": testAUD, "access_keys": keys, "leeway_seconds": 30, "principals": ps,
	}
	if edit != nil {
		edit(doc)
	}
	b, _ := json.Marshal(doc)
	return b
}

func TestParseAccessConfig(t *testing.T) {
	c, err := agentgateway.ParseConfig(accessConfigDoc(t, nil))
	if err != nil || c.Mode != agentgateway.ModeCloudflareAccess || c.Resource != testResource || c.Access.Issuer != testAccessIssuer ||
		c.Access.AUD != testAUD || c.Access.Leeway != 30*time.Second || len(c.Principals) != 4 || c.Verifier.Issuer != "" || c.Verifier.Audience != "" {
		t.Fatalf("ParseConfig = %+v, %v", c, err)
	}
	g, err := c.GatewayConfig()
	if err != nil || g.Access == nil || g.Verifier != nil || g.Resource != testResource || len(g.Principals) != 4 {
		t.Fatalf("GatewayConfig = %+v, %v", g, err)
	}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"missing auth_mode", func(d map[string]any) { delete(d, "auth_mode") }},
		{"OAuth mode with Access members", func(d map[string]any) { d["auth_mode"] = "oauth_bearer_jwt" }},
		{"missing access_issuer", func(d map[string]any) { delete(d, "access_issuer") }},
		{"missing access_aud", func(d map[string]any) { delete(d, "access_aud") }},
		{"missing access_keys", func(d map[string]any) { delete(d, "access_keys") }},
		{"missing resource", func(d map[string]any) { delete(d, "resource") }},
		{"missing leeway", func(d map[string]any) { delete(d, "leeway_seconds") }},
		{"missing principals", func(d map[string]any) { delete(d, "principals") }},
		{"missing listen", func(d map[string]any) { delete(d, "listen") }},
		{"OAuth issuer mixed in", func(d map[string]any) { d["issuer"] = testIssuer }},
		{"OAuth scope mixed in", func(d map[string]any) { d["scope"] = testScope }},
		{"OAuth token types mixed in", func(d map[string]any) { d["token_types"] = []string{"JWT"} }},
		{"OAuth jwks mixed in", func(d map[string]any) { d["jwks"] = d["access_keys"] }},
		{"AUD equals resource", func(d map[string]any) { d["access_aud"] = testResource }},
		{"AUD not a string", func(d map[string]any) { d["access_aud"] = 1 }},
		{"empty AUD", func(d map[string]any) { d["access_aud"] = "" }},
		{"issuer with slash", func(d map[string]any) { d["access_issuer"] = testAccessIssuer + "/" }},
		{"keys as a string", func(d map[string]any) {
			d["access_keys"] = "https://synthetic-team.example.invalid/cdn-cgi/access/certs"
		}},
		{"keys from the OAuth set", func(d map[string]any) {
			var keys any
			_ = json.Unmarshal(jwks(t), &keys)
			d["access_keys"] = keys
		}},
		{"resource without path", func(d map[string]any) { d["resource"] = "https://gateway.example.invalid/" }},
		{"public listener", func(d map[string]any) { d["listen"] = "0.0.0.0:8787" }},
		{"email as principal member", func(d map[string]any) {
			d["principals"] = []any{map[string]any{"label": "member-1", "subject": subjects[0], "email": syntheticEmail}}
		}},
		{"unknown member", func(d map[string]any) { d["client_secret"] = "placeholder" }},
		{"null member", func(d map[string]any) { d["allowed_origins"] = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := agentgateway.ParseConfig(accessConfigDoc(t, tc.edit)); err == nil {
				t.Fatal("refused document accepted")
			}
		})
	}
}

// The deployment profile only selects the listen address rule. In both auth
// modes an omitted profile stays loopback-only, the explicit container profile
// admits the IPv4 wildcard or one RFC 1918 address and nothing wider, and the
// gateway built from an accepted container document still refuses a request
// without its mode's credential (OAuth 401 with a challenge, Access 403).
func TestParseConfigDeploymentProfile(t *testing.T) {
	for _, mode := range []struct {
		name   string
		doc    func(*testing.T, func(map[string]any)) []byte
		denied int
	}{
		{"oauth_bearer_jwt", configDoc, http.StatusUnauthorized},
		{"cloudflare_access", accessConfigDoc, http.StatusForbidden},
	} {
		t.Run(mode.name, func(t *testing.T) {
			profile := func(p any, listen string) func(map[string]any) {
				return func(d map[string]any) {
					d["deployment_profile"], d["listen"] = p, listen
				}
			}
			c, err := agentgateway.ParseConfig(mode.doc(t, nil))
			if err != nil || c.Profile != agentgateway.ProfileLoopback || c.ListenNetwork() != "tcp" {
				t.Fatalf("omitted profile = %q %q, %v", c.Profile, c.ListenNetwork(), err)
			}
			for _, tc := range []struct {
				profile any
				listen  string
				network string
			}{
				{"loopback", "127.0.0.1:8787", "tcp"},
				{"loopback", "[::1]:8787", "tcp"},
				{"container_private", "0.0.0.0:8787", "tcp4"},
				{"container_private", "10.0.0.2:1024", "tcp4"},
				{"container_private", "172.18.0.2:8787", "tcp4"},
				{"container_private", "192.168.50.2:65535", "tcp4"},
			} {
				c, err := agentgateway.ParseConfig(mode.doc(t, profile(tc.profile, tc.listen)))
				if err != nil || string(c.Profile) != tc.profile || c.Listen != tc.listen || c.ListenNetwork() != tc.network {
					t.Fatalf("%v %s = %+v, %v", tc.profile, tc.listen, c, err)
				}
				cfg, err := c.GatewayConfig()
				if err != nil {
					t.Fatal(err)
				}
				g, err := agentgateway.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, testResource, strings.NewReader(`{}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json")
				g.ServeHTTP(rec, req)
				challenged := rec.Header().Get("WWW-Authenticate") != ""
				if rec.Code != mode.denied || challenged != (mode.denied == http.StatusUnauthorized) || rec.Header().Get(agentgateway.HeaderSession) != "" {
					t.Fatalf("%v %s: unauthenticated request = %d %v", tc.profile, tc.listen, rec.Code, rec.Header())
				}
			}
			for _, tc := range []struct {
				name string
				edit func(map[string]any)
			}{
				{"default public wildcard", func(d map[string]any) { d["listen"] = "0.0.0.0:8787" }},
				{"default private address", func(d map[string]any) { d["listen"] = "172.18.0.2:8787" }},
				{"default IPv6 wildcard", func(d map[string]any) { d["listen"] = "[::]:8787" }},
				{"explicit loopback public wildcard", profile("loopback", "0.0.0.0:8787")},
				{"explicit loopback private address", profile("loopback", "10.0.0.2:8787")},
				{"null profile", profile(nil, "0.0.0.0:8787")},
				{"empty profile", profile("", "0.0.0.0:8787")},
				{"unknown profile", profile("container", "0.0.0.0:8787")},
				{"case-folded profile", profile("Container_Private", "0.0.0.0:8787")},
				{"profile not a string", profile(1, "0.0.0.0:8787")},
				{"profile as an object", profile(map[string]any{"name": "container_private"}, "0.0.0.0:8787")},
				{"container loopback", profile("container_private", "127.0.0.1:8787")},
				{"container IPv6 wildcard", profile("container_private", "[::]:8787")},
				{"container IPv6 loopback", profile("container_private", "[::1]:8787")},
				{"container IPv6 unique local", profile("container_private", "[fd00::2]:8787")},
				{"container IPv4-mapped wildcard", profile("container_private", "[::ffff:0.0.0.0]:8787")},
				{"container IPv4-mapped private", profile("container_private", "[::ffff:10.0.0.2]:8787")},
				{"container public address", profile("container_private", "203.0.113.10:8787")},
				{"container shared address", profile("container_private", "100.64.0.2:8787")},
				{"container link-local", profile("container_private", "169.254.0.2:8787")},
				{"container broadcast", profile("container_private", "255.255.255.255:8787")},
				{"container multicast", profile("container_private", "239.0.0.1:8787")},
				{"container name", profile("container_private", "gateway:8787")},
				{"container localhost", profile("container_private", "localhost:8787")},
				{"container empty host", profile("container_private", ":8787")},
				{"container leading-zero octet", profile("container_private", "010.0.0.2:8787")},
				{"container zone", profile("container_private", "[fe80::1%eth0]:8787")},
				{"container privileged port", profile("container_private", "0.0.0.0:443")},
				{"container privileged boundary", profile("container_private", "0.0.0.0:1023")},
				{"container port zero", profile("container_private", "0.0.0.0:0")},
				{"container port beyond range", profile("container_private", "0.0.0.0:65536")},
				{"container signed port", profile("container_private", "0.0.0.0:+8787")},
				{"container leading-zero port", profile("container_private", "0.0.0.0:08787")},
				{"container named port", profile("container_private", "0.0.0.0:http")},
				{"container missing port", profile("container_private", "0.0.0.0")},
				{"loopback signed port", profile("loopback", "127.0.0.1:+8787")},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if _, err := agentgateway.ParseConfig(mode.doc(t, tc.edit)); err == nil {
						t.Fatal("refused document accepted")
					}
				})
			}
			valid := mode.doc(t, profile("container_private", "0.0.0.0:8787"))
			duplicate := append(append([]byte(nil), valid[:len(valid)-1]...), `,"deployment_profile":"container_private"}`...)
			if _, err := agentgateway.ParseConfig(duplicate); err == nil {
				t.Fatal("duplicate deployment_profile accepted")
			}
		})
	}
}

// This detects a mutation that substitutes the valid email for the signed sub.
func TestAccessVerifiedIdentityKeepsSubjectNotEmail(t *testing.T) {
	c := accessClaims("synthetic-outsider")
	c["email"] = syntheticEmail
	id, err := accessVerifier(t).Verify(assertion(t, c))
	if err != nil || id.Issuer != testAccessIssuer || id.Subject != "synthetic-outsider" || id.Subject == syntheticEmail {
		t.Fatalf("verified identity must retain signed issuer/sub: identity=%+v err=%v", id, err)
	}
}
