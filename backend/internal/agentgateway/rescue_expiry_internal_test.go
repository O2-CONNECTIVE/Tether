package agentgateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"
)

type rescueExpiryClock struct {
	mu  sync.Mutex
	now time.Time
}

func newRescueExpiryClock(now time.Time) *rescueExpiryClock {
	return &rescueExpiryClock{now: now}
}

func (c *rescueExpiryClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *rescueExpiryClock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func waitRescueDone(t *testing.T, done <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s reception did not close within 3 seconds", label)
	}
}

func rescueRecordCount(g *Gateway) int {
	g.rescue.mu.Lock()
	defer g.rescue.mu.Unlock()
	return len(g.rescue.records)
}

func TestRescueExpiredUnrequestedReceptionIsEndedByGateway(t *testing.T) {
	// Gateway cleanup must reclaim expired reception capacity even if its worker never disconnects.
	// Otherwise 1,000 stale records can make legitimate registrations fail with 503 (#433).
	clock := newRescueExpiryClock(time.Now())
	now := clock.Now()
	cfg := rescueReceptionConfig(t, &now)
	cfg.Clock = clock.Now
	cfg.Organization = "O2 CONNECTIVE"
	cfg.Principals[0].DisplayName = "山田（サポート）"
	g, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer g.Close(context.Background())
	res := openRescue(t, g, 72)
	before := rescueRecordCount(g)
	if before == 0 {
		t.Fatal("OpenRescue did not add a reception record")
	}
	beforeCodes := g.rescueCodes.liveCount()
	clock.Add(5 * time.Minute)
	waitRescueDone(t, res.Done, "expired")
	if got := rescueRecordCount(g); got != before-1 {
		t.Fatalf("reception records after expiry = %d; want %d", got, before-1)
	}
	if got := g.rescueCodes.liveCount(); got >= beforeCodes {
		t.Fatalf("live rescue codes after expiry = %d; want fewer than %d", got, beforeCodes)
	}
	bearer := internalRescueToken(t, now)
	sid := internalInitialize(t, g, bearer)
	call := internalTool(t, g, bearer, sid, "rescue_request", map[string]any{"code": res.Code})
	var response map[string]any
	if err := json.Unmarshal(call.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	result, _ := response["result"].(map[string]any)
	content, _ := result["content"].([]any)
	message := ""
	if len(content) > 0 {
		message, _ = content[0].(map[string]any)["text"].(string)
	}
	if result["isError"] != true || message != "the rescue code is not valid" {
		t.Fatalf("expired rescue_request = isError %v, message %q; want invalid-code refusal", result["isError"], message)
	}
}

func TestRescueExpiryDoesNotEndRequestMatchedImmediatelyBeforeExpiry(t *testing.T) {
	// A request matched before the code deadline owns the reception through its separate 75-second lifetime.
	clock := newRescueExpiryClock(time.Now())
	now := clock.Now()
	cfg := rescueReceptionConfig(t, &now)
	cfg.Clock = clock.Now
	cfg.CallTimeout = 100 * time.Millisecond
	cfg.Organization = "O2 CONNECTIVE"
	cfg.Principals[0].DisplayName = "山田（サポート）"
	g, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer g.Close(context.Background())
	res := openRescue(t, g, 73)
	clock.Add(4*time.Minute + 59*time.Second)
	bearer := internalRescueToken(t, now)
	sid := internalInitialize(t, g, bearer)
	call := internalTool(t, g, bearer, sid, "rescue_request", map[string]any{"code": res.Code})
	if got := internalState(t, call); got != "pending" {
		t.Fatalf("rescue_request state = %q; want pending", got)
	}
	var identity RescueIdentity
	select {
	case identity = <-res.Request:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach the host")
	}
	clock.Add(2 * time.Second)
	select {
	case <-res.Done:
		t.Fatal("expiry sweep closed a reception with a live request")
	case <-time.After(2 * time.Second):
	}
	if got := rescueRecordCount(g); got != 1 {
		t.Fatalf("reception records with a live request = %d; want 1", got)
	}
	status := internalTool(t, g, bearer, sid, "rescue_request_status", map[string]any{})
	if got := internalState(t, status); got != "pending" {
		t.Fatalf("request status after code expiry = %q; want pending", got)
	}
	// A request matched before code expiry remains answerable for its full lifetime.
	hostKey := rescueHostKey(73)
	digest := sha256.Sum256(RescueRequestTranscript(g.RescueEndpoint(), res.ID, hostKey.PublicKey().Marshal(), identity.Nonce, identity.Client, identity.DisplayName, identity.Organization, identity.Profile))
	signature, err := hostKey.Sign(rand.Reader, RescueAnswerTranscript(res.ID, identity.Nonce, digest[:], RescueDeny, Dispatch{}))
	if err != nil {
		t.Fatal(err)
	}
	conn, peer := net.Pipe()
	defer peer.Close()
	_ = g.AnswerRescue(res.ID, RescueAnswer{Answer: RescueDeny, Signature: signature.Blob}, conn) // nil only when a channel opens
	status = internalTool(t, g, bearer, sid, "rescue_request_status", map[string]any{})
	if got := internalState(t, status); got != RescueDeny {
		t.Fatalf("request status after deny = %q; want deny", got)
	}
}

func TestRescueExpirySweepEndsOnlyUnrequestedReception(t *testing.T) {
	// The one-second sweep must distinguish an unrequested B from A after A's request is attached.
	clock := newRescueExpiryClock(time.Now())
	now := clock.Now()
	cfg := rescueReceptionConfig(t, &now)
	cfg.Clock = clock.Now
	cfg.CallTimeout = 100 * time.Millisecond
	cfg.Organization = "O2 CONNECTIVE"
	cfg.Principals[0].DisplayName = "山田（サポート）"
	g, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer g.Close(context.Background())
	a := openRescue(t, g, 74)
	b := openRescue(t, g, 75)
	clock.Add(4*time.Minute + 30*time.Second)
	bearer := internalRescueToken(t, now)
	sid := internalInitialize(t, g, bearer)
	call := internalTool(t, g, bearer, sid, "rescue_request", map[string]any{"code": a.Code})
	if got := internalState(t, call); got != "pending" {
		t.Fatalf("rescue_request state = %q; want pending", got)
	}
	select {
	case identity := <-a.Request:
		_ = identity
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach reception A")
	}
	clock.Add(30 * time.Second)
	waitRescueDone(t, b.Done, "unrequested B")
	select {
	case <-a.Done:
		t.Fatal("expiry sweep closed requested reception A")
	default:
	}
	status := internalTool(t, g, bearer, sid, "rescue_request_status", map[string]any{})
	if got := internalState(t, status); got != "pending" {
		t.Fatalf("request A status after B expiry = %q; want pending", got)
	}
	if got := rescueRecordCount(g); got != 1 {
		t.Fatalf("reception records after only B expires = %d; want 1", got)
	}
}
