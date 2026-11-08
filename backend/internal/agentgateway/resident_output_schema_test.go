package agentgateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

// The same anonymous golden values are checked against the published MCP
// schemas in mcpadapter. Here they must match the concrete gateway serializers
// and result methods; a permissive mock cannot define the public contract.
func TestResidentResultsMatchOutputSchemaGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "mcpadapter", "testdata", "resident-output.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]map[string]any
	if err = json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	check := func(label string, value map[string]any) {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var normalized map[string]any
		if err = json.Unmarshal(encoded, &normalized); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(normalized, golden[label]) {
			t.Fatalf("%s: got %s, want %v", label, encoded, golden[label])
		}
		seen[label] = true
	}
	id := strings.Repeat("1", 32)
	w := &residentWork{binding: ResidentWorkBinding{RequestID: id, Generation: "generation"}, phase: "pending", ownerCleanup: "pending", gatewayCleanup: "pending"}
	check("pending", residentValue(w))
	w.phase = "running"
	w.status = agentwire.Status{Profile: agentexec.ProfileMacCurrentUser, Status: agentexec.Status{CommandID: strings.Repeat("2", 32), Phase: agentexec.PhaseRunning, Cleanup: agentexec.CleanupPending}}
	w.output = []map[string]any{{"stream": "stdout", "offset": uint64(0), "encoding": "base64", "data": "AP8="}, {"stream": "stderr", "offset": uint64(2), "encoding": "base64", "data": "b2s="}}
	w.cursor = 4
	check("running", residentValue(w))
	w.phase, w.ownerCleanup, w.gatewayCleanup = "finished", "confirmed", "confirmed"
	w.status.Phase, w.status.Cleanup, w.status.Terminal = agentexec.PhaseExited, agentexec.CleanupConfirmed, true
	w.status.Exit = agentexec.ExitStatus{Known: true, Code: 0}
	check("finished", residentValue(w))
	finished := *w
	w.phase, w.ownerCleanup, w.failure = "unknown", "unknown", "result_unobserved_or_uncertain"
	w.status.Phase, w.status.Cleanup, w.status.Terminal = agentexec.PhaseRunning, agentexec.CleanupUnknown, false
	w.status.Exit = agentexec.ExitStatus{}
	check("unknown", residentValue(w))

	// Admission can be refused before a command exists, yet original ownership
	// can be confirmed collected. Releasing that result must not invent terminal.
	done := make(chan struct{})
	close(done)
	w = &residentWork{binding: ResidentWorkBinding{RequestID: id, Generation: "generation"}, phase: "finished", ownerCleanup: "confirmed", gatewayCleanup: "confirmed", failure: "admission_refused_or_uncertain", done: true, completed: done}
	entry := &residentEntry{works: map[string]*residentWork{id: w}}
	p := &principal{}
	g := &Gateway{}
	s := &residentSession{gateway: g, principal: p, entry: entry, generation: "generation"}
	check("released_before_command", s.FixedRelease(context.Background(), id))
	for _, call := range []func(context.Context, string) map[string]any{s.FixedStart, s.FixedStatus, s.FixedCancel, s.FixedRelease} {
		check("released_before_command", call(context.Background(), id))
	}
	check("not_sent", s.FixedStart(nil, id))
	check("not_released", s.FixedRelease(nil, id))
	other := strings.Repeat("3", 32)
	for _, call := range []func(context.Context, string) map[string]any{s.FixedStatus, s.FixedCancel, s.FixedRelease} {
		check("not_found", call(context.Background(), other))
	}
	entry.works[other] = &residentWork{binding: ResidentWorkBinding{Generation: "other"}}
	check("request_generation_mismatch", s.FixedStart(context.Background(), other))
	entry.works[other].binding.Generation = s.generation
	check("cleanup_not_confirmed", s.FixedRelease(context.Background(), other))
	delete(entry.works, other)
	g.closed = true
	check("busy_or_refused", s.FixedStart(context.Background(), other))
	g.closed = false
	g.resident = &residentSetup{control: &residentControlFixture{}, entries: map[*principal]*residentEntry{p: entry}}
	check("control_ended", s.FixedStart(context.Background(), other))
	for i := 0; i < MaxResidentReplayRecords; i++ {
		entry.works[fmt.Sprint(i)] = &residentWork{}
	}
	check("replay_ledger_full", s.FixedStart(context.Background(), other))
	finished.done, finished.completed = true, done
	entry = &residentEntry{works: map[string]*residentWork{id: &finished}}
	s = &residentSession{gateway: &Gateway{}, principal: p, entry: entry, generation: "generation"}
	check("released_finished", s.FixedRelease(context.Background(), id))
	if len(seen) != len(golden) {
		t.Fatalf("unverified golden values: %d/%d", len(seen), len(golden))
	}
}
