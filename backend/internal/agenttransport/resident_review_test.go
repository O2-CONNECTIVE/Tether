package agenttransport

import (
	"context"
	"fmt"
	"github.com/coder/websocket"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestResidentLiveControlsLeaveLegacyHandshakeCapacity(t *testing.T) {
	principal := fixtureSigner(t, 0x21)
	rec := newRecorder(nil)
	hosts := []EnrolledHost{}
	for n := 0; n < MaxHosts; n++ {
		hosts = append(hosts, EnrolledHost{Label: fmt.Sprintf("mac-%d", n+1), PublicKey: fixtureSigner(t, byte(0x11+n)).PublicKey(), Principals: []string{member1}})
	}
	h, e := NewHandler(HandlerConfig{Endpoint: testEndpoint, Registrar: rec, Budgets: testBudgets(), Hosts: hosts, ResidentPrincipals: []ResidentPrincipal{{Label: member1, Signer: principal}}})
	if e != nil {
		t.Fatal(e)
	}
	clients := []*ResidentClient{}
	runs := []<-chan error{}
	defer func() {
		ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		for _, c := range clients {
			_ = c.Close(ctx)
		}
		for _, done := range runs {
			_ = await(t, done, time.Second)
		}
		if e := h.Close(ctx); e != nil {
			t.Error(e)
		}
	}()
	for n := 0; n < MaxHosts; n++ {
		c, e := NewResidentClient(ResidentClientConfig{Endpoint: testEndpoint, HostLabel: hosts[n].Label, PrincipalLabel: member1, HostKey: fixtureSigner(t, byte(0x11+n)), PrincipalKey: principal.PublicKey(), Budgets: testBudgets()})
		if e != nil {
			t.Fatal(e)
		}
		m := newMemTransport(h)
		c.transport = func(own acquirer) http.RoundTripper { m.own = own; m.track = own.attach; return m }
		clients = append(clients, c)
		runs = append(runs, residentRun(c))
		label := hosts[n].Label
		verticalEventually(t, func() bool { return c.Connected() && h.ResidentConnected(label, member1) })
	}
	legacy, response := upgrade(t, h, nil)
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("resident occupancy blocked legacy handshake: HTTP%d", response.StatusCode)
	}
	legacy.text(signedRegister(t, testEndpoint, hostLabel, member1, fixtureDispatch(fixtureSigner(t, 0x11), "legacy-admission-fixture", time.Now().Add(time.Minute)), legacy.challenge(), fixtureSigner(t, 0x11)))
	legacy.expectAccepted()
	call := rec.next(t)
	if call.err != nil {
		t.Fatal(call.err)
	}
	_ = call.conn.Close()
}
func TestResidentTransientHandshakeLossReconnectsAfterOriginalCleanup(t *testing.T) {
	r := newResidentRig(t, nil)
	var attempts atomic.Int32
	real := r.m.handler
	r.m.handler = http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if attempts.Add(1) == 1 {
			ws, e := websocket.Accept(w, q, &websocket.AcceptOptions{Subprotocols: []string{ResidentSubprotocol}, CompressionMode: websocket.CompressionDisabled})
			if e == nil {
				_ = ws.CloseNow()
			}
			return
		}
		real.ServeHTTP(w, q)
	})
	done := residentRun(r.c)
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for !r.c.Connected() {
		select {
		case e := <-done:
			t.Fatalf("transient handshake loss terminated resident: %v", e)
		case <-timeout.C:
			t.Fatal("did not reconnect")
		case <-time.After(time.Millisecond):
		}
	}
	if attempts.Load() != 2 || r.rec.count.Load() != 0 {
		t.Fatal("reconnect did not use fresh identity-only handshake", attempts.Load())
	}
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if e := r.c.Close(ctx); e != nil {
		t.Fatal(e)
	}
	_ = await(t, done, 3*time.Second)
}

// Real Worker relay termination codes, not raw memory EOF. A new proof is
// required after both the planned300s close and relay-observed network loss.
func TestResidentRelayCloseCodesReauthenticateWithoutResumingWork(t *testing.T) {
	for _, code := range []websocket.StatusCode{4000, websocket.StatusInternalError} {
		t.Run(fmt.Sprint(int(code)), func(t *testing.T) {
			r := newResidentRig(t, nil)
			done := residentRun(r.c)
			residentReady(t, r)
			first := residentGeneration(r.h)
			r.h.mu.Lock()
			peer := r.h.residents[residentMapKey(hostLabel, member1)]
			r.h.mu.Unlock()
			_ = peer.ws.Close(code, "synthetic reviewed relay termination")
			timeout := time.NewTimer(5 * time.Second)
			defer timeout.Stop()
			for !(r.c.Connected() && r.h.ResidentConnected(hostLabel, member1) && residentGeneration(r.h) != first) {
				select {
				case e := <-done:
					t.Fatalf("relay close%d permanently ended resident: %v", code, e)
				case <-timeout.C:
					t.Fatal("relay reconnect missing")
				case <-time.After(time.Millisecond):
				}
			}
			if r.m.trips.Load() != 2 || r.rec.count.Load() != 0 {
				t.Fatal("relay reconnect resumed work", r.m.trips.Load())
			}
			ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			if e := r.c.Close(ctx); e != nil {
				t.Fatal(e)
			}
			_ = await(t, done, 3*time.Second)
		})
	}
}
