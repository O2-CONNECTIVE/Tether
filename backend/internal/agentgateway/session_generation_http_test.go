package agentgateway

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// This is the authenticated HTTP layer of rollover coverage. The control is
// synthetic: the separate agenttransport relay-4000 test proves transport
// cleanup/re-authentication, and the Worker test proves the edge timer. These
// layers are not a deployed end-to-end ChatGPT reconnection test.
func TestResidentHTTPGenerationChangePreservesOriginalReadScope(t *testing.T) {
	f := newLifecycleHTTP(t)
	f.control.set(f.s.generation)
	old := f.open(t)
	id := strings.Repeat("1", 32)
	w := readWork(f.s, id)
	f.control.set(strings.Repeat("b", 64))
	fresh := f.open(t)
	f.count(t, 2)
	if v := lifecycleValue(t, f.call(http.MethodPost, old, lifecycleMetadata), false); v["request_id"] != id {
		t.Fatal("old session lost retained result", v)
	}
	if v := lifecycleValue(t, f.call(http.MethodPost, fresh, lifecycleMetadata), true); v["error"] != "not_found" {
		t.Fatal("new generation observed old result", v)
	}
	start := strings.Replace(lifecycleMetadata, "tether_fixed_get_execution", "tether_fixed_start", 1)
	next := strings.Replace(start, id, strings.Repeat("3", 32), 1)
	if v := lifecycleValue(t, f.call(http.MethodPost, old, next), true); v["error"] != "control_ended" || v["reinitialize_required"] != true {
		t.Fatal("old session gained new-work authority", v)
	}
	if v := lifecycleValue(t, f.call(http.MethodPost, fresh, start), true); v["error"] != "request_generation_mismatch" {
		t.Fatal("old request was replayed in new generation", v)
	}
	if f.s.entry.works[id] != w || len(f.s.entry.works) != 1 {
		t.Fatal("refused requests changed retained work")
	}
	if v := lifecycleValue(t, f.call(http.MethodPost, fresh, next), false); v["request_id"] != strings.Repeat("3", 32) {
		t.Fatal("fresh session could not admit new request", v)
	}
	// The fake control refuses before acquiring native work. Wait for that
	// original collector; admission is not command execution or native cleanup.
	until := time.Now().Add(time.Second)
	for {
		f.g.mu.Lock()
		current := f.s.entry.works[strings.Repeat("3", 32)]
		done := current != nil && current.done
		f.g.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(until) {
			t.Fatal("original refused acquisition did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	f.control.mu.Lock()
	begins := f.control.begins
	f.control.mu.Unlock()
	if begins != 1 {
		t.Fatalf("control acquisitions=%d want 1", begins)
	}
	if v := lifecycleValue(t, f.call(http.MethodPost, old, lifecycleMetadata), false); v["request_id"] != id {
		t.Fatal("new work removed old read scope", v)
	}
	if v := lifecycleValue(t, f.call(http.MethodPost, old, strings.Replace(next, "tether_fixed_start", "tether_fixed_get_execution", 1)), true); v["error"] != "not_found" {
		t.Fatal("old session observed new result", v)
	}
	f.count(t, 2)
}

// Replacing a session of the current generation is safe only because results
// belong to the generation: the session that started the work may be the one
// that made room, and a later session must still read that work.
func TestResidentReplacedSessionWorkStaysReadableInItsGeneration(t *testing.T) {
	f := newLifecycleHTTP(t)
	f.control.set(f.s.generation)
	starter := f.open(t)
	id := strings.Repeat("1", 32)
	readWork(f.s, id)
	for i := 1; i <= MaxSessionsPerPrincipal; i++ {
		f.tick()
		f.open(t)
	}
	f.count(t, MaxSessionsPerPrincipal)
	if f.call(http.MethodPost, starter, lifecycleMetadata).Code == http.StatusOK {
		t.Fatal("least recently used session survived")
	}
	latest := f.open(t)
	if v := lifecycleValue(t, f.call(http.MethodPost, latest, lifecycleMetadata), false); v["request_id"] != id {
		t.Fatal("work of the replaced session is no longer readable", v)
	}
	f.count(t, MaxSessionsPerPrincipal)
}
