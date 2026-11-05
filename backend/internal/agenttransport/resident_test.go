package agenttransport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type residentRig struct {
	h   *Handler
	c   *ResidentClient
	m   *memTransport
	rec *recordingRegistrar
}

func newResidentRig(t *testing.T, edit func(*ResidentClientConfig)) *residentRig {
	t.Helper()
	host, principal := fixtureSigner(t, 0x11), fixtureSigner(t, 0x21)
	rec := newRecorder(nil)
	h, e := NewHandler(HandlerConfig{Endpoint: testEndpoint, Registrar: rec, Budgets: testBudgets(), Hosts: []EnrolledHost{{Label: hostLabel, PublicKey: host.PublicKey(), Principals: []string{member1}}}, ResidentPrincipals: []ResidentPrincipal{{Label: member1, Signer: principal}}})
	if e != nil {
		t.Fatal(e)
	}
	cfg := ResidentClientConfig{Endpoint: testEndpoint, HostLabel: hostLabel, PrincipalLabel: member1, HostKey: host, PrincipalKey: principal.PublicKey(), Budgets: testBudgets()}
	if edit != nil {
		edit(&cfg)
	}
	c, e := NewResidentClient(cfg)
	if e != nil {
		t.Fatal(e)
	}
	m := newMemTransport(h)
	c.retryDelay = time.Millisecond
	c.transport = func(own acquirer) http.RoundTripper { m.own = own; m.track = own.attach; return m }
	t.Cleanup(func() {
		ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = c.Close(ctx)
		if e := h.Close(ctx); e != nil {
			t.Error(e)
		}
	})
	return &residentRig{h, c, m, rec}
}
func residentRun(c *ResidentClient) <-chan error {
	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()
	return done
}
func residentReady(t *testing.T, r *residentRig) {
	t.Helper()
	verticalEventually(t, func() bool { return r.c.Connected() && r.h.ResidentConnected(hostLabel, member1) })
}
func residentGeneration(h *Handler) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.residents[residentMapKey(hostLabel, member1)]
	if p == nil {
		return ""
	}
	return p.binding.generation
}
func TestResidentMutualAuthenticationRefusesWorkAndStops(t *testing.T) {
	r := newResidentRig(t, nil)
	done := residentRun(r.c)
	residentReady(t, r)
	if r.rec.count.Load() != 0 {
		t.Fatal("idle control became grant registration")
	}
	deadline := time.Now().Add(10 * time.Second).Truncate(time.Millisecond)
	for n := 0; n < 2; n++ {
		e := r.h.ProposeResidentFixed(context.Background(), hostLabel, member1, fmt.Sprintf("%032x", n+1), deadline)
		if !errors.Is(e, ErrWorkUnconfigured) {
			t.Fatal("work did not fail closed", e)
		}
	}
	if !r.c.Connected() || !r.h.ResidentConnected(hostLabel, member1) || r.rec.count.Load() != 0 {
		t.Fatal("refusal changed authority or ended idle control")
	}
	if e := r.h.ProposeResidentFixed(context.Background(), hostLabel, member1, fmt.Sprintf("%032x", 1), deadline); !errors.Is(e, ErrRefused) {
		t.Fatal("duplicate request accepted", e)
	}
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if e := r.c.Close(ctx); e != nil {
		t.Fatal(e)
	}
	if e := await(t, done, 3*time.Second); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e, ok := r.c.CleanupResult(); !ok || e != nil {
		t.Fatal("original cleanup unconfirmed", e, ok)
	}
	if r.c.Connected() || r.h.ResidentConnected(hostLabel, member1) {
		t.Fatal("stop still reports connected")
	}
	if e := r.c.Run(context.Background()); !errors.Is(e, ErrUsed) {
		t.Fatal("stopped resident restarted", e)
	}
}
func TestResidentReconnectUsesFreshMutualProofWithoutGrant(t *testing.T) {
	r := newResidentRig(t, nil)
	done := residentRun(r.c)
	residentReady(t, r)
	first := residentGeneration(r.h)
	ends := <-r.m.pairs
	_ = ends.server.Close()
	verticalEventually(t, func() bool {
		return r.c.Connected() && r.h.ResidentConnected(hostLabel, member1) && residentGeneration(r.h) != first
	})
	if r.m.trips.Load() != 2 || r.rec.count.Load() != 0 {
		t.Fatal("reconnect resumed work or changed endpoint", r.m.trips.Load())
	}
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if e := r.c.Close(ctx); e != nil {
		t.Fatal(e)
	}
	_ = await(t, done, 3*time.Second)
}
func TestResidentPinsAndEnrollmentRefuseWithoutReconnect(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ResidentClientConfig)
	}{
		{"principal-pin", func(c *ResidentClientConfig) { c.PrincipalKey = fixtureSigner(t, 0x55).PublicKey() }},
		{"host-key", func(c *ResidentClientConfig) { c.HostKey = fixtureSigner(t, 0x56) }},
		{"principal-map", func(c *ResidentClientConfig) { c.PrincipalLabel = member2 }},
		{"endpoint", func(c *ResidentClientConfig) { c.Endpoint = "wss://other.example.invalid/agent/register" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newResidentRig(t, tc.edit)
			e := await(t, residentRun(r.c), 5*time.Second)
			if !errors.Is(e, ErrRefused) || r.c.Connected() || r.h.ResidentConnected(hostLabel, member1) || r.m.trips.Load() != 1 || r.rec.count.Load() != 0 {
				t.Fatal("pin failure retried, connected or granted", e)
			}
		})
	}
}

type residentBadClose struct {
	net.Conn
	calls *atomic.Int32
}

func (c residentBadClose) Close() error {
	c.calls.Add(1)
	_ = c.Conn.Close()
	return errors.New("synthetic retained close failure")
}
func TestResidentUnknownCleanupBlocksReconnectAndDoesNotRetryClose(t *testing.T) {
	r := newResidentRig(t, nil)
	var calls atomic.Int32
	r.c.transport = func(own acquirer) http.RoundTripper {
		r.m.own = own
		r.m.track = func(c net.Conn) (net.Conn, error) { return own.attach(residentBadClose{c, &calls}) }
		return r.m
	}
	done := residentRun(r.c)
	residentReady(t, r)
	ends := <-r.m.pairs
	_ = ends.server.Close()
	e := await(t, done, 5*time.Second)
	if !errors.Is(e, ErrCleanupUnknown) || r.m.trips.Load() != 1 || calls.Load() != 1 {
		t.Fatal("unknown cleanup reconnected or repeated Close", e, calls.Load())
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if r.c.Close(ctx) == nil || calls.Load() != 1 {
		t.Fatal("unknown was cleared/retried")
	}
}
func TestResidentUnobservedTransportNeverAuthenticates(t *testing.T) {
	r := newResidentRig(t, nil)
	r.m.bypassTrack = true
	e := await(t, residentRun(r.c), 5*time.Second)
	if !errors.Is(e, ErrCleanupUnknown) || r.c.Connected() || r.m.trips.Load() != 1 || r.rec.count.Load() != 0 {
		t.Fatal("unobserved transport accepted", e)
	}
}
func TestResidentHandlerOptInAndAmbientCredentialsStayClosed(t *testing.T) {
	host := fixtureSigner(t, 0x11)
	h := newHandler(t, newRecorder(nil), testBudgets(), EnrolledHost{Label: hostLabel, PublicKey: host.PublicKey(), Principals: []string{member1}})
	raw, response := upgrade(t, h, func(q *http.Request) { q.Header.Set("Sec-WebSocket-Protocol", ResidentSubprotocol) })
	if response.StatusCode != http.StatusBadRequest {
		t.Fatal("resident enabled without enrolled principal signer")
	}
	<-raw.served
	r := newResidentRig(t, nil)
	for _, name := range []string{"Origin", "Cookie", "Authorization", "Proxy-Authorization"} {
		raw, response = upgrade(t, r.h, func(q *http.Request) {
			q.Header.Set("Sec-WebSocket-Protocol", ResidentSubprotocol)
			q.Header.Set(name, "synthetic")
		})
		if response.StatusCode != http.StatusForbidden {
			t.Fatal("ambient input accepted", name, response.StatusCode)
		}
		<-raw.served
	}
}
func TestResidentDuplicateLiveHostDoesNotReplaceOriginal(t *testing.T) {
	r := newResidentRig(t, nil)
	done := residentRun(r.c)
	residentReady(t, r)
	generation := residentGeneration(r.h)
	other, e := NewResidentClient(r.c.cfg)
	if e != nil {
		t.Fatal(e)
	}
	m := newMemTransport(r.h)
	other.transport = func(own acquirer) http.RoundTripper { m.own = own; m.track = own.attach; return m }
	e = await(t, residentRun(other), 5*time.Second)
	if !errors.Is(e, ErrRefused) || residentGeneration(r.h) != generation || !r.c.Connected() {
		t.Fatal("duplicate replaced live source", e)
	}
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	_ = r.c.Close(ctx)
	_ = await(t, done, 3*time.Second)
}
func TestResidentConfigurationRejectsMissingPinsAndUnsafeEndpoints(t *testing.T) {
	for _, edit := range []func(*ResidentClientConfig){func(c *ResidentClientConfig) { c.HostKey = nil }, func(c *ResidentClientConfig) { c.PrincipalKey = nil }, func(c *ResidentClientConfig) { c.Endpoint = "ws://gateway.example.invalid/agent/register" }, func(c *ResidentClientConfig) { c.PrincipalKey = c.HostKey.PublicKey() }, func(c *ResidentClientConfig) { c.HostLabel = "invalid host" }} {
		cfg := ResidentClientConfig{Endpoint: testEndpoint, HostLabel: hostLabel, PrincipalLabel: member1, HostKey: fixtureSigner(t, 0x11), PrincipalKey: fixtureSigner(t, 0x21).PublicKey(), Budgets: testBudgets()}
		edit(&cfg)
		if _, e := NewResidentClient(cfg); !errors.Is(e, ErrConfig) {
			t.Fatal("unsafe config accepted", e)
		}
	}
}
