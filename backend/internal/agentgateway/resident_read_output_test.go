package agentgateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

func readChunk(offset uint64, stream string, data []byte) map[string]any {
	return map[string]any{"offset": offset, "stream": stream, "encoding": "base64", "data": base64.StdEncoding.EncodeToString(data)}
}
func readWork(s *residentSession, id string) *residentWork {
	w := listWork(id, s.generation)
	w.output = []map[string]any{readChunk(0, "stdout", []byte{0, 255, 0xc3}), readChunk(3, "stderr", []byte{0xa9, 'x', 'y'})}
	w.cursor, w.outputBytes = 6, 6
	s.entry.works[id] = w
	return w
}
func TestResidentReadOutputBytePagingDoesNotAcknowledge(t *testing.T) {
	_, s, _, _ := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	w := readWork(s, id)
	original, _ := json.Marshal(s.FixedStatus(context.Background(), id))
	var all []byte
	cursor := uint64(0)
	for page := 0; page < 3; page++ {
		v := s.FixedReadOutput(context.Background(), id, cursor, 2)
		if v["cursor"] != cursor || v["next_cursor"] != cursor+2 || v["gap_bytes"] != uint64(0) || v["has_more"] != (page < 2) {
			t.Fatal(v)
		}
		for _, c := range v["chunks"].([]map[string]any) {
			b, err := base64.StdEncoding.DecodeString(c["data"].(string))
			if err != nil || c["offset"] != cursor {
				t.Fatal(c, err)
			}
			all = append(all, b...)
			cursor += uint64(len(b))
			c["data"] = "MUTATED"
		}
	}
	if !bytes.Equal(all, []byte{0, 255, 0xc3, 0xa9, 'x', 'y'}) {
		t.Fatal(all)
	}
	after, _ := json.Marshal(s.FixedStatus(context.Background(), id))
	if !bytes.Equal(original, after) || w.cursor != 6 || w.outputBytes != 6 || len(s.entry.works) != 1 || len(s.entry.tombstones) != 0 || s.entry.current != nil {
		t.Fatal("read changed owned work")
	}
	replay := s.FixedReadOutput(context.Background(), id, 0, 4096)
	if replay["next_cursor"] != uint64(6) || len(replay["chunks"].([]map[string]any)) != 2 {
		t.Fatal("read acknowledged output", replay)
	}
	if got := s.FixedReadOutput(context.Background(), id, 7, 1); got["error"] != "cursor_ahead" {
		t.Fatal(got)
	}
}
func TestResidentReadOutputGapsBoundsAndObservation(t *testing.T) {
	_, s, _, _ := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	w := readWork(s, id)
	w.output = []map[string]any{readChunk(2, "stdout", []byte("ab")), readChunk(7, "stderr", []byte("cd"))}
	w.cursor = 12
	w.failure = "output_limit"
	w.status.OutputEnd, w.status.Truncated, w.status.Dropped = 20, true, 7
	first := s.FixedReadOutput(context.Background(), id, 0, 3)
	if first["next_cursor"] != uint64(8) || first["gap_bytes"] != uint64(5) || first["has_more"] != true || first["work_error"] != "output_limit" || first["source_dropped"] != uint64(7) || first["source_output_end"] != uint64(20) || first["source_truncated"] != true {
		t.Fatal(first)
	}
	tail := s.FixedReadOutput(context.Background(), id, 8, 2)
	if tail["next_cursor"] != uint64(12) || tail["gap_bytes"] != uint64(3) || tail["has_more"] != false {
		t.Fatal(tail)
	}
	// Full budget ends before a lost tail. The next read advances without bytes.
	tail = s.FixedReadOutput(context.Background(), id, 8, 1)
	if tail["next_cursor"] != uint64(9) || tail["has_more"] != true {
		t.Fatal(tail)
	}
	tail = s.FixedReadOutput(context.Background(), id, 9, 1)
	if tail["gap_bytes"] != uint64(3) || len(tail["chunks"].([]map[string]any)) != 0 || tail["next_cursor"] != uint64(12) {
		t.Fatal(tail)
	}
	// Empty current view while running is not a terminal/completion claim.
	w.phase = "running"
	empty := s.FixedReadOutput(context.Background(), id, 12, 4096)
	if empty["collection_ended"] != false || empty["terminal"] != false || empty["has_more"] != false {
		t.Fatal(empty)
	}
	// Observer can finish with uncertain output and cleanup, including a
	// terminal command. No output_complete field is invented in either case.
	w.done, w.phase, w.failure = true, "unknown", "result_unobserved_or_uncertain"
	w.ownerCleanup, w.gatewayCleanup, w.status.Cleanup = "unknown", "unknown", agentexec.CleanupUnknown
	for _, terminal := range []bool{false, true} {
		w.status.Terminal = terminal
		v := s.FixedReadOutput(context.Background(), id, 12, 4096)
		if v["collection_ended"] != true || v["terminal"] != terminal || v["phase"] != "unknown" || v["owner_cleanup"] != "unknown" || v["command_cleanup"] != "unknown" || v["work_error"] != "result_unobserved_or_uncertain" || v["output_complete"] != nil {
			t.Fatal(v)
		}
	}
	// Even confirmed owner cleanup/finished is not proof of observed output.
	w.phase, w.ownerCleanup, w.gatewayCleanup = "finished", "confirmed", "confirmed"
	if v := s.FixedReadOutput(context.Background(), id, 12, 1); v["work_error"] != "result_unobserved_or_uncertain" || v["output_complete"] != nil {
		t.Fatal(v)
	}
	w.output = []map[string]any{readChunk(0, "stdout", bytes.Repeat([]byte{255}, 16384))}
	w.cursor = 16384
	if v := s.FixedReadOutput(context.Background(), id, 0, 4096); len(v["chunks"].([]map[string]any)[0]["data"].(string)) != 5464 || v["next_cursor"] != uint64(4096) {
		t.Fatal(v)
	}
}
func TestResidentReadOutputScopeTombstonesAndInvalidInputs(t *testing.T) {
	g, s, _, _ := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	w := readWork(s, id)
	foreign := *s
	foreign.generation = "other"
	other := &residentSession{gateway: g, entry: &residentEntry{works: map[string]*residentWork{}}, generation: s.generation}
	for _, reader := range []*residentSession{&foreign, other} {
		if got := reader.FixedReadOutput(context.Background(), id, 0, 1); got["error"] != "not_found" {
			t.Fatal(got)
		}
	}
	for _, q := range []struct {
		id     string
		cursor uint64
		max    int
	}{
		{"bad", 0, 1}, {strings.Repeat("F", 32), 0, 1}, {id, 0, 0}, {id, 0, 4097}, {id, mcpadapter.MaxResidentReadCursor + 1, 1},
	} {
		if got := s.FixedReadOutput(context.Background(), q.id, q.cursor, q.max); got["error"] != "invalid" {
			t.Fatal(got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, ctx} {
		if got := s.FixedReadOutput(ctx, id, 0, 1); got["error"] != "not_read" {
			t.Fatal(got)
		}
	}
	if got := s.FixedReadOutput(context.Background(), id, mcpadapter.MaxResidentReadCursor, 1); got["error"] != "cursor_ahead" {
		t.Fatal(got)
	}
	if w.done || w.cancelled {
		t.Fatal("read changed work lifecycle")
	}
	releaseListWork(t, s, id, false)
	if got := s.FixedReadOutput(context.Background(), id, 0, 1); got["error"] != "result_released" {
		t.Fatal(got)
	}
	if got := foreign.FixedReadOutput(context.Background(), id, 0, 1); got["error"] != "not_found" {
		t.Fatal(got)
	}
	if got := s.FixedStart(context.Background(), id); got["result_released"] != true {
		t.Fatal("read lost replay tombstone", got)
	}
}
func TestResidentReadOutputConcurrentReleaseAndCollection(t *testing.T) {
	g, s, _, _ := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	w := readWork(s, id)
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 50; k++ {
				v := s.FixedReadOutput(context.Background(), id, 0, 4096)
				if code, ok := v["error"]; ok {
					if code != "result_released" {
						t.Error(v)
					}
					continue
				}
				var count uint64
				for _, c := range v["chunks"].([]map[string]any) {
					b, _ := base64.StdEncoding.DecodeString(c["data"].(string))
					count += uint64(len(b))
				}
				if v["next_cursor"] != count || v["observed_end"] != count || v["gap_bytes"] != uint64(0) {
					t.Error(v)
				}
			}
		}()
	}
	g.mu.Lock()
	w.output = append(w.output, readChunk(6, "stdout", []byte("z")))
	w.cursor, w.outputBytes = 7, 7
	w.phase, w.done, w.ownerCleanup, w.gatewayCleanup = "finished", true, "confirmed", "confirmed"
	w.completed = make(chan struct{})
	close(w.completed)
	g.mu.Unlock()
	if got := s.FixedRelease(context.Background(), id); got["result_released"] != true {
		t.Fatal(got)
	}
	wg.Wait()
}
func TestResidentReadOutputMatchesSharedSchemaGolden(t *testing.T) {
	_, s, _, _ := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	w := readWork(s, id)
	w.output = []map[string]any{readChunk(2, "stdout", []byte{0, 255}), readChunk(7, "stderr", []byte("x"))}
	w.cursor, w.failure = 10, "result_unobserved_or_uncertain"
	w.done, w.phase, w.ownerCleanup, w.gatewayCleanup = true, "unknown", "unknown", "unknown"
	w.status.OutputEnd, w.status.Truncated, w.status.Dropped = 12, true, 3
	raw, err := os.ReadFile("../mcpadapter/testdata/resident-read-output.json")
	if err != nil {
		t.Fatal(err)
	}
	var want any
	if json.Unmarshal(raw, &want) != nil {
		t.Fatal("bad golden")
	}
	actual, _ := json.Marshal(s.FixedReadOutput(context.Background(), id, 0, 4096))
	var got any
	json.Unmarshal(actual, &got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("read contract changed: %s", actual)
	}
}

func TestResidentReadOutputRejectsBrokenRetainedInvariants(t *testing.T) {
	_, s, _, _ := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	for _, mutate := range []func(*residentWork){
		func(w *residentWork) { w.output[0]["data"] = "bad!" },
		func(w *residentWork) { w.output[0]["data"] = "AB==" },
		func(w *residentWork) { w.output[0]["data"] = "" },
		func(w *residentWork) { w.output[0]["offset"] = 0 },
		func(w *residentWork) { w.output[0]["stream"] = "private" },
		func(w *residentWork) { w.output[0]["encoding"] = "utf8" },
		func(w *residentWork) { w.output[1]["offset"] = uint64(1) },
		func(w *residentWork) { w.output[1]["offset"] = ^uint64(0) },
		func(w *residentWork) { w.cursor = 2 },
		func(w *residentWork) { w.cursor = mcpadapter.MaxResidentReadCursor + 1 },
		func(w *residentWork) { w.output = make([]map[string]any, 129) },
		func(w *residentWork) {
			w.output = []map[string]any{readChunk(0, "stdout", make([]byte, 16385))}
			w.cursor = 16385
		},
	} {
		w := readWork(s, id)
		mutate(w)
		if v := s.FixedReadOutput(context.Background(), id, 0, 1); v["error"] != "output_unavailable" {
			t.Fatal(v)
		}
	}
	// The existing 128-chunk bound remains readable without changing retention.
	w := readWork(s, id)
	w.output = nil
	w.cursor = 128
	for n := uint64(0); n < 128; n++ {
		w.output = append(w.output, readChunk(n, "stdout", []byte{byte(n)}))
	}
	if v := s.FixedReadOutput(context.Background(), id, 0, 4096); len(v["chunks"].([]map[string]any)) != 128 || v["next_cursor"] != uint64(128) {
		t.Fatal(v)
	}
}
