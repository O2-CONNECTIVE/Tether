package mcpadapter

// outputSchema describes the existing structuredContent, not an authorization
// boundary. Error results retain their existing error/message object. Keep the
// status and output success shapes separate; no new operation is introduced.
func outputSchema(name string) map[string]any {
 success := statusSchema()
 if name == "rescue_output" { success = outputValueSchema() }
 return map[string]any{
  "type": "object",
  "oneOf": []any{success, closedObject(map[string]any{
   "error": map[string]any{"type":"string"},
   "message": map[string]any{"type":"string"},
  }, "error", "message")},
 }
}

func closedObject(properties map[string]any, required ...string) map[string]any {
 return map[string]any{"type":"object", "properties":properties, "required":required, "additionalProperties":false}
}

func stringEnum(values ...string) map[string]any {
 return map[string]any{"type":"string", "enum":values}
}

func nonnegativeInteger() map[string]any {
 // Output counters are uint64 on the existing wire; do not silently narrow
 // them to the request cursor's JSON-safe maximum.
 return map[string]any{"type":"integer", "minimum":0}
}

func statusSchema() map[string]any {
 boolean := map[string]any{"type":"boolean"}
 exit := map[string]any{"type":"object", "oneOf":[]any{
  closedObject(map[string]any{"known":map[string]any{"type":"boolean", "enum":[]bool{false}}}, "known"),
  closedObject(map[string]any{
   "known":map[string]any{"type":"boolean", "enum":[]bool{true}},
   "code":map[string]any{"type":"integer", "minimum":-2147483648, "maximum":2147483647},
  }, "known", "code"),
 }}
 return closedObject(map[string]any{
  "profile":stringEnum("windows_guest", "mac_current_user"),
  "command_id":commandIDSchema,
  "phase":stringEnum("starting", "running", "exited", "canceled", "failed", "start_unknown"),
  "exit":exit,
  "stop":stringEnum("none", "canceled", "command_timeout", "revoked", "hard_deadline", "transport_lost", "runtime_fault"),
  "cleanup":stringEnum("pending", "confirmed", "unknown", "not_required"),
  "terminal":boolean,
  "output_end":nonnegativeInteger(),
  "truncated":boolean,
  "dropped_bytes":nonnegativeInteger(),
 }, "profile", "command_id", "phase", "exit", "stop", "cleanup", "terminal", "output_end", "truncated", "dropped_bytes")
}

func outputValueSchema() map[string]any {
 return closedObject(map[string]any{
  "command_id":commandIDSchema,
  "chunks":map[string]any{"type":"array", "items":closedObject(map[string]any{
   "stream":stringEnum("stdout", "stderr"),
   "offset":nonnegativeInteger(),
   "encoding":stringEnum("utf-8", "base64"),
   "data":map[string]any{"type":"string"},
  }, "stream", "offset", "encoding", "data")},
  "next_cursor":nonnegativeInteger(),
  "gap_bytes":nonnegativeInteger(),
  "truncated":map[string]any{"type":"boolean"},
  "dropped_bytes":nonnegativeInteger(),
  "complete":map[string]any{"type":"boolean"},
 }, "command_id", "chunks", "next_cursor", "gap_bytes", "truncated", "dropped_bytes", "complete")
}
