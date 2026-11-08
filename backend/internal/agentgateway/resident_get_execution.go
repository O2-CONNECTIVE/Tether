package agentgateway

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

// FixedGetExecution reads metadata in the session's immutable read scope. It
// does not copy/acknowledge output, contact the host or renew work authority.
func (s *residentSession) FixedGetExecution(ctx context.Context, id string) map[string]any {
	if ctx == nil || ctx.Err() != nil {
		return map[string]any{"error": "not_read"}
	}
	if !agentwire.ValidCommandID(id) {
		return map[string]any{"error": "invalid"}
	}
	g := s.gateway
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() != nil {
		return map[string]any{"error": "not_read"}
	}
	if w := s.entry.works[id]; w != nil && w.binding.Generation == s.generation {
		exit := map[string]any{"known": w.status.Exit.Known}
		if w.status.Exit.Known {
			exit["code"] = w.status.Exit.Code
		}
		return map[string]any{
			"schema_version": 1, "request_id": w.binding.RequestID,
			"phase": w.phase, "profile": agentwire.ProfileName(w.status.Profile),
			"command_id": w.status.CommandID, "command_phase": agentwire.PhaseName(w.status.Phase),
			"exit": exit, "stop": agentwire.StopName(w.status.Stop), "terminal": w.status.Terminal,
			"command_cleanup": agentwire.CleanupName(w.status.Cleanup),
			"owner_cleanup":   w.ownerCleanup, "gateway_cleanup": w.gatewayCleanup,
			"collection_ended": w.done, "result_released": false, "work_error": w.failure,
		}
	}
	if t, ok := s.entry.tombstones[id]; ok && t.generation == s.generation {
		// FixedRelease creates this record only after the original collector has
		// joined and both owners confirm cleanup. It need not have run a command.
		v := map[string]any{"schema_version": 1, "collection_ended": true, "result_released": true, "work_error": t.value["error"]}
		for _, key := range []string{"request_id", "phase", "profile", "command_id", "command_phase", "stop", "terminal", "command_cleanup", "owner_cleanup", "gateway_cleanup"} {
			v[key] = t.value[key]
		}
		exit := map[string]any{}
		for key, value := range t.value["exit"].(map[string]any) {
			exit[key] = value
		}
		v["exit"] = exit
		return v
	}
	return map[string]any{"error": "not_found"}
}
