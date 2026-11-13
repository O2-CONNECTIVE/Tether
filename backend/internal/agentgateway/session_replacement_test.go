package agentgateway_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// generationControl stands in for the resident control connection. Each Mac
// reconnect yields a new generation; an empty generation means no resident is
// connected.
type generationControl struct {
	mu  sync.Mutex
	gen string
}

func (c *generationControl) set(gen string) { c.mu.Lock(); c.gen = gen; c.mu.Unlock() }
func (c *generationControl) ResidentGeneration(string, string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}
func (*generationControl) BeginResidentFixed(ctx context.Context, _, _, _, _ string, _ time.Time) error {
	<-ctx.Done()
	return ctx.Err()
}
func (*generationControl) BeginResidentRun(ctx context.Context, _, _, _, _ string, _ time.Time, _ agentgateway.ResidentRunScope) error {
	return agentgateway.ErrResidentWorkUnconfigured
}

type capacityRig struct {
	*rig
	control *generationControl
	bearer  string
	tick    func()
}

// newCapacityRig enrolls one resident host for the first principal. tick
// advances the gateway clock so "least recently used" is unambiguous.
func newCapacityRig(t *testing.T) *capacityRig {
	t.Helper()
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	r := newModeRig(t, agentgateway.Config{Verifier: verifier(t), Clock: clock})
	control := &generationControl{}
	entries := []agentgateway.ResidentEnrollment{
		{HostLabel: "host1", PrincipalLabel: label(0), HostKey: fixtureSigner(t, 0x61).PublicKey().Marshal()},
		{HostLabel: "host2", PrincipalLabel: label(1), HostKey: fixtureSigner(t, 0x62).PublicKey().Marshal()},
	}
	if err := r.gw.ConfigureResident("wss://example.test/register", control, entries); err != nil {
		t.Fatal(err)
	}
	return &capacityRig{rig: r, control: control, bearer: token(t, subjects[0]),
		tick: func() { mu.Lock(); now = now.Add(time.Second); mu.Unlock() }}
}

// fill opens sessions up to the per-principal bound, oldest first.
func (c *capacityRig) fill() []string {
	c.t.Helper()
	var ids []string
	for i := 0; i < agentgateway.MaxSessionsPerPrincipal; i++ {
		ids = append(ids, c.open(c.bearer))
		c.tick()
	}
	return ids
}

func (c *capacityRig) initializeStatus() int {
	return c.do(http.MethodPost, c.bearer, "", rpc(0, "initialize", initParams()), nil).status
}

func (c *capacityRig) alive(sid string) bool {
	return c.do(http.MethodPost, c.bearer, sid, rpc(1, "tools/list", nil), nil).status == http.StatusOK
}

// assertReplacedOnlyOldest checks that exactly the least recently used of ids
// ended and the others still answer.
func (c *capacityRig) assertReplacedOnlyOldest(ids []string) {
	c.t.Helper()
	if c.alive(ids[0]) {
		c.t.Fatal("least recently used session survived")
	}
	for _, sid := range ids[1:] {
		if !c.alive(sid) {
			c.t.Fatal("more than one session was replaced")
		}
	}
}

func (c *capacityRig) assertAllAlive(ids []string) {
	c.t.Helper()
	for _, sid := range ids {
		if !c.alive(sid) {
			c.t.Fatal("a session that must be kept was replaced")
		}
	}
}

var (
	generationA = strings.Repeat("a", 64)
	generationB = strings.Repeat("b", 64)
)

// A client must reinitialize after every resident reconnect, because a session
// of an older generation can no longer start work. Those superseded sessions
// must not keep the principal locked out with 429 until they idle out: the
// least recently used one makes room, and only that one.
func TestResidentReconnectReplacesOldestSupersededSession(t *testing.T) {
	c := newCapacityRig(t)
	c.control.set(generationA)
	ids := c.fill()
	c.control.set(generationB)

	if status := c.initializeStatus(); status != http.StatusOK {
		t.Fatalf("initialize after reconnect = %d, want 200", status)
	}
	c.assertReplacedOnlyOldest(ids)
}

// Another member's superseded sessions are older and just as unable to start
// work, but they are not this principal's to end: one member reconnecting must
// never cut off another member's client.
func TestReplacementNeverEndsAnotherPrincipalsSupersededSession(t *testing.T) {
	c := newCapacityRig(t)
	other := token(t, subjects[1])
	c.control.set(generationA)
	var theirs []string
	for i := 0; i < agentgateway.MaxSessionsPerPrincipal; i++ {
		theirs = append(theirs, c.open(other))
		c.tick()
	}
	ids := c.fill()
	c.control.set(generationB)

	if status := c.initializeStatus(); status != http.StatusOK {
		t.Fatalf("initialize after reconnect = %d, want 200", status)
	}
	c.assertReplacedOnlyOldest(ids)
	for _, sid := range theirs {
		if res := c.do(http.MethodPost, other, sid, rpc(1, "tools/list", nil), nil); res.status != http.StatusOK {
			t.Fatal("another principal's superseded session was replaced")
		}
	}
}

// "Oldest" means least recently used, not first created: a superseded session
// a client is still reading results from outlives an abandoned newer one.
func TestReplacementFollowsLastUseNotCreationOrder(t *testing.T) {
	c := newCapacityRig(t)
	c.control.set(generationA)
	ids := c.fill()
	if !c.alive(ids[0]) {
		t.Fatal("first session not usable")
	}
	c.control.set(generationB)

	if status := c.initializeStatus(); status != http.StatusOK {
		t.Fatalf("initialize after reconnect = %d, want 200", status)
	}
	if !c.alive(ids[0]) {
		t.Fatal("recently used session was replaced")
	}
	if c.alive(ids[1]) {
		t.Fatal("least recently used session survived")
	}
}

// A session that can still start work is never the one replaced, even when it
// is older than every superseded session.
func TestReplacementSkipsOlderCurrentGenerationSessions(t *testing.T) {
	c := newCapacityRig(t)
	c.control.set(generationB)
	var current []string
	for i := 1; i < agentgateway.MaxSessionsPerPrincipal; i++ {
		current = append(current, c.open(c.bearer))
		c.tick()
	}
	c.control.set(generationA)
	superseded := c.open(c.bearer)
	c.control.set(generationB)

	if status := c.initializeStatus(); status != http.StatusOK {
		t.Fatalf("initialize = %d, want 200", status)
	}
	if c.alive(superseded) {
		t.Fatal("superseded session survived")
	}
	c.assertAllAlive(current)
}

// Replacement is a last resort at the bound, and it must not cost a session
// when no new one results from the request.
func TestNoSessionEndsWithoutAdmittingOne(t *testing.T) {
	t.Run("below the bound", func(t *testing.T) {
		c := newCapacityRig(t)
		c.control.set(generationA)
		ids := []string{c.open(c.bearer), c.open(c.bearer)}
		c.control.set(generationB)
		if status := c.initializeStatus(); status != http.StatusOK {
			t.Fatalf("initialize below the bound = %d, want 200", status)
		}
		c.assertAllAlive(ids)
	})
	t.Run("the initialize request is invalid", func(t *testing.T) {
		c := newCapacityRig(t)
		c.control.set(generationA)
		ids := c.fill()
		c.control.set(generationB)
		if res := c.do(http.MethodPost, c.bearer, "", rpc(0, "tools/list", nil), nil); res.status != http.StatusBadRequest {
			t.Fatalf("invalid initialize = %d, want 400", res.status)
		}
		c.assertAllAlive(ids)
	})
}

// A real client (ChatGPT, measured 2026-11-13) opens a new session for every
// tool call and never deletes one. Work is read by generation, not by session,
// so an abandoned session of the current generation holds nothing but a slot.
// It must not lock the principal out of its fifth call until it idles out: the
// least recently used one makes room, and only that one.
func TestSessionPerCallClientIsNotLockedOutWithinOneGeneration(t *testing.T) {
	c := newCapacityRig(t)
	c.control.set(generationA)
	ids := c.fill()

	if status := c.initializeStatus(); status != http.StatusOK {
		t.Fatalf("fifth initialize in one generation = %d, want 200", status)
	}
	c.assertReplacedOnlyOldest(ids)
}

// Within one generation too, a session a client still polls on outlives an
// abandoned newer one.
func TestReplacementWithinGenerationFollowsLastUse(t *testing.T) {
	c := newCapacityRig(t)
	c.control.set(generationA)
	ids := c.fill()
	if !c.alive(ids[0]) {
		t.Fatal("first session not usable")
	}

	if status := c.initializeStatus(); status != http.StatusOK {
		t.Fatalf("initialize = %d, want 200", status)
	}
	if !c.alive(ids[0]) {
		t.Fatal("recently used session was replaced")
	}
	if c.alive(ids[1]) {
		t.Fatal("least recently used session survived")
	}
}

// Another member's sessions are older, whether discovery sessions or resident
// sessions of the same generation, but they are not this principal's to end.
func TestReplacementWithinGenerationNeverEndsAnotherPrincipalsSession(t *testing.T) {
	c := newCapacityRig(t)
	other := token(t, subjects[1])
	var theirs []string
	for i := 0; i < agentgateway.MaxSessionsPerPrincipal; i++ {
		if i == agentgateway.MaxSessionsPerPrincipal/2 {
			c.control.set(generationA)
		}
		theirs = append(theirs, c.open(other))
		c.tick()
	}
	ids := c.fill()

	if status := c.initializeStatus(); status != http.StatusOK {
		t.Fatalf("initialize = %d, want 200", status)
	}
	c.assertReplacedOnlyOldest(ids)
	for _, sid := range theirs {
		if res := c.do(http.MethodPost, other, sid, rpc(1, "tools/list", nil), nil); res.status != http.StatusOK {
			t.Fatal("another principal's session was replaced")
		}
	}
}

// While the resident is away, a client that opens a session per call must be
// told so by the tool reply of a catalog session, not locked out with 429 by
// the resident sessions it left behind: the least recently used one makes
// room, and only that one.
func TestCatalogInitializeWhileResidentAwayReplacesOldestResidentSession(t *testing.T) {
	c := newCapacityRig(t)
	c.control.set(generationA)
	ids := c.fill()
	c.control.set("")

	if status := c.initializeStatus(); status != http.StatusOK {
		t.Fatalf("catalog initialize while the resident is away = %d, want 200", status)
	}
	c.assertReplacedOnlyOldest(ids)
}

// Replacement costs only one of the principal's own resident sessions.
// Discovery sessions and sessions bound to an approved route keep the bound,
// and an initialize that would bind to an approved route replaces nothing:
// the caller gets 429 and nothing is ended on its behalf.
func TestSessionBoundHoldsWithoutReplaceableSession(t *testing.T) {
	// A catalog client may still be listing tools on a discovery session, so a
	// rejected initialize must not silently end one.
	t.Run("the principal holds only discovery sessions", func(t *testing.T) {
		c := newCapacityRig(t)
		ids := c.fill()
		c.control.set(generationA)
		if status := c.initializeStatus(); status != http.StatusTooManyRequests {
			t.Fatalf("initialize over discovery sessions = %d, want 429", status)
		}
		c.assertAllAlive(ids)
	})
	t.Run("only discovery sessions while the resident is away", func(t *testing.T) {
		c := newCapacityRig(t)
		ids := c.fill()
		if status := c.initializeStatus(); status != http.StatusTooManyRequests {
			t.Fatalf("catalog initialize over discovery sessions = %d, want 429", status)
		}
		c.assertAllAlive(ids)
	})
	t.Run("the new session would bind to an approved route", func(t *testing.T) {
		c := newCapacityRig(t)
		c.control.set(generationA)
		ids := c.fill()
		c.control.set("")
		c.approve(0, 30*time.Second)
		if status := c.initializeStatus(); status != http.StatusTooManyRequests {
			t.Fatalf("route-bound initialize over resident sessions = %d, want 429", status)
		}
		c.assertAllAlive(ids)
	})
	t.Run("sessions are bound to an approved route", func(t *testing.T) {
		c := newCapacityRig(t)
		c.approve(0, 30*time.Second)
		ids := c.fill()
		c.control.set(generationA)
		if status := c.initializeStatus(); status != http.StatusTooManyRequests {
			t.Fatalf("initialize over route-bound sessions = %d, want 429", status)
		}
		c.assertAllAlive(ids)
	})
}
