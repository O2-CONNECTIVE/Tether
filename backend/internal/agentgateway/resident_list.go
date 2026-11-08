package agentgateway

import (
	"context"
	"sort"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

// FixedList reads only the same principal/enrolled host/control generation as
// FixedStatus. It creates no work, refreshes no authority and contacts no host.
// Lexical keyset pagination is a live view, not a retained snapshot: restart at
// the empty cursor to discover newly admitted IDs sorting before a prior page.
func (s *residentSession) FixedList(ctx context.Context, cursor string, limit int) map[string]any {
	if ctx == nil || ctx.Err() != nil {
		return map[string]any{"error": "not_read"}
	}
	if (cursor != "" && !agentwire.ValidCommandID(cursor)) || limit < 1 || limit > 32 {
		return map[string]any{"error": "invalid"}
	}
	g := s.gateway
	g.mu.Lock()
	defer g.mu.Unlock()
	ids := make([]string, 0, len(s.entry.works)+len(s.entry.tombstones))
	for id, w := range s.entry.works {
		if w.binding.Generation == s.generation && id > cursor {
			ids = append(ids, id)
		}
	}
	for id, t := range s.entry.tombstones {
		if t.generation == s.generation && id > cursor {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = ids[len(ids)-1]
	}
	entries := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		var summary map[string]any
		if w := s.entry.works[id]; w != nil {
			summary = map[string]any{"request_id": id, "phase": w.phase, "terminal": w.status.Terminal, "command_cleanup": agentwire.CleanupName(w.status.Cleanup), "owner_cleanup": w.ownerCleanup, "gateway_cleanup": w.gatewayCleanup, "error": w.failure, "result_released": false}
		} else {
			v := s.entry.tombstones[id].value
			summary = map[string]any{"request_id": id, "result_released": true}
			for _, key := range []string{"phase", "terminal", "command_cleanup", "owner_cleanup", "gateway_cleanup", "error"} {
				summary[key] = v[key]
			}
		}
		entries = append(entries, summary)
	}
	return map[string]any{"schema_version": 1, "executions": entries, "next_cursor": next}
}
