package agentlink_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentlink"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"golang.org/x/crypto/ssh"
)

// The full source path under test: MCP stdio (io.Pipe) -> mcpadapter ->
// agentwire.Client -> agentchannel client -> encrypted SSH agent channel over a
// buffered memory conn -> agentchannel server -> agentwire.Server -> the same
// granted agentexec.Session -> a fake Mac current-user runtime. Keys are
// synthetic fixtures derived from fixed seeds; there is no socket, relay,
// shell, process, consent producer or real credential.

func fixtureSigner(t *testing.T, seed byte) ssh.Signer {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	return signer
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

func (p *fakeProc) Release(context.Context) error { return nil }

// macRuntime is a fake ProfiledRuntime for the Mac current-user profile. It
// starts nothing; each start hands a fake process to the test.
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

func budgets() agentchannel.Budgets {
	return agentchannel.Budgets{Handshake: 5 * time.Second, HeartbeatInterval: 20 * time.Millisecond, HeartbeatTimeout: 300 * time.Millisecond,
		ReadStall: 3 * time.Second, WriteStall: 2 * time.Second, Cleanup: 2 * time.Second}
}

type path struct {
	t         *testing.T
	issuer    *ephemeral.Issuer
	authority *supervisor.Authority
	session   *agentexec.Session
	runtime   *macRuntime
	hostConn  *memConn
	hostDone  chan error
	hostOnce  sync.Once
	hostErr   error

	connectErr error
	in         *io.PipeWriter
	lines      chan []byte
	mcpDone    chan error
	next       int
}

// grant issues one Mac current-user grant through the explicit current-user
// issuer and binds the canonical Session to the fake Mac runtime. The issuance
// stands in for the missing local consent producer; it is a test fixture.
func grant(t *testing.T, lifetime time.Duration, keys []ssh.PublicKey, runtime *macRuntime) (*ephemeral.Issuer, *supervisor.Authority, *agentexec.Session) {
	t.Helper()
	digest, err := runbackend.KeySetDigest(keys)
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
	session, err := agentexec.New(agentexec.Config{Issuer: issuer, Authority: authority, Runtime: runtime, Clock: time.Now, CleanupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = session.Close(ctx)
		_ = issuer.Close()
	})
	return issuer, authority, session
}

// newPath composes both ends with the production composition package. mutate
// alters only the client's dispatch to exercise rejections.
func newPath(t *testing.T, lifetime time.Duration, mutate func(*agentchannel.ClientConfig)) *path {
	t.Helper()
	host, client := fixtureSigner(t, 0x11), fixtureSigner(t, 0x22)
	keys := []ssh.PublicKey{client.PublicKey()}
	p := &path{t: t, runtime: &macRuntime{procs: make(chan *fakeProc, 4)}, hostDone: make(chan error, 1), lines: make(chan []byte, 64), mcpDone: make(chan error, 1)}
	p.issuer, p.authority, p.session = grant(t, lifetime, keys, p.runtime)
	h, err := agentlink.NewHost(agentlink.HostConfig{Session: p.session, AllowedKeys: keys, HostKey: host, Budgets: budgets()})
	if err != nil {
		t.Fatal(err)
	}
	sConn, cConn := memPair()
	p.hostConn = sConn
	go func() { p.hostDone <- h.Serve(context.Background(), sConn) }()

	g := p.authority.Grant()
	cfg := agentchannel.ClientConfig{
		Binding: agentchannel.Binding{Grant: g.Binding, KeySetDigest: g.KeySetDigest, Profile: agentexec.ProfileMacCurrentUser},
		HostKey: host.PublicKey(), Signer: client, Deadline: g.HardDeadline, Budgets: budgets(),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	connect := agentlink.Connector(cfg, cConn)
	rw, err := connect(context.Background())
	if err != nil {
		p.connectErr = err
		return p
	}
	if _, again := connect(context.Background()); again == nil {
		t.Fatal("the connector produced a second channel")
	}
	wire, err := agentwire.NewClient(rw)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p.in = inW
	go func() {
		scanner := bufio.NewScanner(outR)
		scanner.Buffer(make([]byte, 64<<10), 4<<20)
		for scanner.Scan() {
			p.lines <- append([]byte(nil), scanner.Bytes()...)
		}
		close(p.lines)
	}()
	go func() {
		err := mcpadapter.Serve(context.Background(), mcpadapter.Config{Backend: wire}, inR, outW)
		_ = outW.Close()
		p.mcpDone <- err
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		go func() {
			for range p.lines {
			}
		}()
		select {
		case <-p.mcpDone:
		case <-time.After(3 * time.Second):
			t.Error("MCP Serve did not return")
		}
		_ = wire.Close()
		_ = rw.Close()
		p.waitHost()
	})
	p.send(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	p.recv()
	p.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return p
}

func (p *path) waitHost() error {
	p.hostOnce.Do(func() {
		select {
		case p.hostErr = <-p.hostDone:
		case <-time.After(5 * time.Second):
			p.hostErr = errors.New("host Serve did not return")
		}
	})
	return p.hostErr
}

func (p *path) send(line string) {
	p.t.Helper()
	if _, err := io.WriteString(p.in, line+"\n"); err != nil {
		p.t.Fatalf("stdin: %v", err)
	}
}

func (p *path) recv() map[string]any {
	p.t.Helper()
	select {
	case line, ok := <-p.lines:
		if !ok {
			p.t.Fatal("stdout closed")
		}
		var msg map[string]any
		if err := json.Unmarshal(line, &msg); err != nil {
			p.t.Fatalf("stdout line %q: %v", line, err)
		}
		return msg
	case <-time.After(5 * time.Second):
		p.t.Fatal("no MCP response")
		return nil
	}
}

func (p *path) tool(name string, args map[string]any) (map[string]any, bool) {
	p.t.Helper()
	p.next++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": p.next, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	p.send(string(b))
	resp := p.recv()
	result, ok := resp["result"].(map[string]any)
	if !ok || resp["id"] != float64(p.next) {
		p.t.Fatalf("%s: %v", name, resp)
	}
	return result["structuredContent"].(map[string]any), result["isError"].(bool)
}

func (p *path) start(id, command string) (map[string]any, bool) {
	return p.tool("rescue_start", map[string]any{"request_id": id, "command": command, "timeout_ms": 60000})
}

func (p *path) proc() *fakeProc {
	p.t.Helper()
	select {
	case proc := <-p.runtime.procs:
		return proc
	case <-time.After(5 * time.Second):
		p.t.Fatal("the fake shell was not started")
		return nil
	}
}

func (p *path) awaitTerminal(id string) map[string]any {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, isErr := p.tool("rescue_status", map[string]any{"command_id": id})
		if isErr {
			p.t.Fatalf("rescue_status: %v", st)
		}
		if st["terminal"] == true {
			return st
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("command did not finish: %v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ownerStatus reads the Session directly, as the protected owner can after the
// channel is gone; the agent has no status path once the channel is lost.
func (p *path) ownerStatus(id string) agentexec.Status {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := p.session.Status(agentexec.StatusRequest{Version: agentexec.Version, Binding: p.session.Binding(), CommandID: id})
		if err != nil {
			p.t.Fatal(err)
		}
		if st.Terminal || time.Now().After(deadline) {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (p *path) revokedBeforeTerminate(proc *fakeProc) <-chan supervisor.State {
	observed := make(chan supervisor.State, 1)
	proc.mu.Lock()
	proc.onTerminate = func() { observed <- p.authority.Snapshot(time.Now()).State }
	proc.mu.Unlock()
	return observed
}

func emit(t *testing.T, proc *fakeProc, s agentexec.Stream, data string) {
	t.Helper()
	select {
	case proc.events <- event{stream: s, data: []byte(data)}:
	case <-time.After(5 * time.Second):
		t.Fatal("output was not read")
	}
}

func commandID(t *testing.T, st map[string]any) string {
	t.Helper()
	id, _ := st["command_id"].(string)
	if !agentwire.ValidCommandID(id) {
		t.Fatalf("command_id = %v", st)
	}
	return id
}

// Scenario 1: fixed output through the whole encrypted path, with explicit
// request_id idempotency and mismatch refusal.
func TestFixedOutputAndRequestIDIdempotencyOverTheChannel(t *testing.T) {
	p := newPath(t, 30*time.Second, nil)
	st, isErr := p.start("req-1", "printf 'hello\\n'\n")
	if isErr || st["profile"] != "mac_current_user" {
		t.Fatalf("rescue_start = %v", st)
	}
	id := commandID(t, st)
	proc := p.proc()
	p.runtime.mu.Lock()
	stdin := string(p.runtime.stdin[0])
	p.runtime.mu.Unlock()
	if stdin != "printf 'hello\\n'\n" {
		t.Fatalf("shell stdin = %q", stdin)
	}
	if again, isErr := p.start("req-1", "printf 'hello\\n'\n"); isErr || commandID(t, again) != id {
		t.Fatalf("same request_id replay = %v", again)
	}
	if res, isErr := p.start("req-1", "id -un\n"); !isErr || res["error"] != "request_mismatch" {
		t.Fatalf("mismatched replay = %v", res)
	}
	emit(t, proc, agentexec.Stdout, "hello\n")
	close(proc.events)
	close(proc.exited)
	final := p.awaitTerminal(id)
	if final["phase"] != "exited" || final["cleanup"] != "confirmed" || final["exit"].(map[string]any)["code"] != 0.0 {
		t.Fatalf("final = %v", final)
	}
	out, isErr := p.tool("rescue_output", map[string]any{"command_id": id, "cursor": 0, "max_bytes": 1024})
	chunks, _ := out["chunks"].([]any)
	if isErr || len(chunks) != 1 || chunks[0].(map[string]any)["data"] != "hello\n" || out["complete"] != true {
		t.Fatalf("rescue_output = %v", out)
	}
	if n := p.runtime.startCount(); n != 1 {
		t.Fatalf("shell starts = %d", n)
	}
	if !p.issuer.Snapshot().HasAuthority {
		t.Fatal("a finished command revoked the grant")
	}
}

// Scenario 2: explicit cancel. Accepting the cancel and the owned wait for
// cleanup are reported separately; the grant stays live.
func TestExplicitCancelOverTheChannel(t *testing.T) {
	p := newPath(t, 30*time.Second, nil)
	st, _ := p.start("req-1", "sleep 60\n")
	id := commandID(t, st)
	p.proc()
	canceled, isErr := p.tool("rescue_cancel", map[string]any{"command_id": id})
	if isErr || canceled["stop"] != "canceled" {
		t.Fatalf("cancel accepted = %v", canceled)
	}
	final := p.awaitTerminal(id)
	if final["phase"] != "canceled" || final["cleanup"] != "confirmed" || final["exit"].(map[string]any)["known"] != false {
		t.Fatalf("owned cleanup after cancel = %v", final)
	}
	if !p.issuer.Snapshot().HasAuthority {
		t.Fatal("command cancel revoked the grant")
	}
}

// Scenario 3: the owned channel is lost (a half-open relay). Heartbeats end
// both ends; the grant is revoked before the shell is stopped, the host joins
// and confirms cleanup, MCP stops, and nothing reconnects or restarts.
func TestOwnedChannelLossRevokesThenCleansUp(t *testing.T) {
	p := newPath(t, 30*time.Second, nil)
	st, _ := p.start("req-1", "sleep 60\n")
	id := commandID(t, st)
	proc := p.proc()
	observed := p.revokedBeforeTerminate(proc)
	p.hostConn.link.setBlackhole()
	if err := p.waitHost(); errors.Is(err, agentchannel.ErrCleanupUnknown) || errors.Is(err, agentexec.ErrCleanupUnknown) || err == nil {
		t.Fatalf("host Serve after channel loss = %v", err)
	}
	if state := <-observed; state != supervisor.Revoked {
		t.Fatalf("shell stopped while the grant was %s", state)
	}
	final := p.ownerStatus(id)
	if final.Stop != agentexec.StopTransportLost || final.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("owner status = %+v", final)
	}
	select {
	case err := <-p.mcpDone:
		p.mcpDone <- err
		if !errors.Is(err, mcpadapter.ErrChannelClosed) {
			t.Fatalf("MCP Serve = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MCP kept serving a lost channel")
	}
	if p.issuer.Snapshot().HasAuthority || p.runtime.startCount() != 1 || !p.hostConn.isClosed() {
		t.Fatal("lost channel left the grant live, restarted the shell or kept the conn")
	}
}

// Scenario 4: a short hard deadline with no further requests ends the grant
// with StopHardDeadline, revokes before stopping the shell and is joined.
func TestShortHardDeadlineWithoutRequests(t *testing.T) {
	p := newPath(t, 1500*time.Millisecond, nil)
	st, _ := p.start("req-1", "sleep 60\n")
	id := commandID(t, st)
	proc := p.proc()
	observed := p.revokedBeforeTerminate(proc)
	if err := p.waitHost(); errors.Is(err, agentchannel.ErrCleanupUnknown) || errors.Is(err, agentexec.ErrCleanupUnknown) || err == nil {
		t.Fatalf("host Serve at the deadline = %v", err)
	}
	if state := <-observed; state != supervisor.Revoked {
		t.Fatalf("shell stopped while the grant was %s", state)
	}
	if final := p.ownerStatus(id); final.Stop != agentexec.StopHardDeadline || final.Cleanup != agentexec.CleanupConfirmed {
		t.Fatalf("owner status = %+v", final)
	}
	if p.issuer.Snapshot().HasAuthority || p.runtime.startCount() != 1 {
		t.Fatal("the deadline left the grant live or restarted the shell")
	}
	if !p.hostConn.isClosed() {
		t.Fatal("deadline did not close host conn")
	}
	select {
	case err := <-p.mcpDone:
		p.mcpDone <- err
		if !errors.Is(err, mcpadapter.ErrChannelClosed) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MCP survived hard deadline")
	}

}

// Wrong pin, unenrolled key and every binding mismatch are refused before any
// command: the grant is revoked, the conn closed and nothing started.
func TestRejectedDispatchesStartNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*agentchannel.ClientConfig)
	}{
		{"wrong pinned host key", func(c *agentchannel.ClientConfig) { c.HostKey = fixtureSigner(t, 0x33).PublicKey() }},
		{"unenrolled client key", func(c *agentchannel.ClientConfig) { c.Signer = fixtureSigner(t, 0x44) }},
		{"other epoch", func(c *agentchannel.ClientConfig) { c.Binding.Grant.Identity.Epoch = "1" }},
		{"other generation", func(c *agentchannel.ClientConfig) { c.Binding.Grant.Generation++ }},
		{"other key set", func(c *agentchannel.ClientConfig) { c.Binding.KeySetDigest[0] ^= 1 }},
		{"Windows profile", func(c *agentchannel.ClientConfig) { c.Binding.Profile = agentexec.ProfileWindowsGuest }},
		{"Windows policy", func(c *agentchannel.ClientConfig) {
			c.Binding.Grant.Identity.PolicyDigest = agentexec.FullPolicyDigest()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPath(t, 30*time.Second, tc.mutate)
			if !errors.Is(p.connectErr, agentchannel.ErrRejected) {
				t.Fatalf("connect = %v", p.connectErr)
			}
			if err := p.waitHost(); !errors.Is(err, agentchannel.ErrRejected) || errors.Is(err, agentexec.ErrCleanupUnknown) {
				t.Fatalf("host Serve = %v", err)
			}
			if p.issuer.Snapshot().HasAuthority || p.runtime.startCount() != 0 || !p.hostConn.isClosed() {
				t.Fatal("a rejected dispatch left the grant live, started a shell or kept the conn")
			}
		})
	}
}

// A peer with the enrolled key that asks for a session, exec or a TCP forward
// gets nothing: there is no shell or forwarding fallback, and the grant ends.
func TestNoShellExecOrForwardFallback(t *testing.T) {
	for _, kind := range []string{"session", "direct-tcpip"} {
		t.Run(kind, func(t *testing.T) {
			host, client := fixtureSigner(t, 0x11), fixtureSigner(t, 0x22)
			keys := []ssh.PublicKey{client.PublicKey()}
			runtime := &macRuntime{procs: make(chan *fakeProc, 1)}
			issuer, authority, session := grant(t, 30*time.Second, keys, runtime)
			h, err := agentlink.NewHost(agentlink.HostConfig{Session: session, AllowedKeys: keys, HostKey: host, Budgets: budgets()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := agentlink.NewHost(agentlink.HostConfig{Session: session, AllowedKeys: keys, HostKey: host, Budgets: budgets()}); err == nil {
				t.Fatal("a second host bound the same Session")
			}
			sConn, cConn := memPair()
			done := make(chan error, 1)
			go func() { done <- h.Serve(context.Background(), sConn) }()
			raw, chans, reqs, err := ssh.NewClientConn(cConn, "fixture", &ssh.ClientConfig{
				User: authority.Grant().Binding.Identity.LaunchID, Auth: []ssh.AuthMethod{ssh.PublicKeys(client)},
				HostKeyCallback: ssh.FixedHostKey(host.PublicKey()),
			})
			if err != nil {
				t.Fatalf("raw handshake: %v", err)
			}
			defer raw.Close()
			go ssh.DiscardRequests(reqs)
			go func() {
				for open := range chans {
					_ = open.Reject(ssh.Prohibited, "fixture")
				}
			}()
			ch, _, err := raw.OpenChannel(kind, nil)
			if err == nil {
				_, _ = ch.SendRequest("exec", true, ssh.Marshal(struct{ Command string }{"id"}))
				t.Fatalf("%s channel was accepted", kind)
			}
			select {
			case err := <-done:
				if !errors.Is(err, agentchannel.ErrRejected) {
					t.Fatalf("host Serve = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("host Serve did not end")
			}
			if issuer.Snapshot().HasAuthority || runtime.startCount() != 0 {
				t.Fatal("a refused channel left the grant live or started a shell")
			}
		})
	}
}

func TestCompositionRefusesMissingInputs(t *testing.T) {
	if _, err := agentlink.NewHost(agentlink.HostConfig{}); !errors.Is(err, agentlink.ErrConfig) {
		t.Fatalf("NewHost without a Session = %v", err)
	}
	var h *agentlink.Host
	a, _ := memPair()
	if err := h.Serve(context.Background(), a); !errors.Is(err, agentlink.ErrConfig) || !a.isClosed() {
		t.Fatalf("nil host Serve = %v", err)
	}
	connect := agentlink.Connector(agentchannel.ClientConfig{}, nil)
	if rw, err := connect(context.Background()); rw != nil || err == nil {
		t.Fatal("a connector without a conn produced a channel")
	}
}
