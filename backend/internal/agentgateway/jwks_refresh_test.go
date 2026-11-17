package agentgateway

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func refreshTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func refreshTestJWKS(t *testing.T, key *ecdsa.PrivateKey, kid string) []byte {
	t.Helper()
	pub := key.PublicKey
	raw, err := json.Marshal(map[string]any{"keys": []any{map[string]any{
		"kty": "EC", "crv": "P-256", "kid": kid, "alg": "ES256",
		"x": b64.EncodeToString(pub.X.FillBytes(make([]byte, 32))),
		"y": b64.EncodeToString(pub.Y.FillBytes(make([]byte, 32))),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func refreshTestToken(t *testing.T, key *ecdsa.PrivateKey, kid string, now time.Time) string {
	t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": "ES256", "kid": kid, "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iss": "https://issuer.example.invalid/", "aud": "tether", "sub": "member",
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()})
	input := b64.EncodeToString(header) + "." + b64.EncodeToString(claims)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return input + "." + b64.EncodeToString(sig)
}

func TestVerifierRefreshesJWKSAfterKeyRotation(t *testing.T) {
	key1, key2 := refreshTestKey(t), refreshTestKey(t)
	var mu sync.Mutex
	current, status, requests := refreshTestJWKS(t, key1, "key-1"), http.StatusOK, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		w.WriteHeader(status)
		_, _ = w.Write(current)
	}))
	defer server.Close()
	now := time.Unix(1_800_000_000, 0)
	v, err := NewVerifier(VerifierConfig{Issuer: "https://issuer.example.invalid/", Audience: "tether", Types: []string{"JWT"},
		JWKSURL: server.URL, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	v.refresh.client = server.Client()

	if _, err := v.Verify(refreshTestToken(t, key1, "key-1", now)); err != nil {
		t.Fatalf("initial JWKS fetch: %v", err)
	}
	mu.Lock()
	current = refreshTestJWKS(t, key2, "key-2")
	now = now.Add(oauthJWKSMinInterval)
	mu.Unlock()
	if _, err := v.Verify(refreshTestToken(t, key2, "key-2", now)); err != nil {
		t.Fatalf("rotated JWKS fetch: %v", err)
	}
	if _, err := v.Verify(refreshTestToken(t, key2, "key-2", now)); err != nil {
		t.Fatalf("cached key verification: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Fatalf("JWKS requests = %d, want initial and rotation fetch only", requests)
	}
}

func TestUnknownKIDRefreshIsRateLimitedAndFailureKeepsCachedKeys(t *testing.T) {
	key, unknown := refreshTestKey(t), refreshTestKey(t)
	var mu sync.Mutex
	current, status, requests := refreshTestJWKS(t, key, "known"), http.StatusOK, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		w.WriteHeader(status)
		_, _ = w.Write(current)
	}))
	defer server.Close()
	now := time.Unix(1_800_000_000, 0)
	v, err := NewVerifier(VerifierConfig{Issuer: "https://issuer.example.invalid/", Audience: "tether", Types: []string{"JWT"},
		JWKSURL: server.URL, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	v.refresh.client = server.Client()
	if _, err := v.Verify(refreshTestToken(t, key, "known", now)); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 20; i++ {
		if _, err := v.Verify(refreshTestToken(t, unknown, "unknown", now)); !errors.Is(err, ErrToken) {
			t.Fatalf("unknown kid accepted: %v", err)
		}
	}
	mu.Lock()
	if requests != 1 {
		mu.Unlock()
		t.Fatalf("rapid unknown kid requests caused %d JWKS fetches, want 1", requests)
	}
	status = http.StatusServiceUnavailable
	now = now.Add(oauthJWKSMinInterval)
	mu.Unlock()
	if _, err := v.Verify(refreshTestToken(t, unknown, "other-unknown", now)); !errors.Is(err, ErrToken) {
		t.Fatalf("unknown key after failed refresh = %v", err)
	}
	if _, err := v.Verify(refreshTestToken(t, key, "known", now)); err != nil {
		t.Fatalf("cached key was lost after failed refresh: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Fatalf("JWKS requests after failed refresh = %d, want 2", requests)
	}
}

func TestVerifierWithoutJWKSAndWithoutURLIsRejected(t *testing.T) {
	_, err := NewVerifier(VerifierConfig{Issuer: "https://issuer.example.invalid/", Audience: "tether", Types: []string{"JWT"}})
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("empty key sources = %v", err)
	}
}
