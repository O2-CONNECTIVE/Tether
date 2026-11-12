package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// This fixture observes the public HTTP boundary with synthetic credentials.
// It does not model an SDK's session reuse or an edge WebSocket connection.
type lifecycleControl struct {
	mu         sync.Mutex
	generation string
	begins     int
}

func (c *lifecycleControl) ResidentGeneration(string, string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}
func (c *lifecycleControl) set(generation string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation = generation
}
func (c *lifecycleControl) BeginResidentFixed(context.Context, string, string, string, string, time.Time) error {
	c.mu.Lock()
	c.begins++
	c.mu.Unlock()
	return ErrResidentWorkUnconfigured
}
func (c *lifecycleControl) BeginResidentRun(context.Context, string, string, string, string, time.Time, ResidentRunScope) error {
	return ErrResidentWorkUnconfigured
}

type lifecycleHTTP struct {
	g       *Gateway
	s       *residentSession
	control *lifecycleControl
	bearer  string
	now     time.Time // read and changed only under Gateway.mu
}

func newLifecycleHTTP(t *testing.T) *lifecycleHTTP {
	t.Helper()
	g, s, _, _ := residentEvidenceRig(t)
	f := &lifecycleHTTP{g: g, s: s, control: &lifecycleControl{}, now: time.Now()}
	g.residentOnly = true
	g.resident.control = f.control
	g.clock = func() time.Time { return f.now }
	header, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": "test", "typ": "at+jwt"})
	claims, _ := json.Marshal(map[string]any{"iss": "https://issuer.example.invalid/", "aud": "https://example.test/mcp", "sub": "subject", "scope": "scope", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	enc := base64.RawURLEncoding
	input := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, 32))
	f.bearer = input + "." + enc.EncodeToString(ed25519.Sign(key, []byte(input)))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := g.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return f
}

func (f *lifecycleHTTP) call(method, sid, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://example.test/mcp", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+f.bearer)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	if sid != "" {
		r.Header.Set(HeaderSession, sid)
		r.Header.Set(HeaderProtocol, "2025-06-18")
	}
	w := httptest.NewRecorder()
	f.g.ServeHTTP(w, r)
	return w
}

const lifecycleInit = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`
const lifecycleList = `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
const lifecycleMetadata = `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"tether_fixed_get_execution","arguments":{"request_id":"11111111111111111111111111111111"}}}`

func (f *lifecycleHTTP) open(t *testing.T) string {
	t.Helper()
	w := f.call(http.MethodPost, "", lifecycleInit)
	sid := w.Header().Get(HeaderSession)
	if w.Code != 200 || sid == "" {
		t.Fatalf("initialize: %d %s", w.Code, w.Body.String())
	}
	w = f.call(http.MethodPost, sid, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if w.Code != 202 {
		t.Fatalf("initialized: %d %s", w.Code, w.Body.String())
	}
	return sid
}

func (f *lifecycleHTTP) count(t *testing.T, want int) {
	t.Helper()
	f.g.mu.Lock()
	defer f.g.mu.Unlock()
	if len(f.g.sessions) != want || f.s.principal.sessions != want {
		t.Fatalf("session accounting: map=%d principal=%d want=%d", len(f.g.sessions), f.s.principal.sessions, want)
	}
}

func lifecycleValue(t *testing.T, res *httptest.ResponseRecorder, wantError bool) map[string]any {
	t.Helper()
	var envelope struct {
		Result struct {
			IsError bool           `json:"isError"`
			Value   map[string]any `json:"structuredContent"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &envelope) != nil || envelope.Error != nil || envelope.Result.Value == nil || envelope.Result.IsError != wantError {
		t.Fatalf("unexpected tool reply: %d %s", res.Code, res.Body.String())
	}
	return envelope.Result.Value
}
