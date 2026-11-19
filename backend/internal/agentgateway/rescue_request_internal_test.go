package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRescueRequestRoleRefusalDoesNotReserveFailureSlot(t *testing.T) {
	now := time.Now()
	cfg := rescueReceptionConfig(t, &now)
	cfg.Organization = "O2 CONNECTIVE"
	cfg.CallTimeout = 5 * time.Second
	cfg.Principals[0].DisplayName = "山田（サポート）"
	cfg.Principals[0].Roles = []Role{RoleResidentOwner}
	g, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer g.Close(context.Background())
	res := openRescue(t, g, 70)
	bearer := internalRescueToken(t, now)
	init := internalRPC(t, g, bearer, "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"},
	}})
	sid := init.Header().Get(HeaderSession)
	if sid == "" {
		t.Fatalf("initialize did not create a session: %s", init.Body.String())
	}
	_ = internalRPC(t, g, bearer, sid, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	call := internalRPC(t, g, bearer, sid, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
		"name": "rescue_request", "arguments": map[string]any{"code": res.Code},
	}})
	var response map[string]any
	if err := json.Unmarshal(call.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	result, _ := response["result"].(map[string]any)
	text := ""
	if content, ok := result["content"].([]any); ok && len(content) > 0 {
		text, _ = content[0].(map[string]any)["text"].(string)
	}
	if result["isError"] != true || text != "this principal may not request a rescue" {
		t.Fatalf("resident_owner request = isError %v, text %q; want role refusal", result["isError"], text)
	}
	if failures := g.rescueCodes.failures["op1"]; len(failures) != 0 {
		t.Fatalf("role refusal reserved failure slots: %v", failures)
	}
}

// A request must expire from the gateway clock even while the host keeps its reception open.
func TestRescueRequestWaitDefaultsToSeventyFiveSecondLifetime(t *testing.T) {
	clock := newRescueExpiryClock(time.Now())
	now := clock.Now()
	cfg := rescueReceptionConfig(t, &now)
	cfg.Clock = clock.Now
	cfg.Organization = "O2 CONNECTIVE"
	cfg.CallTimeout = 5 * time.Second
	cfg.Principals[0].DisplayName = "山田（サポート）"
	g, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer g.Close(context.Background())
	if RescueRequestLifetime != 75*time.Second {
		t.Fatalf("request lifetime = %v; want 75 seconds", RescueRequestLifetime)
	}
	res := openRescue(t, g, 71)
	bearer := internalRescueToken(t, now)
	sid := internalInitialize(t, g, bearer)
	call := internalTool(t, g, bearer, sid, "rescue_request", map[string]any{"code": res.Code})
	if internalState(t, call) != "pending" {
		t.Fatalf("rescue_request state = %q; want pending", internalState(t, call))
	}
	var identity RescueIdentity
	select {
	case identity = <-res.Request:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach the host")
	}
	clock.Add(RescueRequestLifetime)
	statusResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		statusResult <- internalTool(t, g, bearer, sid, "rescue_request_status", map[string]any{})
	}()
	var status *httptest.ResponseRecorder
	select {
	case status = <-statusResult:
	case <-time.After(3 * time.Second):
		t.Fatal("request status did not become timeout within 3 seconds")
	}
	if got := internalState(t, status); got != RescueTimeout {
		t.Fatalf("request status after 75 seconds = %q; want timeout", got)
	}
	select {
	case <-res.Done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout did not close the reception")
	}
	hostKey := rescueHostKey(71)
	digest := sha256.Sum256(RescueRequestTranscript(g.RescueEndpoint(), res.ID, hostKey.PublicKey().Marshal(), identity.Nonce, identity.Client, identity.DisplayName, identity.Organization, identity.Profile))
	signature, err := hostKey.Sign(rand.Reader, RescueAnswerTranscript(res.ID, identity.Nonce, digest[:], RescueAllow, Dispatch{}))
	if err != nil {
		t.Fatal(err)
	}
	conn, peer := net.Pipe()
	err = g.AnswerRescue(res.ID, RescueAnswer{Answer: RescueAllow, Signature: signature.Blob}, conn)
	if err == nil {
		t.Fatal("late allow after timeout was accepted")
	}
	_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	var one [1]byte
	if n, readErr := peer.Read(one[:]); n != 0 || readErr == nil {
		t.Fatalf("late answer left connection open or sent %d bytes (err %v)", n, readErr)
	}
	_ = peer.Close()
	g.mu.Lock()
	route := g.bySubject["s1"].route
	g.mu.Unlock()
	if route != nil {
		t.Fatal("timed-out request created a route")
	}
}

func internalInitialize(t *testing.T, g *Gateway, bearer string) string {
	t.Helper()
	init := internalRPC(t, g, bearer, "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"},
	}})
	sid := init.Header().Get(HeaderSession)
	if sid == "" {
		t.Fatalf("initialize did not create a session: %s", init.Body.String())
	}
	_ = internalRPC(t, g, bearer, sid, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return sid
}

func internalTool(t *testing.T, g *Gateway, bearer, sid, name string, args map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return internalRPC(t, g, bearer, sid, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
}

func internalState(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	result, _ := response["result"].(map[string]any)
	structured, _ := result["structuredContent"].(map[string]any)
	state, _ := structured["state"].(string)
	return state
}

func internalRescueToken(t *testing.T, now time.Time) string {
	t.Helper()
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x52}, ed25519.SeedSize))
	header, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": "test", "typ": "at+jwt"})
	claims, _ := json.Marshal(map[string]any{"iss": "https://issuer.example.invalid/", "aud": "https://example.test/mcp", "sub": "s1", "scope": "scope", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(input)))
}

func internalRPC(t *testing.T, g *Gateway, bearer, session string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "https://example.test/mcp", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set(HeaderSession, session)
		req.Header.Set(HeaderProtocol, "2025-06-18")
	}
	recorder := httptest.NewRecorder()
	g.ServeHTTP(recorder, req)
	return recorder
}
