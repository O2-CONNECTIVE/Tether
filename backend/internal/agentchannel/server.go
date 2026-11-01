package agentchannel

import (
	"bytes"
	"context"
	"net"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/auth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"golang.org/x/crypto/ssh"
)

// ServerConfig comes only from the trusted local owner. Session is the
// canonical agentexec.Session; the grant binding, key set digest, boundary,
// hard deadline, revocation signal and profile are read from the Authority it
// claimed (Session.Grant, Session.Done, Session.Profile). There is no separate
// Authority input, so an equal-looking Authority cannot be substituted.
// HostKey is the existing host signer, which this package never generates.
type ServerConfig struct {
	Session     *agentexec.Session
	AllowedKeys []ssh.PublicKey // the approved raw Ed25519 set; its KeySetDigest must equal the grant's
	HostKey     ssh.Signer
	Budgets     Budgets
}

// Server serves one grant on exactly one supplied connection.
type Server struct {
	session  *agentexec.Session
	binding  Binding
	deadline time.Time
	host     *fixedSigner
	keys     []ssh.PublicKey // canonical copies, parsed once
	allowed  map[string]struct{}
	expect   [32]byte
	budgets  Budgets

	mu   sync.Mutex
	used bool
}

// NewServer validates every input before it takes the Session's single
// transport claim, so a refused configuration leaves the Session unclaimed. A
// second Server for the same Session is refused: one channel per grant.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Session == nil || !cfg.Budgets.valid() {
		return nil, ErrConfig
	}
	grant := cfg.Session.Grant()
	profile := cfg.Session.Profile()
	binding := Binding{Grant: grant.Binding, KeySetDigest: grant.KeySetDigest, Profile: profile}
	if !binding.valid() || cfg.Session.Binding() != grant.Binding || grant.GuestBoundary != boundaryFor(profile) {
		return nil, ErrConfig
	}
	keys, err := CanonicalKeys(cfg.AllowedKeys)
	if err != nil {
		return nil, ErrConfig
	}
	allowed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		allowed[string(key.Marshal())] = struct{}{}
	}
	digest, err := runbackend.KeySetDigest(keys)
	if err != nil || digest != grant.KeySetDigest {
		return nil, ErrConfig
	}
	host, err := newFixedSigner(cfg.HostKey)
	if err != nil {
		return nil, ErrConfig
	}
	select {
	case <-cfg.Session.Done():
		return nil, ErrConfig
	default:
	}
	if !time.Now().Before(grant.HardDeadline) {
		return nil, ErrConfig
	}
	if err := cfg.Session.ClaimTransport(); err != nil {
		return nil, ErrConfig
	}
	return &Server{
		session: cfg.Session, binding: binding, deadline: grant.HardDeadline,
		host: host, keys: keys, allowed: allowed, expect: commitment(binding, host.public.Marshal()), budgets: cfg.Budgets,
	}, nil
}

// CanonicalKeys parses each approved key once into a private canonical raw
// Ed25519 copy. Trusted composition passes the same returned slice to the
// channel and to agentwire, so the digest and the membership checks compare
// exactly the same immutable values.
func CanonicalKeys(keys []ssh.PublicKey) ([]ssh.PublicKey, error) {
	if len(keys) == 0 {
		return nil, ErrConfig
	}
	out := make([]ssh.PublicKey, 0, len(keys))
	for _, key := range keys {
		parsed, err := rawEd25519(key)
		if err != nil {
			return nil, ErrConfig
		}
		out = append(out, parsed)
	}
	return out, nil
}

// ApprovedKeys returns copies of the canonical approved keys for composition.
func (s *Server) ApprovedKeys() []ssh.PublicKey {
	if s == nil {
		return nil
	}
	return append([]ssh.PublicKey(nil), s.keys...)
}

// Accept takes ownership of conn on every call with a non-nil conn, exactly
// once. ctx bounds the whole channel: its end tears the channel down. The
// first call binds the Session: every failure revokes it before closing conn,
// and the returned channel revokes it before closing on any teardown. A later
// call returns ErrUsed and closes only the conn it was given. A non-nil
// Channel with an error is the same owner whose cleanup is still unconfirmed;
// call its Close to retry. Nothing is redialed or retried here.
func (s *Server) Accept(ctx context.Context, conn net.Conn) (*Channel, error) {
	if conn == nil {
		return nil, ErrConfig
	}
	if s == nil || s.session == nil {
		_ = conn.Close()
		return nil, ErrConfig
	}
	s.mu.Lock()
	used := s.used
	s.used = true
	s.mu.Unlock()
	if used {
		_ = conn.Close()
		return nil, ErrUsed
	}
	c := newChannel(conn, s.budgets, s.deadline, s.binding, s.session)
	if ctx == nil {
		return c.fail(ReasonHandshake, ErrConfig)
	}
	revoked := c.revoked()
	admitted := make(chan struct{})
	c.spawn(func() { c.watchAdmission(ctx, revoked, admitted) })
	config, err := s.sshConfig(c)
	if err != nil {
		return c.fail(ReasonHandshake, ErrConfig)
	}
	if !c.live() {
		return c.fail(c.deadReason(), ErrRejected)
	}
	sc, chans, reqs, err := ssh.NewServerConn(c.raw, config)
	if err != nil {
		return c.fail(ReasonHandshake, ErrRejected)
	}
	transport := c.attach(sc, reqs)
	first := make(chan ssh.NewChannel, 1)
	c.spawn(func() { c.serveChannels(chans, first) })
	peer, ok := s.verifyPeer(sc)
	if !ok {
		return c.fail(ReasonRejected, ErrRejected)
	}
	var open ssh.NewChannel
	select {
	case open = <-first:
	case <-transport:
		return c.fail(ReasonTransport, ErrRejected)
	case <-c.stop:
		return c.fail(ReasonHandshake, ErrRejected)
	}
	switch {
	case open.ChannelType() != ChannelType:
		_ = open.Reject(ssh.UnknownChannelType, "only the agent channel is served")
		return c.fail(ReasonViolation, ErrRejected)
	case !bytes.Equal(open.ExtraData(), s.expect[:]):
		_ = open.Reject(ssh.Prohibited, "binding mismatch")
		return c.fail(ReasonRejected, ErrRejected)
	case !c.live():
		_ = open.Reject(ssh.Prohibited, "grant is not live")
		return c.fail(c.deadReason(), ErrRejected)
	}
	ch, chReqs, err := open.Accept()
	if err != nil {
		return c.fail(ReasonTransport, ErrRejected)
	}
	return c.admit(ctx, revoked, admitted, transport, ch, chReqs, peer)
}

// sshConfig reuses the existing raw Ed25519 authenticator (LaunchID user,
// fixed key set, liveness rechecked at query and after the signature) and
// records the verified key so Accept can check it independently.
func (s *Server) sshConfig(c *Channel) (*ssh.ServerConfig, error) {
	keys, err := auth.New(s.binding.Grant.Identity.LaunchID, s.keys)
	if err != nil {
		return nil, err
	}
	config, err := keys.ServerConfig(s.host, c.live)
	if err != nil {
		return nil, err
	}
	config.Config = algorithms()
	verified := config.VerifiedPublicKeyCallback
	config.VerifiedPublicKeyCallback = func(m ssh.ConnMetadata, key ssh.PublicKey, p *ssh.Permissions, algorithm string) (*ssh.Permissions, error) {
		if _, err := verified(m, key, p, algorithm); err != nil {
			return nil, err
		}
		raw := key.Marshal()
		if _, ok := s.allowed[string(raw)]; !ok {
			return nil, auth.ErrDenied
		}
		return &ssh.Permissions{Extensions: map[string]string{peerExtension: string(raw)}}, nil
	}
	return config, nil
}

// verifyPeer checks the authenticated key again, independently of the SSH
// callbacks: the LaunchID selector, raw Ed25519 type, exact membership, and
// that the approved set still has the grant's canonical KeySetDigest.
func (s *Server) verifyPeer(sc *ssh.ServerConn) (Peer, bool) {
	if sc.User() != s.binding.Grant.Identity.LaunchID || sc.Permissions == nil {
		return Peer{}, false
	}
	raw, ok := sc.Permissions.Extensions[peerExtension]
	if !ok {
		return Peer{}, false
	}
	key, err := ssh.ParsePublicKey([]byte(raw))
	if err != nil || key.Type() != ssh.KeyAlgoED25519 || string(key.Marshal()) != raw {
		return Peer{}, false
	}
	if _, ok := s.allowed[raw]; !ok {
		return Peer{}, false
	}
	grant := s.session.Grant()
	digest, err := runbackend.KeySetDigest(s.keys)
	if err != nil || digest != s.binding.KeySetDigest || digest != grant.KeySetDigest || grant.Binding != s.binding.Grant {
		return Peer{}, false
	}
	return newPeer([]byte(raw)), true
}
