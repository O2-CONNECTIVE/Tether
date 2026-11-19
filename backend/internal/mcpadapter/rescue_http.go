package mcpadapter

import (
	"context"
	"encoding/json"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

// RescueBackend is the principal-bound request port exposed only by a rescue
// discovery session; see docs/rescue-connection-start.md, 担当者の入口.
type RescueBackend interface {
	RescueRequest(context.Context, string) map[string]any
	RescueRequestStatus(context.Context) map[string]any
}

// NewRescueDiscoveryHTTPSession exposes request tools on an unbound session;
// see docs/rescue-connection-start.md, 担当者の入口.
func NewRescueDiscoveryHTTPSession(b RescueBackend, timeout time.Duration) (*HTTPSession, error) {
	if b == nil {
		return nil, ErrUnconfigured
	}
	s, err := NewDiscoveryHTTPSession(timeout)
	if err != nil {
		return nil, err
	}
	s.rescue = b
	return s, nil
}

func rescueToolList() []any {
	return []any{
		map[string]any{"name": "rescue_request", "title": "Request rescue connection", "description": "Request a host connection using its one-time rescue code.",
			"inputSchema":  map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}}, "required": []string{"code"}, "additionalProperties": false},
			"outputSchema": map[string]any{"type": "object", "properties": map[string]any{"state": map[string]any{"type": "string", "enum": []string{"pending"}}}, "required": []string{"state"}, "additionalProperties": false}},
		map[string]any{"name": "rescue_request_status", "title": "Rescue request status", "description": "Wait for or read the latest rescue request state.",
			"inputSchema":  map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
			"outputSchema": map[string]any{"type": "object", "properties": map[string]any{"state": map[string]any{"type": "string", "enum": []string{"none", "pending", "allow", "deny", "timeout", "ended", "failed"}}}, "required": []string{"state"}, "additionalProperties": false}},
	}
}

func (s *HTTPSession) rescueToolCall(id *requestID, params json.RawMessage) HTTPReply {
	m, err := agentwire.Fields(params, "name", "arguments", "_meta")
	var name string
	if err != nil || !isString(m["name"]) || json.Unmarshal(m["name"], &name) != nil {
		return failed(id, codeInvalidParams, "tools/call requires name and arguments")
	}
	args, err := arguments(m["arguments"])
	if name == "rescue_request" {
		args, err = arguments(m["arguments"], "code")
		var code string
		if err != nil || !decode(args["code"], &code) {
			return failed(id, codeInvalidParams, "rescue_request requires a code string")
		}
		return s.invokeHTTP(id, func(ctx context.Context) map[string]any {
			v := s.rescue.RescueRequest(ctx, code)
			message, isError := v["error"].(string)
			if isError && message == "" {
				message = "internal error"
			}
			result := toolResult(v, isError)
			if isError {
				result["content"] = []any{map[string]any{"type": "text", "text": message}}
			}
			return result
		})
	}
	if name == "rescue_request_status" {
		if err != nil || len(args) != 0 {
			return failed(id, codeInvalidParams, "rescue_request_status takes no arguments")
		}
		return s.invokeHTTP(id, func(ctx context.Context) map[string]any {
			return toolResult(s.rescue.RescueRequestStatus(ctx), false)
		})
	}
	return failed(id, codeInvalidParams, "unknown tool: "+name)
}
