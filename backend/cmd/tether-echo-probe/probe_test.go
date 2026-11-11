package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// The probe accepts only the existing cloudflare_access document shape, bound
// to the probe resource path, with one to four subjects and no origins. The
// listener rule is the existing loopback/container_private validation.
func TestProbeConfigAcceptsOnlyAccessProbeDocument(t *testing.T) {
	if _, err := load(probeDoc(t, nil), testLimits()); err != nil {
		t.Fatalf("synthetic probe document refused: %v", err)
	}
	c, err := agentgateway.ParseConfig(probeDoc(t, func(d map[string]any) {
		d["deployment_profile"], d["listen"] = "container_private", "0.0.0.0:8787"
	}))
	if err != nil || c.ListenNetwork() != "tcp4" {
		t.Fatalf("container profile = %q, %v", c.ListenNetwork(), err)
	}
	if _, err := newProbe(c, testLimits()); err != nil {
		t.Fatalf("container document refused: %v", err)
	}

	oauth := func(d map[string]any) {
		for _, k := range []string{"access_issuer", "access_aud", "access_keys"} {
			delete(d, k)
		}
		d["auth_mode"], d["issuer"], d["scope"], d["token_types"] = "oauth_bearer_jwt", "https://synthetic-issuer.example.invalid", "tether.probe", []string{"at+jwt"}
		d["jwks"] = map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "synthetic-ed", "alg": "EdDSA",
			"x": base64.RawURLEncoding.EncodeToString(make([]byte, 32))}}}
	}
	if _, err := load(probeDoc(t, oauth), testLimits()); !errors.Is(err, errProbeConfig) {
		t.Fatalf("parsed OAuth bearer document must be refused by the probe: %v", err)
	}
	principals := func(n int) func(map[string]any) {
		return func(d map[string]any) {
			var ps []any
			for i := range n {
				ps = append(ps, map[string]any{"label": "member-" + string(rune('1'+i)), "subject": "synthetic-subject-" + string(rune('1'+i))})
			}
			d["principals"] = ps
		}
	}
	if _, err := load(probeDoc(t, principals(4)), testLimits()); err != nil {
		t.Fatalf("four subjects refused: %v", err)
	}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"real gateway MCP resource", func(d map[string]any) { d["resource"] = "https://probe.example.invalid/mcp" }},
		{"WebSocket path as resource", func(d map[string]any) { d["resource"] = "https://probe.example.invalid" + WSPath }},
		{"resource with trailing path", func(d map[string]any) { d["resource"] = testResource + "/more" }},
		{"encoded resource path", func(d map[string]any) { d["resource"] = "https://probe.example.invalid/tether-probe/v1/%65cho" }},
		{"allowed origins", func(d map[string]any) { d["allowed_origins"] = []string{"https://probe.example.invalid"} }},
		{"five subjects", principals(5)},
		{"no subjects", func(d map[string]any) { d["principals"] = []any{} }},
		{"email as principal member", func(d map[string]any) {
			d["principals"] = []any{map[string]any{"label": "member-1", "subject": subject1, "email": "member@example.invalid"}}
		}},
		{"duplicate subject", func(d map[string]any) {
			d["principals"] = []any{map[string]any{"label": "member-1", "subject": subject1}, map[string]any{"label": "member-2", "subject": subject1}}
		}},
		{"missing auth_mode", func(d map[string]any) { delete(d, "auth_mode") }},
		{"missing access_aud", func(d map[string]any) { delete(d, "access_aud") }},
		{"empty access_aud", func(d map[string]any) { d["access_aud"] = "" }},
		{"empty access_issuer", func(d map[string]any) { d["access_issuer"] = "" }},
		{"rotating source without owner", func(d map[string]any) {
			d["access_issuer"] = "https://synthetic-team.cloudflareaccess.com"
			d["access_key_source"] = "cloudflare_certs"
			delete(d, "access_keys")
		}},
		{"no access keys", func(d map[string]any) { d["access_keys"] = map[string]any{"keys": []any{}} }},
		{"empty listen", func(d map[string]any) { d["listen"] = "" }},
		{"default profile wildcard", func(d map[string]any) { d["listen"] = "0.0.0.0:8787" }},
		{"container loopback", func(d map[string]any) { d["deployment_profile"], d["listen"] = "container_private", "127.0.0.1:8787" }},
		{"container public address", func(d map[string]any) {
			d["deployment_profile"], d["listen"] = "container_private", "203.0.113.10:8787"
		}},
		{"signing key reference", func(d map[string]any) { d["principal_keys"] = []any{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := load(probeDoc(t, tc.edit), testLimits()); err == nil {
				t.Fatal("refused document accepted")
			}
		})
	}
	if _, err := load(probeDoc(t, nil), limits{}); !errors.Is(err, errProbeConfig) {
		t.Fatalf("zero limits accepted: %v", err)
	}
}

func post(p *probe, token, body string, edit func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "https://probe.example.invalid"+HTTPPath, strings.NewReader(body))
	req.Header.Set("Content-Type", echoType)
	if token != "" {
		req.Header.Set(agentgateway.HeaderAccessAssertion, token)
	}
	if edit != nil {
		edit(req)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

// checkReply requires exactly the probe-only reply: no subject, claim or
// header is echoed, and command_access is present and false.
func checkReply(t *testing.T, rec *httptest.ResponseRecorder, body string) {
	t.Helper()
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("reply = %d %v %s", rec.Code, rec.Header(), rec.Body.Bytes())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || len(m) != 5 {
		t.Fatalf("reply members = %v, %v", m, err)
	}
	echoed, _ := base64.StdEncoding.DecodeString(m["echo_base64"].(string))
	if m["probe"] != probeName || m["command_access"] != false || m["note"] != probeNote ||
		m["echo_bytes"] != float64(len(body)) || string(echoed) != body {
		t.Fatalf("reply = %v", m)
	}
	for _, leak := range []string{subject1, subject2, testIssuer, testAUD, "member@example.invalid"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Fatal("reply discloses an identity or claim")
		}
	}
}

// Positive synthetic assertions for both subjects pass; every other caller is
// 403 before the body is read, with no challenge and no echo. A valid
// signature for a subject outside the allowlist is refused.
func TestHTTPEchoAuthentication(t *testing.T) {
	p := newTestProbe(t, testLimits())
	checkReply(t, post(p, assertion(t, subject1), "synthetic-probe", nil), "synthetic-probe")
	checkReply(t, post(p, assertion(t, subject2), "", nil), "")

	now := time.Now()
	edited := func(edit func(map[string]any)) string {
		c := claims(subject1)
		edit(c)
		return signWith(t, c, testKey(t, 0))
	}
	signed := assertion(t, subject1)
	parts := strings.Split(signed, ".")
	swapped, _ := json.Marshal(claims(outsider))
	tampered := parts[0] + "." + enc.EncodeToString(swapped) + "." + parts[2]
	for _, tc := range []struct {
		name  string
		token string
		edit  func(*http.Request)
	}{
		{"unauthenticated", "", nil},
		{"unconfigured signing key", signWith(t, claims(subject1), testKey(t, 1)), nil},
		{"tampered subject", tampered, nil},
		{"valid signature, subject not allowlisted", assertion(t, outsider), nil},
		{"allowlisted email cannot admit another subject", edited(func(c map[string]any) {
			c["sub"], c["email"] = outsider, subject1+"@example.invalid"
		}), nil},
		{"expired", edited(func(c map[string]any) {
			c["iat"], c["nbf"], c["exp"] = now.Add(-11*time.Minute).Unix(), now.Add(-11*time.Minute).Unix(), now.Add(-time.Minute).Unix()
		}), nil},
		{"not yet valid", edited(func(c map[string]any) { c["nbf"] = now.Add(time.Minute).Unix() }), nil},
		{"other issuer", edited(func(c map[string]any) { c["iss"] = "https://other-team.example.invalid" }), nil},
		{"other application AUD", edited(func(c map[string]any) { c["aud"] = []string{strings.Repeat("5b", 32)} }), nil},
		{"service token", edited(func(c map[string]any) { c["common_name"] = "synthetic-client.access" }), nil},
		{"garbage", "synthetic-not-a-jwt", nil},
		{"oversized", strings.Repeat("a", agentgateway.MaxTokenBytes+1), nil},
		{"two assertion headers", "", func(r *http.Request) {
			r.Header.Add(agentgateway.HeaderAccessAssertion, signed)
			r.Header.Add(agentgateway.HeaderAccessAssertion, signed)
		}},
		{"two assertions in one header", signed + "," + signed, nil},
		{"assertion as bearer", "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+signed) }},
		{"assertion cookie only", "", func(r *http.Request) { r.Header.Set("Cookie", "CF_Authorization="+signed) }},
		{"identity headers only", "", func(r *http.Request) {
			r.Header.Set("Cf-Access-Authenticated-User-Email", "member@example.invalid")
			r.Header.Set("X-Forwarded-User", subject1)
		}},
		{"oversized body without assertion", "", func(r *http.Request) {
			r.Body, r.ContentLength = io.NopCloser(strings.NewReader(strings.Repeat("x", 4096))), 4096
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(p, tc.token, "synthetic-probe", tc.edit)
			if rec.Code != http.StatusForbidden || rec.Header().Get("WWW-Authenticate") != "" || strings.Contains(rec.Body.String(), "synthetic-probe") {
				t.Fatalf("status = %d %v %q", rec.Code, rec.Header(), rec.Body.String())
			}
		})
	}
}

// Only POST at the exact echo path is served; sizes and media type are fixed.
func TestHTTPEchoRequestShape(t *testing.T) {
	p := newTestProbe(t, testLimits())
	token := assertion(t, subject1)
	request := func(method, target string, body io.Reader) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, body)
		req.Header.Set("Content-Type", echoType)
		req.Header.Set(agentgateway.HeaderAccessAssertion, token)
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		return rec
	}
	const origin = "https://probe.example.invalid"
	for _, tc := range []struct {
		name   string
		method string
		target string
		status int
	}{
		{"GET echo", http.MethodGet, origin + HTTPPath, http.StatusMethodNotAllowed},
		{"POST WebSocket path", http.MethodPost, origin + WSPath, http.StatusMethodNotAllowed},
		{"root", http.MethodPost, origin + "/", http.StatusNotFound},
		{"real MCP path", http.MethodPost, origin + "/mcp", http.StatusNotFound},
		{"prefix", http.MethodPost, origin + HTTPPath + "/x", http.StatusNotFound},
		{"trailing slash", http.MethodPost, origin + HTTPPath + "/", http.StatusNotFound},
		{"encoded path", http.MethodPost, origin + "/tether-probe/v1/%65cho", http.StatusNotFound},
		{"query", http.MethodPost, origin + HTTPPath + "?target=elsewhere", http.StatusBadRequest},
		{"empty query", http.MethodPost, origin + HTTPPath + "?", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := request(tc.method, tc.target, strings.NewReader("x")); rec.Code != tc.status {
				t.Fatalf("status = %d", rec.Code)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		edit   func(*http.Request)
		status int
	}{
		{"origin", func(r *http.Request) { r.Header.Set("Origin", origin) }, http.StatusForbidden},
		{"text body", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType},
		{"declared oversize", func(r *http.Request) {
			r.Body, r.ContentLength = io.NopCloser(strings.NewReader(strings.Repeat("x", 65))), 65
		}, http.StatusRequestEntityTooLarge},
		{"undeclared oversize", func(r *http.Request) {
			r.Body, r.ContentLength = io.NopCloser(strings.NewReader(strings.Repeat("x", 65))), -1
		}, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := post(p, token, "x", tc.edit); rec.Code != tc.status {
				t.Fatalf("status = %d", rec.Code)
			}
		})
	}
	limit := string(bytes.Repeat([]byte{0, 0xff}, 32))
	checkReply(t, post(p, token, limit, nil), limit)
}

// The HTTP echo admits a bounded number of concurrent requests without
// queueing, and a closed probe admits nothing.
func TestHTTPEchoConcurrencyBound(t *testing.T) {
	l := testLimits()
	l.HTTP = 1
	p := newTestProbe(t, l)
	token := assertion(t, subject1)
	pr, pw := io.Pipe()
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		first <- post(p, token, "", func(r *http.Request) { r.Body, r.ContentLength = pr, -1 })
	}()
	eventually(t, "first request admitted", func() bool { return len(p.httpSlots) == 1 })
	if rec := post(p, token, "x", nil); rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("second concurrent request = %d %v", rec.Code, rec.Header())
	}
	go func() {
		_, _ = pw.Write([]byte("late"))
		_ = pw.Close()
	}()
	select {
	case rec := <-first:
		checkReply(t, rec, "late")
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not finish")
	}
	checkReply(t, post(p, token, "x", nil), "x")
	if err := p.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if rec := post(p, token, "x", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("request after Close = %d", rec.Code)
	}
}
