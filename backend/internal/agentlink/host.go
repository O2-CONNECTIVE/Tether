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
	Session              *agentexec.Session
	AllowedKeys          []ssh.PublicKey
	HostKey              ssh.Signer
	Budgets              agentchannel.Budgets
	ConnectionGeneration uint64
	AllowReconnect       bool
	OnActivity           func()
	OnConnected          func(uint64, func() error) bool
}

// Host serves one granted Session over one supplied connection generation.
type Host struct {
	mu                   sync.Mutex
	held                 *agentchannel.Channel
	accepting            bool
	authenticated        bool
	statusClosing        bool
	started              bool
	reconnectable        bool
	finished             chan struct{}
	serveCleanupErr      error
	serveCleanupObserved bool
	session              *agentexec.Session
	channel              *agentchannel.Server
	budget               agentchannel.Budgets
	generation           uint64
	allowReconnect       bool
	onActivity           func()
	onConnected          func(uint64, func() error) bool
}

// NewHost takes the Session's single transport claim through
// agentchannel.NewServer. The wire claim is taken in Serve once the peer key
// is proven, so it is bound to exactly that verified key.
func NewHost(cfg HostConfig) (*Host, error) {
	if cfg.Session == nil {
		return nil, ErrConfig
	}
	generation := cfg.ConnectionGeneration
	if generation == 0 {
		generation = 1
	}
	server, err := agentchannel.NewServer(agentchannel.ServerConfig{
		Session: cfg.Session, AllowedKeys: cfg.AllowedKeys, HostKey: cfg.HostKey, Budgets: cfg.Budgets,
		ConnectionGeneration: generation, AllowReconnect: cfg.AllowReconnect, OnActivity: cfg.OnActivity,
	})
	if err != nil {
		return nil, errors.Join(ErrConfig, err)
	}
	return &Host{session: cfg.Session, channel: server, budget: cfg.Budgets, generation: generation, allowReconnect: cfg.AllowReconnect, onActivity: cfg.OnActivity, onConnected: cfg.OnConnected, finished: make(chan struct{})}, nil
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

// Serve takes ownership of conn and runs one host path to its end:
// authenticated handshake, agentwire server bound to the proven peer key, the
// canonical approved keys and the channel's heartbeat loss, then teardown.
// An admitted transport loss preserves the grant only after guest cleanup is
// confirmed; every other end revokes it. A reconnectable Host can be followed
// by a new Host for the next generation on the same Session.
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
	if h.onActivity != nil {
		h.onActivity()
	}
	peer, err := ch.Peer().Key()
	if err != nil {
		return h.finish(ch, errors.Join(ErrConfig, err))
	}
	resumeTransport := func() error { return h.session.ResumeTransport(h.generation) }
	if h.generation > 1 && h.onConnected != nil {
		if !h.onConnected(h.generation, resumeTransport) {
			return h.finish(ch, agentchannel.ErrRejected)
		}
	} else if err := resumeTransport(); err != nil {
		return h.finish(ch, err)
	}
	wire, err := agentwire.NewServer(agentwire.ServerConfig{
		Session: h.session, Peer: peer, ApprovedKeys: h.channel.ApprovedKeys(), Liveness: ch.Done(),
		ConnectionGeneration: h.generation, AllowReconnect: h.allowReconnect, OnActivity: h.onActivity,
	})
	if err != nil {
		return h.finish(ch, errors.Join(ErrConfig, err))
	}
	h.mu.Lock()
	h.authenticated = !h.statusClosing
	h.mu.Unlock()
	// Serve ends ch; finish then confirms the same channel and Session cleanup.
	return h.finish(ch, errors.Join(agentwire.ErrPeerLost, wire.Serve(ctx, ch)))
}

// An opt-in reconnectable loss retains the grant for the next generation only
// after closing and joining the channel and confirming Session cleanup.
// Otherwise, or if cleanup is unconfirmed, finish revokes, closes and joins,
// then awaits Session cleanup; both paths report anything unconfirmed.
func (h *Host) finish(ch *agentchannel.Channel, served error) error {
	ctx, cancel := context.WithTimeout(context.Background(), h.budget.Cleanup)
	defer cancel()
	wireEndedFatally := errors.Is(served, agentwire.ErrProtocol) || errors.Is(served, context.Canceled)
	reconnectable := h.allowReconnect && !wireEndedFatally && ch != nil && ch.Reconnectable()
	var cleanupErr error
	if reconnectable {
		cleanupErr = ch.Close()
		cleanupErr = errors.Join(cleanupErr, h.session.AwaitCleanup(ctx))
		if cleanupErr == nil {
			h.mu.Lock()
			h.reconnectable = true
			h.mu.Unlock()
		} else {
			h.session.TransportLost()
			cleanupErr = errors.Join(cleanupErr, h.session.Close(ctx))
		}
	} else {
		cleanupErr = h.Close(ctx)
	}
	h.mu.Lock()
	h.serveCleanupErr, h.serveCleanupObserved = cleanupErr, true
	h.mu.Unlock()
	return errors.Join(served, cleanupErr)
}

func (h *Host) Reconnectable() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reconnectable
}

// Unused reports that Serve has not claimed this Host yet.
func (h *Host) Unused() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.started
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
