package agentchannel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"golang.org/x/crypto/ssh"
)

func TestPinnedHostAndEnrolledClientCarryAgentBytes(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	p := connect(t, env, context.Background(), quickBudgets(), quickBudgets())
	early := env.closedBeforeRevoke(p.sConn)

	clientKey := env.client.PublicKey().Marshal()
	if got := p.server.Peer().PublicKey(); !bytes.Equal(got, clientKey) {
		t.Fatal("server peer is not the enrolled client key")
	}
	if key, err := p.server.Peer().Key(); err != nil || !bytes.Equal(key.Marshal(), clientKey) {
		t.Fatalf("verified key for composition: %v", err)
	}
	if got := p.client.Peer().PublicKey(); !bytes.Equal(got, env.host.PublicKey().Marshal()) {
		t.Fatal("client peer is not the pinned host key")
	}
	copied := p.server.Peer().PublicKey()
	copied[0] ^= 0xff
	if !bytes.Equal(p.server.Peer().PublicKey(), clientKey) {
		t.Fatal("peer key is not an immutable copy")
	}
	if p.server.Binding() != env.binding || p.client.Binding() != env.binding {
		t.Fatal("binding metadata differs from the grant")
	}

	roundTrip(t, p.client, p.server, "agent frame from the client")
	roundTrip(t, p.server, p.client, "agent frame from the host")
	if env.revoked() || p.server.Reason() != ReasonNone {
		t.Fatal("live channel revoked its grant")
	}

	if err := p.client.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}
	if p.client.Reason() != ReasonClosed {
		t.Fatalf("local close reason %v", p.client.Reason())
	}
	// The server learns of it through its transport: the reason is recorded
	// before its own revoke, and Done closes only after that revoke.
	waitDone(t, p.server.Done(), 3*time.Second, "server teardown after the client left")
	if p.server.Reason() != ReasonTransport || !env.revoked() {
		t.Fatalf("reason %v revoked %v", p.server.Reason(), env.revoked())
	}
	if err := p.server.Close(); err != nil {
		t.Fatalf("server close: %v", err)
	}
	if early.Load() || !p.sConn.isClosed() {
		t.Fatal("transport closed before the grant was revoked, or not at all")
	}
}

// rejectedPair runs one handshake whose client config was altered and checks
// that both ends fail closed and the grant is revoked before the close.
func rejectedPair(t *testing.T, env *grantEnv, cfg ClientConfig) (client, server Reason) {
	t.Helper()
	sConn, cConn := memPair()
	early := env.closedBeforeRevoke(sConn)
	res := acceptAsync(context.Background(), env.newServer(t, quickBudgets()), sConn)
	ch, err := Open(context.Background(), cfg, cConn)
	if ch != nil || !errors.Is(err, ErrRejected) {
		t.Fatalf("Open = %v, %v; want rejection", ch, err)
	}
	r := await(t, res, 5*time.Second)
	if r.ch != nil || !errors.Is(r.err, ErrRejected) {
		t.Fatalf("Accept = %v, %v; want rejection", r.ch, r.err)
	}
	if !env.revoked() || early.Load() || !sConn.isClosed() || !cConn.isClosed() {
		t.Fatal("rejected handshake left the grant live or a conn open")
	}
	return reasonOf(err), reasonOf(r.err)
}

func TestWrongPinnedHostKeyIsRejected(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	cfg := env.clientConfig(quickBudgets())
	cfg.HostKey = fixtureSigner(t, 0x33).PublicKey()
	if c, s := rejectedPair(t, env, cfg); c != ReasonHandshake || s != ReasonHandshake {
		t.Fatalf("reasons %v/%v", c, s)
	}
}

func TestUnenrolledClientKeyIsRejected(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	cfg := env.clientConfig(quickBudgets())
	cfg.Signer = fixtureSigner(t, 0x44)
	if c, s := rejectedPair(t, env, cfg); c != ReasonHandshake || s != ReasonHandshake {
		t.Fatalf("reasons %v/%v", c, s)
	}
}

func TestLaunchSelectorMismatchIsRejected(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	cfg := env.clientConfig(quickBudgets())
	cfg.Binding.Grant.Identity.LaunchID = "another-launch"
	if c, s := rejectedPair(t, env, cfg); c != ReasonHandshake || s != ReasonHandshake {
		t.Fatalf("reasons %v/%v", c, s)
	}
}

func TestBindingMismatchRejectsAgentChannel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Binding)
	}{
		{"epoch", func(b *Binding) { b.Grant.Identity.Epoch = "1" }},
		{"generation", func(b *Binding) { b.Grant.Generation++ }},
		{"policy", func(b *Binding) { b.Grant.Identity.PolicyDigest = agentexec.FullPolicyDigest() }},
		{"key set digest", func(b *Binding) { b.KeySetDigest[0] ^= 1 }},
		{"profile", func(b *Binding) { b.Profile = agentexec.ProfileWindowsGuest }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newEnv(t, 30*time.Second)
			cfg := env.clientConfig(quickBudgets())
			tc.mutate(&cfg.Binding)
			if c, s := rejectedPair(t, env, cfg); c != ReasonRejected || s != ReasonRejected {
				t.Fatalf("reasons %v/%v", c, s)
			}
		})
	}
}

// fakeKey and fakeSigner stand for a non-Ed25519 key without generating one.
type fakeKey struct{ kind string }

func (k fakeKey) Type() string { return k.kind }

func (k fakeKey) Marshal() []byte { return ssh.Marshal(struct{ Kind string }{k.kind}) }

func (k fakeKey) Verify([]byte, *ssh.Signature) error { return ErrRejected }

func (k fakeKey) signer() ssh.Signer { return fakeSigner{k} }

type fakeSigner struct{ key fakeKey }

func (s fakeSigner) PublicKey() ssh.PublicKey { return s.key }

func (s fakeSigner) Sign(io.Reader, []byte) (*ssh.Signature, error) { return nil, ErrRejected }

func TestServerConfigRefusesUntrustedInputs(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	revoked := newEnv(t, 30*time.Second)
	if err := revoked.issuer.Revoke(revoked.binding.Grant); err != nil {
		t.Fatal(err)
	}
	ecdsa := fakeKey{"ecdsa-sha2-nistp256"}
	stranger := fixtureSigner(t, 0x55).PublicKey()
	for _, tc := range []struct {
		name   string
		mutate func(*ServerConfig)
	}{
		{"no session", func(c *ServerConfig) { c.Session = nil }},
		{"revoked grant", func(c *ServerConfig) { *c = revoked.serverConfig(quickBudgets()) }},
		{"no host key", func(c *ServerConfig) { c.HostKey = nil }},
		{"non-Ed25519 host key", func(c *ServerConfig) { c.HostKey = ecdsa.signer() }},
		{"empty key set", func(c *ServerConfig) { c.AllowedKeys = nil }},
		{"key set digest differs", func(c *ServerConfig) { c.AllowedKeys = []ssh.PublicKey{stranger} }},
		{"extra approved key", func(c *ServerConfig) {
			c.AllowedKeys = append(append([]ssh.PublicKey(nil), c.AllowedKeys...), stranger)
		}},
		{"non-Ed25519 approved key", func(c *ServerConfig) { c.AllowedKeys = []ssh.PublicKey{ecdsa} }},
		{"zero budgets", func(c *ServerConfig) { c.Budgets = Budgets{} }},
		{"handshake over max", func(c *ServerConfig) { c.Budgets.Handshake = MaxHandshake + 1 }},
		{"heartbeat interval under min", func(c *ServerConfig) { c.Budgets.HeartbeatInterval = MinHeartbeatInterval - 1 }},
		{"heartbeat timeout over max", func(c *ServerConfig) { c.Budgets.HeartbeatTimeout = MaxHeartbeatTimeout + 1 }},
		{"read stall within a heartbeat", func(c *ServerConfig) { c.Budgets.ReadStall = c.Budgets.HeartbeatInterval + c.Budgets.HeartbeatTimeout }},
		{"write stall over max", func(c *ServerConfig) { c.Budgets.WriteStall = MaxWriteStall + 1 }},
		{"cleanup over max", func(c *ServerConfig) { c.Budgets.Cleanup = MaxCleanup + 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := env.serverConfig(quickBudgets())
			tc.mutate(&cfg)
			if s, err := NewServer(cfg); s != nil || !errors.Is(err, ErrConfig) {
				t.Fatalf("NewServer = %v, %v", s, err)
			}
		})
	}
	if env.revoked() {
		t.Fatal("refused configuration revoked the grant")
	}
	// Refusals took no claim; the first valid Server takes the only one.
	if _, err := NewServer(env.serverConfig(quickBudgets())); err != nil {
		t.Fatalf("valid NewServer after refusals: %v", err)
	}
	if s, err := NewServer(env.serverConfig(quickBudgets())); s != nil || !errors.Is(err, ErrConfig) {
		t.Fatalf("second channel server for one Session = %v, %v", s, err)
	}
	if err := env.session.ClaimTransport(); !errors.Is(err, agentexec.ErrSessionClaimed) {
		t.Fatalf("transport claim after NewServer = %v", err)
	}
}

func TestClientConfigRefusesUnpinnedOrUnenrolledInputs(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	ecdsa := fakeKey{"ecdsa-sha2-nistp256"}
	for _, tc := range []struct {
		name   string
		mutate func(*ClientConfig)
	}{
		{"no pinned host key", func(c *ClientConfig) { c.HostKey = nil }},
		{"non-Ed25519 pinned host key", func(c *ClientConfig) { c.HostKey = ecdsa }},
		{"no signer", func(c *ClientConfig) { c.Signer = nil }},
		{"non-Ed25519 signer", func(c *ClientConfig) { c.Signer = ecdsa.signer() }},
		{"no deadline", func(c *ClientConfig) { c.Deadline = time.Time{} }},
		{"past deadline", func(c *ClientConfig) { c.Deadline = time.Now().Add(-time.Second) }},
		{"deadline over max lifetime", func(c *ClientConfig) { c.Deadline = time.Now().Add(MaxClientLifetime + time.Minute) }},
		{"zero generation", func(c *ClientConfig) { c.Binding.Grant.Generation = 0 }},
		{"no profile", func(c *ClientConfig) { c.Binding.Profile = 0 }},
		{"zero key set digest", func(c *ClientConfig) { c.Binding.KeySetDigest = [32]byte{} }},
		{"invalid budgets", func(c *ClientConfig) { c.Budgets.Cleanup = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := env.clientConfig(quickBudgets())
			tc.mutate(&cfg)
			_, conn := memPair()
			if ch, err := Open(context.Background(), cfg, conn); ch != nil || !errors.Is(err, ErrConfig) {
				t.Fatalf("Open = %v, %v", ch, err)
			}
			if !conn.isClosed() {
				t.Fatal("refused Open did not close the conn it owned")
			}
		})
	}
	_, conn := memPair()
	var noContext context.Context
	if ch, err := Open(noContext, env.clientConfig(quickBudgets()), conn); ch != nil || !errors.Is(err, ErrConfig) || !conn.isClosed() {
		t.Fatalf("nil context Open = %v, %v", ch, err)
	}
}

func TestAcceptIsSingleUseAndClosesOnlyItsOwnConn(t *testing.T) {
	env := newEnv(t, 30*time.Second)
	p := connect(t, env, context.Background(), quickBudgets(), quickBudgets())
	second, peer := memPair()
	if ch, err := p.srv.Accept(context.Background(), second); ch != nil || !errors.Is(err, ErrUsed) {
		t.Fatalf("second Accept = %v, %v", ch, err)
	}
	if !second.isClosed() || peer.closeCalls() != 0 || p.sConn.closeCalls() != 0 || p.cConn.closeCalls() != 0 {
		t.Fatal("second Accept closed a conn other than its own")
	}
	if env.revoked() {
		t.Fatal("second Accept revoked the live grant")
	}
	roundTrip(t, p.client, p.server, "still served")
}

func TestPeerProvenanceComesOnlyFromVerification(t *testing.T) {
	var zero Peer
	if key, err := zero.Key(); key != nil || err == nil || len(zero.PublicKey()) != 0 {
		t.Fatal("an unverified Peer must carry no key")
	}
	var nilChannel *Channel
	if err := nilChannel.Close(); !errors.Is(err, ErrConfig) {
		t.Fatalf("nil Close = %v", err)
	}
}
