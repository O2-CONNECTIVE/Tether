//go:build darwin && cgo && ci_actual_producer

package residentpipe

import (
	"fmt"
	"reflect"
	"strings"
)

const ciUnknownWork = "ffffffffffffffffffffffffffffffff"

// These commands go through the unchanged foreground parser and client, never
// directly to the gateway. Each page is one explicit request on the same pipe.
func (p *ciWorkChild) dictionary(g *ciGateway, op, id, suffix string) map[string]any {
	g.t.Helper()
	g.diagnostic.beginTool("tether_fixed_" + op)
	line := op
	if id != "" { line += " " + id }
	line += suffix + "\n"
	if p.send(line) != nil { g.diagnostic.fail(g.t, "work-cli-write") }
	frame, err := p.read()
	value, ok := frame["value"].(map[string]any)
	fields := 4
	if op == "list" { fields = 3 }
	if err != nil || len(frame) != fields || frame["type"] != "result" || frame["operation"] != op || !ok || (op != "list" && frame["request_id"] != id) {
		g.diagnostic.fail(g.t, "work-cli-response")
	}
	// Work metadata has work_error; it is not a read error or cleanup proof.
	g.diagnostic.observeTool(map[string]any{"structuredContent": value})
	return value
}

func (p *ciWorkChild) dictionaryMissing(g *ciGateway) {
	g.diagnostic.at("work-cli-dictionary-empty")
	p.checkList(g, nil)
	for _, op := range []string{"get_execution", "read_output"} {
		suffix := ""
		if op == "read_output" { suffix = " 0 7" }
		value := p.dictionary(g, op, ciUnknownWork, suffix)
		if !reflect.DeepEqual(value, map[string]any{"error": "not_found"}) { g.diagnostic.fail(g.t, "work-cli-dictionary-missing") }
	}
}

func (p *ciWorkChild) dictionaryRunning(g *ciGateway, status map[string]any) {
	g.diagnostic.at("work-cli-dictionary-running")
	id := status["request_id"].(string)
	value := p.dictionary(g, "get_execution", id, "")
	if value["command_id"] != status["command_id"] || value["phase"] != "running" || value["command_phase"] != "running" || value["terminal"] != false || value["collection_ended"] != false || value["result_released"] != false || value["work_error"] != "" {
		g.diagnostic.fail(g.t, "work-cli-dictionary-metadata")
	}
	value = p.dictionary(g, "read_output", id, " 0 7")
	if value["collection_ended"] != false || value["terminal"] != false || value["has_more"] != true || value["cursor"] != float64(0) || value["next_cursor"] != float64(7) || value["gap_bytes"] != float64(0) || value["work_error"] != "" || ciOutput(g.t, value, g.diagnostic) != "TETHER_" {
		g.diagnostic.fail(g.t, "work-cli-dictionary-output")
	}
}

func ciMetadataMatches(value, status map[string]any, released bool) bool {
	if value["schema_version"] != float64(1) || value["collection_ended"] != true || value["result_released"] != released || value["work_error"] != status["error"] {
		return false
	}
	for _, key := range []string{"request_id", "phase", "profile", "command_id", "command_phase", "exit", "stop", "terminal", "command_cleanup", "owner_cleanup", "gateway_cleanup"} {
		if !reflect.DeepEqual(value[key], status[key]) { return false }
	}
	_, output := value["chunks"]
	return !output
}

func (p *ciWorkChild) dictionaryFinished(g *ciGateway, status map[string]any, expected string) {
	g.diagnostic.at("work-cli-dictionary-finished")
	id := status["request_id"].(string)
	if !ciMetadataMatches(p.dictionary(g, "get_execution", id, ""), status, false) { g.diagnostic.fail(g.t, "work-cli-dictionary-metadata") }
	var all strings.Builder
	var first map[string]any
	// Bound both page count and byte count. An empty/nonadvancing page fails;
	// no read retries or background polling can manufacture a complete result.
	for cursor := 0; cursor < len(expected); {
		value := p.dictionary(g, "read_output", id, fmt.Sprintf(" %d 7", cursor))
		end := cursor + 7
		if end > len(expected) { end = len(expected) }
		if value["cursor"] != float64(cursor) || value["next_cursor"] != float64(end) || value["observed_end"] != float64(len(expected)) || value["gap_bytes"] != float64(0) || value["has_more"] != (end < len(expected)) || value["collection_ended"] != true || value["terminal"] != true || value["work_error"] != "" || value["source_output_end"] != float64(len(expected)) || value["source_truncated"] != false || value["source_dropped"] != float64(0) {
			g.diagnostic.fail(g.t, "work-cli-dictionary-output")
		}
		for _, key := range []string{"phase", "command_phase", "command_cleanup", "owner_cleanup", "gateway_cleanup"} {
			if value[key] != status[key] { g.diagnostic.fail(g.t, "work-cli-dictionary-output") }
		}
		text := ciOutput(g.t, value, g.diagnostic)
		if text != expected[cursor:end] { g.diagnostic.fail(g.t, "work-cli-dictionary-output") }
		if first == nil { first = value }
		all.WriteString(text)
		cursor = end
	}
	if all.String() != expected || !reflect.DeepEqual(first, p.dictionary(g, "read_output", id, " 0 7")) { g.diagnostic.fail(g.t, "work-cli-dictionary-retained") }
	last := p.dictionary(g, "read_output", id, fmt.Sprintf(" %d 7", len(expected)))
	if last["cursor"] != float64(len(expected)) || last["next_cursor"] != float64(len(expected)) || last["gap_bytes"] != float64(0) || last["has_more"] != false || last["collection_ended"] != true || ciOutput(g.t, last, g.diagnostic) != "" { g.diagnostic.fail(g.t, "work-cli-dictionary-output") }
	ahead := p.dictionary(g, "read_output", id, fmt.Sprintf(" %d 7", len(expected)+1))
	if !reflect.DeepEqual(ahead, map[string]any{"error": "cursor_ahead"}) { g.diagnostic.fail(g.t, "work-cli-dictionary-output") }
	// Read-only dictionary calls must leave the existing full result retained.
	if !reflect.DeepEqual(status, p.tool(g, "tether_fixed_status", id)) { g.diagnostic.fail(g.t, "work-cli-dictionary-retained") }
}

func (p *ciWorkChild) dictionaryReleased(g *ciGateway, status map[string]any) {
	g.diagnostic.at("work-cli-dictionary-released")
	id := status["request_id"].(string)
	if !ciMetadataMatches(p.dictionary(g, "get_execution", id, ""), status, true) { g.diagnostic.fail(g.t, "work-cli-dictionary-metadata") }
	value := p.dictionary(g, "read_output", id, " 0 7")
	if !reflect.DeepEqual(value, map[string]any{"error": "result_released"}) { g.diagnostic.fail(g.t, "work-cli-dictionary-released") }
}

// The two serial native executions also prove explicit keyset pagination,
// including an old released tombstone beside a newer retained result.
func (p *ciWorkChild) checkList(g *ciGateway, statuses []map[string]any) {
	g.diagnostic.at("work-cli-dictionary-list")
	cursor := "-"
	for index, status := range statuses {
		value := p.dictionary(g, "list", "", " " + cursor + " 1")
		items, ok := value["executions"].([]any)
		if !ok || len(items) != 1 || value["schema_version"] != float64(1) { g.diagnostic.fail(g.t, "work-cli-dictionary-list") }
		item, ok := items[0].(map[string]any)
		if !ok { g.diagnostic.fail(g.t, "work-cli-dictionary-list") }
		for _, key := range []string{"request_id", "phase", "terminal", "command_cleanup", "owner_cleanup", "gateway_cleanup", "error"} {
			if item[key] != status[key] { g.diagnostic.fail(g.t, "work-cli-dictionary-list") }
		}
		released := status["result_released"] == true
		if item["result_released"] != released { g.diagnostic.fail(g.t, "work-cli-dictionary-list") }
		cursor = status["request_id"].(string)
		next := ""
		if index+1 < len(statuses) { next = cursor }
		if value["next_cursor"] != next { g.diagnostic.fail(g.t, "work-cli-dictionary-list") }
	}
	value := p.dictionary(g, "list", "", " " + cursor + " 1")
	items, ok := value["executions"].([]any)
	if !ok || len(items) != 0 || value["next_cursor"] != "" || value["schema_version"] != float64(1) { g.diagnostic.fail(g.t, "work-cli-dictionary-list") }
}
