package agentchannel

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"golang.org/x/crypto/ssh"
)

// fixtureSigner derives a synthetic, deterministic Ed25519 signer from a
// repeated seed byte. It is a test fixture only: not production key issuance,
// enrollment or a host key, and no local key file is read.
func fixtureSigner(t *testing.T, seed byte) ssh.Signer {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

// macRuntime declares the Mac current-user profile and never starts a shell;
// channel tests exercise transport, not command execution.
type macRuntime struct{}

func (macRuntime) StartShell(context.Context, supervisor.Binding, []byte) (agentexec.GuestProcess, error) {
	return nil, agentexec.ErrNotStarted
}
func (macRuntime) AgentProfile() agentexec.Profile { return agentexec.ProfileMacCurrentUser }

type grantEnv struct {
	issuer    *ephemeral.Issuer
	authority *supervisor.Authority
	session   *agentexec.Session
	host      ssh.Signer
	client    ssh.Signer
	keys      []ssh.PublicKey
	binding   Binding
	deadline  time.Time
}

// newEnv issues one Mac current-user grant through the existing in-memory
// issuer and binds a real agentexec.Session to it.
func newEnv(t *testing.T, lifetime time.Duration) *grantEnv {
	t.Helper()
	host, client := fixtureSigner(t, 0x11), fixtureSigner(t, 0x22)
	keys := []ssh.PublicKey{client.PublicKey()}
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
	session, err := agentexec.New(agentexec.Config{Issuer: issuer, Authority: authority, Runtime: macRuntime{}, Clock: time.Now, CleanupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = session.Close(ctx)
		_ = issuer.Close()
	})
	grant := authority.Grant()
	return &grantEnv{
		issuer: issuer, authority: authority, session: session, host: host, client: client, keys: keys,
		binding: Binding{Grant: grant.Binding, KeySetDigest: digest, Profile: agentexec.ProfileMacCurrentUser}, deadline: grant.HardDeadline,
	}
}

func (e *grantEnv) serverConfig(b Budgets) ServerConfig {
	return ServerConfig{Session: e.session, AllowedKeys: e.keys, HostKey: e.host, Budgets: b}
}

func (e *grantEnv) newServer(t *testing.T, b Budgets) *Server {
	t.Helper()
	s, err := NewServer(e.serverConfig(b))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *grantEnv) clientConfig(b Budgets) ClientConfig {
	return ClientConfig{Binding: e.binding, HostKey: e.host.PublicKey(), Signer: e.client, Deadline: e.deadline, Budgets: b}
}

// extra is the binding commitment a correct client sends with the channel.
func (e *grantEnv) extra() []byte {
	c := commitment(e.binding, e.host.PublicKey().Marshal())
	return c[:]
}

func (e *grantEnv) revoked() bool { return isDone(e.authority.Done()) }

// closedBeforeRevoke reports whether conn was ever closed while the grant was
// still live, which would break the revoke-before-close order.
func (e *grantEnv) closedBeforeRevoke(conn *memConn) *atomic.Bool {
	var early atomic.Bool
	conn.setOnClose(func() {
		if !e.revoked() {
			early.Store(true)
		}
	})
	return &early
}

func quickBudgets() Budgets {
	return Budgets{Handshake: 5 * time.Second, HeartbeatInterval: 20 * time.Millisecond, HeartbeatTimeout: time.Second, ReadStall: 3 * time.Second, WriteStall: 2 * time.Second, Cleanup: 2 * time.Second}
}

// quietBudgets keep our own heartbeats out of a scripted raw peer's way.
func quietBudgets() Budgets {
	return Budgets{Handshake: 5 * time.Second, HeartbeatInterval: 10 * time.Second, HeartbeatTimeout: time.Second, ReadStall: 20 * time.Second, WriteStall: 2 * time.Second, Cleanup: 2 * time.Second}
}

func isDone(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func waitDone(t *testing.T, ch <-chan struct{}, within time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(within):
		t.Fatalf("%s did not happen within %v", what, within)
	}
}

type result struct {
	ch  *Channel
	err error
}

func acceptAsync(ctx context.Context, s *Server, conn *memConn) <-chan result {
	out := make(chan result, 1)
	go func() {
		ch, err := s.Accept(ctx, conn)
		out <- result{ch, err}
	}()
	return out
}

func await(t *testing.T, res <-chan result, within time.Duration) result {
	t.Helper()
	select {
	case r := <-res:
		return r
	case <-time.After(within):
		t.Fatalf("Accept did not return within %v", within)
		return result{}
	}
}

func reasonOf(err error) Reason {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return ReasonNone
}

type pair struct {
	srv            *Server
	server, client *Channel
	sConn, cConn   *memConn
}

func connect(t *testing.T, env *grantEnv, ctx context.Context, sb, cb Budgets) pair {
	t.Helper()
	sConn, cConn := memPair()
	srv := env.newServer(t, sb)
	res := acceptAsync(ctx, srv, sConn)
	client, err := Open(context.Background(), env.clientConfig(cb), cConn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	r := await(t, res, 5*time.Second)
	if r.err != nil {
		t.Fatalf("Accept: %v", r.err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = r.ch.Close()
	})
	return pair{srv: srv, server: r.ch, client: client, sConn: sConn, cConn: cConn}
}

// rawClient is a scripted peer built directly on the SSH library with the
// enrolled fixture key, used to send what a correct client never sends.
func rawClient(t *testing.T, env *grantEnv, conn *memConn, answer bool) ssh.Conn {
	t.Helper()
	auth := []ssh.AuthMethod{ssh.PublicKeys(env.client)}
	cfg := &ssh.ClientConfig{User: env.binding.Grant.Identity.LaunchID, Auth: auth, HostKeyCallback: ssh.FixedHostKey(env.host.PublicKey())}
	sc, chans, reqs, err := ssh.NewClientConn(conn, "fixture", cfg)
	if err != nil {
		t.Fatalf("raw handshake: %v", err)
	}
	go func() {
		for open := range chans {
			_ = open.Reject(ssh.Prohibited, "fixture")
		}
	}()
	go func() {
		for req := range reqs {
			_ = req.Reply(answer && req.Type == HeartbeatRequest, nil)
		}
	}()
	t.Cleanup(func() { _ = sc.Close() })
	return sc
}

type rawPair struct {
	raw    ssh.Conn
	ch     ssh.Channel
	server *Channel
	sConn  *memConn
	early  *atomic.Bool // the server conn closed while the grant was live
}

// rawAdmitted opens the correct agent channel from a raw peer.
func rawAdmitted(t *testing.T, env *grantEnv, b Budgets, answer bool) rawPair {
	t.Helper()
	sConn, cConn := memPair()
	early := env.closedBeforeRevoke(sConn)
	res := acceptAsync(context.Background(), env.newServer(t, b), sConn)
	raw := rawClient(t, env, cConn, answer)
	ch, reqs, err := raw.OpenChannel(ChannelType, env.extra())
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	go ssh.DiscardRequests(reqs)
	r := await(t, res, 5*time.Second)
	if r.err != nil {
		t.Fatalf("Accept: %v", r.err)
	}
	t.Cleanup(func() { _ = r.ch.Close() })
	return rawPair{raw: raw, ch: ch, server: r.ch, sConn: sConn, early: early}
}

// endsWith waits for the server channel to end and checks the reason, the
// revocation and the revoke-before-close order on its own conn.
func endsWith(t *testing.T, env *grantEnv, p rawPair, want Reason) {
	t.Helper()
	waitDone(t, p.server.Done(), 3*time.Second, "server teardown")
	if got := p.server.Reason(); got != want {
		t.Fatalf("reason %v, want %v", got, want)
	}
	if err := p.server.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !env.revoked() || p.early.Load() || !p.sConn.isClosed() {
		t.Fatal("grant not revoked before the owned conn closed")
	}
}

func roundTrip(t *testing.T, from io.Writer, to io.Reader, payload string) {
	t.Helper()
	errs := make(chan error, 1)
	go func() {
		_, err := from.Write([]byte(payload))
		errs <- err
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(to, got); err != nil || string(got) != payload {
		t.Fatalf("read %q, %v; want %q", got, err, payload)
	}
	if err := <-errs; err != nil {
		t.Fatalf("write: %v", err)
	}
}

func (c *Channel) answeredBeats() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.answered
}
