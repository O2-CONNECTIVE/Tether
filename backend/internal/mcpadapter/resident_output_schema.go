package mcpadapter

// Resident output is a different existing contract from the grant-bound tools:
// pending command fields may be empty, errors have no message, and a retained
// tombstone can be returned by start/status/cancel as well as explicit release.
func residentOutputSchema(name string) map[string]any {
	branches := []any{residentWorkSchema(true),
		closedObject(map[string]any{"error": stringEnum("not_sent", "not_released", "not_found", "request_generation_mismatch", "replay_ledger_full", "cleanup_not_confirmed")}, "error"),
		closedObject(map[string]any{"error": stringEnum("busy_or_refused"), "owner_cleanup": stringEnum("unknown")}, "error", "owner_cleanup"),
		closedObject(map[string]any{"error": stringEnum("control_ended"), "owner_cleanup": stringEnum("unknown"), "reinitialize_required": map[string]any{"type": "boolean", "enum": []bool{true}}}, "error", "owner_cleanup", "reinitialize_required"),
	}
	if name != "tether_fixed_release" {
		branches = append(branches, residentWorkSchema(false))
	}
	if name == "tether_run_command" {
		branches = append(branches, closedObject(map[string]any{"error": stringEnum("invalid_run_scope", "request_scope_mismatch")}, "error"))
	}
	return map[string]any{"type": "object", "oneOf": branches}
}

func residentWorkSchema(released bool) map[string]any {
	status := statusSchema()["properties"].(map[string]any)
	chunks := map[string]any{"type": "array", "maxItems": 128, "items": closedObject(map[string]any{
		"stream":   stringEnum("stdout", "stderr"),
		"offset":   nonnegativeInteger(),
		"encoding": stringEnum("base64"),
		"data":     map[string]any{"type": "string", "maxLength": 21848, "pattern": "^([A-Za-z0-9+/]{4})*([A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$"},
	}, "stream", "offset", "encoding", "data")}
	properties := map[string]any{
		"request_id":    map[string]any{"type": "string", "pattern": "^[0-9a-f]{32}$"},
		"phase":         stringEnum("pending", "running", "finished", "unknown"),
		"profile":       stringEnum("", "mac_current_user"),
		"command_id":    map[string]any{"type": "string", "pattern": "^([0-9a-f]{32})?$"},
		"command_phase": stringEnum("", "starting", "running", "exited", "canceled", "failed", "start_unknown"),
		"exit":          status["exit"], "stop": status["stop"], "terminal": status["terminal"],
		"command_cleanup": stringEnum("", "pending", "confirmed", "unknown", "not_required"),
		"owner_cleanup":   stringEnum("pending", "confirmed", "unknown"),
		"gateway_cleanup": stringEnum("pending", "confirmed", "unknown"),
		"chunks":          chunks, "next_cursor": nonnegativeInteger(),
		"error": stringEnum("", "admission_refused_or_uncertain", "registration_unobserved", "start_uncertain", "output_limit", "result_unobserved_or_uncertain"),
	}
	required := []string{"request_id", "phase", "profile", "command_id", "command_phase", "exit", "stop", "terminal", "command_cleanup", "owner_cleanup", "gateway_cleanup", "chunks", "next_cursor", "error"}
	if released {
		properties["phase"] = stringEnum("finished")
		properties["owner_cleanup"], properties["gateway_cleanup"] = stringEnum("confirmed"), stringEnum("confirmed")
		properties["result_released"] = map[string]any{"type": "boolean", "enum": []bool{true}}
		chunks["maxItems"] = 0
		required = append(required, "result_released")
	}
	return closedObject(properties, required...)
}
