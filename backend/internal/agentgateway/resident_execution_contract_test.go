package agentgateway

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentapi"
)

func TestResidentExecutionReaderReturnsDetachedMetadata(t *testing.T) {
	_, s, _, _ := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	w := readWork(s, id)
	w.phase = "running"
	w.status = agentwire.Status{Profile: agentexec.ProfileMacCurrentUser, Status: agentexec.Status{
		CommandID: strings.Repeat("2", 32), Phase: agentexec.PhaseExited,
		Exit: agentexec.ExitStatus{Known: true, Code: -7}, Stop: agentexec.StopCanceled,
		Terminal: true, Cleanup: agentexec.CleanupConfirmed,
	}}
	w.failure = "output_limit"
	want := residentapi.Execution{
		RequestID: id, Phase: "running", Profile: "mac_current_user", CommandID: strings.Repeat("2", 32), CommandPhase: "exited",
		Exit: residentapi.ExitStatus{Known: true, Code: -7}, Stop: "canceled", Terminal: true,
		CommandCleanup: "confirmed", OwnerCleanup: "pending", GatewayCleanup: "pending", WorkError: "output_limit",
	}
	var reader residentapi.ExecutionReader = s
	for _, released := range []bool{false, true} {
		if released {
			releaseListWork(t, s, id, true)
			want.Phase, want.OwnerCleanup, want.GatewayCleanup = "finished", "confirmed", "confirmed"
			want.CollectionEnded, want.ResultReleased = true, true
		}
		before := s.FixedStatus(context.Background(), id)
		got, err := reader.GetExecution(context.Background(), id)
		if err != nil || got != want {
			t.Fatalf("released=%v: got %+v, %v; want %+v", released, got, err, want)
		}
		got.Exit.Code, got.WorkError, got.RequestID = 99, "changed", "changed"
		again, err := reader.GetExecution(context.Background(), id)
		if err != nil || again != want || !reflect.DeepEqual(before, s.FixedStatus(context.Background(), id)) || w.cancelled || w.cursor != 6 || w.outputBytes != 6 {
			t.Fatal("typed read aliased or changed owned work", again, err)
		}
	}
}

func TestResidentExecutionReaderOmitsUnobservedExit(t *testing.T) {
	_, s, _, _ := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	w := readWork(s, id)
	w.status.Exit.Code = 99 // Unknown exit must not expose a stray internal code.
	w.failure = "admission_refused_or_uncertain"
	for _, released := range []bool{false, true} {
		if released {
			releaseListWork(t, s, id, false)
		}
		got, err := s.GetExecution(context.Background(), id)
		if err != nil || got.Exit != (residentapi.ExitStatus{}) || got.Terminal || got.CollectionEnded != released || got.ResultReleased != released || got.WorkError != w.failure {
			t.Fatal("collection invented command execution or a read failure", got, err)
		}
		if exit := s.FixedGetExecution(context.Background(), id)["exit"]; !reflect.DeepEqual(exit, map[string]any{"known": false}) {
			t.Fatal("unknown exit leaked code", exit)
		}
	}
}

func TestResidentExecutionReaderKeepsReadScopeAndErrors(t *testing.T) {
	g, s, _, _ := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	w := readWork(s, id)
	foreign := *s
	foreign.generation = "other"
	other := &residentSession{gateway: g, entry: &residentEntry{works: map[string]*residentWork{}}, generation: s.generation}
	g.resident.control = &metadataNoContactControl{}
	check := func(reader residentapi.ExecutionReader, ctx context.Context, id string, want residentapi.ReadError) {
		t.Helper()
		got, err := reader.GetExecution(ctx, id)
		var code residentapi.ReadError
		if !errors.Is(err, want) || !errors.As(err, &code) || code != want || got != (residentapi.Execution{}) {
			t.Fatal("read failure leaked an observation or changed code", got, err)
		}
	}
	for _, released := range []bool{false, true} {
		if released {
			releaseListWork(t, s, id, false)
		}
		check(&foreign, context.Background(), id, residentapi.ErrNotFound)
		check(other, context.Background(), id, residentapi.ErrNotFound)
		check(s, context.Background(), strings.Repeat("9", 32), residentapi.ErrNotFound)
		for _, bad := range []string{"", "bad", strings.Repeat("F", 32), strings.Repeat("a", 10000)} {
			check(s, context.Background(), bad, residentapi.ErrInvalid)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer stop()
		for _, ctx := range []context.Context{nil, ctx, expired, &metadataCanceledAfterEntry{Context: context.Background()}} {
			check(s, ctx, id, residentapi.ErrNotRead)
		}
		check(s, nil, "invalid", residentapi.ErrNotRead)
		if got, err := s.GetExecution(context.Background(), id); err != nil || got.ResultReleased != released {
			t.Fatal("read consulted current control instead of retained scope", got, err)
		}
	}
	if w.cancelled || s.principal.route != nil || s.entry.current != nil {
		t.Fatal("read acquired or canceled work")
	}
}

func TestResidentExecutionReaderRejectsMalformedTombstone(t *testing.T) {
	_, s, _, _ := residentEvidenceRig(t)
	id := strings.Repeat("1", 32)
	readWork(s, id).status.Exit = agentexec.ExitStatus{Known: true, Code: 7}
	releaseListWork(t, s, id, true)
	original := s.entry.tombstones[id]
	for _, mutate := range []func(map[string]any){
		func(v map[string]any) { delete(v, "request_id") },
		func(v map[string]any) { v["terminal"] = "secret detail" },
		func(v map[string]any) { v["exit"] = "secret detail" },
		func(v map[string]any) { v["exit"].(map[string]any)["known"] = "secret detail" },
		func(v map[string]any) { v["exit"].(map[string]any)["code"] = int(7) },
	} {
		value := tombstoneValue(original, s.generation)
		mutate(value)
		s.entry.tombstones[id] = residentTombstone{generation: s.generation, value: value}
		got, err := s.GetExecution(context.Background(), id)
		if got != (residentapi.Execution{}) || err != residentapi.ErrNotRead {
			t.Fatal("malformed retained metadata escaped", got, err)
		}
		if value := s.FixedGetExecution(context.Background(), id); !reflect.DeepEqual(value, map[string]any{"error": "not_read"}) {
			t.Fatal("malformed metadata leaked through compatibility response", value)
		}
	}
}
