package mcpadapter

import (
	"context"
	"encoding/json"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

// ResidentListBackend optionally adds bounded discovery of the fixed work
// already readable by this immutable principal/host/generation session.
type ResidentListBackend interface {
	FixedList(context.Context, string, int) map[string]any
}

func residentListTool() map[string]any {
	return map[string]any{
		"name": "rescue_fixed_list", "title": "List owned fixed executions",
		"description":  "Lists only this session's principal, enrolled host and control generation's retained fixed executions and released-result tombstones. No command output or other hosts/generations. Read-only; no work or permission is created. Use request_id with rescue_fixed_status/cancel/release. cursor is empty initially or the prior next_cursor; empty next_cursor ends this page sequence. This is a live lexical request_id view, not a snapshot: restart with an empty cursor to discover newly admitted IDs before a previous cursor. Empty/missing records never prove non-execution; process restart loses this ledger.",
		"inputSchema":  closedObject(map[string]any{"cursor": map[string]any{"type": "string", "pattern": "^([0-9a-f]{32})?$"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 32}}, "cursor", "limit"),
		"outputSchema": residentListSchema(),
	}
}
func residentListSchema() map[string]any {
	p := residentWorkSchema(false)["properties"].(map[string]any)
	fields := map[string]any{"result_released": map[string]any{"type": "boolean"}}
	required := []string{"request_id", "phase", "terminal", "command_cleanup", "owner_cleanup", "gateway_cleanup", "error", "result_released"}
	for _, key := range required[:len(required)-1] {
		fields[key] = p[key]
	}
	return map[string]any{"type": "object", "oneOf": []any{
		closedObject(map[string]any{"schema_version": map[string]any{"type": "integer", "enum": []int{1}}, "executions": map[string]any{"type": "array", "maxItems": 32, "items": closedObject(fields, required...)}, "next_cursor": map[string]any{"type": "string", "pattern": "^([0-9a-f]{32})?$"}}, "schema_version", "executions", "next_cursor"),
		closedObject(map[string]any{"error": stringEnum("not_read", "invalid")}, "error"),
	}}
}
func (s *HTTPSession) residentListCall(id *requestID, raw json.RawMessage) HTTPReply {
	b, ok := s.resident.(ResidentListBackend)
	if !ok {
		return failed(id, codeInvalidParams, "fixed execution list unavailable")
	}
	args, err := arguments(raw, "cursor", "limit")
	var cursor string
	var limit int
	if err != nil || !decode(args["cursor"], &cursor) || (cursor != "" && !agentwire.ValidCommandID(cursor)) || !decode(args["limit"], &limit) || limit < 1 || limit > 32 {
		return failed(id, codeInvalidParams, "cursor must be empty or 32 lowercase hex digits; limit must be an integer from 1 to 32; no other arguments are accepted")
	}
	return s.invokeHTTP(id, func(ctx context.Context) map[string]any {
		v := b.FixedList(ctx, cursor, limit)
		code, _ := v["error"].(string)
		return toolResult(v, code != "")
	})
}
