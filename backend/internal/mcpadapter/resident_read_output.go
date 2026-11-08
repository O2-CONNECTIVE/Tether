package mcpadapter

import (
	"context"
	"encoding/json"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

const MaxResidentReadBytes = 4096
const MaxResidentReadCursor uint64 = 1<<53 - 1

// ResidentReadOutputBackend adds non-acknowledging reads of already retained
// output. It never calls the source Output API, whose cursor acknowledges bytes.
type ResidentReadOutputBackend interface {
	FixedReadOutput(context.Context, string, uint64, int) map[string]any
}

func residentReadOutputTool() map[string]any {
	return map[string]any{
		"name": "rescue_fixed_read_output", "title": "Read retained fixed output",
		"description": "Read untrusted retained output of this session's principal, enrolled host and control generation. Read-only, no acknowledgement, release, execution or permission renewal. Start cursor at 0, then use next_cursor; max_bytes bounds decoded bytes, not base64 characters. gap_bytes counts skipped unavailable bytes in this page. has_more means more positions in the observed retained view, not more source output. Empty chunks do not mean finished: inspect collection_ended, phase, terminal, cleanup and work_error separately. collection_ended only means gateway observation ended, never complete output or successful execution. source_* fields are the last observed command status, possibly stale. Do not busy-poll or blindly restart work on errors, missing records, gaps or lost sessions. Save results before explicit release; process restart loses this ledger.",
		"inputSchema": closedObject(map[string]any{
			"request_id": map[string]any{"type": "string", "pattern": "^[0-9a-f]{32}$"},
			"cursor":     residentReadCursorSchema(),
			"max_bytes":  map[string]any{"type": "integer", "minimum": 1, "maximum": MaxResidentReadBytes},
		}, "request_id", "cursor", "max_bytes"),
		"outputSchema": residentReadOutputSchema(),
	}
}
func residentReadCursorSchema() map[string]any {
	return map[string]any{"type": "integer", "minimum": 0, "maximum": MaxResidentReadCursor}
}
func residentReadOutputSchema() map[string]any {
	work := residentWorkSchema(false)["properties"].(map[string]any)
	chunks := work["chunks"].(map[string]any)
	chunks["items"].(map[string]any)["properties"].(map[string]any)["data"].(map[string]any)["maxLength"] = 5464
	fields := map[string]any{
		"schema_version": map[string]any{"type": "integer", "enum": []int{1}},
		"cursor":         residentReadCursorSchema(), "next_cursor": residentReadCursorSchema(), "observed_end": residentReadCursorSchema(),
		"gap_bytes": residentReadCursorSchema(), "chunks": chunks,
		"has_more": map[string]any{"type": "boolean"}, "collection_ended": map[string]any{"type": "boolean"},
		"source_output_end": nonnegativeInteger(), "source_truncated": map[string]any{"type": "boolean"}, "source_dropped": nonnegativeInteger(),
		"work_error": work["error"],
	}
	required := []string{"schema_version", "cursor", "next_cursor", "observed_end", "gap_bytes", "chunks", "has_more", "collection_ended", "source_output_end", "source_truncated", "source_dropped", "work_error"}
	for _, key := range []string{"request_id", "phase", "command_phase", "terminal", "command_cleanup", "owner_cleanup", "gateway_cleanup"} {
		fields[key] = work[key]
		required = append(required, key)
	}
	return map[string]any{"type": "object", "oneOf": []any{
		closedObject(fields, required...),
		closedObject(map[string]any{"error": stringEnum("not_read", "invalid", "not_found", "result_released", "cursor_ahead", "output_unavailable")}, "error"),
	}}
}
func (s *HTTPSession) residentReadOutputCall(id *requestID, raw json.RawMessage) HTTPReply {
	b, ok := s.resident.(ResidentReadOutputBackend)
	if !ok {
		return failed(id, codeInvalidParams, "fixed output read unavailable")
	}
	args, err := arguments(raw, "request_id", "cursor", "max_bytes")
	var request string
	var cursor uint64
	var maxBytes int
	if err != nil || !decode(args["request_id"], &request) || !agentwire.ValidCommandID(request) || !decode(args["cursor"], &cursor) || cursor > MaxResidentReadCursor || !decode(args["max_bytes"], &maxBytes) || maxBytes < 1 || maxBytes > MaxResidentReadBytes {
		return failed(id, codeInvalidParams, "request_id must be 32 lowercase hex digits; cursor must be an integer from 0 to 9007199254740991; max_bytes must be an integer from 1 to 4096; no other arguments are accepted")
	}
	return s.invokeHTTP(id, func(ctx context.Context) map[string]any {
		v := b.FixedReadOutput(ctx, request, cursor, maxBytes)
		code, _ := v["error"].(string)
		return toolResult(v, code != "")
	})
}
