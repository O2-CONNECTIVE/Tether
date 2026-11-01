package agenttransport

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

// Composed path under test (memory only, nothing listens or dials):
// Client.Run -> real coder/websocket Dial through memTransport (real HTTP/1.1
// upgrade bytes) -> real Handler -> registration proof -> the real
// agentgateway.Gateway.Connect -> agentchannel.Open (encrypted SSH over the
// WebSocket stream) -> on the Mac, owner.Serve -> real agentlink.Host -> the
// granted agentexec.Session on a fake Mac runtime. The approval itself is the
// linkOwner fixture standing in for maclaunch's /dev/tty confirmation; no
// real OAuth token, tunnel, TLS, key or account is involved.

type composed struct {
	host1, host2, m1, m2 ssh.Signer
	gateway              *agentgateway.Gateway
	rec                  *recordingRegistrar
	h                    *Handler
	owner                *linkOwner
	m                    *memTransport
	c                    *Client
}

func newComposed(t *testing.T, edit func(*ClientConfig)) *composed {
	t.Helper()
	p := &composed{host1: fixtureSigner(t, 0x11), host2: fixtureSigner(t, 0x12), m1: fixtureSigner(t, 0x21), m2: fixtureSigner(t, 0x22)}
	p.gateway = newGateway(t, map[string]ssh.Signer{member1: p.m1, member2: p.m2})
	p.rec = newRecorder(p.gateway)
	p.h = newHandler(t, p.rec, testBudgets(),
		EnrolledHost{Label: hostLabel, PublicKey: p.host1.PublicKey(), Principals: []string{member1}},
		EnrolledHost{Label: otherHost, PublicKey: p.host2.PublicKey(), Principals: []string{member2}})
	p.owner = newLinkOwner(t, p.host1, 30*time.Second, p.m1.PublicKey(), p.m2.PublicKey())
	p.m = newMemTransport(p.h)
	cfg := clientConfig(p.host1)
	if edit != nil {
		edit(&cfg)
	}
	p.c = newMemClient(t, cfg, p.owner, p.m)
	return p
}

func (p *composed) ownerDispatch() agentgateway.Dispatch {
	d := p.owner.Dispatch()
	return agentgateway.Dispatch{Binding: d.Binding, Deadline: d.Deadline, HostKey: d.HostKey}
}

// connected runs the client and requires the real gateway to have opened its
// authenticated agent channel for exactly the approved binding.
func (p *composed) connected(t *testing.T) (<-chan error, memPairEnds) {
	t.Helper()
	done := runAsync(context.Background(), p.c)
	call := p.rec.next(t)
	if call.err != nil {
		t.Fatalf("gateway Connect = %v", call.err)
	}
	if call.label != member1 || !sameDispatch(call.d, p.ownerDispatch()) {
		t.Fatalf("gateway got %q %+v, want the approved dispatch", call.label, call.d)
	}
	if !p.owner.live() || p.owner.serves.Load() != 1 {
		t.Fatal("approved owner is not serving the registered stream")
	}
	return done, <-p.m.pairs
}

func (p *composed) ended(t *testing.T, done <-chan error, ends memPairEnds) {
	t.Helper()
	err := await(t, done, 10*time.Second)
	if err == nil || errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("Run = %v", err)
	}
	if p.owner.live() || !ends.client.isClosed() {
		t.Fatal("the grant or the Mac transport outlived the stream")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !ends.server.isClosed() {
		if time.Now().After(deadline) {
			t.Fatal("the gateway kept the ended stream")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := p.c.Run(context.Background()); !errors.Is(err, ErrUsed) || p.owner.serves.Load() != 1 {
		t.Fatalf("second Run = %v", err)
	}
}

// Valid host and principal reach the same approved binding; losing the
// transport (a half-open tunnel) revokes the grant, closes both ends, and the
// same dispatch cannot be registered again.
func TestRegisteredHostReachesTheApprovedBindingAndLossDoesNotResurrect(t *testing.T) {
	p := newComposed(t, nil)
	done, ends := p.connected(t)
	ends.client.link.setBlackhole()
	p.ended(t, done, ends)

	// A fresh, correctly signed proof of the same dispatch (the strongest
	// replay) reaches the gateway, which refuses the consumed LaunchID.
	r := upgraded(t, p.h)
	r.text(signedRegister(t, testEndpoint, hostLabel, member1, p.ownerDispatch(), r.challenge(), p.host1))
	r.expectAccepted()
	if call := p.rec.next(t); !errors.Is(call.err, agentgateway.ErrRefused) {
		t.Fatalf("re-registration = %v", call.err)
	}
	r.expectRefused(3 * time.Second)
}

// Revoking the approved grant on the Mac ends the stream at both ends.
func TestRevokedGrantEndsTheStream(t *testing.T) {
	p := newComposed(t, nil)
	done, ends := p.connected(t)
	if err := p.owner.issuer.Revoke(p.owner.d.Binding.Grant); err != nil {
		t.Fatal(err)
	}
	p.ended(t, done, ends)
}

// Closing the gateway ends the route; the Mac owner is closed and revoked.
func TestGatewayCloseEndsTheMacOwner(t *testing.T) {
	p := newComposed(t, nil)
	done, ends := p.connected(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.gateway.Close(ctx); err != nil {
		t.Fatal(err)
	}
	p.ended(t, done, ends)
}

// A Mac whose fixed mapping or endpoint does not match the gateway's
// configuration is refused before any channel; the owner is closed.
func TestMismatchedMacConfigurationIsRefusedAndClosesTheOwner(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ClientConfig)
	}{
		{"host label of another enrolled key", func(c *ClientConfig) { c.HostLabel = otherHost }},
		{"principal not associated", func(c *ClientConfig) { c.PrincipalLabel = member2 }},
		{"other resource identity", func(c *ClientConfig) { c.Endpoint = "wss://other.example.invalid/agent/register" }},
		{"unknown path", func(c *ClientConfig) { c.Endpoint = "wss://gateway.example.invalid/agent/other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newComposed(t, tc.edit)
			err := await(t, runAsync(context.Background(), p.c), 5*time.Second)
			if !errors.Is(err, ErrRefused) || errors.Is(err, ErrCleanupUnknown) {
				t.Fatalf("Run = %v", err)
			}
			if p.owner.live() || p.owner.serves.Load() != 0 || p.rec.count.Load() != 0 {
				t.Fatal("refused registration left the grant live, served or reached the gateway")
			}
			if ends := <-p.m.pairs; !ends.client.isClosed() {
				t.Fatal("Mac transport not closed")
			}
		})
	}
}

// Fake gateways (real coder/websocket Accept) that misbehave: the client
// never serves, closes the owner and ends within its budgets.
func TestMisbehavingGatewayIsRefused(t *testing.T) {
	accept := func(w http.ResponseWriter, r *http.Request, protos ...string) *websocket.Conn {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: protos})
		if err != nil {
			return nil
		}
		return ws
	}
	hold := func(ws *websocket.Conn) { _, _, _ = ws.Read(context.Background()) }
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		trips   int32
	}{
		{"never challenges", func(w http.ResponseWriter, r *http.Request) {
			if ws := accept(w, r, Subprotocol); ws != nil {
				hold(ws)
			}
		}, 1},
		{"binary challenge", func(w http.ResponseWriter, r *http.Request) {
			if ws := accept(w, r, Subprotocol); ws != nil {
				_ = ws.Write(context.Background(), websocket.MessageBinary, encodeChallenge(make([]byte, NonceSize)))
				hold(ws)
			}
		}, 1},
		{"oversized challenge", func(w http.ResponseWriter, r *http.Request) {
			if ws := accept(w, r, Subprotocol); ws != nil {
				// Otherwise valid and followed by accepted: only the bound refuses.
				_ = ws.Write(context.Background(), websocket.MessageText, padded(encodeChallenge(make([]byte, NonceSize))))
				hold(ws)
				_ = ws.Write(context.Background(), websocket.MessageText, encodeAccepted())
				hold(ws)
			}
		}, 1},
		{"never accepts", func(w http.ResponseWriter, r *http.Request) {
			if ws := accept(w, r, Subprotocol); ws != nil {
				_ = ws.Write(context.Background(), websocket.MessageText, encodeChallenge(make([]byte, NonceSize)))
				hold(ws)
				hold(ws)
			}
		}, 1},
		{"malformed accepted", func(w http.ResponseWriter, r *http.Request) {
			if ws := accept(w, r, Subprotocol); ws != nil {
				_ = ws.Write(context.Background(), websocket.MessageText, encodeChallenge(make([]byte, NonceSize)))
				hold(ws)
				_ = ws.Write(context.Background(), websocket.MessageText, []byte(`{"type":"accepted","host":"mac-1"}`))
				hold(ws)
			}
		}, 1},
		{"no subprotocol", func(w http.ResponseWriter, r *http.Request) {
			if ws := accept(w, r); ws != nil {
				_ = ws.Write(context.Background(), websocket.MessageText, encodeChallenge(make([]byte, NonceSize)))
				hold(ws)
			}
		}, 1},
		{"redirect elsewhere", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://elsewhere.example.invalid/agent/register", http.StatusFound)
		}, 1},
		{"plain response", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("hello")) }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host, member := fixtureSigner(t, 0x11), fixtureSigner(t, 0x21)
			owner := newLinkOwner(t, host, 30*time.Second, member.PublicKey())
			m := newMemTransport(tc.handler)
			c := newMemClient(t, clientConfig(host), owner, m)
			start := time.Now()
			err := await(t, runAsync(context.Background(), c), 5*time.Second)
			if !errors.Is(err, ErrRefused) || errors.Is(err, ErrCleanupUnknown) {
				t.Fatalf("Run = %v", err)
			}
			if elapsed := time.Since(start); elapsed > testBudgets().Registration+testBudgets().Cleanup+time.Second {
				t.Fatalf("Run took %v", elapsed)
			}
			if owner.live() || owner.serves.Load() != 0 || m.trips.Load() != tc.trips {
				t.Fatalf("live %v serves %d trips %d", owner.live(), owner.serves.Load(), m.trips.Load())
			}
			ends := <-m.pairs
			if !ends.client.isClosed() {
				t.Fatal("Mac transport not closed")
			}
		})
	}
}

// An owner whose cleanup is unconfirmed is reported and retried as the same
// owner; a confirmed owner is not closed again.
func TestOwnerCleanupIsRetriedOnTheSameOwner(t *testing.T) {
	host := fixtureSigner(t, 0x11)
	owner := newStubOwner(host, time.Now().Add(time.Minute), agentchannel.ErrCleanupUnknown)
	m := newMemTransport(http.NotFoundHandler())
	c := newMemClient(t, clientConfig(host), owner, m)
	err := await(t, runAsync(context.Background(), c), 5*time.Second)
	if !errors.Is(err, ErrRefused) || !errors.Is(err, ErrCleanupUnknown) || owner.closeCount() != 1 || owner.serves.Load() != 0 {
		t.Fatalf("Run = %v, closes %d", err, owner.closeCount())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Close(ctx); err != nil || owner.closeCount() != 2 {
		t.Fatalf("retry = %v, closes %d", err, owner.closeCount())
	}
	if err := c.Close(ctx); err != nil || owner.closeCount() != 2 {
		t.Fatalf("confirmed owner closed again: %v, closes %d", err, owner.closeCount())
	}
}

// A transport close that does not return in the cleanup budget is reported
// unknown, kept, and confirmed later from the same attempt: the raw conn is
// closed once and never replaced.
func TestTransportCleanupIsRetriedOnTheSameConn(t *testing.T) {
	host := fixtureSigner(t, 0x11)
	owner := newStubOwner(host, time.Now().Add(time.Minute))
	gate := make(chan struct{})
	stall := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}}); err == nil {
			_, _, _ = ws.Read(context.Background())
		}
	})
	m := newMemTransport(stall)
	m.prepare = func(client, _ *memConn) { client.setGate(gate) }
	cfg := clientConfig(host)
	cfg.Budgets.Cleanup = 200 * time.Millisecond
	c := newMemClient(t, cfg, owner, m)
	err := await(t, runAsync(context.Background(), c), 5*time.Second)
	if !errors.Is(err, ErrRefused) || !errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("Run = %v", err)
	}
	ends := <-m.pairs
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.Close(short); !errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("Close before the transport closed = %v", err)
	}
	close(gate)
	ctx, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if err := c.Close(ctx); err != nil {
		t.Fatalf("Close after release = %v", err)
	}
	if n := ends.client.closeCount(); n != 1 {
		t.Fatalf("raw conn closed %d times", n)
	}
}

// Close before Run closes the owner and Run then refuses without dialing.
func TestCloseBeforeRunClosesTheOwner(t *testing.T) {
	host := fixtureSigner(t, 0x11)
	owner := newStubOwner(host, time.Now().Add(time.Minute))
	m := newMemTransport(http.NotFoundHandler())
	c := newMemClient(t, clientConfig(host), owner, m)
	if err := c.Close(context.Background()); err != nil || owner.closeCount() != 1 {
		t.Fatalf("Close = %v", err)
	}
	if err := c.Run(context.Background()); !errors.Is(err, ErrUsed) || m.trips.Load() != 0 || owner.serves.Load() != 0 {
		t.Fatalf("Run after Close = %v", err)
	}
}

// Canceling during registration ends the attempt promptly and closes the owner.
func TestCancelDuringRegistrationClosesTheOwner(t *testing.T) {
	host, member := fixtureSigner(t, 0x11), fixtureSigner(t, 0x21)
	owner := newLinkOwner(t, host, 30*time.Second, member.PublicKey())
	stall := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}}); err == nil {
			_, _, _ = ws.Read(context.Background())
		}
	})
	m := newMemTransport(stall)
	cfg := clientConfig(host)
	cfg.Budgets = Budgets{Registration: 10 * time.Second, Approval: 20 * time.Second, Cleanup: time.Second}
	c := newMemClient(t, cfg, owner, m)
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, c)
	ends := <-m.pairs
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	cancel()
	if err := await(t, done, 3*time.Second); err == nil || errors.Is(err, ErrCleanupUnknown) {
		t.Fatalf("Run = %v", err)
	}
	if time.Since(start) > 2*time.Second || owner.live() || owner.serves.Load() != 0 || !ends.client.isClosed() {
		t.Fatal("cancel did not end the attempt, revoke the owner or close the transport")
	}
}

func TestNewClientRefusesUntrustedConfiguration(t *testing.T) {
	host, other := fixtureSigner(t, 0x11), fixtureSigner(t, 0x12)
	for _, tc := range []struct {
		name  string
		edit  func(*ClientConfig)
		owner Owner
	}{
		{"plaintext ws", func(c *ClientConfig) { c.Endpoint = "ws://gateway.example.invalid/agent/register" }, nil},
		{"https scheme", func(c *ClientConfig) { c.Endpoint = "https://gateway.example.invalid/agent/register" }, nil},
		{"query", func(c *ClientConfig) { c.Endpoint = testEndpoint + "?host=mac-1" }, nil},
		{"userinfo", func(c *ClientConfig) { c.Endpoint = "wss://user:pw@gateway.example.invalid/agent/register" }, nil},
		{"fragment", func(c *ClientConfig) { c.Endpoint = testEndpoint + "#x" }, nil},
		{"upper-case host", func(c *ClientConfig) { c.Endpoint = "wss://Gateway.example.invalid/agent/register" }, nil},
		{"no path", func(c *ClientConfig) { c.Endpoint = "wss://gateway.example.invalid" }, nil},
		{"port zero", func(c *ClientConfig) { c.Endpoint = "wss://gateway.example.invalid:0/agent/register" }, nil},
		{"bad host label", func(c *ClientConfig) { c.HostLabel = "Mac" }, nil},
		{"bad principal label", func(c *ClientConfig) { c.PrincipalLabel = "" }, nil},
		{"other host signer", func(c *ClientConfig) { c.HostKey = other }, nil},
		{"no signer", func(c *ClientConfig) { c.HostKey = nil }, nil},
		{"zero budgets", func(c *ClientConfig) { c.Budgets = Budgets{} }, nil},
		{"expired dispatch", nil, newStubOwner(host, time.Now().Add(-time.Second))},
		{"dispatch beyond 24 hours", nil, newStubOwner(host, time.Now().Add(25*time.Hour))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := clientConfig(host)
			if tc.edit != nil {
				tc.edit(&cfg)
			}
			owner := tc.owner
			if owner == nil {
				owner = newStubOwner(host, time.Now().Add(time.Minute))
			}
			if _, err := NewClient(cfg, owner); !errors.Is(err, ErrConfig) {
				t.Fatalf("NewClient = %v", err)
			}
			if s := owner.(*stubOwner); s.closeCount() != 0 || s.serves.Load() != 0 {
				t.Fatal("a refused configuration touched the owner")
			}
		})
	}
	if _, err := NewClient(clientConfig(host), nil); !errors.Is(err, ErrConfig) {
		t.Fatalf("nil owner = %v", err)
	}
}

// The native transport is inspected, not executed: no proxy, normal TLS
// verification, HTTP/1.1 only, no reuse or compression.
func TestNativeTransportPolicy(t *testing.T) {
	tr, ok := nativeTransport(nil).(*http.Transport)
	if !ok {
		t.Fatal("native transport is not *http.Transport")
	}
	if tr.Proxy != nil || tr.TLSClientConfig == nil || tr.TLSClientConfig.InsecureSkipVerify || tr.TLSClientConfig.MinVersion < tls.VersionTLS12 ||
		tr.TLSClientConfig.VerifyPeerCertificate != nil || tr.TLSClientConfig.RootCAs != nil || tr.ForceAttemptHTTP2 ||
		tr.TLSNextProto == nil || len(tr.TLSNextProto) != 0 || !tr.DisableKeepAlives || !tr.DisableCompression || tr.DialTLSContext != nil {
		t.Fatalf("native transport policy: %+v", tr)
	}
}
