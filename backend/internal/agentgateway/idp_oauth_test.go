package agentgateway_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

const p256Kid = "synthetic-p256-kid"
const idpIssuer = "https://syiadkekkobdkplpnivl.supabase.co/auth/v1"

func p256Key(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func p256JWKS(t *testing.T, key *ecdsa.PrivateKey, kid string) []byte {
	t.Helper()
	pub := key.PublicKey
	set := map[string]any{"keys": []any{map[string]any{
		"kty": "EC", "crv": "P-256", "kid": kid, "alg": "ES256", "use": "sig",
		"x": enc.EncodeToString(pub.X.FillBytes(make([]byte, 32))),
		"y": enc.EncodeToString(pub.Y.FillBytes(make([]byte, 32))),
	}}}
	raw, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func signP256(t *testing.T, key *ecdsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	h, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	c, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := enc.EncodeToString(h) + "." + enc.EncodeToString(c)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return input + "." + enc.EncodeToString(sig)
}

func TestVerifierAcceptsES256AndRejectsChangedSignature(t *testing.T) {
	key := p256Key(t)
	now := time.Now().Truncate(time.Second)
	v, err := agentgateway.NewVerifier(agentgateway.VerifierConfig{
		Issuer: testIssuer, Audience: testResource, Scope: testScope, Types: []string{"at+jwt"},
		JWKS: p256JWKS(t, key, p256Kid), Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"iss": testIssuer, "aud": testResource, "sub": subjects[0], "scope": testScope,
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
	token := signP256(t, key, map[string]any{"alg": "ES256", "kid": p256Kid, "typ": "at+jwt"}, claims)
	if id, err := v.Verify(token); err != nil || id.Subject != subjects[0] {
		t.Fatalf("ES256 token = %+v, %v", id, err)
	}
	parts := strings.Split(token, ".")
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("decoded signature length = %d, %v", len(sig), err)
	}
	sig[0] ^= 1
	parts[2] = base64.RawURLEncoding.EncodeToString(sig)
	if _, err := v.Verify(strings.Join(parts, ".")); err == nil {
		t.Fatal("one-byte signature mutation was accepted")
	}
}

func TestVerifierAcceptsO2CIdentityTokenClaims(t *testing.T) {
	key := p256Key(t)
	now := time.Now().Truncate(time.Second)
	v, err := agentgateway.NewVerifier(agentgateway.VerifierConfig{
		Issuer: idpIssuer, Audience: "tether", Scope: "", Types: []string{"JWT"},
		JWKS: p256JWKS(t, key, p256Kid), Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{
		"iss": idpIssuer, "aud": "tether", "sub": subjects[0], "scope": "openid", "role": "authenticated",
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	}
	token := signP256(t, key, map[string]any{"alg": "ES256", "kid": p256Kid, "typ": "JWT"}, claims)
	if id, err := v.Verify(token); err != nil || id.Subject != subjects[0] {
		t.Fatalf("O2C identity token = %+v, %v", id, err)
	}
}

func TestO2CIdentityTokenClaimFailuresReturn401(t *testing.T) {
	key := p256Key(t)
	now := time.Now().Truncate(time.Second)
	v, err := agentgateway.NewVerifier(agentgateway.VerifierConfig{
		Issuer: idpIssuer, Audience: "tether", Scope: "", Types: []string{"JWT"},
		JWKS: p256JWKS(t, key, p256Kid), Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	r := newModeRig(t, agentgateway.Config{Verifier: v, Resource: testResource})
	claims := func() map[string]any {
		return map[string]any{"iss": idpIssuer, "aud": "tether", "sub": subjects[0], "scope": "openid",
			"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
	}
	other := p256Key(t)
	for _, tc := range []struct {
		name   string
		header map[string]any
		claims map[string]any
		key    *ecdsa.PrivateKey
	}{
		{name: "wrong audience", header: map[string]any{"alg": "ES256", "kid": p256Kid, "typ": "JWT"}, claims: map[string]any{"iss": idpIssuer, "aud": "other", "sub": subjects[0], "scope": "openid", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()}, key: key},
		{name: "wrong issuer", header: map[string]any{"alg": "ES256", "kid": p256Kid, "typ": "JWT"}, claims: map[string]any{"iss": "https://other.example.invalid/", "aud": "tether", "sub": subjects[0], "scope": "openid", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()}, key: key},
		{name: "expired", header: map[string]any{"alg": "ES256", "kid": p256Kid, "typ": "JWT"}, claims: map[string]any{"iss": idpIssuer, "aud": "tether", "sub": subjects[0], "scope": "openid", "iat": now.Add(-2 * time.Minute).Unix(), "exp": now.Add(-time.Minute).Unix()}, key: key},
		{name: "key absent from JWKS", header: map[string]any{"alg": "ES256", "kid": "other-key", "typ": "JWT"}, claims: claims(), key: other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bearer := signP256(t, tc.key, tc.header, tc.claims)
			res := r.do("POST", bearer, "", rpc(0, "initialize", initParams()), nil)
			if res.status != 401 || !strings.Contains(res.header.Get("WWW-Authenticate"), `error="invalid_token"`) {
				t.Fatalf("status = %d, challenge = %q", res.status, res.header.Get("WWW-Authenticate"))
			}
		})
	}
}
