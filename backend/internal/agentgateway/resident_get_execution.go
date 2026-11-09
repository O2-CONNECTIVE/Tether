package agentgateway

import (
	"context"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentapi"
)

var _ residentapi.ExecutionReader = (*residentSession)(nil)

// GetExecution reads metadata in the session's immutable read scope. It
// does not copy/acknowledge output, contact the host or renew work authority.
func (s *residentSession) GetExecution(ctx context.Context, id string) (residentapi.Execution, error) {
	if ctx == nil || ctx.Err() != nil {
		return residentapi.Execution{}, residentapi.ErrNotRead
	}
	if !agentwire.ValidCommandID(id) {
		return residentapi.Execution{}, residentapi.ErrInvalid
	}
	g := s.gateway
	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() != nil {
		return residentapi.Execution{}, residentapi.ErrNotRead
	}
	if w := s.entry.works[id]; w != nil && w.binding.Generation == s.generation {
		exit := residentapi.ExitStatus{Known: w.status.Exit.Known}
		if w.status.Exit.Known {
			exit.Code = w.status.Exit.Code
		}
		return residentapi.Execution{
			RequestID: w.binding.RequestID, Phase: w.phase, Profile: agentwire.ProfileName(w.status.Profile),
			CommandID: w.status.CommandID, CommandPhase: agentwire.PhaseName(w.status.Phase),
			Exit: exit, Stop: agentwire.StopName(w.status.Stop), Terminal: w.status.Terminal,
			CommandCleanup: agentwire.CleanupName(w.status.Cleanup),
			OwnerCleanup:   w.ownerCleanup, GatewayCleanup: w.gatewayCleanup,
			CollectionEnded: w.done, ResultReleased: false, WorkError: w.failure,
		}, nil
	}
	if t, ok := s.entry.tombstones[id]; ok && t.generation == s.generation {
		// FixedRelease creates this record only after the original collector has
		// joined and both owners confirm cleanup. It need not have run a command.
		return executionFromTombstone(t)
	}
	return residentapi.Execution{}, residentapi.ErrNotFound
}

// Tombstones remain owned by the existing release path. Decode only its known
// metadata fields; malformed internal evidence never becomes a partial result.
func executionFromTombstone(t residentTombstone) (residentapi.Execution, error) {
	valid := true
	stringField := func(key string) string {
		value, ok := t.value[key].(string)
		valid = valid && ok
		return value
	}
	v := residentapi.Execution{
		RequestID: stringField("request_id"), Phase: stringField("phase"), Profile: stringField("profile"),
		CommandID: stringField("command_id"), CommandPhase: stringField("command_phase"),
		Stop: stringField("stop"), CommandCleanup: stringField("command_cleanup"),
		OwnerCleanup: stringField("owner_cleanup"), GatewayCleanup: stringField("gateway_cleanup"),
		CollectionEnded: true, ResultReleased: true, WorkError: stringField("error"),
	}
	terminal, terminalOK := t.value["terminal"].(bool)
	exit, exitOK := t.value["exit"].(map[string]any)
	known, knownOK := exit["known"].(bool)
	v.Terminal, v.Exit.Known = terminal, known
	if known {
		code, ok := exit["code"].(int32)
		valid = valid && ok
		v.Exit.Code = code
	}
	if !valid || !terminalOK || !exitOK || !knownOK {
		return residentapi.Execution{}, residentapi.ErrNotRead
	}
	return v, nil
}

// FixedGetExecution is the compatibility adapter for the existing MCP shape.
// Its schema version and error map are wire concerns, not application metadata.
func (s *residentSession) FixedGetExecution(ctx context.Context, id string) map[string]any {
	v, err := s.GetExecution(ctx, id)
	switch err {
	case nil:
		return fixedExecutionValue(v)
	case residentapi.ErrInvalid:
		return map[string]any{"error": "invalid"}
	case residentapi.ErrNotFound:
		return map[string]any{"error": "not_found"}
	default:
		return map[string]any{"error": "not_read"}
	}
}

func fixedExecutionValue(v residentapi.Execution) map[string]any {
	exit := map[string]any{"known": v.Exit.Known}
	if v.Exit.Known {
		exit["code"] = v.Exit.Code
	}
	return map[string]any{
		"schema_version": 1, "request_id": v.RequestID, "phase": v.Phase, "profile": v.Profile,
		"command_id": v.CommandID, "command_phase": v.CommandPhase,
		"exit": exit, "stop": v.Stop, "terminal": v.Terminal,
		"command_cleanup": v.CommandCleanup, "owner_cleanup": v.OwnerCleanup, "gateway_cleanup": v.GatewayCleanup,
		"collection_ended": v.CollectionEnded, "result_released": v.ResultReleased, "work_error": v.WorkError,
	}
}
