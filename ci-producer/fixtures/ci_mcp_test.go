//go:build darwin && cgo && ci_actual_producer

package residentpipe

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func ciJWT(t *testing.T, key ed25519.PrivateKey, resource string) string {
	return ciJWTSubject(t, key, resource, ciSubject)
}
func ciJWTSubject(t *testing.T, key ed25519.PrivateKey, resource, subject string) string {
	t.Helper()
	encode := func(v any) string {
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	data := encode(map[string]any{"alg": "EdDSA", "kid": "ci", "typ": "at+jwt"}) + "." + encode(map[string]any{"iss": ciIssuer, "aud": resource, "sub": subject, "scope": "ci", "iat": time.Now().Unix(), "exp": time.Now().Add(10 * time.Minute).Unix()})
	return data + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(data)))
}
func (g *ciGateway) request(method string, params any, token string) (int, http.Header, map[string]any) {
	g.t.Helper()
	g.sequence++
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": g.sequence, "method": method, "params": params})
	if err != nil {
		g.diagnostic.fail(g.t, "mcp-construction")
	}
	req, err := http.NewRequest(http.MethodPost, g.resource, strings.NewReader(string(b)))
	if err != nil {
		g.diagnostic.fail(g.t, "mcp-construction")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if g.sid != "" {
		req.Header.Set(agentgateway.HeaderSession, g.sid)
		req.Header.Set(agentgateway.HeaderProtocol, mcpadapter.ProtocolVersion)
	}
	res, err := g.client.Do(req)
	if err != nil {
		g.diagnostic.fail(g.t, "mcp-request")
	}
	data, readErr := io.ReadAll(io.LimitReader(res.Body, mcpadapter.MaxLine+1))
	closeErr := res.Body.Close()
	if readErr != nil || closeErr != nil || len(data) > mcpadapter.MaxLine {
		g.diagnostic.fail(g.t, "mcp-response-io")
	}
	if res.TLS == nil || len(res.TLS.VerifiedChains) == 0 {
		g.diagnostic.fail(g.t, "mcp-tls")
	}
	var message map[string]any
	if strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		if err = json.Unmarshal(data, &message); err != nil {
			g.diagnostic.fail(g.t, "mcp-construction")
		}
	}
	if res.StatusCode == http.StatusOK && message["id"] != float64(g.sequence) {
		g.diagnostic.fail(g.t, "mcp-correlation")
	}
	return res.StatusCode, res.Header, message
}
func (g *ciGateway) initialize() {
	g.t.Helper()
	g.sid = ""
	params := map[string]any{"protocolVersion": mcpadapter.ProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "explicit-ci-producer-client", "version": "1"}}
	for _, token := range []string{"", "invalid-synthetic-jwt"} {
		status, header, _ := g.request("initialize", params, token)
		if status != http.StatusUnauthorized || header.Get(agentgateway.HeaderSession) != "" {
			g.diagnostic.fail(g.t, "mcp-auth-negative")
		}
	}
	status, header, message := g.request("initialize", params, g.token)
	result, ok := message["result"].(map[string]any)
	g.sid = header.Get(agentgateway.HeaderSession)
	if status != http.StatusOK || !ok || result["protocolVersion"] != mcpadapter.ProtocolVersion || g.sid == "" {
		g.diagnostic.fail(g.t, "mcp-initialize")
	}
	// Complete the actual MCP lifecycle with a notification, never an RPC id.
	req, err := http.NewRequest(http.MethodPost, g.resource, strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if err != nil {
		g.diagnostic.fail(g.t, "mcp-notification-build")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set(agentgateway.HeaderSession, g.sid)
	req.Header.Set(agentgateway.HeaderProtocol, mcpadapter.ProtocolVersion)
	res, err := g.client.Do(req)
	if err != nil {
		g.diagnostic.fail(g.t, "mcp-notification-request")
	}
	body, readErr := io.ReadAll(io.LimitReader(res.Body, 2))
	closeErr := res.Body.Close()
	if readErr != nil || closeErr != nil || res.StatusCode != http.StatusAccepted || len(body) != 0 || res.TLS == nil || len(res.TLS.VerifiedChains) == 0 {
		g.diagnostic.fail(g.t, "mcp-notification-response")
	}
	// A valid separately mapped principal cannot adopt the first principal's SID.
	status, _, _ = g.request("tools/list", nil, g.otherToken)
	if status != http.StatusNotFound {
		g.diagnostic.fail(g.t, "mcp-principal-isolation")
	}
}
func (g *ciGateway) tool(name, id string) map[string]any {
	g.t.Helper()
	g.diagnostic.beginTool(name)
	if name != "rescue_fixed_start" && name != "rescue_fixed_status" && name != "rescue_fixed_cancel" {
		g.diagnostic.fail(g.t, "mcp-tool-name")
	}
	status, _, message := g.request("tools/call", map[string]any{"name": name, "arguments": map[string]any{"request_id": id}}, g.token)
	result, ok := message["result"].(map[string]any)
	if status != http.StatusOK || !ok {
		g.diagnostic.fail(g.t, "mcp-tool-response")
	}
	g.diagnostic.observeTool(result)
	if code := ciToolResultFailure(result); code != "" {
		g.diagnostic.fail(g.t, code)
	}
	return result["structuredContent"].(map[string]any)
}

// Classify only fixed protocol/error enums. Never emit response values, text,
// identities, commands, paths or error strings. Every prior rejection remains
// a rejection; this does not make an error result eligible for another poll.
func ciToolResultFailure(result map[string]any) string {
	value, ok := result["structuredContent"].(map[string]any)
	if !ok {
		return "mcp-tool-result-shape"
	}
	failed, ok := result["isError"].(bool)
	if !ok {
		return "mcp-tool-result-flag"
	}
	if !failed {
		return ""
	}
	switch value["error"] {
	case "admission_refused_or_uncertain":
		return "mcp-tool-admission-uncertain"
	case "start_uncertain":
		return "mcp-tool-start-uncertain"
	case "registration_unobserved":
		return "mcp-tool-registration-unobserved"
	case "result_unobserved_or_uncertain":
		if value["command_cleanup"] == "unknown" {
			return "mcp-tool-native-cleanup-unknown"
		}
		return "mcp-tool-result-uncertain"
	case "output_limit":
		return "mcp-tool-output-limit"
	case "not_found":
		return "mcp-tool-not-found"
	case "control_ended":
		return "mcp-tool-control-ended"
	case "request_generation_mismatch":
		return "mcp-tool-generation-mismatch"
	case "busy_or_refused":
		return "mcp-tool-busy-refused"
	case "", nil:
		return "mcp-tool-error-unspecified"
	default:
		return "mcp-tool-error-other"
	}
}
