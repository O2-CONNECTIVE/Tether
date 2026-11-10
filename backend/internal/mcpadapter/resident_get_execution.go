package mcpadapter

import "context"

// ResidentGetExecutionBackend optionally adds metadata-only reads of retained
// fixed work, including released-result tombstones, in the same read scope.
type ResidentGetExecutionBackend interface {
	FixedGetExecution(context.Context, string) map[string]any
}

func residentGetExecutionTool() map[string]any {
	return map[string]any{
		"name": "tether_fixed_get_execution", "title": "Get fixed execution metadata",
		"description":  "Read retained metadata for this session's principal, enrolled host and control generation. No output payload, host contact, work creation, permission renewal, acknowledgement or release. Command fields are the last observed status, possibly stale; empty fields and exit.known=false do not prove non-execution. Command terminal/exit/stop, command cleanup, original host-owner cleanup and gateway cleanup are separate. collection_ended means gateway observation ended, not complete output or successful execution. result_released means payload was explicitly discarded; the replay tombstone remains. work_error describes the work and does not make this metadata read fail. Read errors use error. Missing records never prove non-execution; do not blindly restart work. Process restart loses this ledger. Use tether_fixed_read_output for bytes when available, and save results before release.",
		"inputSchema":  closedObject(map[string]any{"request_id": map[string]any{"type": "string", "pattern": "^[0-9a-f]{32}$"}}, "request_id"),
		"outputSchema": residentGetExecutionSchema(),
	}
}

func residentGetExecutionSchema() map[string]any {
	branches := []any{}
	for _, released := range []bool{false, true} {
		work := residentWorkSchema(released)["properties"].(map[string]any)
		fields := map[string]any{
			"schema_version":   map[string]any{"type": "integer", "enum": []int{1}},
			"collection_ended": map[string]any{"type": "boolean"},
			"result_released":  map[string]any{"type": "boolean", "enum": []bool{released}},
			"work_error":       work["error"],
		}
		if released {
			fields["collection_ended"] = map[string]any{"type": "boolean", "enum": []bool{true}}
		}
		required := []string{"schema_version", "collection_ended", "result_released", "work_error"}
		for _, key := range []string{"request_id", "phase", "profile", "command_id", "command_phase", "exit", "stop", "terminal", "command_cleanup", "owner_cleanup", "gateway_cleanup"} {
			fields[key] = work[key]
			required = append(required, key)
		}
		branches = append(branches, closedObject(fields, required...))
	}
	branches = append(branches, closedObject(map[string]any{"error": stringEnum("not_read", "invalid", "not_found")}, "error"))
	return map[string]any{"type": "object", "oneOf": branches}
}
