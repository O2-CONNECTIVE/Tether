package agenttransport

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// This regression joins the existing registration/HTTP fixtures across two
// fresh grant epochs on one gateway/Handler. It proves continued authenticated
// gateway availability, not a persistent idle-host protocol or dots behavior.
// The fixture approval and runtime stay synthetic; no native resource starts.
func TestResidentGatewaySurvivesGrantExpiryAndAcceptsOnlyFreshEpoch(t *testing.T) {
	p, firstRuntime := verticalComposed(t, 2*time.Second)
	firstOwner, firstClient := p.owner, p.c
	firstDispatch := firstOwner.Dispatch()
	done, ends := p.connected(t)
	firstMCP := newVerticalMCP(t, p.gateway)
	started, failed := firstMCP.start("same-local-intent", "hold in memory")
	if failed {
		t.Fatal(started)
	}
	verticalID(t, started)
	firstProcess := verticalProcNext(t, firstRuntime)

	// The grant expires without a poll/call driving expiry. Its transport,
	// canonical authority and original process cleanup end; gateway stays up.
	p.ended(t, done, ends)
	if time.Now().Before(firstDispatch.Deadline) {
		t.Fatal("first transport ended before the grant deadline")
	}
	verticalJoined(t, firstProcess)
	if firstOwner.live() || firstRuntime.starts.Load() != 1 {
		t.Fatal("first grant survived or restarted")
	}
	if err, observed := firstClient.CleanupResult(); !observed || err != nil {
		t.Fatal("first original cleanup unconfirmed", observed, err)
	}
	if _, err := NewClient(clientConfig(p.host1), firstOwner); !errors.Is(err, ErrConfig) {
		t.Fatal("expired owner was accepted by a new client", err)
	}
	verticalEventually(t, func() bool {
		r, _ := firstMCP.request(30, "tools/list", nil)
		return r.Code == http.StatusNotFound
	})
	idleMCP := newVerticalMCP(t, p.gateway)
	if r, _ := idleMCP.request(31, "tools/list", nil); r.Code != http.StatusOK {
		t.Fatal("gateway catalog unavailable between grants", r.Code)
	}

	// A new fixture approval owns a different Session, authority, launch and
	// single-use client. The real enrolled Handler and gateway are unchanged.
	secondRuntime := &verticalRuntime{procs: make(chan *verticalProc, 4)}
	p.owner = verticalOwner(t, p.host1, p.m1, secondRuntime, 30*time.Second)
	secondDispatch := p.owner.Dispatch()
	if secondDispatch.Binding == firstDispatch.Binding || firstOwner.live() {
		t.Fatal("fresh epoch reused or revived the old authority")
	}
	p.m = newMemTransport(p.h)
	p.c = newMemClient(t, clientConfig(p.host1), p.owner, p.m)
	done, ends = p.connected(t)
	if !p.owner.Dispatch().Deadline.Equal(secondDispatch.Deadline) {
		t.Fatal("registration renewed the approved deadline")
	}

	// Neither the old bound MCP session nor the idle catalog session may
	// adopt the new grant. Exact same request still needs fresh initialize.
	for _, c := range []*verticalMCP{firstMCP, idleMCP} {
		r, _ := c.request(32, "tools/call", map[string]any{"name": "rescue_start", "arguments": map[string]any{"request_id": "same-local-intent", "command": "hold in memory", "timeout_ms": 1000}})
		if r.Code != http.StatusNotFound || secondRuntime.starts.Load() != 0 {
			t.Fatal("old MCP session adopted fresh authority", r.Code)
		}
	}
	freshMCP := newVerticalMCP(t, p.gateway)
	credential := freshMCP.token
	freshMCP.token = ""
	if r, _ := freshMCP.request(33, "tools/list", nil); r.Code != http.StatusUnauthorized {
		t.Fatal("persistent session bypassed per-request authentication", r.Code)
	}
	freshMCP.token = credential
	started, failed = freshMCP.start("same-local-intent", "hold in memory")
	if failed {
		t.Fatal(started)
	}
	secondID := verticalID(t, started)
	secondProcess := verticalProcNext(t, secondRuntime)
	replayed, failed := freshMCP.start("same-local-intent", "hold in memory")
	if failed || verticalID(t, replayed) != secondID || secondRuntime.starts.Load() != 1 {
		t.Fatal("new epoch replay started another command")
	}

	// The CLI's existing shutdown order closes registration transports before
	// gateway routes. This ends the exact active grant, then all availability.
	cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.h.Close(cleanup); err != nil {
		t.Fatal(err)
	}
	p.ended(t, done, ends)
	verticalJoined(t, secondProcess)
	if err, observed := p.c.CleanupResult(); !observed || err != nil {
		t.Fatal("second original cleanup unconfirmed", observed, err)
	}
	if err := p.gateway.Close(cleanup); err != nil {
		t.Fatal(err)
	}
	freshMCP.sid = ""
	if r, _ := freshMCP.request(34, "initialize", nil); r.Code != http.StatusServiceUnavailable {
		t.Fatal("stopped gateway accepted initialize", r.Code)
	}
	if firstOwner.live() || p.owner.live() || firstRuntime.starts.Load() != 1 || secondRuntime.starts.Load() != 1 || p.rec.count.Load() != 2 {
		t.Fatal("stop/reconnect revived a grant or created unexpected work")
	}
}
