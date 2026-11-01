package agentgateway_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentlink"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"golang.org/x/crypto/ssh"
)

// The path under test: an in-memory HTTP request (httptest.NewRecorder and a
// direct ServeHTTP call) -> bearer verification -> principal -> session ->
// mcpadapter.HTTPSession -> agentwire.Client -> agentchannel client opened by
// Gateway.Connect -> encrypted SSH agent channel over a buffered memory conn
// -> agentlink.Host -> the granted agentexec.Session -> a fake Mac runtime.
// Every key and token is a synthetic fixture. Nothing listens, dials, starts
// a process or contacts a provider.

const (
	testIssuer   = "https://issuer.example.invalid/"
	testResource = "https://gateway.example.invalid/mcp"
	testScope    = "rescue.agent"
	edKid        = "synthetic-ed"
	rsaKid       = "synthetic-rsa"
)

var subjects = []string{"synthetic-subject-1", "synthetic-subject-2", "synthetic-subject-3", "synthetic-subject-4"}

func label(i int) string { return "member-" + string(rune('1'+i)) }

func seedKey(seed byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
}

func fixtureSigner(t *testing.T, seed byte) ssh.Signer {
	t.Helper()
	s, err := ssh.NewSignerFromKey(seedKey(seed))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var (
	issuerKey = seedKey(0x51) // the synthetic provider signing key
	otherKey  = seedKey(0x52) // an unconfigured key with the same kid
	rsaOnce   sync.Once
	rsaFixed  *rsa.PrivateKey
)

func rsaKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	rsaOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		rsaFixed = k
	})
	return rsaFixed
}

var enc = base64.RawURLEncoding

func jwks(t *testing.T) []byte {
	t.Helper()
	pub := issuerKey.Public().(ed25519.PublicKey)
	r := rsaKey(t)
	b, _ := json.Marshal(map[string]any{"keys": []any{
		map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": edKid, "alg": "EdDSA", "use": "sig", "x": enc.EncodeToString(pub)},
		map[string]any{"kty": "RSA", "kid": rsaKid, "alg": "RS256", "use": "sig", "n": enc.EncodeToString(r.N.Bytes()), "e": enc.EncodeToString(big.NewInt(int64(r.E)).Bytes())},
	}})
	return b
}

func claims(subject string) map[string]any {
	now := time.Now()
	return map[string]any{"iss": testIssuer, "aud": testResource, "sub": subject, "scope": testScope,
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
}

func edHeader() map[string]any { return map[string]any{"alg": "EdDSA", "kid": edKid, "typ": "at+jwt"} }

// sign produces a compact JWS with the synthetic key matching header alg.
func sign(t *testing.T, header, body map[string]any, ed ed25519.PrivateKey) string {
	t.Helper()
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(body)
	input := enc.EncodeToString(h) + "." + enc.EncodeToString(c)
	var sig []byte
	if header["alg"] == "RS256" {
		digest := sha256.Sum256([]byte(input))
		var err error
		if sig, err = rsa.SignPKCS1v15(rand.Reader, rsaKey(t), crypto.SHA256, digest[:]); err != nil {
			t.Fatal(err)
		}
	} else {
		sig = ed25519.Sign(ed, []byte(input))
	}
	return input + "." + enc.EncodeToString(sig)
}

func token(t *testing.T, subject string) string {
	return sign(t, edHeader(), claims(subject), issuerKey)
}

func verifier(t *testing.T) *agentgateway.Verifier {
	t.Helper()
	v, err := agentgateway.NewVerifier(agentgateway.VerifierConfig{
		Issuer: testIssuer, Audience: testResource, Scope: testScope, Types: []string{"at+jwt"}, JWKS: jwks(t), Leeway: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func budgets() agentchannel.Budgets {
	return agentchannel.Budgets{Handshake: 5 * time.Second, HeartbeatInterval: time.Second, HeartbeatTimeout: 5 * time.Second,
		ReadStall: 7 * time.Second, WriteStall: 5 * time.Second, Cleanup: 2 * time.Second}
}

// rig is one gateway with the four synthetic principals. In Access mode the
// credential is sent as the Access assertion header instead of a bearer token.
type rig struct {
	t      *testing.T
	gw     *agentgateway.Gateway
	access bool
}

func newRig(t *testing.T, callTimeout time.Duration) *rig {
	t.Helper()
	return newModeRig(t, agentgateway.Config{Verifier: verifier(t), CallTimeout: callTimeout})
}

// newModeRig completes cfg (one verifier mode) with the four principals.
func newModeRig(t *testing.T, cfg agentgateway.Config) *rig {
	t.Helper()
	var ps []agentgateway.Principal
	for i, s := range subjects {
		ps = append(ps, agentgateway.Principal{Label: label(i), Subject: s, Signer: fixtureSigner(t, byte(0x21+i))})
	}
	cfg.Principals, cfg.Budgets, cfg.AllowedOrigins = ps, budgets(), []string{"https://client.example.invalid"}
	gw, err := agentgateway.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := gw.Close(ctx); err != nil {
			t.Errorf("gateway Close = %v", err)
		}
	})
	return &rig{t: t, gw: gw, access: cfg.Access != nil}
}

// mac is one recipient-approved grant served by agentlink.Host. The grant
// issuance stands in for the recipient consent owner; it is a fixture.
type mac struct {
	issuer    *ephemeral.Issuer
	authority *supervisor.Authority
	session   *agentexec.Session
	runtime   *macRuntime
	hostConn  *memConn
	dispatch  agentgateway.Dispatch
	hostDone  chan error
	hostOnce  sync.Once
	hostErr   error
}

// waitHost returns the host Serve result; the host always ends by revoking
// the grant and joining its own cleanup.
func (m *mac) waitHost(t *testing.T) error {
	t.Helper()
	m.hostOnce.Do(func() {
		select {
		case m.hostErr = <-m.hostDone:
		case <-time.After(10 * time.Second):
			t.Error("host Serve did not return")
			m.hostErr = io.ErrNoProgress
		}
	})
	return m.hostErr
}

func (r *rig) approve(i int, lifetime time.Duration) *mac {
	t := r.t
	t.Helper()
	client := fixtureSigner(t, byte(0x21+i))
	host := fixtureSigner(t, byte(0x61+i))
	keys := []ssh.PublicKey{client.PublicKey()}
	digest, err := runbackend.KeySetDigest(keys)
	if err != nil {
		t.Fatal(err)
	}
	m := &mac{runtime: &macRuntime{procs: make(chan *fakeProc, 4)}, hostDone: make(chan error, 1)}
	if m.issuer, err = ephemeral.NewCurrentUser(); err != nil {
		t.Fatal(err)
	}
	challenge, err := m.issuer.Prepare(ephemeral.ApprovedPolicy{
		PolicyDigest: agentexec.MacCurrentUserPolicyDigest(), KeySetDigest: digest, GuestBoundary: supervisor.CurrentUserNoIsolation,
		HardDeadline: time.Now().Add(lifetime), BackendLeaseCeiling: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.authority, err = m.issuer.Launch(context.Background(), challenge, time.Now, func(context.Context, *supervisor.Authority) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if m.session, err = agentexec.New(agentexec.Config{Issuer: m.issuer, Authority: m.authority, Runtime: m.runtime, Clock: time.Now, CleanupTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	h, err := agentlink.NewHost(agentlink.HostConfig{Session: m.session, AllowedKeys: keys, HostKey: host, Budgets: budgets()})
	if err != nil {
		t.Fatal(err)
	}
	g := m.authority.Grant()
	m.dispatch = agentgateway.Dispatch{
		Binding:  agentchannel.Binding{Grant: g.Binding, KeySetDigest: g.KeySetDigest, Profile: agentexec.ProfileMacCurrentUser},
		Deadline: g.HardDeadline, HostKey: host.PublicKey().Marshal(),
	}
	sConn, cConn := memPair()
	m.hostConn = sConn
	go func() { m.hostDone <- h.Serve(context.Background(), sConn) }()
	t.Cleanup(func() {
		_ = sConn.Close()
		_ = m.waitHost(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = m.session.Close(ctx)
		_ = m.issuer.Close()
	})
	if err := r.gw.Connect(label(i), m.dispatch, cConn); err != nil {
		t.Fatalf("Connect(%s) = %v", label(i), err)
	}
	return m
}

func (m *mac) proc(t *testing.T) *fakeProc {
	t.Helper()
	select {
	case p := <-m.runtime.procs:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("the fake shell was not started")
		return nil
	}
}

// --- HTTP client side, in memory only ---

type reply struct {
	status  int
	header  http.Header
	body    []byte
	message map[string]any
}

func (r *rig) do(method, bearer, session string, body string, mutate func(*http.Request)) reply {
	req := httptest.NewRequest(method, testResource, strings.NewReader(body))
	switch {
	case bearer == "":
	case r.access:
		req.Header.Set(agentgateway.HeaderAccessAssertion, bearer)
	default:
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if session != "" {
		req.Header.Set(agentgateway.HeaderSession, session)
		req.Header.Set(agentgateway.HeaderProtocol, mcpadapter.ProtocolVersion)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	r.gw.ServeHTTP(rec, req)
	out := reply{status: rec.Code, header: rec.Header(), body: rec.Body.Bytes()}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(out.body, &out.message)
	}
	return out
}

func rpc(id any, method string, params any) string {
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		m["id"] = id
	}
	if params != nil {
		m["params"] = params
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func initParams() map[string]any {
	return map[string]any{"protocolVersion": mcpadapter.ProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}}
}

// open initializes one MCP session and completes the lifecycle.
func (r *rig) open(bearer string) string {
	r.t.Helper()
	res := r.do(http.MethodPost, bearer, "", rpc(0, "initialize", initParams()), nil)
	sid := res.header.Get(agentgateway.HeaderSession)
	if res.status != http.StatusOK || sid == "" || res.message["result"].(map[string]any)["protocolVersion"] != mcpadapter.ProtocolVersion {
		r.t.Fatalf("initialize = %d %s", res.status, res.body)
	}
	if n := r.do(http.MethodPost, bearer, sid, rpc(nil, "notifications/initialized", nil), nil); n.status != http.StatusAccepted || len(n.body) != 0 {
		r.t.Fatalf("initialized notification = %d %s", n.status, n.body)
	}
	return sid
}

type caller struct {
	r      *rig
	bearer string
	sid    string
	next   int
}

func (r *rig) caller(i int) *caller {
	bearer := token(r.t, subjects[i])
	if r.access {
		bearer = assertion(r.t, accessClaims(subjects[i]))
	}
	return &caller{r: r, bearer: bearer, sid: r.open(bearer), next: 100}
}

func (c *caller) tool(name string, args map[string]any) (map[string]any, bool) {
	c.r.t.Helper()
	c.next++
	return c.toolWithID(c.next, name, args)
}

func (c *caller) toolWithID(id int, name string, args map[string]any) (map[string]any, bool) {
	c.r.t.Helper()
	res := c.r.do(http.MethodPost, c.bearer, c.sid, rpc(id, "tools/call", map[string]any{"name": name, "arguments": args}), nil)
	result, ok := res.message["result"].(map[string]any)
	if res.status != http.StatusOK || !ok || res.message["id"] != float64(id) {
		c.r.t.Fatalf("%s = %d %s", name, res.status, res.body)
	}
	return result["structuredContent"].(map[string]any), result["isError"].(bool)
}

func (c *caller) start(requestID, command string) (map[string]any, bool) {
	return c.tool("rescue_start", map[string]any{"request_id": requestID, "command": command, "timeout_ms": 60000})
}

func (c *caller) awaitTerminal(id string) map[string]any {
	c.r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, isErr := c.tool("rescue_status", map[string]any{"command_id": id})
		if isErr {
			c.r.t.Fatalf("rescue_status: %v", st)
		}
		if st["terminal"] == true {
			return st
		}
		if time.Now().After(deadline) {
			c.r.t.Fatalf("command did not finish: %v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func commandID(t *testing.T, st map[string]any) string {
	t.Helper()
	id, _ := st["command_id"].(string)
	if len(id) != 32 {
		t.Fatalf("command_id = %v", st)
	}
	return id
}

// eventually polls cond for up to five seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// --- fake Mac runtime: starts nothing ---

type event struct {
	stream agentexec.Stream
	data   []byte
}

type fakeProc struct {
	events   chan event
	exited   chan struct{}
	killed   chan struct{}
	killOnce sync.Once
}

func (p *fakeProc) Next(ctx context.Context) (agentexec.Stream, []byte, error) {
	select {
	case ev, ok := <-p.events:
		if !ok {
			return 0, nil, io.EOF
		}
		return ev.stream, ev.data, nil
	case <-p.killed:
		return 0, nil, io.ErrClosedPipe
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

func (p *fakeProc) Wait(ctx context.Context) (agentexec.ExitStatus, error) {
	select {
	case <-p.exited:
		return agentexec.ExitStatus{}, nil
	case <-p.killed:
		return agentexec.ExitStatus{Code: -1}, nil
	case <-ctx.Done():
		return agentexec.ExitStatus{}, ctx.Err()
	}
}

func (p *fakeProc) Terminate(context.Context) error {
	p.killOnce.Do(func() { close(p.killed) })
	return nil
}

func (p *fakeProc) Release(context.Context) error { return nil }

type macRuntime struct {
	mu     sync.Mutex
	starts int
	stdin  [][]byte
	procs  chan *fakeProc
}

func (r *macRuntime) AgentProfile() agentexec.Profile { return agentexec.ProfileMacCurrentUser }

func (r *macRuntime) StartShell(_ context.Context, _ supervisor.Binding, stdin []byte) (agentexec.GuestProcess, error) {
	r.mu.Lock()
	r.starts++
	r.stdin = append(r.stdin, append([]byte(nil), stdin...))
	r.mu.Unlock()
	p := &fakeProc{events: make(chan event), exited: make(chan struct{}), killed: make(chan struct{})}
	r.procs <- p
	return p, nil
}

func (r *macRuntime) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

func emit(t *testing.T, p *fakeProc, data string) {
	t.Helper()
	select {
	case p.events <- event{stream: agentexec.Stdout, data: []byte(data)}:
	case <-time.After(5 * time.Second):
		t.Fatal("output was not read")
	}
}

// --- buffered in-memory net.Conn with deadlines and a write hold ---

type pipeBuf struct {
	mu   sync.Mutex
	data []byte
	eof  bool
	wake chan struct{}
}

func (b *pipeBuf) notifyLocked() {
	close(b.wake)
	b.wake = make(chan struct{})
}

type memConn struct {
	rd, wr *pipeBuf

	mu       sync.Mutex
	readDL   time.Time
	writeDL  time.Time
	dlWake   chan struct{}
	hold     chan struct{} // non-nil while writes are held
	closed   bool
	closedCh chan struct{}
}

func memPair() (*memConn, *memConn) {
	ab, ba := &pipeBuf{wake: make(chan struct{})}, &pipeBuf{wake: make(chan struct{})}
	a := &memConn{rd: ba, wr: ab, dlWake: make(chan struct{}), closedCh: make(chan struct{})}
	b := &memConn{rd: ab, wr: ba, dlWake: make(chan struct{}), closedCh: make(chan struct{})}
	return a, b
}

const memLimit = 64 << 10

func wait(wake, dlWake, closed <-chan struct{}, deadline time.Time) error {
	var timeout <-chan time.Time
	if !deadline.IsZero() {
		d := time.Until(deadline)
		if d <= 0 {
			return os.ErrDeadlineExceeded
		}
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-wake:
	case <-dlWake:
	case <-closed:
	case <-timeout:
		return os.ErrDeadlineExceeded
	}
	return nil
}

// Hold delays every later write (replies and heartbeats) until Release.
func (c *memConn) Hold() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hold == nil {
		c.hold = make(chan struct{})
	}
}

func (c *memConn) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hold != nil {
		close(c.hold)
		c.hold = nil
	}
}

func (c *memConn) Read(p []byte) (int, error) {
	for {
		c.mu.Lock()
		closed, dl, dlWake := c.closed, c.readDL, c.dlWake
		c.mu.Unlock()
		if closed {
			return 0, net.ErrClosed
		}
		c.rd.mu.Lock()
		if len(c.rd.data) > 0 {
			n := copy(p, c.rd.data)
			c.rd.data = c.rd.data[n:]
			c.rd.notifyLocked()
			c.rd.mu.Unlock()
			return n, nil
		}
		if c.rd.eof {
			c.rd.mu.Unlock()
			return 0, io.EOF
		}
		wake := c.rd.wake
		c.rd.mu.Unlock()
		if err := wait(wake, dlWake, c.closedCh, dl); err != nil {
			return 0, err
		}
	}
}

func (c *memConn) Write(p []byte) (int, error) {
	written := 0
	for {
		c.mu.Lock()
		closed, dl, dlWake, hold := c.closed, c.writeDL, c.dlWake, c.hold
		c.mu.Unlock()
		if closed {
			return written, net.ErrClosed
		}
		if hold != nil {
			if err := wait(hold, dlWake, c.closedCh, dl); err != nil {
				return written, err
			}
			continue
		}
		c.wr.mu.Lock()
		if c.wr.eof {
			c.wr.mu.Unlock()
			return written, io.ErrClosedPipe
		}
		if space := memLimit - len(c.wr.data); space > 0 {
			n := min(space, len(p)-written)
			c.wr.data = append(c.wr.data, p[written:written+n]...)
			written += n
			c.wr.notifyLocked()
		}
		if written == len(p) {
			c.wr.mu.Unlock()
			return written, nil
		}
		wake := c.wr.wake
		c.wr.mu.Unlock()
		if err := wait(wake, dlWake, c.closedCh, dl); err != nil {
			return written, err
		}
	}
}

func (c *memConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.closedCh)
	c.mu.Unlock()
	for _, b := range []*pipeBuf{c.wr, c.rd} {
		b.mu.Lock()
		b.eof = true
		b.notifyLocked()
		b.mu.Unlock()
	}
	return nil
}

func (c *memConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *memConn) setDeadlines(read, write bool, t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if read {
		c.readDL = t
	}
	if write {
		c.writeDL = t
	}
	close(c.dlWake)
	c.dlWake = make(chan struct{})
	return nil
}

func (c *memConn) SetDeadline(t time.Time) error      { return c.setDeadlines(true, true, t) }
func (c *memConn) SetReadDeadline(t time.Time) error  { return c.setDeadlines(true, false, t) }
func (c *memConn) SetWriteDeadline(t time.Time) error { return c.setDeadlines(false, true, t) }

type memAddr struct{}

func (memAddr) Network() string { return "memory" }
func (memAddr) String() string  { return "memory" }

func (c *memConn) LocalAddr() net.Addr  { return memAddr{} }
func (c *memConn) RemoteAddr() net.Addr { return memAddr{} }
