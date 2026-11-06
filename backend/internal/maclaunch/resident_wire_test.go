package maclaunch

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
)

func fixedNativeFixture(t *testing.T) (*ResidentFactory, *neverRuntime) {
	t.Helper()
	old, _, raw, rt := residentFixture(t)
	var p map[string]interface{}
	if e := json.Unmarshal(*raw, &p); e != nil {
		t.Fatal(e)
	}
	p["operations"] = []interface{}{map[string]interface{}{"id": ResidentFixedRequestID, "command": ResidentFixedCommand, "timeout_ms": 15000}}
	*raw, _ = json.Marshal(p)
	ctx, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	f, e := loadResident(ctx, old.path, old.deps)
	if e != nil {
		t.Fatal(e)
	}
	return f, rt
}
func TestResidentWireRequiresGenuineOpaqueReceiverAndIntent(t *testing.T) {
	f, rt := fixedNativeFixture(t)
	for _, r := range []*agenttransport.ResidentReceiver{nil, {}} {
		if _, e := f.OpenWireSource(r); !errors.Is(e, ErrResident) {
			t.Fatal("field-only receiver admitted", e)
		}
	}
	s, e := f.openSource(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e = s.NewWireRequest(agenttransport.ResidentFixedIntent{}); !errors.Is(e, ErrResident) {
		t.Fatal("zero/decoded context admitted", e)
	}
	if rt.starts.Load() != 0 {
		t.Fatal("invalid identity spawned native work")
	}
}
func TestResidentFixedPolicyRejectsAnyScopeExpansion(t *testing.T) {
	for _, kind := range []string{"id", "command", "timeout", "extra"} {
		t.Run(kind, func(t *testing.T) {
			f, rt := fixedNativeFixture(t)
			if !f.FixedWirePolicy() {
				t.Fatal("exact fixed scope rejected")
			}
			o := f.policy.operations[ResidentFixedRequestID]
			switch kind {
			case "id":
				delete(f.policy.operations, ResidentFixedRequestID)
				o.id = "other"
				f.policy.operations["other"] = o
			case "command":
				o.command += "; true"
				f.policy.operations[ResidentFixedRequestID] = o
			case "timeout":
				o.timeout++
				f.policy.operations[ResidentFixedRequestID] = o
			case "extra":
				f.policy.operations["other"] = o
			}
			if f.FixedWirePolicy() {
				t.Fatal("expanded scope accepted")
			}
			if rt.starts.Load() != 0 {
				t.Fatal("policy check spawned")
			}
		})
	}
}

// This tests the private canonical native core separately from authenticated
// receiver coverage. It does not forge an opaque wire intent or prove full live
// cross-package execution/OAuth/macOS cleanup.
func TestResidentNativeCoreSeparatesOuterCorrelationFromFixedStart(t *testing.T) {
	f, rt := fixedNativeFixture(t)
	s, e := f.openSource(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	outer := "0123456789abcdef0123456789abcdef"
	entry := &residentEntry{}
	s.entries[outer] = entry
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	owned, e := s.approveWithID(ctx, ctx, entry, outer, ResidentFixedRequestID, f.policy.operations[ResidentFixedRequestID], time.Now().Add(8*time.Second))
	if e != nil || owned == nil {
		t.Fatal(e)
	}
	defer closeResidentTest(t, owned)
	w := owned.Session.(*ResidentWorker)
	req := agentexec.StartRequest{Version: agentexec.Version, Binding: w.approved.session.Binding(), Mode: agentexec.ModeFull, RequestID: ResidentFixedRequestID, Command: []byte(ResidentFixedCommand), TimeoutMillis: 15000}
	for _, kind := range []string{"id", "command", "timeout"} {
		changed := req
		switch kind {
		case "id":
			changed.RequestID = outer
		case "command":
			changed.Command = []byte("true")
		case "timeout":
			changed.TimeoutMillis++
		}
		if _, e = w.approved.session.Start(changed); !errors.Is(e, agentexec.ErrPolicy) {
			t.Fatal(kind, e)
		}
	}
	if rt.starts.Load() != 0 {
		t.Fatal("changed fixed payload reached spawn")
	}
	first, e := w.approved.session.Start(req)
	if e != nil || first.CommandID == "" {
		t.Fatal(first, e)
	}
	second, e := w.approved.session.Start(req)
	if e != nil || second.CommandID != first.CommandID {
		t.Fatal(second, e)
	}
	deadline := time.Now().Add(time.Second)
	for rt.starts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if rt.starts.Load() != 1 {
		t.Fatal("replay executed more than once", rt.starts.Load())
	}
	if v := w.Observation(); v.Running != 0 {
		t.Fatal("transport/admission became executing command", v)
	}
	s.Close()
	if w.Dispatch().Binding.Profile != 0 {
		t.Fatal("source revocation did not seal actual dispatch")
	}
}
