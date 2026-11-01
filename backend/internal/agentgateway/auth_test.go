package agentgateway_test

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// Authentication failures are 401 invalid_token, a missing scope is 403
// insufficient_scope, and an authenticated but unconfigured subject is 403
// without a challenge. The gateway has no route here, so an accepted token
// reaches initialize and is answered 503: authentication passed, nothing else.
func TestBearerTokenChecks(t *testing.T) {
	r := newRig(t, 0)
	now := time.Now()
	with := func(edit func(map[string]any)) map[string]any {
		c := claims(subjects[0])
		edit(c)
		return c
	}
	header := func(edit func(map[string]any)) map[string]any {
		h := edHeader()
		edit(h)
		return h
	}
	signed := token(t, subjects[0])
	parts := strings.Split(signed, ".")
	forged, _ := json.Marshal(claims(subjects[1]))
	tampered := parts[0] + "." + enc.EncodeToString(forged) + "." + parts[2]
	noneHeader, _ := json.Marshal(map[string]any{"alg": "none", "kid": edKid, "typ": "at+jwt"})
	unsigned := enc.EncodeToString(noneHeader) + "." + parts[1] + "." + enc.EncodeToString([]byte("x"))

	for _, tc := range []struct {
		name   string
		bearer string
		status int
		code   string
	}{
		{"accepted control", signed, http.StatusOK, ""},
		{"accepted audience list and scp array", sign(t, edHeader(), with(func(c map[string]any) {
			c["aud"] = []string{"https://other.example.invalid/api", testResource}
			delete(c, "scope")
			c["scp"] = []string{"other", testScope}
		}), issuerKey), http.StatusOK, ""},
		{"accepted RS256", sign(t, map[string]any{"alg": "RS256", "kid": rsaKid, "typ": "at+jwt"}, claims(subjects[0]), nil), http.StatusOK, ""},
		{"missing", "", http.StatusUnauthorized, ""},
		{"wrong signature", sign(t, edHeader(), claims(subjects[0]), otherKey), http.StatusUnauthorized, "invalid_token"},
		{"tampered subject", tampered, http.StatusUnauthorized, "invalid_token"},
		{"unknown kid", sign(t, header(func(h map[string]any) { h["kid"] = "unknown" }), claims(subjects[0]), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"alg none", unsigned, http.StatusUnauthorized, "invalid_token"},
		{"alg confusion", sign(t, header(func(h map[string]any) { h["alg"] = "RS256" }), claims(subjects[0]), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"embedded jwk", sign(t, header(func(h map[string]any) { h["jwk"] = map[string]any{"kty": "OKP"} }), claims(subjects[0]), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"crit header", sign(t, header(func(h map[string]any) { h["crit"] = []string{"exp"} }), claims(subjects[0]), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"missing typ", sign(t, header(func(h map[string]any) { delete(h, "typ") }), claims(subjects[0]), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"plain JWT type", sign(t, header(func(h map[string]any) { h["typ"] = "JWT" }), claims(subjects[0]), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"wrong issuer", sign(t, edHeader(), with(func(c map[string]any) { c["iss"] = "https://issuer.example.invalid/other" }), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"wrong audience", sign(t, edHeader(), with(func(c map[string]any) { c["aud"] = "https://gateway.example.invalid/other" }), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"expired", sign(t, edHeader(), with(func(c map[string]any) {
			c["iat"], c["exp"] = now.Add(-11*time.Minute).Unix(), now.Add(-time.Minute).Unix()
		}), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"not yet valid", sign(t, edHeader(), with(func(c map[string]any) { c["nbf"] = now.Add(time.Minute).Unix() }), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"issued in the future", sign(t, edHeader(), with(func(c map[string]any) { c["iat"] = now.Add(time.Minute).Unix() }), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"lifetime too long", sign(t, edHeader(), with(func(c map[string]any) { c["exp"] = now.Add(25 * time.Hour).Unix() }), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"missing exp", sign(t, edHeader(), with(func(c map[string]any) { delete(c, "exp") }), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"fractional exp", sign(t, edHeader(), with(func(c map[string]any) { c["exp"] = float64(now.Add(time.Minute).Unix()) + 0.5 }), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"missing subject", sign(t, edHeader(), with(func(c map[string]any) { delete(c, "sub") }), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"scope and scp together", sign(t, edHeader(), with(func(c map[string]any) { c["scp"] = testScope }), issuerKey), http.StatusUnauthorized, "invalid_token"},
		{"missing scope", sign(t, edHeader(), with(func(c map[string]any) { c["scope"] = "openid profile" }), issuerKey), http.StatusForbidden, "insufficient_scope"},
		{"unconfigured subject", token(t, "synthetic-outsider"), http.StatusForbidden, "-"},
		{"oversized", strings.Repeat("a", agentgateway.MaxTokenBytes+1), http.StatusUnauthorized, "invalid_token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := r.do(http.MethodPost, tc.bearer, "", rpc(0, "initialize", initParams()), nil)
			if res.status != tc.status || (tc.status != http.StatusOK && res.header.Get(agentgateway.HeaderSession) != "") {
				t.Fatalf("status = %d %s", res.status, res.body)
			}
			if tc.status == http.StatusOK {
				if res.header.Get(agentgateway.HeaderSession) == "" {
					t.Fatal("missing discovery session")
				}
				r.do(http.MethodDelete, tc.bearer, res.header.Get(agentgateway.HeaderSession), "", nil)
			}
			challenge := res.header.Get("WWW-Authenticate")
			switch tc.code {
			case "-":
				if challenge != "" {
					t.Fatalf("authorization refusal sent a challenge %q", challenge)
				}
			case "":
				if tc.status == http.StatusUnauthorized && (!strings.Contains(challenge, `resource_metadata="https://gateway.example.invalid/.well-known/oauth-protected-resource/mcp"`) || strings.Contains(challenge, "error=")) {
					t.Fatalf("challenge = %q", challenge)
				}
			default:
				if !strings.Contains(challenge, `error="`+tc.code+`"`) || !strings.Contains(challenge, "resource_metadata=") {
					t.Fatalf("challenge = %q", challenge)
				}
				if tc.code == "insufficient_scope" && !strings.Contains(challenge, `scope="`+testScope+`"`) {
					t.Fatalf("challenge = %q", challenge)
				}
			}
		})
	}
	for _, auth := range []string{"Basic c3ludGhldGlj", "Bearer", "Bearer  " + signed, "bearer " + signed + " extra"} {
		res := r.do(http.MethodPost, "", "", rpc(0, "initialize", initParams()), func(q *http.Request) { q.Header.Set("Authorization", auth) })
		if res.status != http.StatusUnauthorized {
			t.Fatalf("Authorization %q = %d", auth, res.status)
		}
	}
	res := r.do(http.MethodPost, signed, "", rpc(0, "initialize", initParams()), func(q *http.Request) { q.Header.Add("Authorization", "Bearer "+signed) })
	if res.status != http.StatusUnauthorized {
		t.Fatalf("two Authorization headers = %d", res.status)
	}
}

func TestVerifierAndGatewayConfigRefusals(t *testing.T) {
	good := agentgateway.VerifierConfig{Issuer: testIssuer, Audience: testResource, Scope: testScope, Types: []string{"at+jwt"}, JWKS: jwks(t)}
	keySet := func(key map[string]any) []byte {
		b, _ := json.Marshal(map[string]any{"keys": []any{key}})
		return b
	}
	pub := enc.EncodeToString(issuerKey.Public().(ed25519.PublicKey))
	small := new(big.Int).Lsh(big.NewInt(1), 1023)
	for _, tc := range []struct {
		name string
		edit func(*agentgateway.VerifierConfig)
	}{
		{"http issuer", func(c *agentgateway.VerifierConfig) { c.Issuer = "http://issuer.example.invalid/" }},
		{"audience without endpoint path", func(c *agentgateway.VerifierConfig) { c.Audience = "https://gateway.example.invalid/" }},
		{"audience with query", func(c *agentgateway.VerifierConfig) { c.Audience = testResource + "?x=1" }},
		{"empty scope", func(c *agentgateway.VerifierConfig) { c.Scope = "" }},
		{"no token types", func(c *agentgateway.VerifierConfig) { c.Types = nil }},
		{"leeway too long", func(c *agentgateway.VerifierConfig) { c.Leeway = 2 * time.Minute }},
		{"no keys", func(c *agentgateway.VerifierConfig) { c.JWKS = []byte(`{"keys":[]}`) }},
		{"private member", func(c *agentgateway.VerifierConfig) {
			c.JWKS = keySet(map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "k", "alg": "EdDSA", "x": pub, "d": pub})
		}},
		{"symmetric key", func(c *agentgateway.VerifierConfig) {
			c.JWKS = keySet(map[string]any{"kty": "oct", "kid": "k", "alg": "HS256", "k": pub})
		}},
		{"missing alg", func(c *agentgateway.VerifierConfig) {
			c.JWKS = keySet(map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "k", "x": pub})
		}},
		{"short RSA", func(c *agentgateway.VerifierConfig) {
			c.JWKS = keySet(map[string]any{"kty": "RSA", "kid": "k", "alg": "RS256", "n": enc.EncodeToString(small.Bytes()), "e": "AQAB"})
		}},
		{"encryption key", func(c *agentgateway.VerifierConfig) {
			c.JWKS = keySet(map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "k", "alg": "EdDSA", "use": "enc", "x": pub})
		}},
		{"duplicate kid", func(c *agentgateway.VerifierConfig) {
			k := map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "k", "alg": "EdDSA", "x": pub}
			c.JWKS, _ = json.Marshal(map[string]any{"keys": []any{k, k}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := good
			tc.edit(&cfg)
			if _, err := agentgateway.NewVerifier(cfg); !errors.Is(err, agentgateway.ErrConfig) {
				t.Fatalf("NewVerifier = %v", err)
			}
		})
	}
	v, err := agentgateway.NewVerifier(good)
	if err != nil {
		t.Fatal(err)
	}
	five := append(append([]string(nil), subjects...), "synthetic-subject-5")
	for _, tc := range []struct {
		name string
		ps   []agentgateway.Principal
	}{
		{"none", nil},
		{"five principals", func() (ps []agentgateway.Principal) {
			for i, s := range five {
				ps = append(ps, agentgateway.Principal{Label: "member-" + string(rune('1'+i)), Subject: s})
			}
			return
		}()},
		{"duplicate subject", []agentgateway.Principal{{Label: "member-1", Subject: subjects[0]}, {Label: "member-2", Subject: subjects[0]}}},
		{"duplicate label", []agentgateway.Principal{{Label: "member-1", Subject: subjects[0]}, {Label: "member-1", Subject: subjects[1]}}},
		{"bad label", []agentgateway.Principal{{Label: "Member 1", Subject: subjects[0]}}},
		{"empty subject", []agentgateway.Principal{{Label: "member-1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := agentgateway.New(agentgateway.Config{Verifier: v, Principals: tc.ps}); !errors.Is(err, agentgateway.ErrConfig) {
				t.Fatalf("New = %v", err)
			}
		})
	}
	if _, err := agentgateway.New(agentgateway.Config{Principals: []agentgateway.Principal{{Label: "member-1", Subject: subjects[0]}}}); !errors.Is(err, agentgateway.ErrConfig) {
		t.Fatalf("New without a verifier = %v", err)
	}
}

func configDoc(t *testing.T, edit func(map[string]any)) []byte {
	t.Helper()
	var keys any
	_ = json.Unmarshal(jwks(t), &keys)
	var ps []any
	for i, s := range subjects {
		ps = append(ps, map[string]any{"label": label(i), "subject": s})
	}
	doc := map[string]any{
		"listen": "127.0.0.1:8787", "auth_mode": "oauth_bearer_jwt", "issuer": testIssuer, "resource": testResource, "scope": testScope,
		"token_types": []string{"at+jwt"}, "leeway_seconds": 30, "jwks": keys, "principals": ps,
	}
	if edit != nil {
		edit(doc)
	}
	b, _ := json.Marshal(doc)
	return b
}

func TestParseConfig(t *testing.T) {
	c, err := agentgateway.ParseConfig(configDoc(t, nil))
	if err != nil || len(c.Principals) != 4 || c.Principals[0].Signer != nil || c.Verifier.Leeway != 30*time.Second ||
		c.Mode != agentgateway.ModeOAuthBearer || c.Resource != testResource || c.Verifier.Audience != testResource {
		t.Fatalf("ParseConfig = %+v, %v", c, err)
	}
	if g, err := c.GatewayConfig(); err != nil || g.Verifier == nil || g.Access != nil || g.Resource != "" {
		t.Fatalf("GatewayConfig = %+v, %v", g, err)
	}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"public listener", func(d map[string]any) { d["listen"] = "0.0.0.0:8787" }},
		{"named listener", func(d map[string]any) { d["listen"] = "localhost:8787" }},
		{"unknown member", func(d map[string]any) { d["client_secret"] = "placeholder" }},
		{"missing issuer", func(d map[string]any) { delete(d, "issuer") }},
		{"signer in file", func(d map[string]any) {
			d["principals"] = []any{map[string]any{"label": "member-1", "subject": subjects[0], "signer": "placeholder"}}
		}},
		{"leeway beyond bound", func(d map[string]any) { d["leeway_seconds"] = 1 << 40 }},
		{"null member", func(d map[string]any) { d["allowed_origins"] = nil }},
		{"legacy document without auth_mode", func(d map[string]any) { delete(d, "auth_mode") }},
		{"unknown auth_mode", func(d map[string]any) { d["auth_mode"] = "OAuth_Bearer_JWT" }},
		{"null auth_mode", func(d map[string]any) { d["auth_mode"] = nil }},
		{"auth_mode not a string", func(d map[string]any) { d["auth_mode"] = 1 }},
		{"Access member mixed in", func(d map[string]any) { d["access_aud"] = testAUD }},
		{"Access mode with OAuth members", func(d map[string]any) { d["auth_mode"] = "cloudflare_access" }},
		{"missing resource", func(d map[string]any) { delete(d, "resource") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := agentgateway.ParseConfig(configDoc(t, tc.edit)); err == nil {
				t.Fatal("refused document accepted")
			}
		})
	}
}
