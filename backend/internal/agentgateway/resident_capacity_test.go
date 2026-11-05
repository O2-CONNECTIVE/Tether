package agentgateway

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestResidentExplicitReleaseReclaims32ResultsWithoutReexecution(t *testing.T) {
	g, s, _, control := residentEvidenceRig(t)
	close(control.begin) // trusted fake control refuses without native acquisition
	t.Cleanup(func() {
		ctx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = g.Close(ctx)
	})
	admit := func(id string) {
		t.Helper()
		if v := s.FixedStart(context.Background(), id); v["error"] != "" {
			t.Fatal(v)
		}
		g.mu.Lock()
		w := s.entry.works[id]
		g.mu.Unlock()
		select {
		case <-w.completed:
		case <-time.After(time.Second):
			t.Fatal("original run not joined")
		}
	}
	for n := 1; n <= MaxResidentResults; n++ {
		admit(fmt.Sprintf("%032x", n))
	}
	next := fmt.Sprintf("%032x", MaxResidentResults+1)
	if v := s.FixedStart(context.Background(), next); v["error"] != "busy_or_refused" {
		t.Fatal(v)
	}
	id := fmt.Sprintf("%032x", 1)
	v := s.FixedRelease(context.Background(), id)
	if v["result_released"] != true || len(v["chunks"].([]map[string]any)) != 0 {
		t.Fatal(v)
	}
	admit(next)
	if v = s.FixedStart(context.Background(), id); v["result_released"] != true {
		t.Fatal("replay resurrected", v)
	}
	if len(s.entry.works) != 32 || len(s.entry.tombstones) != 1 {
		t.Fatal("capacity/ledger mismatch")
	}
	newer := *s
	newer.generation = strings.Repeat("b", 64)
	if v = newer.FixedStart(context.Background(), id); v["error"] != "request_generation_mismatch" {
		t.Fatal(v)
	}
	if v = newer.FixedRelease(context.Background(), id); v["error"] != "request_generation_mismatch" {
		t.Fatal(v)
	}
	if v = newer.FixedStatus(context.Background(), id); v["error"] != "not_found" {
		t.Fatal(v)
	}
	if v = s.FixedCancel(context.Background(), id); v["result_released"] != true {
		t.Fatal(v)
	}
}

func TestResidentReleaseNeverRecyclesUnknownUnjoinedOrAnotherGeneration(t *testing.T) {
	for _, kind := range []string{"owner", "gateway", "running", "unjoined", "current", "generation"} {
		t.Run(kind, func(t *testing.T) {
			g, s, _, _ := residentEvidenceRig(t)
			id := fmt.Sprintf("%032x", 1)
			done := make(chan struct{})
			if kind != "unjoined" {
				close(done)
			}
			w := &residentWork{binding: ResidentWorkBinding{Generation: s.generation, RequestID: id}, completed: done, done: true, phase: "finished", ownerCleanup: "confirmed", gatewayCleanup: "confirmed"}
			s.entry.works[id] = w
			switch kind {
			case "owner":
				w.ownerCleanup = "unknown"
			case "gateway":
				w.gatewayCleanup = "unknown"
			case "running":
				w.done = false
			case "current":
				s.entry.current = w
			case "generation":
				s.generation = strings.Repeat("b", 64)
			}
			if v := s.FixedRelease(context.Background(), id); v["error"] == nil {
				t.Fatal("released", v)
			}
			if s.entry.works[id] != w || len(s.entry.tombstones) != 0 {
				t.Fatal("original ownership lost")
			}
			g.mu.Lock()
			canRotate := g.residentGenerationCollectedLocked(s.entry, w.binding.Generation)
			g.mu.Unlock()
			if (kind == "owner" || kind == "gateway" || kind == "running" || kind == "current") && canRotate {
				t.Fatal("unknown/work authorized rotation")
			}
		})
	}
}

func TestResidentCapacityIsGatewayWideAndReplayLedgerFailsClosed(t *testing.T) {
	g, s, _, _ := residentEvidenceRig(t)
	other := &residentEntry{works: map[string]*residentWork{}, tombstones: map[string]residentTombstone{}}
	g.resident.entries[&principal{}] = other
	for n := 0; n < MaxResidentResults; n++ {
		other.works[fmt.Sprintf("%032x", n)] = &residentWork{}
	}
	if v := s.FixedStart(context.Background(), strings.Repeat("f", 32)); v["error"] != "busy_or_refused" {
		t.Fatal("per-entry cap only", v)
	}
	other.works = map[string]*residentWork{}
	for n := 0; n < MaxResidentReplayRecords; n++ {
		id := fmt.Sprintf("%032x", n)
		other.tombstones[id] = residentTombstone{generation: s.generation, value: map[string]any{"request_id": id, "result_released": true}}
	}
	if v := s.FixedStart(context.Background(), strings.Repeat("f", 32)); v["error"] != "replay_ledger_full" {
		t.Fatal(v)
	}
	if len(other.tombstones) != MaxResidentReplayRecords {
		t.Fatal("silently evicted replay evidence")
	}
}
