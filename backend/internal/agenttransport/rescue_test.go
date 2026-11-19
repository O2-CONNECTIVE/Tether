package agenttransport

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"golang.org/x/crypto/ssh"
)

// The rescue reception route end to end over memory: the real gateway behind
// RescueHandler, and RescueClient as the host (docs/rescue-connection-start.md,
// 受付の登録 and 受付 OFF のホストへの要求). Each test keeps a positive
// control, so an end that refuses everything fails it too.

// rescueEndpoint is RescueEndpoint of newRescueGateway (audience
// https://gateway.example.invalid/mcp).
const rescueEndpoint = "wss://gateway.example.invalid" + agentgateway.RescuePath

func rescueKey(seed byte) ssh.Signer {
	s, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize)))
	if err != nil {
		panic(err)
	}
	return s
}

var (
	rescueIdentity = rescueKey(0x49) // the gateway's identity key
	rescueNext     = rescueKey(0x4e) // the next identity key, bundled beside it
	rescueStranger = rescueKey(0x53) // an identity key the host never pinned
)

func newRescueGateway(t *testing.T, now *time.Time) *agentgateway.Gateway {
	t.Helper()
	pub := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	set, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{
		"kty": "OKP", "crv": "Ed25519", "kid": "synthetic-ed", "alg": "EdDSA", "use": "sig", "x": base64.RawURLEncoding.EncodeToString(pub),
	}}})
	v, err := agentgateway.NewVerifier(agentgateway.VerifierConfig{
		Issuer: "https://issuer.example.invalid/", Audience: "https://gateway.example.invalid/mcp", Scope: "rescue.agent",
		Types: []string{"at+jwt"}, JWKS: set,
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err := agentgateway.New(agentgateway.Config{
		Verifier: v, Budgets: chanBudgets(), Clock: func() time.Time { return *now }, RescueSigner: rescueIdentity,
		Principals: []agentgateway.Principal{{Label: member1, Subject: "synthetic-subject-1", Roles: []agentgateway.Role{agentgateway.RoleRescueOperator}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := g.RescueEndpoint(); got != rescueEndpoint {
		t.Fatalf("RescueEndpoint = %q", got)
	}
	return g
}

// rescueRecorder passes every call to the real gateway and records the
// receptions it opened and ended. tamper, if set, rewrites an opened result
// before the route sends it, as a relay or a wrong gateway would.
type rescueRecorder struct {
	RescueRegistry
	tamper func(agentgateway.RescueRequest, agentgateway.RescueReception) agentgateway.RescueReception
	opened chan agentgateway.RescueReception
	ended  chan []byte
}

func newRescueRecorder(reg RescueRegistry) *rescueRecorder {
	return &rescueRecorder{RescueRegistry: reg, opened: make(chan agentgateway.RescueReception, 8), ended: make(chan []byte, 8)}
}

func (r *rescueRecorder) OpenRescue(req agentgateway.RescueRequest) (agentgateway.RescueReception, error) {
	res, err := r.RescueRegistry.OpenRescue(req)
	if err == nil {
		r.opened <- res
		if r.tamper != nil {
			res = r.tamper(req, res)
		}
	}
	return res, err
}

func (r *rescueRecorder) EndRescue(id []byte) {
	r.RescueRegistry.EndRescue(id)
	r.ended <- append([]byte(nil), id...)
}

func (r *rescueRecorder) expectEnded(t *testing.T, id []byte) {
	t.Helper()
	select {
	case got := <-r.ended:
		if !bytes.Equal(got, id) {
			t.Fatalf("ended reception %x, want %x", got, id)
		}
	case <-time.After(2 * time.Second): // shorter than DefaultBudgets().Cleanup
		t.Fatal("the gateway did not end the reception")
	}
}

func newRescueHandler(t *testing.T, reg RescueRegistry) *RescueHandler {
	t.Helper()
	h, err := NewRescueHandler(reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.Close(ctx); err != nil {
			t.Errorf("RescueHandler.Close = %v", err)
		}
	})
	return h
}

// newRescueClient builds a host whose only round tripper is m, as newMemClient
// does for the registration client.
func newRescueClient(t *testing.T, pins []ssh.PublicKey, m *memTransport) *RescueClient {
	t.Helper()
	c, err := NewRescueClient(RescueClientConfig{Endpoint: rescueEndpoint, Pins: pins})
	if err != nil {
		t.Fatal(err)
	}
	c.transport = func(own acquirer) http.RoundTripper {
		m.own = own
		m.track = own.attach
		return m
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func openRescue(t *testing.T, c *RescueClient) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Open(ctx)
}

func expectDone(t *testing.T, c *RescueClient) {
	t.Helper()
	select {
	case <-c.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the reception did not end")
	}
}

// expectLive requires the reception to outlive Open's context: openRescue
// cancels it as Open returns, and only 受付 OFF, the expiry or the connection
// may end a reception.
func expectLive(t *testing.T, c *RescueClient, reg *rescueRecorder) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	select {
	case <-c.Done():
		t.Fatal("the reception ended with Open's context")
	case id := <-reg.ended:
		t.Fatalf("the gateway ended reception %x with Open's context", id)
	default:
	}
}

// expectDropped requires K's private bytes, captured before the end, to be
// cleared.
func expectDropped(t *testing.T, k ed25519.PrivateKey) {
	t.Helper()
	if len(k) != ed25519.PrivateKeySize {
		t.Fatalf("K was %d bytes", len(k))
	}
	if !bytes.Equal(k, make([]byte, len(k))) {
		t.Fatal("K was not dropped")
	}
}

func resign(t *testing.T, s ssh.Signer, endpoint string, hostNonce, key, id []byte, code string) []byte {
	t.Helper()
	sig, err := s.Sign(rand.Reader, agentgateway.RescueResultTranscript(endpoint, hostNonce, key, id, code))
	if err != nil {
		t.Fatal(err)
	}
	return sig.Blob
}

func flipped(b []byte) []byte {
	out := append([]byte(nil), b...)
	out[len(out)-1] ^= 1
	return out
}

// The host shows a code only when the result is signed by a pinned identity
// key over its own URL, its own nonce, its own K, and the id and code it got.
func TestRescueHostShowsOnlyAVerifiedCode(t *testing.T) {
	pinned := []ssh.PublicKey{rescueIdentity.PublicKey()}
	type tamper = func(*testing.T, agentgateway.RescueRequest, agentgateway.RescueReception) agentgateway.RescueReception
	for _, tc := range []struct {
		name   string
		pins   []ssh.PublicKey
		tamper tamper
		ok     bool
	}{
		{"the gateway's result", pinned, nil, true},
		{"a result by the next pinned key", []ssh.PublicKey{rescueIdentity.PublicKey(), rescueNext.PublicKey()}, func(t *testing.T, q agentgateway.RescueRequest, r agentgateway.RescueReception) agentgateway.RescueReception {
			r.Signature = resign(t, rescueNext, rescueEndpoint, q.HostNonce, q.Key, r.ID, r.Code)
			return r
		}, true},
		{"another URL", pinned, func(t *testing.T, q agentgateway.RescueRequest, r agentgateway.RescueReception) agentgateway.RescueReception {
			r.Signature = resign(t, rescueIdentity, "wss://other.example.invalid"+agentgateway.RescuePath, q.HostNonce, q.Key, r.ID, r.Code)
			return r
		}, false},
		{"another host nonce", pinned, func(t *testing.T, q agentgateway.RescueRequest, r agentgateway.RescueReception) agentgateway.RescueReception {
			r.Signature = resign(t, rescueIdentity, rescueEndpoint, flipped(q.HostNonce), q.Key, r.ID, r.Code)
			return r
		}, false},
		{"another K", pinned, func(t *testing.T, q agentgateway.RescueRequest, r agentgateway.RescueReception) agentgateway.RescueReception {
			r.Signature = resign(t, rescueIdentity, rescueEndpoint, q.HostNonce, rescueKey(0x4b).PublicKey().Marshal(), r.ID, r.Code)
			return r
		}, false},
		{"another reception id", pinned, func(t *testing.T, q agentgateway.RescueRequest, r agentgateway.RescueReception) agentgateway.RescueReception {
			r.ID = flipped(r.ID)
			return r
		}, false},
		{"another code", pinned, func(t *testing.T, q agentgateway.RescueRequest, r agentgateway.RescueReception) agentgateway.RescueReception {
			n, _ := strconv.ParseUint(r.Code, 10, 64)
			r.Code = strconv.FormatUint(1_000_000_000_000+(n+1)%1_000_000_000_000, 10)[1:]
			return r
		}, false},
		{"another signature", pinned, func(t *testing.T, q agentgateway.RescueRequest, r agentgateway.RescueReception) agentgateway.RescueReception {
			r.Signature = flipped(r.Signature)
			return r
		}, false},
		{"an unpinned identity key", pinned, func(t *testing.T, q agentgateway.RescueRequest, r agentgateway.RescueReception) agentgateway.RescueReception {
			r.Signature = resign(t, rescueStranger, rescueEndpoint, q.HostNonce, q.Key, r.ID, r.Code)
			return r
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			reg := newRescueRecorder(newRescueGateway(t, &now))
			if tc.tamper != nil {
				reg.tamper = func(q agentgateway.RescueRequest, r agentgateway.RescueReception) agentgateway.RescueReception { return tc.tamper(t, q, r) }
			}
			c := newRescueClient(t, tc.pins, newMemTransport(newRescueHandler(t, reg)))
			code, err := openRescue(t, c)
			if !tc.ok {
				if err == nil || code != "" {
					t.Fatalf("Open = %q, %v; want no code", code, err)
				}
				expectDone(t, c)
				return
			}
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			want := <-reg.opened
			if code != want.Code {
				t.Fatalf("code %q, the gateway issued %q", code, want.Code)
			}
			expectLive(t, c, reg)
		})
	}
}

func TestRescueClientPinsTheCurrentAndAtMostOneNextKey(t *testing.T) {
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecPub, err := ssh.NewPublicKey(&ec.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	cur, next := rescueIdentity.PublicKey(), rescueNext.PublicKey()
	for _, tc := range []struct {
		name     string
		endpoint string
		pins     []ssh.PublicKey
		ok       bool
	}{
		{"the current key", rescueEndpoint, []ssh.PublicKey{cur}, true},
		{"the current and the next key", rescueEndpoint, []ssh.PublicKey{cur, next}, true},
		{"no key", rescueEndpoint, nil, false},
		{"three keys", rescueEndpoint, []ssh.PublicKey{cur, next, rescueStranger.PublicKey()}, false},
		{"one key twice", rescueEndpoint, []ssh.PublicKey{cur, cur}, false},
		{"an ECDSA key", rescueEndpoint, []ssh.PublicKey{ecPub}, false},
		{"an https endpoint", "https://gateway.example.invalid" + agentgateway.RescuePath, []ssh.PublicKey{cur}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewRescueClient(RescueClientConfig{Endpoint: tc.endpoint, Pins: tc.pins})
			if tc.ok != (err == nil) {
				t.Fatalf("NewRescueClient = %v, %v", c, err)
			}
			if !tc.ok && !errors.Is(err, ErrConfig) {
				t.Fatalf("NewRescueClient = %v, want ErrConfig", err)
			}
			if tc.ok && len(c.key) != ed25519.PrivateKeySize {
				t.Fatal("受付 ON made no K")
			}
		})
	}
}

// 受付 OFF and a dropped reception connection end the reception on the
// gateway at once (agentgateway's test shows EndRescue invalidates the code),
// and the host drops K without reconnecting.
func TestRescueOffAndDisconnectEndTheReceptionAtOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stall bool // the gateway's own close of the connection does not finish
		end   func(*RescueClient, *memTransport)
	}{
		{"受付 OFF", false, func(c *RescueClient, _ *memTransport) { _ = c.Close() }},
		{"the connection drops", false, func(_ *RescueClient, m *memTransport) {
			ends := <-m.pairs
			_ = ends.server.Close()
		}},
		// The code goes before the transport's close is confirmed.
		{"受付 OFF while the gateway's close stalls", true, func(c *RescueClient, _ *memTransport) { _ = c.Close() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			reg := newRescueRecorder(newRescueGateway(t, &now))
			m := newMemTransport(newRescueHandler(t, reg))
			if tc.stall {
				gate := make(chan struct{})
				m.prepare = func(_, server *memConn) { server.setGate(gate) }
				t.Cleanup(func() { close(gate) }) // before the handler's Close
			}
			c := newRescueClient(t, []ssh.PublicKey{rescueIdentity.PublicKey()}, m)
			k := c.key
			if _, err := openRescue(t, c); err != nil {
				t.Fatalf("Open: %v", err)
			}
			opened := <-reg.opened
			expectLive(t, c, reg)
			tc.end(c, m)
			reg.expectEnded(t, opened.ID)
			expectDone(t, c)
			expectDropped(t, k)
			time.Sleep(200 * time.Millisecond)
			if n := m.trips.Load(); n != 1 {
				t.Fatalf("the host connected %d times", n)
			}
		})
	}
}

// When the code expires the host ends the reception, drops K and takes no new
// code: a new code needs the user to turn the reception on again.
func TestRescueHostEndsWhenTheCodeExpiresAndTakesNoNewCode(t *testing.T) {
	now := time.Now()
	reg := newRescueRecorder(newRescueGateway(t, &now))
	m := newMemTransport(newRescueHandler(t, reg))
	c := newRescueClient(t, []ssh.PublicKey{rescueIdentity.PublicKey()}, m)
	asked := make(chan time.Duration, 1)
	fire := make(chan time.Time, 1)
	c.after = func(d time.Duration) <-chan time.Time {
		asked <- d
		return fire
	}
	k := c.key
	if _, err := openRescue(t, c); err != nil {
		t.Fatalf("Open: %v", err)
	}
	opened := <-reg.opened
	select {
	case d := <-asked:
		if d != 5*time.Minute { // the design value, not RescueCodeLifetime
			t.Fatalf("the host waits %v for the code, want 5m", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the host never timed its code")
	}
	expectLive(t, c, reg)
	fire <- time.Now()
	reg.expectEnded(t, opened.ID)
	expectDone(t, c)
	expectDropped(t, k)
	time.Sleep(200 * time.Millisecond)
	if n := m.trips.Load(); n != 1 {
		t.Fatalf("the host connected %d times", n)
	}
	if code, err := openRescue(t, c); err == nil || code != "" {
		t.Fatalf("a second Open = %q, %v; want a refusal", code, err)
	}
}

func rescueUpgrade(edit func(*http.Request)) func(*http.Request) {
	return func(r *http.Request) {
		r.URL.Path = agentgateway.RescuePath
		r.Header.Set("Sec-WebSocket-Protocol", RescueSubprotocol)
		if edit != nil {
			edit(r)
		}
	}
}

func TestRescueRouteRefusesCredentialsBeforeUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*http.Request)
		want int
	}{
		{"a clean upgrade", nil, http.StatusSwitchingProtocols},
		{"a cookie", func(r *http.Request) { r.Header.Set("Cookie", "CF_Authorization=synthetic") }, http.StatusForbidden},
		{"authorization", func(r *http.Request) { r.Header.Set("Authorization", "Bearer synthetic") }, http.StatusForbidden},
		{"proxy authorization", func(r *http.Request) { r.Header.Set("Proxy-Authorization", "Basic synthetic") }, http.StatusForbidden},
		{"an Origin", func(r *http.Request) { r.Header.Set("Origin", "https://gateway.example.invalid") }, http.StatusForbidden},
		{"the registration subprotocol", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Protocol", Subprotocol) }, http.StatusBadRequest},
		{"a query", func(r *http.Request) { r.URL.RawQuery = "x=1" }, http.StatusBadRequest},
		{"another path", func(r *http.Request) { r.URL.Path = "/tether-agent/v1/register" }, http.StatusNotFound},
		{"POST", func(r *http.Request) { r.Method = http.MethodPost }, http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			h := newRescueHandler(t, newRescueGateway(t, &now))
			_, resp := upgrade(t, h, rescueUpgrade(tc.edit))
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.want)
			}
			if tc.want == http.StatusSwitchingProtocols && resp.Header.Get("Sec-WebSocket-Protocol") != RescueSubprotocol {
				t.Fatalf("subprotocol %q", resp.Header.Get("Sec-WebSocket-Protocol"))
			}
		})
	}
}

// The limits answer before the upgrade: 503 for a full gateway, 429 for the
// 31st new reception in a minute.
func TestRescueRouteAnswersBusyAndRateBeforeUpgrade(t *testing.T) {
	t.Run("100 valid codes", func(t *testing.T) {
		now := time.Now()
		g := newRescueGateway(t, &now)
		h := newRescueHandler(t, g)
		for i := range 100 {
			k := rescueKey(byte(i))
			q := agentgateway.RescueRequest{Key: k.PublicKey().Marshal(), HostNonce: bytes.Repeat([]byte{1}, 32), GatewayNonce: bytes.Repeat([]byte{2}, 32), Purpose: agentgateway.RescuePurposeNew}
			sig, err := k.Sign(rand.Reader, agentgateway.RescueTranscript(rescueEndpoint, q.Key, q.GatewayNonce, q.Purpose, nil))
			if err != nil {
				t.Fatal(err)
			}
			q.Proof = sig.Blob
			if _, err := g.OpenRescue(q); err != nil {
				t.Fatalf("OpenRescue #%d: %v", i, err)
			}
		}
		if _, resp := upgrade(t, h, rescueUpgrade(nil)); resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("status %d with 100 valid codes, want 503", resp.StatusCode)
		}
		now = now.Add(5 * time.Minute)
		if _, resp := upgrade(t, h, rescueUpgrade(nil)); resp.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("status %d once the codes expired, want 101", resp.StatusCode)
		}
	})
	t.Run("30 new receptions in a minute", func(t *testing.T) {
		now := time.Now()
		h := newRescueHandler(t, newRescueGateway(t, &now))
		for i := range 30 {
			if _, resp := upgrade(t, h, rescueUpgrade(nil)); resp.StatusCode != http.StatusSwitchingProtocols {
				t.Fatalf("upgrade #%d = %d", i+1, resp.StatusCode)
			}
		}
		if _, resp := upgrade(t, h, rescueUpgrade(nil)); resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("upgrade #31 = %d, want 429", resp.StatusCode)
		}
	})
}

// K is made in memory and never written: no file of the rescue reception
// imports a way to the file system or to the protected key stores, or
// marshals a private key. This inspects the rescue_*.go files only; K is kept
// in RescueClient and passed nowhere else.
func TestRescueHostKeyNeverReachesTheDisk(t *testing.T) {
	forbidden := map[string]bool{
		"os": true, "os/exec": true, "io/fs": true, "io/ioutil": true, "path/filepath": true, "syscall": true, "plugin": true,
		"encoding/pem": true, "unsafe": true, "golang.org/x/sys/unix": true, "golang.org/x/sys/windows": true,
		"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys":    true,
		"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey": true,
	}
	files, err := filepath.Glob("rescue_*.go")
	if err != nil {
		t.Fatal(err)
	}
	generates := false
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); forbidden[path] {
				t.Errorf("%s imports %s", name, path)
			}
		}
		if bytes.Contains(src, []byte("MarshalPrivateKey")) {
			t.Errorf("%s marshals a private key", name)
		}
		generates = generates || bytes.Contains(src, []byte("ed25519.GenerateKey("))
	}
	if !generates {
		t.Fatal("no rescue_*.go file makes K, so this inspection covers nothing")
	}
}

// closingPin turns the reception OFF while the host checks the result's
// signature: the window between the last read and Open's return.
type closingPin struct {
	ssh.PublicKey
	c **RescueClient
}

func (p closingPin) CryptoPublicKey() crypto.PublicKey {
	_ = (*p.c).Close()
	return p.PublicKey.(ssh.CryptoPublicKey).CryptoPublicKey()
}

// 受付 OFF wins over a code still being verified: the user turned the
// reception off, so the host must not show a code for it.
func TestRescueOffDuringOpenShowsNoCode(t *testing.T) {
	now := time.Now()
	reg := newRescueRecorder(newRescueGateway(t, &now))
	var c *RescueClient
	c = newRescueClient(t, []ssh.PublicKey{closingPin{PublicKey: rescueIdentity.PublicKey(), c: &c}}, newMemTransport(newRescueHandler(t, reg)))
	code, err := openRescue(t, c)
	if code != "" || !errors.Is(err, ErrClosed) {
		t.Fatalf("Open = %q, %v; want no code and ErrClosed", code, err)
	}
	opened := <-reg.opened
	reg.expectEnded(t, opened.ID)
	expectDone(t, c)
}

// rescueProtocol rewrites the upgrade response's subprotocol, as a wrong
// server behind the endpoint would answer.
type rescueProtocol struct {
	*memTransport
	set func(http.Header)
}

func (p rescueProtocol) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := p.memTransport.RoundTrip(r)
	if resp != nil {
		p.set(resp.Header)
	}
	return resp, err
}

// The host speaks only rescue-agent-reception.v1, exactly, as the Mac
// registration client checks its own subprotocol.
func TestRescueHostRequiresTheExactSubprotocol(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(http.Header)
		ok   bool
	}{
		{"the reception subprotocol", func(http.Header) {}, true},
		{"no subprotocol", func(h http.Header) { h.Del("Sec-WebSocket-Protocol") }, false},
		{"another letter case", func(h http.Header) { h.Set("Sec-WebSocket-Protocol", strings.ToUpper(RescueSubprotocol)) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			reg := newRescueRecorder(newRescueGateway(t, &now))
			m := newMemTransport(newRescueHandler(t, reg))
			c := newRescueClient(t, []ssh.PublicKey{rescueIdentity.PublicKey()}, m)
			c.transport = func(own acquirer) http.RoundTripper {
				m.own = own
				m.track = own.attach
				return rescueProtocol{m, tc.set}
			}
			code, err := openRescue(t, c)
			if tc.ok != (err == nil && code != "") {
				t.Fatalf("Open = %q, %v", code, err)
			}
			if !tc.ok {
				expectDone(t, c)
			}
		})
	}
}

// A used host refuses another Open with ErrUsed, or ErrClosed once 受付 OFF
// came first, without racing Close (run with -race).
func TestRescueClientReopenAndCloseDoNotRace(t *testing.T) {
	for range 50 {
		c, err := NewRescueClient(RescueClientConfig{Endpoint: rescueEndpoint, Pins: []ssh.PublicKey{rescueIdentity.PublicKey()}})
		if err != nil {
			t.Fatal(err)
		}
		c.used = true
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Close()
		}()
		_, err = c.Open(context.Background())
		wg.Wait()
		if !errors.Is(err, ErrUsed) && !errors.Is(err, ErrClosed) {
			t.Fatalf("Open = %v, want ErrUsed or ErrClosed", err)
		}
	}
}

// While a dropped connection's close is still unconfirmed, Close does not
// report a clean end, as the registration Handler keeps its transports until
// their close is confirmed. Once the close finishes, Close succeeds.
func TestRescueCloseWaitsForAConnectionStillClosing(t *testing.T) {
	now := time.Now()
	reg := newRescueRecorder(newRescueGateway(t, &now))
	h := newRescueHandler(t, reg)
	m := newMemTransport(h)
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	m.prepare = func(_, server *memConn) { server.setGate(gate) }
	t.Cleanup(release) // before the handler's Close
	c := newRescueClient(t, []ssh.PublicKey{rescueIdentity.PublicKey()}, m)
	if _, err := openRescue(t, c); err != nil {
		t.Fatalf("Open: %v", err)
	}
	opened := <-reg.opened
	_ = c.Close()
	reg.expectEnded(t, opened.ID)
	short, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := h.Close(short); err == nil {
		t.Fatal("Close reported a clean end while the connection's close was unconfirmed")
	}
	release()
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := h.Close(ctx); err != nil {
		t.Fatalf("Close after the connection closed = %v", err)
	}
}
