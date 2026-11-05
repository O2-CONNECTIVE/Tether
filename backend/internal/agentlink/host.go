// Package agentlink is the trusted local composition of the agent path. The
// host side binds one granted agentexec.Session to one authenticated
// agentchannel connection and one agentwire server; the client side opens the
// channel and hands it to agentwire. It uses only the net.Conn, signer and
// pinned host key that its owner supplies: it never dials, listens, generates
// or enrolls keys, issues or approves grants, retries or reconnects.
package agentlink

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"golang.org/x/crypto/ssh"
)

var ErrConfig = errors.New("agentlink: untrusted or incomplete composition")

// HostConfig is chosen by the protected owner. Session is the canonical
// Session of an approved grant; its Authority, key set digest and profile are
// the only grant inputs. AllowedKeys are the approved raw Ed25519 public keys,
// HostKey the existing host signer.
type HostConfig struct {
	Session     *agentexec.Session
	AllowedKeys []ssh.PublicKey
	HostKey     ssh.Signer
	Budgets     agentchannel.Budgets
}

// Host serves one granted Session over exactly one supplied connection.
type Host struct {
	mu                   sync.Mutex
	held                 *agentchannel.Channel
	accepting            bool
	authenticated        bool
	statusClosing        bool
	started              bool
	finished             chan struct{}
	serveCleanupErr      error
	serveCleanupObserved bool
	session              *agentexec.Session
	channel              *agentchannel.Server
	budget               agentchannel.Budgets
}

// NewHost takes the Session's single transport claim through
// agentchannel.NewServer. The wire claim is taken in Serve once the peer key
// is proven, so it is bound to exactly that verified key.
func NewHost(cfg HostConfig) (*Host, error) {
	if cfg.Session == nil {
		return nil, ErrConfig
	}
	server, err := agentchannel.NewServer(agentchannel.ServerConfig{
		Session: cfg.Session, AllowedKeys: cfg.AllowedKeys, HostKey: cfg.HostKey, Budgets: cfg.Budgets,
	})
	if err != nil {
		return nil, errors.Join(ErrConfig, err)
	}
	return &Host{session: cfg.Session, channel: server, budget: cfg.Budgets, finished: make(chan struct{})}, nil
}

// ChannelDispatch exposes only the held canonical channel's immutable public metadata.
// A platform's original runtime owner may delegate this part of its transport
// Owner port to Host; it must still own issuer/native-token/preparation cleanup.
// This is neither connection state nor native provenance nor a live grant.
func (h *Host) ChannelDispatch() agentchannel.Dispatch {
	if h == nil || h.channel == nil {
		return agentchannel.Dispatch{}
	}
	return h.channel.Dispatch()
}

// Serve takes ownership of conn and runs the whole host path to its end:
// authenticated handshake, agentwire server bound to the proven peer key, the
// canonical approved keys and the channel's heartbeat loss, then teardown.
// On every return the grant has been revoked (by the channel or the wire
// server, always before the conn is closed), the channel Close was joined and
// Session.Close waited for guest cleanup within the cleanup budget. A
// non-nil error reports the end and any unconfirmed cleanup, errors.Is with
// agentchannel.ErrCleanupUnknown or agentexec.ErrCleanupUnknown. There is no
// second Serve, reconnect or resume for the Session.
func (h *Host) Serve(ctx context.Context, conn net.Conn) error {
	if h == nil || h.channel == nil {
		if conn != nil {
			_ = conn.Close()
		}
		return ErrConfig
	}
	h.mu.Lock()
	if h.started {
		h.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		return agentchannel.ErrUsed
	}
	h.started, h.accepting = true, true
	h.mu.Unlock()
	defer close(h.finished)
	ch, err := h.channel.Accept(ctx, conn)
	h.mu.Lock()
	h.held, h.accepting = ch, false
	h.mu.Unlock()
	if err != nil {
		return h.finish(ch, err)
	}
	peer, err := ch.Peer().Key()
	if err != nil {
		return h.finish(ch, errors.Join(ErrConfig, err))
	}
	wire, err := agentwire.NewServer(agentwire.ServerConfig{
		Session: h.session, Peer: peer, ApprovedKeys: h.channel.ApprovedKeys(), Liveness: ch.Done(),
	})
	if err != nil {
		return h.finish(ch, errors.Join(ErrConfig, err))
	}
	h.mu.Lock()
	h.authenticated = !h.statusClosing
	h.mu.Unlock()
	// Serve revokes before it closes ch; finish then joins the same channel.
	return h.finish(ch, errors.Join(agentwire.ErrPeerLost, wire.Serve(ctx, ch)))
}

// finish revokes, closes and joins the one channel (if any), then waits for
// the Session's guest cleanup, and reports everything unconfirmed.
func (h *Host) finish(ch *agentchannel.Channel, served error) error {
	ctx, cancel := context.WithTimeout(context.Background(), h.budget.Cleanup)
	defer cancel()
	cleanupErr := h.Close(ctx)
	h.mu.Lock()
	h.serveCleanupErr, h.serveCleanupObserved = cleanupErr, true
	h.mu.Unlock()
	return errors.Join(served, cleanupErr)
}

// Close retains and retries only this Host's channel and Session. A Close while
// Accept is still acquiring its channel reports unknown; call again after Serve
// returns. It never drops an unconfirmed channel or adopts a replacement.
func (h *Host) Close(ctx context.Context) error {
	if h == nil || h.session == nil {
		return ErrConfig
	}
	h.mu.Lock()
	h.authenticated = false
	h.statusClosing = true
	h.mu.Unlock()
	h.session.ObserveDeadline()
	h.session.TransportLost()
	h.mu.Lock()
	ch, pending := h.held, h.accepting
	h.mu.Unlock()
	var closed error
	if ch != nil {
		closed = ch.Close()
	}
	if pending {
		closed = errors.Join(closed, agentchannel.ErrCleanupUnknown)
	}
	return errors.Join(closed, h.session.Close(ctx))
}

// LocallyAuthenticated is a pure display snapshot after peer and wire validation.
func (h *Host) LocallyAuthenticated() (bool, bool) {
	if h == nil || !h.mu.TryLock() {
		return false, false
	}
	defer h.mu.Unlock()
	if !h.authenticated || h.held == nil {
		return false, true
	}
	select {
	case <-h.held.Done():
		return false, true
	default:
		return true, true
	}
}

// CleanupResult observes only the original cleanup performed by Serve, after
// Serve actually returned. It performs no Close/retry/revoke and does not read
// current OS state. A standalone or later explicit Close cannot overwrite this
// historical result. The caller still owns any returned cleanup uncertainty.
func (h *Host) CleanupResult() (error, bool) {
	if h == nil || !h.mu.TryLock() {
		return nil, false
	}
	defer h.mu.Unlock()
	if !h.started || !h.serveCleanupObserved {
		return nil, false
	}
	select {
	case <-h.finished:
		return h.serveCleanupErr, true
	default:
		return nil, false
	}
}
