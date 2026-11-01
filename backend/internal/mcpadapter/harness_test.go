package mcpadapter_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"golang.org/x/crypto/ssh"
)

// The chain under test is MCP stdio (io.Pipe) -> agentwire over net.Pipe ->
// the real agentexec Session with a memory-only fake guest runtime. Nothing
// binds a socket or starts a process, and the memory stream is not an
// authenticated transport.

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type event struct {
	stream agentexec.Stream
	data   []byte
}

type fakeProc struct {
	events   chan event
	exited   chan struct{}
	killed   chan struct{}
	killOnce sync.Once

	mu          sync.Mutex
	releaseErrs []error
	onTerminate func()
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
	p.mu.Lock()
	hook := p.onTerminate
	p.onTerminate = nil
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	p.killOnce.Do(func() { close(p.killed) })
	return nil
}

func (p *fakeProc) Release(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.releaseErrs) > 0 {
		err := p.releaseErrs[0]
		p.releaseErrs = p.releaseErrs[1:]
		return err
	}
	return nil
}

type fakeRuntime struct {
	mu        sync.Mutex
	starts    int
	configure func(*fakeProc)
	procs     chan *fakeProc
}

func (r *fakeRuntime) StartShell(context.Context, supervisor.Binding, []byte) (agentexec.GuestProcess, error) {
	r.mu.Lock()
	r.starts++
	configure := r.configure
	r.mu.Unlock()
	p := &fakeProc{events: make(chan event), exited: make(chan struct{}), killed: make(chan struct{})}
	if configure != nil {
		configure(p)
	}
	r.procs <- p
	return p, nil
}

func (r *fakeRuntime) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

// gateConn holds agentwire server replies while armed.
type gateConn struct {
	net.Conn
	mu     sync.Mutex
	hold   chan struct{}
	closed chan struct{}
	once   sync.Once
}

func (g *gateConn) Hold() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hold == nil {
		g.hold = make(chan struct{})
	}
}

func (g *gateConn) Release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.hold != nil {
		close(g.hold)
		g.hold = nil
	}
}

func (g *gateConn) Write(b []byte) (int, error) {
	g.mu.Lock()
	hold := g.hold
	g.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-g.closed:
			return 0, net.ErrClosed
		}
	}
	return g.Conn.Write(b)
}

func (g *gateConn) Close() error {
	g.once.Do(func() { close(g.closed) })
	return g.Conn.Close()
}

type options struct {
	output             func(io.Writer) io.Writer
	allowOutputUnknown bool
	mac                bool // Mac current-user profile instead of the Windows guest
	callTimeout        time.Duration
}

// macRuntime declares the Mac current-user profile for the same fake.
type macRuntime struct{ *fakeRuntime }

func (macRuntime) AgentProfile() agentexec.Profile { return agentexec.ProfileMacCurrentUser }

// fixtureKeys are deterministic public Ed25519 fixtures; no private key exists.
func fixtureKeys(t *testing.T) []ssh.PublicKey {
	t.Helper()
	var keys []ssh.PublicKey
	for _, b := range []byte{1, 2} {
		key, err := ssh.NewPublicKey(ed25519.PublicKey(bytes.Repeat([]byte{b}, ed25519.PublicKeySize)))
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	return keys
}

type harness struct {
	t         *testing.T
	clock     *testClock
	issuer    *ephemeral.Issuer
	authority *supervisor.Authority
	runtime   *fakeRuntime
	session   *agentexec.Session
	gate      *gateConn
	client    *agentwire.Client
	stopWire  context.CancelFunc
	served    chan error
	in        *io.PipeWriter
	lines     chan []byte
	done      chan error
	doneOnce  sync.Once
	doneErr   error
	next      int
	lastLen   int // bytes of the last stdout line received
}

func newHarness(t *testing.T, opt options) *harness {
	t.Helper()
	h := &harness{
		t: t, clock: &testClock{now: time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)}, runtime: &fakeRuntime{procs: make(chan *fakeProc, 8)},
		served: make(chan error, 1), lines: make(chan []byte, 64), done: make(chan error, 1),
	}
	keys := fixtureKeys(t)
	keySet, err := runbackend.KeySetDigest(keys)
	if err != nil {
		t.Fatal(err)
	}
	newIssuer, policy, boundary := ephemeral.New, agentexec.FullPolicyDigest(), supervisor.SeparatedNonAdminGuest
	var runtime agentexec.GuestRuntime = h.runtime
	if opt.mac {
		newIssuer, policy, boundary = ephemeral.NewCurrentUser, agentexec.MacCurrentUserPolicyDigest(), supervisor.CurrentUserNoIsolation
		runtime = macRuntime{h.runtime}
	}
	issuer, err := newIssuer()
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := issuer.Prepare(ephemeral.ApprovedPolicy{
		PolicyDigest: policy, KeySetDigest: keySet, GuestBoundary: boundary,
		HardDeadline: h.clock.Now().Add(time.Hour), BackendLeaseCeiling: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := issuer.Launch(context.Background(), challenge, h.clock.Now, func(context.Context, *supervisor.Authority) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	h.issuer, h.authority = issuer, authority
	if h.session, err = agentexec.New(agentexec.Config{Issuer: issuer, Authority: authority, Runtime: runtime, Clock: h.clock.Now, CleanupTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	// The peer key stands in for the one the future channel owner verified.
	server, err := agentwire.NewServer(agentwire.ServerConfig{Session: h.session, Peer: keys[0], ApprovedKeys: keys, Liveness: make(chan struct{})})
	if err != nil {
		t.Fatal(err)
	}
	serverEnd, clientEnd := net.Pipe()
	h.gate = &gateConn{Conn: serverEnd, closed: make(chan struct{})}
	wireCtx, stopWire := context.WithCancel(context.Background())
	h.stopWire = stopWire
	go func() { h.served <- server.Serve(wireCtx, h.gate) }()
	if h.client, err = agentwire.NewClient(clientEnd); err != nil {
		t.Fatal(err)
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	h.in = inW
	go func() {
		scanner := bufio.NewScanner(outR)
		scanner.Buffer(make([]byte, 64<<10), 4<<20)
		for scanner.Scan() {
			h.lines <- append([]byte(nil), scanner.Bytes()...)
		}
		close(h.lines)
	}()
	var output io.Writer = outW
	if opt.output != nil {
		output = opt.output(outW)
	}
	go func() {
		err := mcpadapter.Serve(context.Background(), mcpadapter.Config{Backend: h.client, CallTimeout: opt.callTimeout}, inR, output)
		_ = outW.Close()
		h.done <- err
	}()
	t.Cleanup(func() {
		h.gate.Release()
		_ = inW.Close()
		go func() {
			for range h.lines {
			}
		}()
		if err := h.waitDone(); err != nil && !errors.Is(err, mcpadapter.ErrChannelClosed) && !(opt.allowOutputUnknown && errors.Is(err, mcpadapter.ErrOutputUnconfirmed)) {
			t.Errorf("Serve = %v", err)
		}
		_ = h.client.Close()
		stopWire()
		select {
		case <-h.served:
		case <-time.After(2 * time.Second):
			t.Error("agentwire Serve did not return")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.session.Close(ctx)
	})
	return h
}

func (h *harness) waitDone() error {
	h.doneOnce.Do(func() {
		select {
		case h.doneErr = <-h.done:
		case <-time.After(2 * time.Second):
			h.doneErr = io.ErrNoProgress
		}
	})
	return h.doneErr
}

func (h *harness) raw(line string) {
	h.t.Helper()
	if _, err := io.WriteString(h.in, line+"\n"); err != nil {
		h.t.Fatalf("write stdin: %v", err)
	}
}

func (h *harness) send(msg map[string]any) {
	h.t.Helper()
	msg["jsonrpc"] = "2.0"
	b, err := json.Marshal(msg)
	if err != nil {
		h.t.Fatal(err)
	}
	h.raw(string(b))
}

// recv returns the next stdout message. Every stdout line must be a JSON-RPC
// message; anything else is a protocol violation by the adapter.
func (h *harness) recv() map[string]any {
	h.t.Helper()
	select {
	case line, ok := <-h.lines:
		if !ok {
			h.t.Fatal("stdout closed")
		}
		h.lastLen = len(line)
		var msg map[string]any
		if err := json.Unmarshal(line, &msg); err != nil || msg["jsonrpc"] != "2.0" {
			h.t.Fatalf("non-protocol stdout line %q: %v", line, err)
		}
		return msg
	case <-time.After(2 * time.Second):
		h.t.Fatal("no response")
		return nil
	}
}

func (h *harness) noMessage(d time.Duration) {
	h.t.Helper()
	select {
	case line, ok := <-h.lines:
		if ok {
			h.t.Fatalf("unexpected message %s", line)
		}
	case <-time.After(d):
	}
}

func (h *harness) id() int {
	h.next++
	return h.next
}

func (h *harness) request(method string, params any) map[string]any {
	h.t.Helper()
	id := h.id()
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	h.send(msg)
	resp := h.recv()
	if resp["id"] != float64(id) {
		h.t.Fatalf("response %v does not answer request %d", resp, id)
	}
	return resp
}

func (h *harness) notify(method string, params any) {
	h.t.Helper()
	msg := map[string]any{"method": method}
	if params != nil {
		msg["params"] = params
	}
	h.send(msg)
}

func initParams(version string) map[string]any {
	return map[string]any{"protocolVersion": version, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}}
}

func (h *harness) initialize() {
	h.t.Helper()
	resp := h.request("initialize", initParams(mcpadapter.ProtocolVersion))
	if resp["result"] == nil {
		h.t.Fatalf("initialize = %v", resp)
	}
	h.notify("notifications/initialized", nil)
}

func errorCode(t *testing.T, resp map[string]any) float64 {
	t.Helper()
	e, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error response, got %v", resp)
	}
	return e["code"].(float64)
}

// tool calls one tool and checks that the text content mirrors the
// structured content.
func (h *harness) tool(name string, args any) (map[string]any, bool) {
	h.t.Helper()
	resp := h.request("tools/call", map[string]any{"name": name, "arguments": args})
	result, ok := resp["result"].(map[string]any)
	if !ok {
		h.t.Fatalf("%s: %v", name, resp)
	}
	structured := result["structuredContent"].(map[string]any)
	content := result["content"].([]any)
	var mirrored map[string]any
	text := content[0].(map[string]any)
	if text["type"] != "text" || json.Unmarshal([]byte(text["text"].(string)), &mirrored) != nil || !reflect.DeepEqual(mirrored, structured) {
		h.t.Fatalf("%s: text content does not mirror structuredContent: %v", name, result)
	}
	return structured, result["isError"].(bool)
}

func (h *harness) proc() *fakeProc {
	h.t.Helper()
	select {
	case p := <-h.runtime.procs:
		return p
	case <-time.After(2 * time.Second):
		h.t.Fatal("guest shell was not started")
		return nil
	}
}

func (h *harness) start(requestID, command string) (map[string]any, bool) {
	h.t.Helper()
	return h.tool("rescue_start", map[string]any{"request_id": requestID, "command": command, "timeout_ms": 60000})
}

func (h *harness) awaitTerminal(id string) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		st, isErr := h.tool("rescue_status", map[string]any{"command_id": id})
		if isErr {
			h.t.Fatalf("rescue_status: %v", st)
		}
		if st["terminal"] == true {
			return st
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("command did not finish: %v", st)
		}
		time.Sleep(time.Millisecond)
	}
}

func emit(t *testing.T, p *fakeProc, s agentexec.Stream, data []byte) {
	t.Helper()
	select {
	case p.events <- event{stream: s, data: data}:
	case <-time.After(2 * time.Second):
		t.Fatal("guest output was not read")
	}
}

func finite(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}
