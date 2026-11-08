package agentgateway

import (
	"context"
	"encoding/base64"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

// FixedReadOutput copies one bounded live view under the existing owner lock.
// Unlike the source Output cursor, this cursor never acknowledges/discards data.
func (s *residentSession) FixedReadOutput(ctx context.Context, id string, cursor uint64, maxBytes int) map[string]any {
	if ctx == nil || ctx.Err() != nil {
		return map[string]any{"error": "not_read"}
	}
	if !agentwire.ValidCommandID(id) || cursor > mcpadapter.MaxResidentReadCursor || maxBytes < 1 || maxBytes > mcpadapter.MaxResidentReadBytes {
		return map[string]any{"error": "invalid"}
	}
	g := s.gateway
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() != nil {
		return map[string]any{"error": "not_read"}
	}
	if w := s.entry.works[id]; w != nil && w.binding.Generation == s.generation {
		if cursor > w.cursor {
			return map[string]any{"error": "cursor_ahead"}
		}
		return residentReadValue(w, cursor, maxBytes)
	}
	if t, ok := s.entry.tombstones[id]; ok && t.generation == s.generation {
		return map[string]any{"error": "result_released"}
	}
	return map[string]any{"error": "not_found"}
}

func residentReadValue(w *residentWork, cursor uint64, maxBytes int) map[string]any {
	if w.cursor > mcpadapter.MaxResidentReadCursor || len(w.output) > 128 {
		return map[string]any{"error": "output_unavailable"}
	}
	chunks := make([]map[string]any, 0)
	next, gap, previousEnd := cursor, uint64(0), uint64(0)
	total := 0
	for _, c := range w.output {
		offset, offsetOK := c["offset"].(uint64)
		encoded, dataOK := c["data"].(string)
		stream, streamOK := c["stream"].(string)
		if !offsetOK || !dataOK || !streamOK || c["encoding"] != "base64" || (stream != "stdout" && stream != "stderr") || len(encoded) > 21848 {
			return map[string]any{"error": "output_unavailable"}
		}
		data, err := base64.StdEncoding.Strict().DecodeString(encoded)
		total += len(data)
		end := offset + uint64(len(data))
		if err != nil || len(data) == 0 || total > 16384 || offset < previousEnd || end < offset || end > w.cursor {
			return map[string]any{"error": "output_unavailable"}
		}
		previousEnd = end
		// Still validate later retained chunks after filling the byte budget.
		if maxBytes == 0 || end <= next {
			continue
		}
		if next < offset {
			gap += offset - next
			next = offset
		}
		part := data[next-offset:]
		if len(part) > maxBytes {
			part = part[:maxBytes]
		}
		chunks = append(chunks, map[string]any{"stream": stream, "offset": next, "encoding": "base64", "data": base64.StdEncoding.EncodeToString(part)})
		next += uint64(len(part))
		maxBytes -= len(part)
	}
	// The collection cursor can advance beyond retained bytes on output_limit
	// or a source gap. Surface the whole missing tail rather than silently end.
	if maxBytes > 0 && next < w.cursor {
		gap += w.cursor - next
		next = w.cursor
	}
	return map[string]any{
		"schema_version": 1, "request_id": w.binding.RequestID,
		"cursor": cursor, "next_cursor": next, "observed_end": w.cursor,
		"gap_bytes": gap, "chunks": chunks, "has_more": next < w.cursor,
		"collection_ended": w.done, "phase": w.phase, "command_phase": agentwire.PhaseName(w.status.Phase), "terminal": w.status.Terminal,
		"command_cleanup": agentwire.CleanupName(w.status.Cleanup), "owner_cleanup": w.ownerCleanup, "gateway_cleanup": w.gatewayCleanup,
		"source_output_end": w.status.OutputEnd, "source_truncated": w.status.Truncated, "source_dropped": w.status.Dropped,
		"work_error": w.failure,
	}
}
