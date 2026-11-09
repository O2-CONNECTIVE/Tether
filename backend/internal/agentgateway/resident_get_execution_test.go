package agentgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentapi"
)

func TestResidentGetExecutionMatchesSharedSchemaGolden(t *testing.T) {
	_, s, _, _ := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	w := readWork(s, id)
	raw, err := os.ReadFile("../mcpadapter/testdata/resident-get-execution.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]map[string]any
	if json.Unmarshal(raw, &golden) != nil {
		t.Fatal("bad golden")
	}
	seen := map[string]bool{}
	var reader residentapi.ExecutionReader = s
	check := func(label string) {
		t.Helper()
		observation, err := reader.GetExecution(context.Background(), id)
		if err != nil {
			t.Fatal("typed metadata read failed", label, err)
		}
		value := s.FixedGetExecution(context.Background(), id)
		if !reflect.DeepEqual(fixedExecutionValue(observation), value) {
			t.Fatal("typed and compatibility observations differ", label)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var normalized map[string]any
		json.Unmarshal(encoded, &normalized)
		if !reflect.DeepEqual(normalized, golden[label]) {
			t.Fatalf("%s: got %s, want %v", label, encoded, golden[label])
		}
		for _, forbidden := range []string{"chunks", "next_cursor", "source_output_end", "source_truncated", "source_dropped", "output_complete", "input_supported", "generation", "host", "principal", "error"} {
			if _, found := value[forbidden]; found {
				t.Fatal("unexpected metadata field", forbidden)
			}
		}
		seen[label] = true
	}
	check("pending")
	w.phase = "running"
	w.status = agentwire.Status{Profile: agentexec.ProfileMacCurrentUser, Status: agentexec.Status{CommandID: strings.Repeat("2", 32), Phase: agentexec.PhaseRunning, Cleanup: agentexec.CleanupPending}}
	check("running")
	w.status.Phase, w.status.Cleanup, w.status.Terminal = agentexec.PhaseExited, agentexec.CleanupConfirmed, true
	w.status.Exit = agentexec.ExitStatus{Known: true, Code: 0}
	// Command terminal is not owner cleanup or the collector's end.
	check("terminal_collecting")
	w.phase, w.ownerCleanup, w.gatewayCleanup, w.done = "finished", "confirmed", "confirmed", true
	check("finished")
	finished := *w
	w.phase, w.ownerCleanup, w.failure = "unknown", "unknown", "result_unobserved_or_uncertain"
	w.status.Phase, w.status.Cleanup, w.status.Terminal = agentexec.PhaseRunning, agentexec.CleanupUnknown, false
	w.status.Exit = agentexec.ExitStatus{}
	check("unknown")
	// Release before command observation must not invent a command or exit.
	w = listWork(id, s.generation)
	s.entry.works[id] = w
	w.failure = "admission_refused_or_uncertain"
	releaseListWork(t, s, id, false)
	check("released_before_command")
	delete(s.entry.tombstones, id)
	s.entry.works[id] = &finished
	releaseListWork(t, s, id, true)
	check("released_finished")
	if len(seen) != len(golden) {
		t.Fatalf("unverified golden values: %d/%d", len(seen), len(golden))
	}
}

func TestResidentGetExecutionDetachedNonDestructiveReads(t *testing.T) {
	_, s, _, _ := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	w := readWork(s, id)
	w.status.Exit = agentexec.ExitStatus{Known: true, Code: 7}
	w.status.Stop = agentexec.StopCanceled
	w.failure = "output_limit"
	for _, released := range []bool{false, true} {
		if released {
			releaseListWork(t, s, id, false)
		}
		before, _ := json.Marshal(s.FixedStatus(context.Background(), id))
		v := s.FixedGetExecution(context.Background(), id)
		if v["exit"].(map[string]any)["code"] != int32(7) || v["stop"] != "canceled" || v["work_error"] != "output_limit" || v["result_released"] != released {
			t.Fatal(v)
		}
		v["exit"].(map[string]any)["code"] = 99
		v["phase"] = "MUTATED"
		after, _ := json.Marshal(s.FixedStatus(context.Background(), id))
		if !bytes.Equal(before, after) || w.cancelled || s.entry.current != nil || s.principal.route != nil || w.cursor != 6 || w.outputBytes != 6 {
			t.Fatal("metadata read changed owned work")
		}
		if !released && (len(s.entry.works) != 1 || len(s.entry.tombstones) != 0 || len(s.FixedReadOutput(context.Background(), id, 0, 4096)["chunks"].([]map[string]any)) != 2) {
			t.Fatal("metadata read discarded retained bytes")
		}
		if released && (len(s.entry.works) != 0 || len(s.entry.tombstones) != 1 || s.FixedStart(context.Background(), id)["result_released"] != true) {
			t.Fatal("metadata read lost replay evidence")
		}
	}
}

func TestResidentGetExecutionScopeAndInvalidInputs(t *testing.T) {
	g, s, _, _ := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	w := readWork(s, id)
	foreign := *s
	foreign.generation = "other"
	other := &residentSession{gateway: g, entry: &residentEntry{works: map[string]*residentWork{}}, generation: s.generation}
	g.resident.control = &metadataNoContactControl{}
	for _, released := range []bool{false, true} {
		if released {
			releaseListWork(t, s, id, false)
		}
		for _, reader := range []*residentSession{&foreign, other} {
			if got := reader.FixedGetExecution(context.Background(), id); !reflect.DeepEqual(got, map[string]any{"error": "not_found"}) {
				t.Fatal("foreign record leaked", got)
			}
		}
		if got := s.FixedGetExecution(context.Background(), strings.Repeat("9", 32)); got["error"] != "not_found" {
			t.Fatal(got)
		}
		for _, bad := range []string{"", "bad", strings.Repeat("F", 32), strings.Repeat("a", 10000)} {
			if got := s.FixedGetExecution(context.Background(), bad); got["error"] != "invalid" {
				t.Fatal(got)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for _, ctx := range []context.Context{nil, ctx, &metadataCanceledAfterEntry{Context: context.Background()}} {
			if got := s.FixedGetExecution(ctx, id); got["error"] != "not_read" {
				t.Fatal(got)
			}
		}
	}
	if w.cancelled {
		t.Fatal("read requested cancellation")
	}
	// Old-generation reads must not consult or renew the control connection.
	if got := s.FixedGetExecution(context.Background(), id); got["result_released"] != true {
		t.Fatal("read contacted current control generation", got)
	}
}

type metadataNoContactControl struct{}

func (*metadataNoContactControl) ResidentGeneration(string, string) string {
	panic("metadata read consulted control")
}
func (*metadataNoContactControl) BeginResidentFixed(context.Context, string, string, string, string, time.Time) error {
	panic("metadata read dispatched work")
}

type metadataCanceledAfterEntry struct {
	context.Context
	checks int
}

func (c *metadataCanceledAfterEntry) Err() error {
	c.checks++
	if c.checks > 1 {
		return context.Canceled
	}
	return nil
}

func TestResidentGetExecutionObservesOriginalCollector(t *testing.T) {
	g, s, _, control := residentEvidenceRig(t)
	defer g.Close(context.Background())
	id := strings.Repeat("1", 32)
	if v := s.FixedStart(context.Background(), id); v["phase"] != "pending" {
		t.Fatal(v)
	}
	g.mu.Lock()
	w := s.entry.works[id]
	g.mu.Unlock()
	close(control.begin) // The existing source refuses before work acquisition.
	select {
	case <-w.completed:
	case <-time.After(time.Second):
		t.Fatal("original collector did not finish")
	}
	for _, released := range []bool{false, true} {
		if released {
			if v := s.FixedRelease(context.Background(), id); v["result_released"] != true {
				t.Fatal(v)
			}
		}
		v := s.FixedGetExecution(context.Background(), id)
		if v["work_error"] != "admission_refused_or_uncertain" || v["collection_ended"] != true || v["result_released"] != released || v["owner_cleanup"] != "confirmed" || v["gateway_cleanup"] != "confirmed" || v["terminal"] != false || !reflect.DeepEqual(v["exit"], map[string]any{"known": false}) {
			t.Fatal("collector facts conflated with execution", v)
		}
	}
}

func TestResidentGetExecutionConcurrentReleaseAndCollection(t *testing.T) {
	g, s, _, _ := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	w := readWork(s, id)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 50; k++ {
				observation, err := s.GetExecution(context.Background(), id)
				if err != nil || (observation.CollectionEnded && (observation.Phase != "finished" || observation.OwnerCleanup != "confirmed" || observation.GatewayCleanup != "confirmed")) {
					t.Error("inconsistent typed observation", observation, err)
				}
				observation.Exit.Code = 99
				v := s.FixedGetExecution(context.Background(), id)
				if v["error"] != nil || (v["collection_ended"] == true && (v["phase"] != "finished" || v["owner_cleanup"] != "confirmed" || v["gateway_cleanup"] != "confirmed")) {
					t.Error("inconsistent observation", v)
				}
				v["exit"].(map[string]any)["known"] = "MUTATED"
			}
		}()
	}
	g.mu.Lock()
	w.done, w.phase, w.ownerCleanup, w.gatewayCleanup = true, "finished", "confirmed", "confirmed"
	w.completed = make(chan struct{})
	close(w.completed)
	g.mu.Unlock()
	if got := s.FixedRelease(context.Background(), id); got["result_released"] != true {
		t.Fatal(got)
	}
	wg.Wait()
}
