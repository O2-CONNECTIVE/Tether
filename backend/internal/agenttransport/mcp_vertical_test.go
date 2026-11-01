package agenttransport

// This is the complete in-memory protocol path, not an actual Mac execution:
// synthetic OAuth access token -> HTTP MCP -> signed WSS registration -> SSH
// agentchannel -> approved Session -> fake current-user runtime. No listener,
// TLS socket, tty, account, key file or real process is opened.
import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentlink"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"golang.org/x/crypto/ssh"
)

type verticalRuntime struct {
	starts atomic.Int32
	procs  chan *verticalProc
}

func (*verticalRuntime) AgentProfile() agentexec.Profile { return agentexec.ProfileMacCurrentUser }
func (r *verticalRuntime) StartShell(_ context.Context, _ supervisor.Binding, input []byte) (agentexec.GuestProcess, error) {
	r.starts.Add(1)
	p := &verticalProc{input: string(input), output: make(chan []byte, 1), exited: make(chan struct{}), killed: make(chan struct{})}
	r.procs <- p
	return p, nil
}

type verticalProc struct {
	input                      string
	output                     chan []byte
	exited, killed             chan struct{}
	once                       sync.Once
	waits, reads, releases     atomic.Int32
	activeReads, exitConfirmed atomic.Int32
}

func (p *verticalProc) Next(ctx context.Context) (agentexec.Stream, []byte, error) {
	p.activeReads.Add(1)
	defer p.activeReads.Add(-1)
	defer p.reads.Add(1)
	select {
	case b, ok := <-p.output:
		if !ok {
			return 0, nil, io.EOF
		}
		return agentexec.Stdout, b, nil
	case <-p.killed:
		return 0, nil, io.ErrClosedPipe
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}
func (p *verticalProc) Wait(ctx context.Context) (agentexec.ExitStatus, error) {
	defer p.waits.Add(1)
	select {
	case <-p.exited:
		return agentexec.ExitStatus{}, nil
	case <-p.killed:
		return agentexec.ExitStatus{Code: -1}, nil
	case <-ctx.Done():
		return agentexec.ExitStatus{}, ctx.Err()
	}
}
func (p *verticalProc) Terminate(context.Context) error {
	p.once.Do(func() { close(p.killed) })
	return nil
}

// Release models the runtime contract: confirmation includes exit and joined I/O.
// Session.Wait is used for a natural exit only; cancellation goes straight to
// Terminate/Release, whose native implementation owns the actual wait/reap.
func (p *verticalProc) Release(ctx context.Context) error {
	if p.activeReads.Load() != 0 {
		return errors.New("fake I/O not joined")
	}
	select {
	case <-p.exited:
	case <-p.killed:
	case <-ctx.Done():
		return ctx.Err()
	default:
		return errors.New("fake process still live")
	}
	p.exitConfirmed.Store(1)
	p.releases.Add(1)
	return nil
}

func verticalOwner(t *testing.T, hostKey, client ssh.Signer, rt *verticalRuntime, lifetime time.Duration) *linkOwner {
	t.Helper()
	keys := []ssh.PublicKey{client.PublicKey()}
	digest, err := runbackend.KeySetDigest(keys)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := ephemeral.NewCurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	ch, err := issuer.Prepare(ephemeral.ApprovedPolicy{PolicyDigest: agentexec.MacCurrentUserPolicyDigest(), KeySetDigest: digest, GuestBoundary: supervisor.CurrentUserNoIsolation, HardDeadline: time.Now().Add(lifetime), BackendLeaseCeiling: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	a, err := issuer.Launch(context.Background(), ch, time.Now, func(context.Context, *supervisor.Authority) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	s, err := agentexec.New(agentexec.Config{Issuer: issuer, Authority: a, Runtime: rt, Clock: time.Now, CleanupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	h, err := agentlink.NewHost(agentlink.HostConfig{Session: s, AllowedKeys: keys, HostKey: hostKey, Budgets: chanBudgets()})
	if err != nil {
		t.Fatal(err)
	}
	g := s.Grant()
	o := &linkOwner{issuer: issuer, session: s, host: h, d: maclaunch.Dispatch{Binding: agentchannel.Binding{Grant: g.Binding, KeySetDigest: g.KeySetDigest, Profile: s.Profile()}, Deadline: g.HardDeadline, HostKey: hostKey.PublicKey().Marshal()}}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := o.Close(ctx); err != nil {
			t.Errorf("owner cleanup: %v", err)
		}
	})
	return o
}
func verticalComposed(t *testing.T, lifetime time.Duration) (*composed, *verticalRuntime) {
	t.Helper()
	p := &composed{host1: fixtureSigner(t, 0x11), m1: fixtureSigner(t, 0x21)}
	p.gateway = newGateway(t, map[string]ssh.Signer{member1: p.m1})
	p.rec = newRecorder(p.gateway)
	p.h = newHandler(t, p.rec, testBudgets(), EnrolledHost{Label: hostLabel, PublicKey: p.host1.PublicKey(), Principals: []string{member1}})
	rt := &verticalRuntime{procs: make(chan *verticalProc, 4)}
	p.owner = verticalOwner(t, p.host1, p.m1, rt, lifetime)
	p.m = newMemTransport(p.h)
	p.c = newMemClient(t, clientConfig(p.host1), p.owner, p.m)
	return p, rt
}

type verticalMCP struct {
	t          *testing.T
	g          *agentgateway.Gateway
	token, sid string
	id         int
}

func verticalToken(subject string) string {
	enc := base64.RawURLEncoding
	h, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": "synthetic-ed", "typ": "at+jwt"})
	c, _ := json.Marshal(map[string]any{"iss": "https://issuer.example.invalid/", "aud": "https://gateway.example.invalid/mcp", "scope": "rescue.agent", "sub": subject, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()})
	in := enc.EncodeToString(h) + "." + enc.EncodeToString(c)
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize))
	return in + "." + enc.EncodeToString(ed25519.Sign(key, []byte(in)))
}
func (c *verticalMCP) request(id any, method string, params any) (*httptest.ResponseRecorder, map[string]any) {
	c.t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		body["id"] = id
	}
	if params != nil {
		body["params"] = params
	}
	b, _ := json.Marshal(body)
	q := httptest.NewRequest(http.MethodPost, "https://gateway.example.invalid/mcp", bytes.NewReader(b))
	q.Header.Set("Authorization", "Bearer "+c.token)
	q.Header.Set("Content-Type", "application/json")
	q.Header.Set("Accept", "application/json, text/event-stream")
	if c.sid != "" {
		q.Header.Set(agentgateway.HeaderSession, c.sid)
		q.Header.Set(agentgateway.HeaderProtocol, mcpadapter.ProtocolVersion)
	}
	rec := httptest.NewRecorder()
	c.g.ServeHTTP(rec, q)
	var out map[string]any
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			c.t.Fatal(err)
		}
	}
	return rec, out
}
func newVerticalMCP(t *testing.T, g *agentgateway.Gateway) *verticalMCP {
	t.Helper()
	c := &verticalMCP{t: t, g: g, token: verticalToken("synthetic-subject-1"), id: 10}
	rec, msg := c.request(1, "initialize", map[string]any{"protocolVersion": mcpadapter.ProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "memory-vertical", "version": "1"}})
	c.sid = rec.Header().Get(agentgateway.HeaderSession)
	if rec.Code != 200 || c.sid == "" || msg["result"] == nil {
		t.Fatalf("initialize %d %s", rec.Code, rec.Body.String())
	}
	rec, _ = c.request(nil, "notifications/initialized", nil)
	if rec.Code != 202 {
		t.Fatal("initialize notification refused")
	}
	return c
}
func (c *verticalMCP) tool(name string, args map[string]any) (map[string]any, bool) {
	c.t.Helper()
	c.id++
	rec, msg := c.request(c.id, "tools/call", map[string]any{"name": name, "arguments": args})
	r, ok := msg["result"].(map[string]any)
	if rec.Code != 200 || !ok {
		c.t.Fatalf("tool %s: %d %s", name, rec.Code, rec.Body.String())
	}
	v, ok := r["structuredContent"].(map[string]any)
	if !ok {
		c.t.Fatal("no structured result")
	}
	e, ok := r["isError"].(bool)
	if !ok {
		c.t.Fatal("no explicit error flag")
	}
	return v, e
}
func (c *verticalMCP) start(id, command string) (map[string]any, bool) {
	return c.tool("rescue_start", map[string]any{"request_id": id, "command": command, "timeout_ms": 60000})
}
func verticalID(t *testing.T, v map[string]any) string {
	t.Helper()
	id, ok := v["command_id"].(string)
	if !ok || len(id) != 32 {
		t.Fatalf("bad command id %v", v)
	}
	return id
}
func verticalEventually(t *testing.T, f func() bool) {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(end) {
			t.Fatal("owned state did not reach expected terminal")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func (c *verticalMCP) terminal(id string) map[string]any {
	c.t.Helper()
	var out map[string]any
	verticalEventually(c.t, func() bool {
		v, e := c.tool("rescue_status", map[string]any{"command_id": id})
		if e {
			c.t.Fatalf("status refused %v", v)
		}
		out = v
		return v["terminal"] == true
	})
	if out["cleanup"] != "confirmed" {
		c.t.Fatalf("unconfirmed %v", out)
	}
	return out
}
func verticalProcNext(t *testing.T, r *verticalRuntime) *verticalProc {
	t.Helper()
	select {
	case p := <-r.procs:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no fake process")
		return nil
	}
}
func verticalJoined(t *testing.T, p *verticalProc) {
	t.Helper()
	verticalEventually(t, func() bool { return p.releases.Load() == 1 })
	if p.exitConfirmed.Load() != 1 || p.activeReads.Load() != 0 || p.reads.Load() == 0 {
		t.Fatalf("missing fake exit/I/O join evidence: natural Wait calls=%d read=%d", p.waits.Load(), p.reads.Load())
	}
}

func TestMCPWSSVerticalNormalReplayOutputAndExplicitCancel(t *testing.T) {
	p, rt := verticalComposed(t, 30*time.Second)
	done, ends := p.connected(t)
	c := newVerticalMCP(t, p.gateway)
	command := "printf 'hello\\n'\n"
	st, e := c.start("normal", command)
	if e {
		t.Fatal(st)
	}
	id := verticalID(t, st)
	proc := verticalProcNext(t, rt)
	if proc.input != command {
		t.Fatal("command changed before runtime")
	}
	replay, e := c.start("normal", command)
	if e || verticalID(t, replay) != id || rt.starts.Load() != 1 {
		t.Fatal("replay restarted")
	}
	mismatch, e := c.start("normal", "other\n")
	if !e || mismatch["error"] != "request_mismatch" {
		t.Fatal("mismatch accepted")
	}
	proc.output <- []byte("hello\n")
	close(proc.output)
	close(proc.exited)
	if s := c.terminal(id); s["phase"] != "exited" {
		t.Fatal(s)
	}
	out, e := c.tool("rescue_output", map[string]any{"command_id": id, "cursor": 0, "max_bytes": 1024})
	chunks, ok := out["chunks"].([]any)
	if e || !ok || len(chunks) != 1 || chunks[0].(map[string]any)["data"] != "hello\n" || out["complete"] != true {
		t.Fatal(out)
	}
	verticalJoined(t, proc)
	st, e = c.start("cancel", "sleep 20\n")
	if e {
		t.Fatal(st)
	}
	id = verticalID(t, st)
	proc = verticalProcNext(t, rt)
	accepted, e := c.tool("rescue_cancel", map[string]any{"command_id": id})
	if e || accepted["stop"] != "canceled" {
		t.Fatal(accepted)
	}
	if s := c.terminal(id); s["phase"] != "canceled" {
		t.Fatal(s)
	}
	verticalJoined(t, proc)
	if !p.owner.live() || rt.starts.Load() != 2 {
		t.Fatal("cancel affected grant or wrong execution")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	p.ended(t, done, ends)
}
func TestMCPWSSVerticalLossAndRequestIndependentExpiry(t *testing.T) {
	for _, mode := range []string{"transport-loss", "no-request-expiry"} {
		t.Run(mode, func(t *testing.T) {
			lifetime := 30 * time.Second
			if mode == "no-request-expiry" {
				lifetime = 2 * time.Second
			}
			p, rt := verticalComposed(t, lifetime)
			done, ends := p.connected(t)
			c := newVerticalMCP(t, p.gateway)
			st, e := c.start("owned", "sleep 20\n")
			if e {
				t.Fatal(st)
			}
			verticalID(t, st)
			proc := verticalProcNext(t, rt)
			if mode == "transport-loss" {
				ends.client.link.setBlackhole()
			}
			// No request polls drive expiry. Wait for transport/owner termination first.
			p.ended(t, done, ends)
			verticalJoined(t, proc)
			if p.owner.live() || rt.starts.Load() != 1 {
				t.Fatal("grant survived or execution restarted")
			}
			verticalEventually(t, func() bool { rec, _ := c.request(80, "tools/list", nil); return rec.Code == http.StatusNotFound })
			c.sid = ""
			rec, _ := c.request(81, "initialize", map[string]any{"protocolVersion": mcpadapter.ProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "memory", "version": "1"}})
			if rec.Code != http.StatusOK || rec.Header().Get(agentgateway.HeaderSession) == "" {
				t.Fatalf("static discovery unavailable %d", rec.Code)
			}
			c.sid = rec.Header().Get(agentgateway.HeaderSession)
			c.request(nil, "notifications/initialized", nil)
			if list, _ := c.request(82, "tools/list", nil); list.Code != http.StatusOK {
				t.Fatal("catalog unavailable")
			}
			call, _ := c.request(83, "tools/call", map[string]any{"name": "rescue_start", "arguments": map[string]any{"request_id": "owned", "command": "sleep 20\n", "timeout_ms": 1000}})
			if call.Code != http.StatusNotFound || rt.starts.Load() != 1 || p.owner.live() {
				t.Fatal("discovery resumed ended grant", call.Code)
			}
			if ended, _ := c.request(84, "tools/list", nil); ended.Code != http.StatusNotFound {
				t.Fatal("discovery session retained after tool call")
			}

			if err := p.c.Run(context.Background()); !errors.Is(err, ErrUsed) {
				t.Fatal("client reconnected")
			}
		})
	}
}
