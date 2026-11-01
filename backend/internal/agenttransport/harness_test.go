package agenttransport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentlink"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"golang.org/x/crypto/ssh"
)

// The harness never listens, dials, starts a process or touches a real key or
// account. HTTP/1.1 requests and responses and every WebSocket frame are real
// bytes on a buffered in-memory conn (memPair): memServe plays the server side
// of one connection and memTransport is the Mac client's round tripper. Keys
// are synthetic fixtures derived from fixed seeds.

const (
	testEndpoint = "wss://gateway.example.invalid/agent/register"
	testURL      = "http://gateway.example.invalid/agent/register"
	hostLabel    = "mac-1"
	otherHost    = "mac-2"
	member1      = "member-1"
	member2      = "member-2"
)

func fixtureSigner(t *testing.T, seed byte) ssh.Signer {
	t.Helper()
	s, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testBudgets() Budgets {
	return Budgets{Registration: 500 * time.Millisecond, Approval: 3 * time.Second, Cleanup: time.Second}
}

func chanBudgets() agentchannel.Budgets {
	return agentchannel.Budgets{Handshake: 3 * time.Second, HeartbeatInterval: 50 * time.Millisecond, HeartbeatTimeout: 500 * time.Millisecond,
		ReadStall: 1500 * time.Millisecond, WriteStall: time.Second, Cleanup: time.Second}
}

// ---- in-memory HTTP ----

type memWriter struct {
	conn     *memConn
	br       *bufio.Reader
	header   http.Header
	status   int
	body     bytes.Buffer
	hijacked bool
}

func (w *memWriter) Header() http.Header { return w.header }

func (w *memWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *memWriter) Write(p []byte) (int, error) {
	if w.hijacked {
		return 0, http.ErrHijacked
	}
	w.WriteHeader(http.StatusOK)
	return w.body.Write(p)
}

// Hijack writes the pending status line and headers (as net/http does for a
// 101) and hands over the same conn and the reader that parsed the request.
func (w *memWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.hijacked {
		return nil, nil, http.ErrHijacked
	}
	w.hijacked = true
	if w.status != 0 {
		var b bytes.Buffer
		fmt.Fprintf(&b, "HTTP/1.1 %d %s\r\n", w.status, http.StatusText(w.status))
		_ = w.header.Write(&b)
		b.WriteString("\r\n")
		if _, err := w.conn.Write(b.Bytes()); err != nil {
			return nil, nil, err
		}
	}
	return w.conn, bufio.NewReadWriter(w.br, bufio.NewWriter(w.conn)), nil
}

func (w *memWriter) finish() {
	if w.hijacked {
		return
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	resp := &http.Response{StatusCode: w.status, ProtoMajor: 1, ProtoMinor: 1, Header: w.header,
		ContentLength: int64(w.body.Len()), Body: io.NopCloser(&w.body)}
	_ = resp.Write(w.conn)
	_ = w.conn.Close()
}

// memServe answers the one HTTP/1.1 request on conn with h.
func memServe(h http.Handler, conn *memConn, done chan<- struct{}) {
	defer func() {
		if done != nil {
			close(done)
		}
	}()
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		_ = conn.Close()
		return
	}
	req.RemoteAddr = "memory"
	w := &memWriter{conn: conn, br: br, header: http.Header{}}
	h.ServeHTTP(w, req)
	w.finish()
}

type memPairEnds struct {
	client, server *memConn
	served         chan struct{} // closed when the server's ServeHTTP returns
}

// memTransport is an http.RoundTripper over memPair: it writes the real
// request bytes, lets memServe answer, and parses the real response. A 101
// body is the raw conn, as net/http's transport returns for an upgrade. It
// follows nothing; the caller's http.Client decides about redirects.
type memTransport struct {
	handler http.Handler
	track   func(net.Conn) (net.Conn, error)
	prepare func(client, server *memConn)
	pairs   chan memPairEnds
	trips   atomic.Int32
	// bypassTrack models a transport whose connection the Client never
	// observes. accept, if set, hands the server end to a real http.Server
	// (through memListener) instead of memServe.
	bypassTrack bool
	accept      func(*memConn)
	own         acquirer // the Client's owner, when built by newMemClient
}

func newMemTransport(h http.Handler) *memTransport {
	return &memTransport{handler: h, pairs: make(chan memPairEnds, 8), track: func(c net.Conn) (net.Conn, error) { return c, nil }}
}

func (m *memTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.trips.Add(1)
	client, server := memPair()
	if m.prepare != nil {
		m.prepare(client, server)
	}
	ends := memPairEnds{client: client, server: server, served: make(chan struct{})}
	select {
	case m.pairs <- ends:
	default:
	}
	var conn net.Conn = client
	if !m.bypassTrack {
		if m.own != nil {
			if !m.own.begin() {
				_ = client.Close()
				_ = server.Close()
				return nil, ErrRefused
			}
			defer m.own.end()
		}
		var err error
		if conn, err = m.track(client); err != nil {
			_ = server.Close()
			return nil, err
		}
	}
	if m.accept != nil {
		m.accept(server)
	} else {
		go memServe(m.handler, server, ends.served)
	}
	// Until the response arrives, cancellation closes the conn (as the real
	// transport does); after an upgrade the conn belongs to the caller.
	stop := context.AfterFunc(req.Context(), func() { _ = conn.Close() })
	defer stop()
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	// The body reads the conn directly: the upgraded stream for a 101, the
	// rest of the response (until the server closes) otherwise.
	resp.Body = &upgradedBody{r: br, conn: conn}
	return resp, nil
}

type upgradedBody struct {
	r    *bufio.Reader
	conn net.Conn
}

func (u *upgradedBody) Read(p []byte) (int, error)  { return u.r.Read(p) }
func (u *upgradedBody) Write(p []byte) (int, error) { return u.conn.Write(p) }
func (u *upgradedBody) Close() error                { return u.conn.Close() }

// ---- raw WebSocket client (hand-written frames) ----

type rawWS struct {
	t      *testing.T
	conn   *memConn
	server *memConn
	served chan struct{}
	br     *bufio.Reader
}

func upgrade(t *testing.T, h http.Handler, edit func(*http.Request)) (*rawWS, *http.Response) {
	t.Helper()
	return upgradeWith(t, h, nil, edit)
}

// upgradeWith lets prepare adjust the memory pair (for example gate the
// server end's Close) before the request is served.
func upgradeWith(t *testing.T, h http.Handler, prepare func(client, server *memConn), edit func(*http.Request)) (*rawWS, *http.Response) {
	t.Helper()
	r, req := sendUpgrade(t, h, prepare, edit)
	_ = r.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := http.ReadResponse(r.br, req)
	if err != nil {
		t.Fatalf("upgrade response: %v", err)
	}
	return r, resp
}

// sendUpgrade writes the upgrade request and returns without reading the
// response, for tests whose server side stalls.
func sendUpgrade(t *testing.T, h http.Handler, prepare func(client, server *memConn), edit func(*http.Request)) (*rawWS, *http.Request) {
	t.Helper()
	client, server := memPair()
	if prepare != nil {
		prepare(client, server)
	}
	served := make(chan struct{})
	go memServe(h, server, served)
	req, err := http.NewRequest(http.MethodGet, testURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString(key))
	req.Header.Set("Sec-WebSocket-Protocol", Subprotocol)
	if edit != nil {
		edit(req)
	}
	if err := req.Write(client); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return &rawWS{t: t, conn: client, server: server, served: served, br: bufio.NewReader(client)}, req
}

func upgraded(t *testing.T, h http.Handler) *rawWS {
	t.Helper()
	r, resp := upgrade(t, h, nil)
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Sec-WebSocket-Protocol") != Subprotocol {
		t.Fatalf("upgrade = %d %v", resp.StatusCode, resp.Header)
	}
	return r
}

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
)

// send writes one masked client frame.
func (r *rawWS) send(fin bool, op byte, payload []byte) error {
	hdr := []byte{op}
	if fin {
		hdr[0] |= 0x80
	}
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n <= 0xffff:
		hdr = append(hdr, 0x80|126)
		hdr = binary.BigEndian.AppendUint16(hdr, uint16(n))
	default:
		hdr = append(hdr, 0x80|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	mask := [4]byte{0x11, 0x22, 0x33, 0x44}
	hdr = append(hdr, mask[:]...)
	for i, c := range payload {
		hdr = append(hdr, c^mask[i%4])
	}
	_, err := r.conn.Write(hdr)
	return err
}

func (r *rawWS) text(msg []byte) {
	r.t.Helper()
	if err := r.send(true, opText, msg); err != nil {
		r.t.Fatalf("send: %v", err)
	}
}

// next reads one unmasked server frame within timeout.
func (r *rawWS) next(timeout time.Duration) (byte, []byte, error) {
	_ = r.conn.SetReadDeadline(time.Now().Add(timeout))
	var h [2]byte
	if _, err := io.ReadFull(r.br, h[:]); err != nil {
		return 0, nil, err
	}
	if h[1]&0x80 != 0 {
		return 0, nil, errors.New("masked server frame")
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(r.br, b[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(r.br, b[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > 1<<20 {
		return 0, nil, errors.New("oversized server frame")
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r.br, p); err != nil {
		return 0, nil, err
	}
	return h[0] & 0x0f, p, nil
}

func (r *rawWS) challenge() []byte {
	r.t.Helper()
	op, p, err := r.next(2 * time.Second)
	if err != nil || op != opText {
		r.t.Fatalf("challenge frame: op %d err %v", op, err)
	}
	nonce, err := decodeChallenge(p)
	if err != nil {
		r.t.Fatalf("challenge %q: %v", p, err)
	}
	return nonce
}

func (r *rawWS) expectAccepted() {
	r.t.Helper()
	op, p, err := r.next(3 * time.Second)
	if err != nil || op != opText || !isAccepted(p) {
		r.t.Fatalf("expected accepted, got op %d %q err %v", op, p, err)
	}
}

// expectRefused requires the server to end the connection (a close frame is
// tolerated) without ever sending accepted, and the request to finish.
func (r *rawWS) expectRefused(within time.Duration) {
	r.t.Helper()
	deadline := time.Now().Add(within)
	for {
		op, p, err := r.next(time.Until(deadline))
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				r.t.Fatal("the gateway kept the refused connection open")
			}
			break
		}
		if op == opText && isAccepted(p) {
			r.t.Fatal("refused registration was accepted")
		}
	}
	select {
	case <-r.served:
	case <-time.After(within):
		r.t.Fatal("ServeHTTP did not return")
	}
	if !r.server.isClosed() {
		r.t.Fatal("hijacked conn was not closed")
	}
}

func signedRegister(t *testing.T, endpoint, host, principal string, d agentgateway.Dispatch, nonce []byte, signer ssh.Signer) []byte {
	t.Helper()
	sig, err := sign(signer, signer.PublicKey(), transcript(endpoint, host, principal, d, nonce))
	if err != nil {
		t.Fatal(err)
	}
	return encodeRegister(host, principal, d, sig)
}

func fixtureDispatch(host ssh.Signer, launch string, deadline time.Time) agentgateway.Dispatch {
	return agentgateway.Dispatch{
		Binding: agentchannel.Binding{
			Grant: supervisor.Binding{Generation: 1, Identity: supervisor.Identity{
				LaunchID: launch, Epoch: "epoch-1", PolicyDigest: agentexec.MacCurrentUserPolicyDigest(),
			}},
			KeySetDigest: [32]byte{1}, Profile: agentexec.ProfileMacCurrentUser,
		},
		Deadline: deadline, HostKey: host.PublicKey().Marshal(),
	}
}

func sameDispatch(a, b agentgateway.Dispatch) bool {
	return a.Binding == b.Binding && a.Deadline.Equal(b.Deadline) && bytes.Equal(a.HostKey, b.HostKey)
}

// ---- registrar ----

type registrarCall struct {
	label string
	d     agentgateway.Dispatch
	conn  net.Conn
	err   error
}

// recordingRegistrar records every Connect. With inner set it delegates to
// the real gateway; otherwise it keeps the stream for the test.
type recordingRegistrar struct {
	inner Registrar
	calls chan registrarCall
	count atomic.Int32
}

func newRecorder(inner Registrar) *recordingRegistrar {
	return &recordingRegistrar{inner: inner, calls: make(chan registrarCall, 16)}
}

func (r *recordingRegistrar) Connect(label string, d agentgateway.Dispatch, conn net.Conn) error {
	r.count.Add(1)
	var err error
	if r.inner != nil {
		err = r.inner.Connect(label, d, conn)
	}
	r.calls <- registrarCall{label: label, d: d, conn: conn, err: err}
	return err
}

func (r *recordingRegistrar) next(t *testing.T) registrarCall {
	t.Helper()
	select {
	case c := <-r.calls:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("registrar was not called")
		return registrarCall{}
	}
}

// ---- handler ----

func newHandler(t *testing.T, reg Registrar, budgets Budgets, hosts ...EnrolledHost) *Handler {
	t.Helper()
	h, err := NewHandler(HandlerConfig{Endpoint: testEndpoint, Hosts: hosts, Registrar: reg, Budgets: budgets})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.Close(ctx); err != nil {
			t.Errorf("Handler.Close = %v", err)
		}
	})
	return h
}

// ---- real gateway ----

func newGateway(t *testing.T, members map[string]ssh.Signer) *agentgateway.Gateway {
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
	var principals []agentgateway.Principal
	for i, label := range []string{member1, member2} {
		if s, ok := members[label]; ok {
			principals = append(principals, agentgateway.Principal{Label: label, Subject: fmt.Sprintf("synthetic-subject-%d", i+1), Signer: s})
		}
	}
	g, err := agentgateway.New(agentgateway.Config{Verifier: v, Principals: principals, Budgets: chanBudgets()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := g.Close(ctx); err != nil {
			t.Errorf("Gateway.Close = %v", err)
		}
	})
	return g
}

// ---- Mac owner ----

type noShell struct{}

func (noShell) AgentProfile() agentexec.Profile { return agentexec.ProfileMacCurrentUser }
func (noShell) StartShell(context.Context, supervisor.Binding, []byte) (agentexec.GuestProcess, error) {
	return nil, errors.New("fixture: no shell")
}

// linkOwner reproduces what maclaunch.Approved owns after confirmation: the
// current-user issuer, the canonical Session on a fake Mac runtime and the
// agentlink.Host with the pinned host signer, with the same Serve and Close
// order. The issuance stands in for maclaunch's /dev/tty approval, which a
// test cannot (and must not) produce; it is a fixture, not self-approval.
type linkOwner struct {
	issuer  *ephemeral.Issuer
	session *agentexec.Session
	host    *agentlink.Host
	d       maclaunch.Dispatch
	serves  atomic.Int32
}

func newLinkOwner(t *testing.T, hostKey ssh.Signer, lifetime time.Duration, clients ...ssh.PublicKey) *linkOwner {
	t.Helper()
	digest, err := runbackend.KeySetDigest(clients)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := ephemeral.NewCurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := issuer.Prepare(ephemeral.ApprovedPolicy{
		PolicyDigest: agentexec.MacCurrentUserPolicyDigest(), KeySetDigest: digest, GuestBoundary: supervisor.CurrentUserNoIsolation,
		HardDeadline: time.Now().Add(lifetime), BackendLeaseCeiling: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := issuer.Launch(context.Background(), challenge, time.Now, func(context.Context, *supervisor.Authority) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	session, err := agentexec.New(agentexec.Config{Issuer: issuer, Authority: authority, Runtime: noShell{}, Clock: time.Now, CleanupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	host, err := agentlink.NewHost(agentlink.HostConfig{Session: session, AllowedKeys: clients, HostKey: hostKey, Budgets: chanBudgets()})
	if err != nil {
		t.Fatal(err)
	}
	g := session.Grant()
	o := &linkOwner{issuer: issuer, session: session, host: host, d: maclaunch.Dispatch{
		Binding:  agentchannel.Binding{Grant: g.Binding, KeySetDigest: g.KeySetDigest, Profile: session.Profile()},
		Deadline: g.HardDeadline, HostKey: hostKey.PublicKey().Marshal(),
	}}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = o.Close(ctx)
	})
	return o
}

func (o *linkOwner) Dispatch() maclaunch.Dispatch {
	d := o.d
	d.HostKey = append([]byte(nil), d.HostKey...)
	return d
}

func (o *linkOwner) Serve(ctx context.Context, conn net.Conn) error {
	o.serves.Add(1)
	err := o.host.Serve(ctx, conn)
	if !errors.Is(err, agentchannel.ErrUsed) {
		_ = o.issuer.Close()
	}
	return err
}

func (o *linkOwner) Close(ctx context.Context) error {
	return errors.Join(o.issuer.Close(), o.host.Close(ctx))
}

func (o *linkOwner) live() bool { return o.issuer.Snapshot().HasAuthority }

// stubOwner is a scripted owner for cleanup-ownership tests only; it serves
// nothing and is never presented as a real approval.
type stubOwner struct {
	d       maclaunch.Dispatch
	mu      sync.Mutex
	closes  int
	results []error // successive Close results; nil after they run out
	serves  atomic.Int32
}

func newStubOwner(host ssh.Signer, deadline time.Time, results ...error) *stubOwner {
	d := fixtureDispatch(host, "launch-stub", deadline)
	return &stubOwner{d: maclaunch.Dispatch{Binding: d.Binding, Deadline: d.Deadline, HostKey: d.HostKey}, results: results}
}

func (s *stubOwner) Dispatch() maclaunch.Dispatch { return s.d }

func (s *stubOwner) Serve(_ context.Context, conn net.Conn) error {
	s.serves.Add(1)
	_ = conn.Close()
	return errors.New("stub: serves nothing")
}

func (s *stubOwner) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes++
	if len(s.results) == 0 {
		return nil
	}
	err := s.results[0]
	s.results = s.results[1:]
	return err
}

func (s *stubOwner) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

// ---- Mac client over memory ----

func clientConfig(signer ssh.Signer) ClientConfig {
	return ClientConfig{Endpoint: testEndpoint, HostLabel: hostLabel, PrincipalLabel: member1, HostKey: signer, Budgets: testBudgets()}
}

// newMemClient builds a Client whose only round tripper is m; it receives the
// Client's own transport owner, exactly as nativeTransport does, and brackets
// its one memory "dial" with begin/end like the native DialContext.
func newMemClient(t *testing.T, cfg ClientConfig, owner Owner, m *memTransport) *Client {
	t.Helper()
	c, err := NewClient(cfg, owner)
	if err != nil {
		t.Fatal(err)
	}
	c.transport = func(own acquirer) http.RoundTripper {
		m.own = own
		m.track = own.attach
		return m
	}
	return c
}

func runAsync(ctx context.Context, c *Client) <-chan error {
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	return done
}

func await(t *testing.T, done <-chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(within):
		t.Fatal("Run did not return")
		return nil
	}
}
