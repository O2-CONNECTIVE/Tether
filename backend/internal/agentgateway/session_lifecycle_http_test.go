package agentgateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResidentCatalogAndBoundSessionsShareFourSlots(t *testing.T) {
	f := newLifecycleHTTP(t)
	if MaxSessionsPerPrincipal != 4 {
		t.Fatal("review session-bound change explicitly")
	}
	catalog := []string{f.open(t), f.open(t)}
	f.control.set(f.s.generation)
	bound := []string{f.open(t)}
	f.tick()
	bound = append(bound, f.open(t))
	w := readWork(f.s, strings.Repeat("1", 32))
	f.s.entry.current = w // synthetic active work, not a running command
	tombID := strings.Repeat("2", 32)
	readWork(f.s, tombID)
	releaseListWork(t, f.s, tombID, true)
	tomb := f.s.entry.tombstones[tombID]
	f.count(t, 4)
	// At the bound a resident initialize takes the slot of the least recently
	// used resident session. Catalog sessions share the bound but keep theirs.
	f.tick()
	replacement := f.open(t)
	if got := f.call(http.MethodPost, bound[0], lifecycleList); got.Code != 404 {
		t.Fatal("least recently used resident session survived", got.Code)
	}
	bound[0] = replacement
	for _, sid := range append(catalog, bound...) {
		if got := f.call(http.MethodPost, sid, lifecycleList); got.Code != 200 {
			t.Fatal("capacity ended a session that must be kept", got.Code)
		}
	}
	// A resident catalog refusal does not upgrade or silently evict its SID.
	for _, sid := range catalog {
		res := f.call(http.MethodPost, sid, lifecycleMetadata)
		var envelope map[string]any
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &envelope) != nil || envelope["error"] == nil {
			t.Fatal("catalog unexpectedly observed work", res.Body.String())
		}
	}
	f.count(t, 4)
	// DELETE frees only the chosen transport slot, never results/replay/work.
	if res := f.call(http.MethodDelete, bound[0], ""); res.Code != 204 {
		t.Fatal(res.Code)
	}
	if f.s.entry.works[w.binding.RequestID] != w {
		t.Fatal("DELETE discarded retained result")
	}
	if f.s.entry.current != w || w.cancelled || f.s.entry.tombstones[tombID].generation != tomb.generation || len(f.s.entry.tombstones) != 1 {
		t.Fatal("DELETE changed active work or replay tombstone")
	}
	if res := f.call(http.MethodPost, bound[0], lifecycleList); res.Code != 404 {
		t.Fatal("deleted SID live")
	}
	if res := f.call(http.MethodDelete, bound[0], ""); res.Code != 404 {
		t.Fatal("repeated delete changed accounting")
	}
	f.count(t, 3)
	f.open(t)
	f.count(t, 4)
	res := f.call(http.MethodPost, bound[1], lifecycleMetadata)
	if v := lifecycleValue(t, res, false); v["request_id"] != w.binding.RequestID {
		t.Fatal("remaining owner lost result", v)
	}
}

func TestResidentConcurrentInitializeNeverExceedsFourSlots(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(map[bool]string{false: "catalog", true: "bound"}[bound], func(t *testing.T) {
			f := newLifecycleHTTP(t)
			if bound {
				f.control.set(f.s.generation)
			}
			start := make(chan struct{})
			results := make(chan int, 16)
			var wg sync.WaitGroup
			for i := 0; i < cap(results); i++ {
				wg.Add(1)
				go func() { defer wg.Done(); <-start; results <- f.call(http.MethodPost, "", lifecycleInit).Code }()
			}
			close(start)
			wg.Wait()
			close(results)
			accepted := 0
			for code := range results {
				switch code {
				case 200:
					accepted++
				case 429, 503: // A race losing the second admission check is 503.
				default:
					t.Fatalf("unexpected initialize status %d", code)
				}
			}
			// Resident initializes at the bound replace one another, so more
			// than four may be admitted; four live sessions is the invariant.
			if accepted < 4 || (!bound && accepted != 4) {
				t.Fatalf("accepted %d sessions", accepted)
			}
			f.count(t, 4)
		})
	}
}

func TestResidentIdleExpiryIsLazyAndPreservesRecentlyUsedSID(t *testing.T) {
	f := newLifecycleHTTP(t)
	old := []string{f.open(t), f.open(t)}
	f.control.set(f.s.generation)
	bound := f.open(t)
	kept := f.open(t)
	f.g.mu.Lock()
	f.now = f.now.Add(SessionIdle)
	f.g.mu.Unlock()
	// Equality is not expiry, and a rejected initialize cannot silently evict.
	// The resident is away for this request: a catalog initialize replaces nothing.
	f.control.set("")
	if res := f.call(http.MethodPost, "", lifecycleInit); res.Code != 429 {
		t.Fatal(res.Code)
	}
	f.control.set(f.s.generation)
	if res := f.call(http.MethodPost, kept, lifecycleList); res.Code != 200 {
		t.Fatal(res.Code)
	}
	f.g.mu.Lock()
	f.now = f.now.Add(time.Nanosecond)
	f.g.mu.Unlock()
	f.count(t, 4) // Time alone has no background eviction side effect.
	f.open(t)     // initialize lazily expires only the other three sessions.
	f.count(t, 2)
	for _, sid := range append(old, bound) {
		if res := f.call(http.MethodPost, sid, lifecycleList); res.Code != 404 {
			t.Fatal("expired SID remains", res.Code)
		}
	}
	if res := f.call(http.MethodPost, kept, lifecycleList); res.Code != 200 {
		t.Fatal("recently used SID lost", res.Code)
	}
	f.count(t, 2)
}
