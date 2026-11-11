package agentgateway

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const rotationIssuer = "https://synthetic-team.cloudflareaccess.com"

type rotationTransport func(*http.Request) (*http.Response, error)

func (f rotationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type rotationFixture struct {
	v     *AccessVerifier
	now   time.Time
	body  string
	calls int
}

var rotationRSAOnce sync.Once
var rotationRSA *rsa.PrivateKey
var rotationNextOnce sync.Once
var rotationNext *rsa.PrivateKey

func rotationSigningKey(t *testing.T, kid string) *rsa.PrivateKey {
	if kid != "next" {
		return rotationKey(t)
	}
	rotationNextOnce.Do(func() {
		var err error
		rotationNext, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
	})
	return rotationNext
}

func rotationKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	rotationRSAOnce.Do(func() {
		var err error
		rotationRSA, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
	})
	return rotationRSA
}

func rotationCerts(t *testing.T, kids ...string) string {
	t.Helper()
	keys := []any{}
	for _, kid := range kids {
		key := rotationSigningKey(t, kid)
		keys = append(keys, map[string]any{"kty": "RSA", "kid": kid, "alg": "RS256", "use": "sig",
			"n": b64.EncodeToString(key.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(key.E)).Bytes())})
	}
	raw, _ := json.Marshal(map[string]any{"keys": keys, "public_cert": "ignored synthetic PEM",
		"public_certs": []any{map[string]any{"kid": "not-a-trust-source", "cert": "ignored"}}})
	return string(raw)
}

func rotationToken(t *testing.T, kid string, now time.Time, edit func(map[string]any, map[string]any)) string {
	t.Helper()
	header := map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"}
	claims := map[string]any{"iss": rotationIssuer, "aud": []string{"synthetic-aud"}, "sub": "synthetic-subject",
		"email": "person@example.invalid", "type": "app", "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(time.Hour).Unix()}
	if edit != nil {
		edit(header, claims)
	}
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	input := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rotationSigningKey(t, kid), crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func newRotationFixture(t *testing.T) *rotationFixture {
	t.Helper()
	f := &rotationFixture{now: time.Unix(2000000000, 0), body: rotationCerts(t, "old")}
	var err error
	f.v, err = NewRotatingAccessVerifier(AccessVerifierConfig{Issuer: rotationIssuer, AUD: "synthetic-aud",
		Clock: func() time.Time { return f.now }}, AccessKeyRefreshPolicy{MinInterval: time.Minute, MaxAge: 10 * time.Minute, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.v.rotation.client.Transport = rotationTransport(func(req *http.Request) (*http.Response, error) {
		f.calls++
		if req.URL.String() != rotationIssuer+"/cdn-cgi/access/certs" || req.Method != "GET" ||
			req.Body != nil || req.Header.Get("Authorization") != "" || req.Header.Get(HeaderAccessAssertion) != "" ||
			req.Header.Get("Cookie") != "" {
			t.Fatalf("unexpected fetch request: %s %s", req.Method, req.URL)
		}
		if _, ok := req.Context().Deadline(); !ok {
			t.Fatal("fetch has no timeout")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(f.body))}, nil
	})
	return f
}

func TestAccessRotationOverlapAndRetirement(t *testing.T) {
	f := newRotationFixture(t)
	old := rotationToken(t, "old", f.now, nil)
	next := rotationToken(t, "next", f.now, nil)
	if _, err := f.v.Verify(old); !errors.Is(err, ErrToken) || f.calls != 0 {
		t.Fatal("empty verifier accepted or fetched implicitly")
	}
	if err := f.v.RefreshKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.v.Verify(old); err != nil {
		t.Fatal(err)
	}
	if _, err := f.v.Verify(next); !errors.Is(err, ErrToken) || f.calls != 1 {
		t.Fatal("unknown kid accepted or caused a fetch")
	}
	f.now = f.now.Add(time.Minute)
	f.body = rotationCerts(t, "old", "next")
	if err := f.v.RefreshKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{old, next} {
		if _, err := f.v.Verify(tok); err != nil {
			t.Fatal("provider overlap was not honored", err)
		}
	}
	f.now = f.now.Add(time.Minute)
	f.body = rotationCerts(t, "next")
	if err := f.v.RefreshKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.v.Verify(old); !errors.Is(err, ErrToken) {
		t.Fatal("retired key survived replacement")
	}
	if _, err := f.v.Verify(next); err != nil {
		t.Fatal(err)
	}
}

func TestAccessRotationFailureDoesNotExtendAge(t *testing.T) {
	f := newRotationFixture(t)
	tok := rotationToken(t, "old", f.now, nil)
	if err := f.v.RefreshKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	start := f.now
	f.body = `{"keys":[]}`
	f.now = start.Add(9 * time.Minute)
	if err := f.v.RefreshKeys(context.Background()); !errors.Is(err, ErrAccessKeyRefresh) {
		t.Fatal("empty set accepted")
	}
	if _, err := f.v.Verify(tok); err != nil {
		t.Fatal("known still-fresh key lost on failed refresh")
	}
	f.now = start.Add(10 * time.Minute)
	if _, err := f.v.Verify(tok); !errors.Is(err, ErrToken) {
		t.Fatal("expired cache accepted")
	}
	f.now = start.Add(5 * time.Minute)
	if _, err := f.v.Verify(tok); !errors.Is(err, ErrToken) {
		t.Fatal("clock rollback resurrected expired cache")
	}
}

func TestAccessRotationRejectsMalformedResponses(t *testing.T) {
	for name, body := range map[string]func(*testing.T) string{
		"empty":            func(*testing.T) string { return `{"keys":[]}` },
		"duplicate key id": func(t *testing.T) string { return rotationCerts(t, "same", "same") },
		"too many keys": func(t *testing.T) string {
			return rotationCerts(t, strings.Fields("a b c d e f g h i j k l m n o p q")...)
		},
		"private key": func(t *testing.T) string {
			return strings.Replace(rotationCerts(t, "old"), `"kty":`, `"d":"secret","kty":`, 1)
		},
		"wrong algorithm": func(t *testing.T) string { return strings.Replace(rotationCerts(t, "old"), "RS256", "HS256", 1) },
		"duplicate envelope": func(t *testing.T) string {
			return strings.Replace(rotationCerts(t, "old"), `{"keys":`, `{"keys":[],"keys":`, 1)
		},
		"unknown envelope": func(t *testing.T) string {
			return strings.Replace(rotationCerts(t, "old"), `{"keys":`, `{"jku":"https://attacker.invalid","keys":`, 1)
		},
		"oversized": func(*testing.T) string { return strings.Repeat(" ", maxAccessCertBytes+1) },
		"non JSON":  func(*testing.T) string { return "no" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newRotationFixture(t)
			f.body = body(t)
			if err := f.v.RefreshKeys(context.Background()); !errors.Is(err, ErrAccessKeyRefresh) {
				t.Fatal("malformed refresh accepted", err)
			}
			if _, err := f.v.Verify(rotationToken(t, "old", f.now, nil)); !errors.Is(err, ErrToken) {
				t.Fatal("malformed response populated trust")
			}
		})
	}
}

func TestAccessRotationPreservesClaimAndHeaderChecks(t *testing.T) {
	f := newRotationFixture(t)
	if err := f.v.RefreshKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(map[string]any, map[string]any){
		"issuer":      func(_ map[string]any, c map[string]any) { c["iss"] = "https://other.cloudflareaccess.com" },
		"audience":    func(_ map[string]any, c map[string]any) { c["aud"] = []string{"other"} },
		"service":     func(_ map[string]any, c map[string]any) { c["common_name"] = "service" },
		"expired":     func(_ map[string]any, c map[string]any) { c["exp"] = f.now.Unix() },
		"kind":        func(_ map[string]any, c map[string]any) { c["type"] = "other" },
		"jku":         func(h map[string]any, _ map[string]any) { h["jku"] = "https://attacker.invalid/keys" },
		"jwk":         func(h map[string]any, _ map[string]any) { h["jwk"] = map[string]any{"kid": "old"} },
		"unknown kid": func(h map[string]any, _ map[string]any) { h["kid"] = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.v.Verify(rotationToken(t, "old", f.now, edit)); !errors.Is(err, ErrToken) {
				t.Fatal("token validation widened", err)
			}
		})
	}
	if f.calls != 1 {
		t.Fatal("untrusted assertion triggered fetch")
	}
}

func TestAccessRotationRateLimitAndCancellation(t *testing.T) {
	f := newRotationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if f.v.RefreshKeys(ctx) == nil || f.calls != 0 {
		t.Fatal("cancelled call fetched")
	}
	f.body = "invalid"
	if f.v.RefreshKeys(context.Background()) == nil || f.calls != 1 {
		t.Fatal("invalid response accepted")
	}
	if f.v.RefreshKeys(context.Background()) == nil || f.calls != 1 {
		t.Fatal("failed refresh bypassed rate limit")
	}
	f.now = f.now.Add(time.Minute)
	if f.v.RefreshKeys(context.Background()) == nil || f.calls != 2 {
		t.Fatal("attempt not permitted at interval boundary")
	}
}

func TestAccessRotationSingleFlightAndTimeout(t *testing.T) {
	f := newRotationFixture(t)
	f.v.rotation.policy.Timeout = 20 * time.Millisecond
	entered := make(chan struct{})
	var calls atomic.Int32
	f.v.rotation.client.Transport = rotationTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		close(entered)
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	result := make(chan error, 1)
	go func() { result <- f.v.RefreshKeys(context.Background()) }()
	<-entered
	if f.v.RefreshKeys(context.Background()) == nil || calls.Load() != 1 {
		t.Fatal("concurrent attempt was not bounded")
	}
	if err := <-result; !errors.Is(err, ErrAccessKeyRefresh) {
		t.Fatal("timeout accepted", err)
	}
}

func TestAccessRotationReplacesSameKidMaterial(t *testing.T) {
	f := newRotationFixture(t)
	old := rotationToken(t, "old", f.now, nil)
	next := rotationToken(t, "next", f.now, func(h, _ map[string]any) { h["kid"] = "old" })
	if err := f.v.RefreshKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.v.Verify(next); !errors.Is(err, ErrToken) {
		t.Fatal("foreign key accepted")
	}
	f.now = f.now.Add(time.Minute)
	f.body = strings.Replace(rotationCerts(t, "next"), `"kid":"next"`, `"kid":"old"`, 1)
	if err := f.v.RefreshKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.v.Verify(old); !errors.Is(err, ErrToken) {
		t.Fatal("old key material retained")
	}
	if _, err := f.v.Verify(next); err != nil {
		t.Fatal("new material rejected", err)
	}
}

func TestAccessRotationVerifyConcurrentReplacement(t *testing.T) {
	f := newRotationFixture(t)
	old := rotationToken(t, "old", f.now, nil)
	next := rotationToken(t, "next", f.now, nil)
	if err := f.v.RefreshKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	f.body = rotationCerts(t, "next")
	original := f.v.rotation.client.Transport
	entered, release := make(chan struct{}), make(chan struct{})
	f.v.rotation.client.Transport = rotationTransport(func(req *http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return original.RoundTrip(req)
	})
	refreshed := make(chan error, 1)
	go func() { refreshed <- f.v.RefreshKeys(context.Background()) }()
	<-entered
	if _, err := f.v.Verify(old); err != nil {
		t.Fatal("fetch blocked current keys", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				for _, tok := range []string{old, next} {
					id, err := f.v.Verify(tok)
					if err != nil && !errors.Is(err, ErrToken) {
						t.Error(err)
					}
					if err == nil && id.Subject != "synthetic-subject" {
						t.Error("mixed identity")
					}
				}
			}
		}()
	}
	close(release)
	if err := <-refreshed; err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, err := f.v.Verify(old); !errors.Is(err, ErrToken) {
		t.Fatal("retired key accepted")
	}
	if _, err := f.v.Verify(next); err != nil {
		t.Fatal(err)
	}
}
