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
	// A resident catalog refusal never upgrades its SID. The refusal is the
	// last reply of that session: it frees the slot, not anyone else's.
	for _, sid := range catalog {
		res := f.call(http.MethodPost, sid, lifecycleMetadata)
		var envelope map[string]any
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &envelope) != nil || envelope["error"] == nil {
			t.Fatal("catalog unexpectedly observed work", res.Body.String())
		}
		if got := f.call(http.MethodPost, sid, lifecycleList); got.Code != 404 {
			t.Fatal("refused catalog SID stayed live", got.Code)
		}
	}
	f.count(t, 2)
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
	f.count(t, 1)
	f.open(t)
	f.count(t, 2)
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
	// Equality is not expiry: a session used exactly at the idle bound answers.
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

// A catalog session cannot call tools and never upgrades. Listing tools keeps
// it; once it has refused a tools/call it holds nothing but a slot, and a
// client that opens a session per call would fill the bound with such
// sessions, so the refusal ends it.
func TestResidentCatalogRefusalEndsOnlyItsOwnSession(t *testing.T) {
	f := newLifecycleHTTP(t)
	listing, calling := f.open(t), f.open(t)
	if res := f.call(http.MethodPost, calling, lifecycleList); res.Code != 200 {
		t.Fatal("catalog listing", res.Code)
	}
	f.count(t, 2)
	res := f.call(http.MethodPost, calling, lifecycleMetadata)
	if res.Code != 200 || !strings.Contains(res.Body.String(), "Resident catalog only") {
		t.Fatal("refusal did not reach the client", res.Code, res.Body.String())
	}
	if res := f.call(http.MethodPost, calling, lifecycleList); res.Code != 404 {
		t.Fatal("refused catalog SID stayed live", res.Code)
	}
	if res := f.call(http.MethodPost, listing, lifecycleList); res.Code != 200 {
		t.Fatal("another catalog session ended", res.Code)
	}
	f.count(t, 1)
}

// The measured client (ChatGPT, 2026-11-13) opens a session for every tool
// call and deletes none. Calls made while standby is off must each get the
// catalog refusal, and must not leave the principal locked out with 429 once
// standby is back on.
func TestSessionPerCallClientRecoversAfterStandbyOff(t *testing.T) {
	f := newLifecycleHTTP(t)
	f.control.set(f.s.generation)
	for i := 0; i < MaxSessionsPerPrincipal; i++ {
		f.open(t)
		f.tick()
	}
	f.control.set("")
	for i := 0; i <= MaxSessionsPerPrincipal; i++ {
		res := f.call(http.MethodPost, f.open(t), lifecycleMetadata)
		if res.Code != 200 || !strings.Contains(res.Body.String(), "Resident catalog only") {
			t.Fatal("call while standby is off", i, res.Code, res.Body.String())
		}
		f.tick()
	}
	f.control.set(strings.Repeat("b", 64))
	f.open(t)
}

// Catalog sessions keep the bound: a rejected initialize ends none of them,
// and a session used exactly SessionIdle ago has not expired.
func TestRejectedCatalogInitializeAtIdleBoundEndsNothing(t *testing.T) {
	f := newLifecycleHTTP(t)
	var ids []string
	for i := 0; i < MaxSessionsPerPrincipal; i++ {
		ids = append(ids, f.open(t))
	}
	f.g.mu.Lock()
	f.now = f.now.Add(SessionIdle)
	f.g.mu.Unlock()
	if res := f.call(http.MethodPost, "", lifecycleInit); res.Code != 429 || res.Header().Get(HeaderSession) != "" {
		t.Fatal("initialize over catalog sessions", res.Code)
	}
	for _, sid := range ids {
		if res := f.call(http.MethodPost, sid, lifecycleList); res.Code != 200 {
			t.Fatal("rejected initialize ended a catalog session", res.Code)
		}
	}
	f.count(t, MaxSessionsPerPrincipal)
}

// flipControl reports generation first for its first ResidentGeneration call
// and then for every later one: the resident changes during one initialize.
type flipControl struct {
	*lifecycleControl
	first, then string
	calls       int // read and changed only under Gateway.mu
}

func (c *flipControl) ResidentGeneration(string, string) string {
	c.calls++
	if c.calls == 1 {
		return c.first
	}
	return c.then
}

// An initialize decides what it may replace from the resident state it saw
// first. If the resident connects before the session is admitted, the catalog
// session built for "away" must not cost a resident session that can work.
func TestInitializeEndsNothingWhenResidentChangesMidway(t *testing.T) {
	f := newLifecycleHTTP(t)
	f.control.set(f.s.generation)
	var ids []string
	for i := 0; i < MaxSessionsPerPrincipal; i++ {
		ids = append(ids, f.open(t))
		f.tick()
	}
	f.g.resident.control = &flipControl{lifecycleControl: f.control, first: "", then: f.s.generation}
	res := f.call(http.MethodPost, "", lifecycleInit)
	f.g.resident.control = f.control
	if res.Code != 503 || res.Header().Get(HeaderSession) != "" || !strings.Contains(res.Body.String(), "resident control changed") {
		t.Fatal("initialize across a resident change", res.Code, res.Body.String())
	}
	for _, sid := range ids {
		if got := f.call(http.MethodPost, sid, lifecycleList); got.Code != 200 {
			t.Fatal("a resident session was ended for a session that was not admitted", got.Code)
		}
	}
	f.count(t, MaxSessionsPerPrincipal)
}
