package residentclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"reflect"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

func decodeDictionary(raw []byte, op, id, after string, cursor uint64, budget int) (json.RawMessage, error) {
	if len(raw) > mcpadapter.MaxLine {
		return nil, ErrProtocol
	}
	m, e := fields(raw, "content", "structuredContent", "isError")
	if e != nil || !exact(m, "content", "structuredContent", "isError") {
		return nil, ErrProtocol
	}
	failed, ok := boolean(m["isError"])
	if !ok {
		return nil, ErrProtocol
	}
	var content []json.RawMessage
	if json.Unmarshal(m["content"], &content) != nil || len(content) != 1 {
		return nil, ErrProtocol
	}
	c, e := fields(content[0], "type", "text")
	if e != nil || !exact(c, "type", "text") || !enum(c["type"], "text") {
		return nil, ErrProtocol
	}
	mirror, ok := str(c["text"])
	if !ok || len(mirror) > mcpadapter.MaxLine {
		return nil, ErrProtocol
	}
	v, e := fields(m["structuredContent"])
	if e != nil {
		return nil, ErrProtocol
	}
	if exact(v, "error") {
		allowed := []string{"not_read", "invalid"}
		if op != "list" {
			allowed = append(allowed, "not_found")
		}
		if op == "read_output" {
			allowed = append(allowed, "result_released", "cursor_ahead", "output_unavailable")
		}
		if !failed || !enum(v["error"], allowed...) {
			return nil, ErrProtocol
		}
	} else {
		if failed {
			return nil, ErrProtocol
		}
		valid := false
		switch op {
		case "list":
			valid = validList(v, after, budget)
		case "get_execution":
			valid = validExecution(v, id)
		case "read_output":
			valid = validReadOutput(v, id, cursor, budget)
		}
		if !valid {
			return nil, ErrProtocol
		}
	}
	var a, b any
	for _, p := range []struct {
		data []byte
		dst  *any
	}{{m["structuredContent"], &a}, {[]byte(mirror), &b}} {
		if _, e := fields(p.data); e != nil {
			return nil, ErrProtocol
		}
		d := json.NewDecoder(bytes.NewReader(p.data))
		d.UseNumber()
		if d.Decode(p.dst) != nil {
			return nil, ErrProtocol
		}
	}
	if !reflect.DeepEqual(a, b) {
		return nil, ErrProtocol
	}
	out, e := json.Marshal(v)
	if e != nil {
		return nil, ErrProtocol
	}
	return out, nil
}
func workState(v map[string]json.RawMessage) bool {
	if !enum(v["phase"], "pending", "running", "finished", "unknown") || !enum(v["command_cleanup"], "", "pending", "confirmed", "unknown", "not_required") {
		return false
	}
	for _, k := range []string{"owner_cleanup", "gateway_cleanup"} {
		if !enum(v[k], "pending", "confirmed", "unknown") {
			return false
		}
	}
	_, ok := boolean(v["terminal"])
	return ok
}
func workError(v json.RawMessage) bool {
	return enum(v, "", "admission_refused_or_uncertain", "registration_unobserved", "start_uncertain", "output_limit", "result_unobserved_or_uncertain")
}
func validList(v map[string]json.RawMessage, after string, limit int) bool {
	if !exact(v, "schema_version", "executions", "next_cursor") || string(v["schema_version"]) != "1" {
		return false
	}
	next, ok := str(v["next_cursor"])
	if !ok || (next != "" && !agentwire.ValidCommandID(next)) {
		return false
	}
	var items []json.RawMessage
	if len(v["executions"]) == 0 || v["executions"][0] != '[' || json.Unmarshal(v["executions"], &items) != nil || len(items) > limit {
		return false
	}
	previous := after
	for _, raw := range items {
		item, e := fields(raw)
		if e != nil || !exact(item, "request_id", "phase", "terminal", "command_cleanup", "owner_cleanup", "gateway_cleanup", "error", "result_released") || !workState(item) || !workError(item["error"]) {
			return false
		}
		id, ok := str(item["request_id"])
		if !ok || !agentwire.ValidCommandID(id) || id <= previous {
			return false
		}
		previous = id
		released, ok := boolean(item["result_released"])
		if !ok {
			return false
		}
		if released && (!enum(item["phase"], "finished") || !enum(item["owner_cleanup"], "confirmed") || !enum(item["gateway_cleanup"], "confirmed")) {
			return false
		}
	}
	return next == "" || (len(items) == limit && next == previous)
}
func validExecution(v map[string]json.RawMessage, id string) bool {
	if !exact(v, "schema_version", "request_id", "phase", "profile", "command_id", "command_phase", "exit", "stop", "terminal", "command_cleanup", "owner_cleanup", "gateway_cleanup", "collection_ended", "result_released", "work_error") || string(v["schema_version"]) != "1" {
		return false
	}
	ended, ok := boolean(v["collection_ended"])
	if !ok {
		return false
	}
	released, ok := boolean(v["result_released"])
	if !ok || (released && !ended) {
		return false
	}
	// Reuse the existing fixed status metadata contract, without copying bytes.
	synthetic := map[string]json.RawMessage{}
	for k, value := range v {
		synthetic[k] = value
	}
	for _, k := range []string{"schema_version", "collection_ended", "result_released", "work_error"} {
		delete(synthetic, k)
	}
	synthetic["error"] = v["work_error"]
	synthetic["chunks"] = json.RawMessage(`[]`)
	synthetic["next_cursor"] = json.RawMessage(`0`)
	if released {
		synthetic["result_released"] = json.RawMessage(`true`)
	}
	return validValue(synthetic, "result", id) == nil
}
func validReadOutput(v map[string]json.RawMessage, id string, cursor uint64, budget int) bool {
	if !exact(v, "schema_version", "request_id", "cursor", "next_cursor", "observed_end", "gap_bytes", "chunks", "has_more", "collection_ended", "phase", "command_phase", "terminal", "command_cleanup", "owner_cleanup", "gateway_cleanup", "source_output_end", "source_truncated", "source_dropped", "work_error") || string(v["schema_version"]) != "1" || text(v["request_id"]) != id || !workState(v) || !workError(v["work_error"]) || !enum(v["command_phase"], "", "starting", "running", "exited", "canceled", "failed", "start_unknown") {
		return false
	}
	start, ok := integer(v["cursor"])
	if !ok || start != cursor {
		return false
	}
	next, ok := integer(v["next_cursor"])
	if !ok || next < start {
		return false
	}
	end, ok := integer(v["observed_end"])
	if !ok || end < next {
		return false
	}
	gap, ok := integer(v["gap_bytes"])
	if !ok || gap > next-start {
		return false
	}
	more, ok := boolean(v["has_more"])
	if !ok || more != (next < end) {
		return false
	}
	for _, k := range []string{"collection_ended", "source_truncated"} {
		if _, ok := boolean(v[k]); !ok {
			return false
		}
	}
	for _, k := range []string{"source_output_end", "source_dropped"} {
		if _, ok := counter(v[k]); !ok {
			return false
		}
	}
	var chunks []json.RawMessage
	if len(v["chunks"]) == 0 || v["chunks"][0] != '[' || json.Unmarshal(v["chunks"], &chunks) != nil || len(chunks) > 128 {
		return false
	}
	previous, total := start, uint64(0)
	for _, raw := range chunks {
		c, e := fields(raw)
		if e != nil || !exact(c, "stream", "offset", "encoding", "data") || !enum(c["stream"], "stdout", "stderr") || !enum(c["encoding"], "base64") {
			return false
		}
		offset, ok := integer(c["offset"])
		if !ok || offset < previous || offset > next {
			return false
		}
		encoded, ok := str(c["data"])
		if !ok || len(encoded) > 5464 {
			return false
		}
		data, e := base64.StdEncoding.Strict().DecodeString(encoded)
		if e != nil || len(data) == 0 || len(data) > budget || base64.StdEncoding.EncodeToString(data) != encoded || uint64(len(data)) > next-offset {
			return false
		}
		total += uint64(len(data))
		if total > uint64(budget) {
			return false
		}
		previous = offset + uint64(len(data))
	}
	return total == next-start-gap
}
