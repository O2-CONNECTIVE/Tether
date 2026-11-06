package agentwire

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

// ServerConfig comes only from trusted local composition, never from wire or
// MCP fields. Peer is the raw Ed25519 public key that the future authenticated
// channel owner verified; agentwire does not authenticate it. ApprovedKeys are
// the approved raw Ed25519 public keys whose runbackend.KeySetDigest must equal
// the Session grant's KeySetDigest, and Peer must be one of them.
//
// Liveness is closed by the channel owner when its heartbeat or lease declares
// the peer gone (a half-open stream). Serve then revokes the grant before it
// closes the stream. agentwire has no idle timer of its own and does not stop
// valid long-running commands; producing the signal is the owner's duty.
type ServerConfig struct {
	Session      *agentexec.Session
	Peer         ssh.PublicKey
	ApprovedKeys []ssh.PublicKey
	Liveness     <-chan struct{}
}

// Server serves one stream for one Session. NewServer takes the Session's
// single adapter claim, so no second Server can serve the same grant. The
// Server owns only the command IDs that its own Start replies returned.
type Server struct {
	session  *agentexec.Session
	binding  supervisor.Binding
	mode     agentexec.Mode
	profile  agentexec.Profile
	liveness <-chan struct{}
	// prefix namespaces core request IDs, so a Start issued directly by the
	// local owner can never be replayed into and adopted through the wire.
	prefix string

	mu     sync.Mutex
	served bool
	owned  map[string]struct{}
}

// NewServer checks every trusted input before it takes the Session claim, so
// a refused composition leaves the Session unclaimed.
func NewServer(cfg ServerConfig) (*Server, error) {
	s := cfg.Session
	if s == nil || cfg.Peer == nil || cfg.Liveness == nil {
		return nil, ErrUnconfigured
	}
	binding := s.Binding()
	if binding.Generation == 0 || s.Mode() == 0 || profileNames[s.Profile()] == "" {
		return nil, ErrUnconfigured
	}
	if !approvedPeer(cfg.Peer, cfg.ApprovedKeys, s.KeySetDigest()) {
		return nil, ErrPeer
	}
	var raw [prefixBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, ErrUnconfigured
	}
	prefix := hex.EncodeToString(raw[:]) + "."
	if err := s.ClaimWithRequestPrefix(prefix); err != nil {
		return nil, errors.Join(ErrUnconfigured, err)
	}
	return &Server{
		session: s, binding: binding, mode: s.Mode(), profile: s.Profile(), liveness: cfg.Liveness,
		prefix: prefix, owned: map[string]struct{}{},
	}, nil
}

// approvedPeer reuses the canonical raw Ed25519 rules of runbackend.KeySetDigest:
// the approved set must digest to the grant value, and the peer's canonical
// marshal must equal one approved key's canonical marshal.
//
// Every key is canonicalized once into a private parsed copy, and the digest
// and the membership test both use exactly those copies, so a caller-held key
// object that changes its encoding between calls cannot pass one check with
// one value and the other with another.
func approvedPeer(peer ssh.PublicKey, approved []ssh.PublicKey, grantDigest [32]byte) bool {
	want, ok := canonical(peer)
	if !ok || len(approved) == 0 {
		return false
	}
	canon := make([]ssh.PublicKey, 0, len(approved))
	for _, key := range approved {
		parsed, ok := canonical(key)
		if !ok {
			return false
		}
		canon = append(canon, parsed)
	}
	digest, err := runbackend.KeySetDigest(canon)
	if err != nil || grantDigest == ([32]byte{}) || digest != grantDigest {
		return false
	}
	for _, key := range canon {
		if bytes.Equal(key.Marshal(), want.Marshal()) {
			return true
		}
	}
	return false
}

// canonical parses one private copy of a raw Ed25519 key and requires its
// encoding to be byte-identical when marshaled again.
func canonical(key ssh.PublicKey) (ssh.PublicKey, bool) {
	if key == nil || key.Type() != ssh.KeyAlgoED25519 {
		return nil, false
	}
	wire := append([]byte(nil), key.Marshal()...)
	parsed, err := ssh.ParsePublicKey(wire)
	if err != nil || parsed.Type() != ssh.KeyAlgoED25519 || !bytes.Equal(parsed.Marshal(), wire) {
		return nil, false
	}
	return parsed, true
}

// Serve reads requests from conn until the stream ends, ctx is done, the
// Liveness signal closes or the peer violates the protocol, and takes ownership
// of conn. Every return revokes the grant through Session.TransportLost before
// conn is closed, which also unblocks a stalled Read or Write: there is no
// reconnect or resume. The protected owner still confirms guest cleanup with
// Session.Close; Serve does not claim it. A clean end of stream returns nil.
//
// An early refusal with a held Session (nil ctx or conn) also revokes the
// grant before closing the given conn, because no other stream can serve the
// claimed Session. A second Serve only closes its own conn: the first Serve
// owns the revoke. An owner that never calls Serve must close the Session.
func (s *Server) Serve(ctx context.Context, conn io.ReadWriteCloser) error {
	held := s != nil && s.session != nil
	if held {
		s.mu.Lock()
		used := s.served
		s.served = true
		s.mu.Unlock()
		if used {
			if conn != nil {
				_ = conn.Close()
			}
			return ErrUsed
		}
	}
	if conn == nil || ctx == nil || !held {
		if held {
			s.session.ObserveDeadline()
			s.session.TransportLost()
		}
		if conn != nil {
			_ = conn.Close()
		}
		return ErrUnconfigured
	}

	stop := make(chan struct{})
	var once sync.Once
	var lost atomic.Bool
	halt := func() {
		once.Do(func() {
			s.session.ObserveDeadline()
			s.session.TransportLost()
			close(stop)
			_ = conn.Close()
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			halt()
		case <-s.liveness:
			lost.Store(true)
			halt()
		case <-stop:
		}
	}()

	var handlers sync.WaitGroup
	var writeMu sync.Mutex
	slots := make(chan struct{}, maxServerCalls)
	ended := func(err error) error {
		switch {
		case lost.Load():
			return ErrPeerLost
		case ctx.Err() != nil:
			return ctx.Err()
		}
		return err
	}
	err := func() error {
		for {
			frame, err := readFrame(conn)
			switch {
			case err == nil:
			case errors.Is(err, ErrProtocol):
				return ErrProtocol
			case lost.Load() || ctx.Err() != nil:
				return ended(err)
			case errors.Is(err, io.EOF):
				return nil
			default:
				return fmt.Errorf("%w: %w", ErrClosed, err)
			}
			req, err := decodeRequest(frame)
			if err != nil {
				return ErrProtocol
			}
			select {
			case slots <- struct{}{}:
			case <-stop:
				return ended(ErrClosed)
			}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				defer func() { <-slots }()
				reply := s.handle(req)
				writeMu.Lock()
				werr := writeFrame(conn, reply)
				writeMu.Unlock()
				if werr != nil {
					halt()
				}
			}()
		}
	}()
	halt()
	handlers.Wait()
	return err
}

func (s *Server) handle(req request) (reply []byte) {
	defer func() {
		if recover() != nil {
			reply = encodeReply(req.id, "error", "internal")
		}
	}()
	if req.op != opStart && !s.owns(req.commandID) {
		return errorReply(req.id, agentexec.ErrNotFound)
	}
	ref := agentexec.StatusRequest{Version: agentexec.Version, Binding: s.binding, CommandID: req.commandID}
	switch req.op {
	case opStart:
		if !ValidRequestID(req.requestID) || len(req.command) == 0 || len(req.command) > MaxCommandBytes {
			return errorReply(req.id, agentexec.ErrInvalid)
		}
		st, err := s.session.Start(agentexec.StartRequest{
			Version: agentexec.Version, Binding: s.binding, RequestID: s.prefix + req.requestID,
			Mode: s.mode, Command: req.command, TimeoutMillis: req.timeout,
		})
		if err != nil {
			return errorReply(req.id, err)
		}
		s.mu.Lock()
		s.owned[st.CommandID] = struct{}{}
		s.mu.Unlock()
		return statusReply(req.id, st, s.profile)
	case opStatus:
		st, err := s.session.Status(ref)
		if err != nil {
			return errorReply(req.id, err)
		}
		return statusReply(req.id, st, s.profile)
	case opCancel:
		st, err := s.session.Cancel(ref)
		if err != nil {
			return errorReply(req.id, err)
		}
		return statusReply(req.id, st, s.profile)
	case opOutput:
		if req.maxBytes <= 0 || req.maxBytes > MaxOutputBytes {
			return errorReply(req.id, agentexec.ErrInvalid)
		}
		out, err := s.session.Output(agentexec.OutputRequest{
			Version: agentexec.Version, Binding: s.binding, CommandID: req.commandID, Cursor: req.cursor, MaxBytes: req.maxBytes,
		})
		if err != nil {
			return errorReply(req.id, err)
		}
		return outputReply(req.id, out)
	}
	return encodeReply(req.id, "error", "internal")
}

func (s *Server) owns(commandID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.owned[commandID]
	return ok
}
