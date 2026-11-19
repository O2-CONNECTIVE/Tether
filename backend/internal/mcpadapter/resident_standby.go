package mcpadapter

import "context"

// ResidentStandbyBackend optionally reads the standby of this session's enrolled host.
type ResidentStandbyBackend interface {
	StandbyStatus(context.Context) map[string]any
}

func residentStandbyTool() map[string]any {
	return map[string]any{
		"name":         "tether_standby_status",
		"description":  "Read the Mac's reported standby state, its end time and remaining milliseconds. remaining_ms is calculated using the gateway clock. A null standby_until means the Mac will accept no new work. This tool cannot extend standby; only the person in front of the Mac can extend it.",
		"inputSchema":  map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false},
		"outputSchema": residentStandbySchema(),
	}
}

func residentStandbySchema() map[string]any {
	success := closedObject(map[string]any{
		"schema_version": map[string]any{"type": "integer", "enum": []int{1}},
		"standby":        stringEnum("on", "stopping"),
		"standby_until":  map[string]any{"oneOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}},
		"remaining_ms":   nonnegativeInteger(),
	}, "schema_version", "standby", "standby_until", "remaining_ms")
	return map[string]any{"type": "object", "oneOf": []any{
		success,
		closedObject(map[string]any{"error": stringEnum("not_read")}, "error"),
		closedObject(map[string]any{"error": stringEnum("not_reported")}, "error"),
		closedObject(map[string]any{"error": stringEnum("control_ended")}, "error"),
		closedObject(map[string]any{"error": stringEnum("control_ended"), "reinitialize_required": map[string]any{"type": "boolean", "enum": []bool{true}}}, "error", "reinitialize_required"),
	}}
}
