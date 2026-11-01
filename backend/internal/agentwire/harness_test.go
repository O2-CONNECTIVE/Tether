package agentwire_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"golang.org/x/crypto/ssh"
)

// Everything below is memory-only: net.Pipe streams, a fake GuestRuntime and
// a manual clock. No socket, process, shell, account or OS object is created,
// and the memory stream is not an authenticated transport.

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
	terminates  int
	releases    int
	onTerminate func()
}

func newFakeProc() *fakeProc {
	return &fakeProc{events: make(chan event), exited: make(chan struct{}), killed: make(chan struct{})}
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
	p.terminates++
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
	p.releases++
	return nil
}

func (p *fakeProc) setTerminateHook(f func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onTerminate = f
}

type fakeRuntime struct {
	mu     sync.Mutex
	starts int
	stdin  [][]byte
	procs  chan *fakeProc
}

func (r *fakeRuntime) StartShell(_ context.Context, _ supervisor.Binding, stdin []byte) (agentexec.GuestProcess, error) {
	r.mu.Lock()
	r.starts++
	r.stdin = append(r.stdin, append([]byte(nil), stdin...))
	r.mu.Unlock()
	p := newFakeProc()
	r.procs <- p
	return p, nil
}

func (r *fakeRuntime) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

// gateConn holds server writes while armed, modelling a reply that is delayed
// or lost after the request was already handled.
type gateConn struct {
	net.Conn
	mu     sync.Mutex
	hold   chan struct{}
	closed chan struct{}
	once   sync.Once
	// onClose runs once before the stream is closed, to observe ordering.
	onClose atomic.Pointer[func()]
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
	g.once.Do(func() {
		if hook := g.onClose.Load(); hook != nil {
			(*hook)()
		}
		close(g.closed)
	})
	return g.Conn.Close()
}

// countConn counts client frames; writeFrame issues one Write per frame.
type countConn struct {
	net.Conn
	writes atomic.Int32
}

func (c *countConn) Write(b []byte) (int, error) {
	c.writes.Add(1)
	return c.Conn.Write(b)
}

type rig struct {
	t         *testing.T
	clock     *testClock
	issuer    *ephemeral.Issuer
	authority *supervisor.Authority
	runtime   *fakeRuntime
	session   *agentexec.Session
	server    *agentwire.Server
	gate      *gateConn
	peer      net.Conn // client end of the stream
	client    *agentwire.Client
	counted   *countConn
	lost      chan struct{} // the owner's liveness signal

	cancelServe context.CancelFunc
	served      chan error
	serveOnce   sync.Once
	serveErr    error
}

// fixtureKey is a deterministic public Ed25519 key fixture. No private key is
// generated or held; the bytes only need the raw Ed25519 public key length.
func fixtureKey(t *testing.T, b byte) ssh.PublicKey {
	t.Helper()
	key, err := ssh.NewPublicKey(ed25519.PublicKey(bytes.Repeat([]byte{b}, ed25519.PublicKeySize)))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// approvedKeys are the grant's approved keys; the channel peer is the first.
func approvedKeys(t *testing.T) []ssh.PublicKey {
	return []ssh.PublicKey{fixtureKey(t, 1), fixtureKey(t, 2)}
}

// macRuntime declares the Mac current-user profile for the same fake.
type macRuntime struct{ *fakeRuntime }

func (macRuntime) AgentProfile() agentexec.Profile { return agentexec.ProfileMacCurrentUser }

// newGrant runs the real issuer path of the profile with the approved agent
// policy and the key set digest of approvedKeys.
func newGrant(t *testing.T, clock *testClock, profile agentexec.Profile) (*ephemeral.Issuer, *supervisor.Authority) {
	t.Helper()
	digest, err := runbackend.KeySetDigest(approvedKeys(t))
	if err != nil {
		t.Fatal(err)
	}
	newIssuer, policy, boundary := ephemeral.New, agentexec.FullPolicyDigest(), supervisor.SeparatedNonAdminGuest
	if profile == agentexec.ProfileMacCurrentUser {
		newIssuer, policy, boundary = ephemeral.NewCurrentUser, agentexec.MacCurrentUserPolicyDigest(), supervisor.CurrentUserNoIsolation
	}
	issuer, err := newIssuer()
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := issuer.Prepare(ephemeral.ApprovedPolicy{
		PolicyDigest: policy, KeySetDigest: digest, GuestBoundary: boundary,
		HardDeadline: clock.Now().Add(time.Hour), BackendLeaseCeiling: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := issuer.Launch(context.Background(), challenge, clock.Now, func(context.Context, *supervisor.Authority) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return issuer, authority
}

// newSessionRig composes issuer Authority -> Session for the profile, without
// any Server.
func newSessionRig(t *testing.T, profile agentexec.Profile) *rig {
	t.Helper()
	r := &rig{t: t, clock: &testClock{now: time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)}, runtime: &fakeRuntime{procs: make(chan *fakeProc, 8)}, lost: make(chan struct{})}
	r.issuer, r.authority = newGrant(t, r.clock, profile)
	var runtime agentexec.GuestRuntime = r.runtime
	if profile == agentexec.ProfileMacCurrentUser {
		runtime = macRuntime{r.runtime}
	}
	session, err := agentexec.New(agentexec.Config{Issuer: r.issuer, Authority: r.authority, Runtime: runtime, Clock: r.clock.Now, CleanupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	r.session = session
	t.Cleanup(func() {
		ctx, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		_ = session.Close(ctx)
	})
	return r
}

func (r *rig) config() agentwire.ServerConfig {
	return agentwire.ServerConfig{Session: r.session, Peer: approvedKeys(r.t)[0], ApprovedKeys: approvedKeys(r.t), Liveness: r.lost}
}

// newServed composes the trusted side: issuer Authority -> Session -> Server,
// serving the server end of a memory stream. The test owns the peer end.
func newServed(t *testing.T, profile agentexec.Profile) *rig {
	t.Helper()
	r := newSessionRig(t, profile)
	session := r.session
	var err error
	if r.server, err = agentwire.NewServer(r.config()); err != nil {
		t.Fatal(err)
	}
	serverEnd, peer := net.Pipe()
	r.gate, r.peer = &gateConn{Conn: serverEnd, closed: make(chan struct{})}, peer
	ctx, cancel := context.WithCancel(context.Background())
	r.cancelServe, r.served = cancel, make(chan error, 1)
	go func() { r.served <- r.server.Serve(ctx, r.gate) }()
	t.Cleanup(func() {
		r.gate.Release()
		if r.client != nil {
			_ = r.client.Close()
		}
		_ = peer.Close()
		cancel()
		if err := r.waitServed(); err != nil && errors.Is(err, errServeHung) {
			t.Error(err)
		}
		ctx, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		_ = session.Close(ctx)
	})
	return r
}

// newRig adds the Client side on the peer end.
func newRig(t *testing.T, profile agentexec.Profile) *rig {
	t.Helper()
	r := newServed(t, profile)
	r.counted = &countConn{Conn: r.peer}
	client, err := agentwire.NewClient(r.counted)
	if err != nil {
		t.Fatal(err)
	}
	r.client = client
	return r
}

var errServeHung = errors.New("Serve did not return")

func (r *rig) waitServed() error {
	r.serveOnce.Do(func() {
		select {
		case r.serveErr = <-r.served:
		case <-time.After(2 * time.Second):
			r.serveErr = errServeHung
		}
	})
	return r.serveErr
}

func (r *rig) proc() *fakeProc {
	r.t.Helper()
	select {
	case p := <-r.runtime.procs:
		return p
	case <-time.After(2 * time.Second):
		r.t.Fatal("guest shell was not started")
		return nil
	}
}

func (r *rig) awaitTerminal(id string) agentexec.Status {
	r.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		st, err := r.session.Status(agentexec.StatusRequest{Version: agentexec.Version, Binding: r.session.Binding(), CommandID: id})
		if err != nil {
			r.t.Fatalf("owner Status: %v", err)
		}
		if st.Terminal {
			return st
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("command did not finish: %+v", st)
		}
		time.Sleep(time.Millisecond)
	}
}

func send(t *testing.T, p *fakeProc, s agentexec.Stream, data []byte) {
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
