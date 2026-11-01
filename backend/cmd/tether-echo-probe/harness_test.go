package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/coder/websocket"
)

// Every key, assertion, issuer, AUD tag, subject, host and address here is a
// synthetic placeholder. RSA keys are generated in memory per test binary; no
// Access team, certs endpoint, account, file, socket or process is touched.

const (
	testIssuer   = "https://synthetic-team.example.invalid"
	testKid      = "synthetic-access"
	testResource = "https://probe.example.invalid" + HTTPPath
	subject1     = "synthetic-subject-1"
	subject2     = "synthetic-subject-2"
	outsider     = "synthetic-outsider"
)

var (
	testAUD  = strings.Repeat("5a", 32)
	keysOnce sync.Once
	testKeys [2]*rsa.PrivateKey // [0] configured, [1] never configured
)

func testKey(t *testing.T, i int) *rsa.PrivateKey {
	t.Helper()
	keysOnce.Do(func() {
		for j := range testKeys {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			testKeys[j] = k
		}
	})
	return testKeys[i]
}

var enc = base64.RawURLEncoding

// probeDoc is a complete synthetic cloudflare_access document for the probe.
func probeDoc(t *testing.T, edit func(map[string]any)) []byte {
	t.Helper()
	k := testKey(t, 0)
	doc := map[string]any{
		"listen": "127.0.0.1:8787", "auth_mode": "cloudflare_access", "resource": testResource,
		"access_issuer": testIssuer, "access_aud": testAUD, "leeway_seconds": 5,
		"access_keys": map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": testKid, "alg": "RS256", "use": "sig",
			"n": enc.EncodeToString(k.N.Bytes()), "e": enc.EncodeToString(big.NewInt(int64(k.E)).Bytes())}}},
		"principals": []any{
			map[string]any{"label": "member-1", "subject": subject1},
			map[string]any{"label": "member-2", "subject": subject2},
		},
	}
	if edit != nil {
		edit(doc)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func load(raw []byte, l limits) (*probe, error) {
	c, err := agentgateway.ParseConfig(raw)
	if err != nil {
		return nil, err
	}
	return newProbe(c, l)
}

func newTestProbe(t *testing.T, l limits) *probe {
	t.Helper()
	p, err := load(probeDoc(t, nil), l)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Close(ctx)
	})
	return p
}

func testLimits() limits {
	return limits{Body: 64, Message: 64, Frames: 8, HTTP: 2, Conns: 2, Lifetime: 5 * time.Second}
}

func claims(subject string) map[string]any {
	now := time.Now()
	return map[string]any{"iss": testIssuer, "aud": []string{testAUD}, "sub": subject, "email": "member@example.invalid",
		"type": "app", "iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
}

func signWith(t *testing.T, c map[string]any, key *rsa.PrivateKey) string {
	t.Helper()
	h, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": testKid, "typ": "JWT"})
	b, _ := json.Marshal(c)
	input := enc.EncodeToString(h) + "." + enc.EncodeToString(b)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + enc.EncodeToString(sig)
}

func assertion(t *testing.T, subject string) string {
	t.Helper()
	return signWith(t, claims(subject), testKey(t, 0))
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(end) {
			t.Fatalf("%s did not happen", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---- in-memory listener ----

// pipeListener is a net.Listener without a socket: each dial makes a net.Pipe
// and hands the server end to Accept.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.done:
	case <-ctx.Done():
	}
	_ = client.Close()
	_ = server.Close()
	return nil, net.ErrClosed
}

// client follows no redirect and keeps no idle connection.
func (l *pipeListener) client() *http.Client {
	return &http.Client{
		Transport:     &http.Transport{DialContext: l.dial, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "memory" }

// serveInMemory runs the production HTTP server for p on a pipeListener.
func serveInMemory(t *testing.T, p *probe) *pipeListener {
	t.Helper()
	return serveWith(t, p, nil)
}

// serveWith lets a test shorten the production server's own timeouts.
func serveWith(t *testing.T, p *probe, edit func(*http.Server)) *pipeListener {
	t.Helper()
	ln := newPipeListener()
	srv := newHTTPServer(p)
	if edit != nil {
		edit(srv)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() {
		if err := finish(srv, served, p.Close, 5*time.Second); err != nil {
			t.Errorf("finish: %v", err)
		}
	})
	return ln
}

func dialWS(t *testing.T, ln *pipeListener, token string, protocols ...string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	h := http.Header{}
	if token != "" {
		h.Set(agentgateway.HeaderAccessAssertion, token)
	}
	// The dial context outlives the handshake so that its cancellation can never
	// be mistaken for a server-side close.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return websocket.Dial(ctx, "ws://probe.example.invalid"+WSPath, &websocket.DialOptions{
		HTTPClient: ln.client(), HTTPHeader: h, Subprotocols: protocols, CompressionMode: websocket.CompressionDisabled,
	})
}

func slotsInUse(p *probe) int { return len(p.httpSlots) + len(p.wsSlots) }
