package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// Cloudflare Access assertion mode through the concrete CLI composition. The
// RSA keys are generated in memory per test binary; issuer, AUD tag, subjects
// and email are synthetic placeholders. No Access team is contacted.

const (
	accessIssuer = "https://synthetic-team.example.invalid"
	accessKid    = "synthetic-access"
)

var (
	accessAUD  = strings.Repeat("5a", 32)
	accessOnce sync.Once
	accessKeys [2]*rsa.PrivateKey // [0] configured, [1] never configured
)

func accessKey(t *testing.T, i int) *rsa.PrivateKey {
	t.Helper()
	accessOnce.Do(func() {
		for j := range accessKeys {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			accessKeys[j] = k
		}
	})
	return accessKeys[i]
}

func accessGatewayDoc(t *testing.T) map[string]any {
	t.Helper()
	k := accessKey(t, 0)
	return map[string]any{
		"listen": "127.0.0.1:8787", "auth_mode": "cloudflare_access", "resource": "https://gateway.example.invalid/mcp",
		"access_issuer": accessIssuer, "access_aud": accessAUD, "leeway_seconds": 30,
		"access_keys": map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": accessKid, "alg": "RS256", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())}}},
		"principals": []any{
			map[string]any{"label": "member-1", "subject": "synthetic-subject-1"},
			map[string]any{"label": "member-2", "subject": "synthetic-subject-2"},
		},
	}
}

func accessLocalDoc(t *testing.T) map[string]any {
	t.Helper()
	d := localDoc(t)
	d["gateway"] = accessGatewayDoc(t)
	return d
}

func accessAssertion(t *testing.T, subject string, key *rsa.PrivateKey) string {
	t.Helper()
	now := time.Now()
	h, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": accessKid, "typ": "JWT"})
	c, _ := json.Marshal(map[string]any{"iss": accessIssuer, "aud": []string{accessAUD}, "sub": subject, "email": "member@example.invalid",
		"type": "app", "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()})
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestLocalConfigAcceptsAccessMode(t *testing.T) {
	c, err := parseLocalConfig(encode(t, accessLocalDoc(t)), configPath)
	if err != nil || c.gateway.Mode != agentgateway.ModeCloudflareAccess || c.gateway.Resource != "https://gateway.example.invalid/mcp" ||
		c.gateway.Access.AUD != accessAUD || len(c.keys) != 2 || len(c.hosts) != 2 {
		t.Fatalf("config = %+v, %v", c, err)
	}
}

func TestLocalConfigRefusesAmbiguousAccessMode(t *testing.T) {
	gw := func(d map[string]any) map[string]any { return d["gateway"].(map[string]any) }
	for _, tc := range []struct {
		name   string
		mutate func(d map[string]any)
	}{
		{"missing auth_mode", func(d map[string]any) { delete(gw(d), "auth_mode") }},
		{"OAuth mode named", func(d map[string]any) { gw(d)["auth_mode"] = "oauth_bearer_jwt" }},
		{"OAuth jwks mixed in", func(d map[string]any) { gw(d)["jwks"] = gw(d)["access_keys"] }},
		{"OAuth scope mixed in", func(d map[string]any) { gw(d)["scope"] = "rescue.agent" }},
		{"missing access_aud", func(d map[string]any) { delete(gw(d), "access_aud") }},
		{"missing access_issuer", func(d map[string]any) { delete(gw(d), "access_issuer") }},
		{"missing access_keys", func(d map[string]any) { delete(gw(d), "access_keys") }},
		{"missing resource", func(d map[string]any) { delete(gw(d), "resource") }},
		{"AUD is the resource", func(d map[string]any) { gw(d)["access_aud"] = "https://gateway.example.invalid/mcp" }},
		{"endpoint on the issuer host", func(d map[string]any) {
			d["registration_endpoint"] = "wss://synthetic-team.example.invalid/agent-register"
		}},
		{"endpoint is the resource", func(d map[string]any) { d["registration_endpoint"] = "wss://gateway.example.invalid/mcp" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := accessLocalDoc(t)
			tc.mutate(doc)
			if _, err := parseLocalConfig(encode(t, doc), configPath); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

// The concrete CLI composes the Access verifier from the document: a valid
// synthetic assertion for a configured subject passes authentication and
// authorization and static discovery (no approved grant); everything else is 403 with no
// challenge, and the origin serves no OAuth metadata.
func TestEntryAccessModeBindsAssertionCaller(t *testing.T) {
	doc := encode(t, accessLocalDoc(t))
	r := newRecorder(t, doc)
	r.listener = newFakeListener()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var diagnostic bytes.Buffer
	exited := make(chan int, 1)
	go func() { exited <- execute(ctx, []string{"-config", configPath}, &diagnostic, r.deps()) }()
	select {
	case <-r.listened:
	case <-time.After(5 * time.Second):
		t.Fatal("never listened")
	}
	if got := r.trace(); got != "read load:member-1 load:member-2 listen:127.0.0.1:8787" {
		t.Fatalf("trace = %q", got)
	}
	const host = "Host: gateway.example.invalid\r\nConnection: close\r\n"
	post := func(extra, body string) (int, http.Header, string) {
		return exchange(t, r.listener, "POST /mcp HTTP/1.1\r\n"+host+extra+"Content-Type: application/json\r\nAccept: application/json\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"+body)
	}
	for _, path := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"} {
		if code, _ := roundTrip(t, r.listener, "GET "+path+" HTTP/1.1\r\n"+host+"\r\n"); code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, code)
		}
	}
	for _, tc := range []struct {
		name   string
		header string
		status int
	}{
		{"valid assertion", "Cf-Access-Jwt-Assertion: " + accessAssertion(t, "synthetic-subject-1", accessKey(t, 0)) + "\r\n", http.StatusOK},
		{"second member", "Cf-Access-Jwt-Assertion: " + accessAssertion(t, "synthetic-subject-2", accessKey(t, 0)) + "\r\n", http.StatusOK},
		{"missing", "", http.StatusForbidden},
		{"bearer only", "Authorization: Bearer synthetic-opaque-edge-token\r\n", http.StatusForbidden},
		{"unconfigured subject", "Cf-Access-Jwt-Assertion: " + accessAssertion(t, "synthetic-outsider", accessKey(t, 0)) + "\r\n", http.StatusForbidden},
		{"unconfigured key", "Cf-Access-Jwt-Assertion: " + accessAssertion(t, "synthetic-subject-1", accessKey(t, 1)) + "\r\n", http.StatusForbidden},
		{"spoofed identity header", "Cf-Access-Authenticated-User-Email: member@example.invalid\r\n", http.StatusForbidden},
		{"duplicate assertion", strings.Repeat("Cf-Access-Jwt-Assertion: "+accessAssertion(t, "synthetic-subject-1", accessKey(t, 0))+"\r\n", 2), http.StatusForbidden},
	} {
		code, header, body := post(tc.header, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`)
		if code != tc.status || header.Get("WWW-Authenticate") != "" || (tc.status != http.StatusOK && header.Get(agentgateway.HeaderSession) != "") {
			t.Fatalf("%s: %d %v %q", tc.name, code, header, body)
		}
		if tc.status == http.StatusOK {
			sid := header.Get(agentgateway.HeaderSession)
			if sid == "" {
				t.Fatal("no discovery session")
			}
			boundHeaders := tc.header + "Mcp-Session-Id: " + sid + "\r\nMcp-Protocol-Version: 2025-06-18\r\n"
			if code, _, _ := post(boundHeaders, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != 202 {
				t.Fatal("notification refused", code)
			}
			if code, _, body := post(boundHeaders, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); code != 200 || !strings.Contains(body, "rescue_start") {
				t.Fatal("static catalog refused", code)
			}
			if code, _, _ := post(boundHeaders, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"rescue_start","arguments":{"request_id":"fixture","command":"fixed","timeout_ms":1000}}}`); code != 404 {
				t.Fatal("offline call admitted", code)
			}
			if code, _, _ := post(boundHeaders, `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`); code != 404 {
				t.Fatal("old session survived")
			}
		}

	}
	if code, _ := roundTrip(t, r.listener, "GET /agent-register HTTP/1.1\r\n"+host+"\r\n"); code != http.StatusBadRequest {
		t.Fatalf("registration without subprotocol: %d", code)
	}
	cancel()
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit = %d (%q)", code, diagnostic.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	checkDiagnostic(t, diagnostic.String())
}
