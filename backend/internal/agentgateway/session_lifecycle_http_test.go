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
	if MaxSessionsPerPrincipal != 4 { t.Fatal("review session-bound change explicitly") }
	catalog := []string{f.open(t), f.open(t)}
	f.control.set(f.s.generation)
	bound := []string{f.open(t), f.open(t)}
	w := readWork(f.s, strings.Repeat("1", 32))
	f.count(t, 4)
	full := f.call(http.MethodPost, "", lifecycleInit)
	if full.Code != 429 || full.Header().Get(HeaderSession) != "" { t.Fatal("full initialization", full.Code) }
	for _, sid := range append(catalog, bound...) {
		if got := f.call(http.MethodPost, sid, lifecycleList); got.Code != 200 {
			t.Fatal("capacity rejected existing session", got.Code)
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
	if res := f.call(http.MethodDelete, bound[0], ""); res.Code != 204 { t.Fatal(res.Code) }
	if f.s.entry.works[w.binding.RequestID] != w { t.Fatal("DELETE discarded retained result") }
	if res := f.call(http.MethodPost, bound[0], lifecycleList); res.Code != 404 { t.Fatal("deleted SID live") }
	if res := f.call(http.MethodDelete, bound[0], ""); res.Code != 404 { t.Fatal("repeated delete changed accounting") }
	f.count(t, 3)
	f.open(t)
	f.count(t, 4)
	res := f.call(http.MethodPost, bound[1], lifecycleMetadata)
	if !strings.Contains(res.Body.String(), `"request_id":"11111111111111111111111111111111"`) { t.Fatal("remaining owner lost result", res.Body.String()) }
}

func TestResidentConcurrentInitializeNeverExceedsFourSlots(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(map[bool]string{false:"catalog", true:"bound"}[bound], func(t *testing.T) {
			f := newLifecycleHTTP(t)
			if bound { f.control.set(f.s.generation) }
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
				case 200: accepted++
				case 429, 503: // A race losing the second admission check is 503.
				default: t.Fatalf("unexpected initialize status %d", code)
				}
			}
			if accepted != 4 { t.Fatalf("accepted %d sessions", accepted) }
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
	if res := f.call(http.MethodPost, "", lifecycleInit); res.Code != 429 { t.Fatal(res.Code) }
	if res := f.call(http.MethodPost, kept, lifecycleList); res.Code != 200 { t.Fatal(res.Code) }
	f.g.mu.Lock()
	f.now = f.now.Add(time.Nanosecond)
	f.g.mu.Unlock()
	f.count(t, 4) // Time alone has no background eviction side effect.
	f.open(t) // initialize lazily expires only the other three sessions.
	f.count(t, 2)
	for _, sid := range append(old, bound) {
		if res := f.call(http.MethodPost, sid, lifecycleList); res.Code != 404 { t.Fatal("expired SID remains", res.Code) }
	}
	if res := f.call(http.MethodPost, kept, lifecycleList); res.Code != 200 { t.Fatal("recently used SID lost", res.Code) }
	f.count(t, 2)
}
