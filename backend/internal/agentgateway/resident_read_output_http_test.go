package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestResidentReadOutputThroughAuthenticatedGateway(t *testing.T) {
	g, s, _, _ := residentEvidenceRig(t)
	defer g.Close(context.Background())
	id := strings.Repeat("1", 32)
	readWork(s, id)
	sign := func(subject string) string {
		header, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": "test", "typ": "at+jwt"})
		claims, _ := json.Marshal(map[string]any{"iss": "https://issuer.example.invalid/", "aud": "https://example.test/mcp", "sub": subject, "scope": "scope", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()})
		enc := base64.RawURLEncoding
		input := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
		key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, 32))
		return input + "." + enc.EncodeToString(ed25519.Sign(key, []byte(input)))
	}
	call := func(bearer, sid, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "https://example.test/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if sid != "" {
			req.Header.Set(HeaderSession, sid)
			req.Header.Set(HeaderProtocol, "2025-06-18")
		}
		res := httptest.NewRecorder()
		g.ServeHTTP(res, req)
		return res
	}
	bearer := sign("subject")
	init := call(bearer, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`)
	sid := init.Header().Get(HeaderSession)
	if init.Code != http.StatusOK || sid == "" {
		t.Fatal(init.Code, init.Body.String())
	}
	if n := call(bearer, sid, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); n.Code != http.StatusAccepted {
		t.Fatal(n.Code, n.Body.String())
	}
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"rescue_fixed_read_output","arguments":{"request_id":"11111111111111111111111111111111","cursor":1,"max_bytes":2}}}`
	for _, credential := range []string{"", sign("other-subject")} {
		if res := call(credential, sid, body); res.Code != http.StatusUnauthorized && res.Code != http.StatusForbidden {
			t.Fatal("unauthorized output read", res.Code, res.Body.String())
		}
	}
	res := call(bearer, sid, body)
	var envelope map[string]any
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &envelope) != nil {
		t.Fatal(res.Code, res.Body.String())
	}
	result := envelope["result"].(map[string]any)
	wantRaw, _ := json.Marshal(s.FixedReadOutput(context.Background(), id, 1, 2))
	var want any
	json.Unmarshal(wantRaw, &want)
	if result["isError"] != false || !reflect.DeepEqual(result["structuredContent"], want) {
		t.Fatal(result)
	}
	var mirror any
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if json.Unmarshal([]byte(text), &mirror) != nil || !reflect.DeepEqual(mirror, want) {
		t.Fatal("text mirror changed")
	}
	if s.entry.current != nil || s.principal.route != nil {
		t.Fatal("read acquired work")
	}
}
