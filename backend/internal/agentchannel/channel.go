// Package agentchannel carries one authenticated agent byte stream over the
// existing raw Ed25519 SSH encryption on a net.Conn that the trusted owner
// already holds. It never dials, listens, registers an endpoint, reconnects,
// retries or resumes, and it is not a human SSH service: there is no session,
// shell, exec, pty, env, subsystem, forwarding, inner sshd or fallback. A relay
// only carries the opaque encrypted bytes and never authorizes anything. The
// grant, approved key set, hard deadline and revocation come only from the
// trusted local owner's canonical supervisor.Authority and agentexec.Session.
package agentchannel

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

const (
	// ChannelType is the only SSH channel either end opens or admits, once per
	// connection. Its version is part of the name; there is no negotiation.
	ChannelType = "rescue-agent-command-v1@rescue-host"
	// HeartbeatRequest is the only global request either end answers. It
	// proves that the authenticated peer still reads and writes; it is not a
	// lease, never renews the grant and never moves the hard deadline.
	HeartbeatRequest = "rescue-agent-heartbeat-v1@rescue-host"
)

// Upper bounds for Budgets. The trusted owner must choose every value; zero
// is refused rather than defaulted.
const (
	MaxHandshake         = 30 * time.Second
	MinHeartbeatInterval = 10 * time.Millisecond
	MaxHeartbeatInterval = 30 * time.Second
	MaxHeartbeatTimeout  = 30 * time.Second
	MaxReadStall         = 120 * time.Second
	MaxWriteStall        = 60 * time.Second
	MaxCleanup           = 10 * time.Second
	MaxClientLifetime    = 24 * time.Hour
	// MaxPeerHeartbeats is the number of peer heartbeats answered per
	// one-second window; one more is a protocol violation.
	MaxPeerHeartbeats = 100
)

var (
	ErrConfig         = errors.New("agentchannel: untrusted or incomplete configuration")
	ErrUsed           = errors.New("agentchannel: server already took its one connection")
	ErrRejected       = errors.New("agentchannel: handshake, authentication or admission rejected")
	ErrClosed         = errors.New("agentchannel: channel closed")
	ErrCleanupUnknown = errors.New("agentchannel: transport close or worker exit is unconfirmed")
)

// Budgets bound every wait of one channel. The hard deadline still caps the
// handshake and each read and write deadline; Cleanup runs after teardown.
type Budgets struct {
	Handshake         time.Duration // SSH handshake, authentication and agent channel admission
	HeartbeatInterval time.Duration // gap between an answered heartbeat and the next one
	HeartbeatTimeout  time.Duration // reply deadline for one heartbeat
	ReadStall         time.Duration // longest single transport read; must exceed interval+timeout
	WriteStall        time.Duration // longest single transport or channel write
	Cleanup           time.Duration // longest wait in Close for the revoke, conn close and worker join
}

func (b Budgets) valid() bool {
	return b.Handshake > 0 && b.Handshake <= MaxHandshake &&
		b.HeartbeatInterval >= MinHeartbeatInterval && b.HeartbeatInterval <= MaxHeartbeatInterval &&
		b.HeartbeatTimeout > 0 && b.HeartbeatTimeout <= MaxHeartbeatTimeout &&
		b.ReadStall > b.HeartbeatInterval+b.HeartbeatTimeout && b.ReadStall <= MaxReadStall &&
		b.WriteStall > 0 && b.WriteStall <= MaxWriteStall &&
		b.Cleanup > 0 && b.Cleanup <= MaxCleanup
}

// Binding is the immutable grant metadata one channel serves. The server reads
// it from its Authority and Session; the client gets it from the explicit
// dispatch. Neither end derives it from bytes the peer sent.
type Binding struct {
	Grant        supervisor.Binding
	KeySetDigest [32]byte
	Profile      agentexec.Profile
}

func (b Binding) valid() bool {
	return b.Grant.Generation != 0 && b.Grant.Identity.Valid() && b.KeySetDigest != ([32]byte{}) && boundaryFor(b.Profile) != 0
}

// boundaryFor pairs each agent profile with the only grant boundary it serves.
func boundaryFor(p agentexec.Profile) supervisor.GuestBoundary {
	switch p {
	case agentexec.ProfileMacCurrentUser:
		return supervisor.CurrentUserNoIsolation
	case agentexec.ProfileWindowsGuest:
		return supervisor.SeparatedNonAdminGuest
	}
	return 0
}

// Peer is the remote raw Ed25519 key that this package verified: the enrolled
// client key on the server, the pinned host key on the client. Its field is
// unexported, so only a completed handshake produces a non-zero Peer.
type Peer struct {
	key string
}

func newPeer(raw []byte) Peer { return Peer{key: string(raw)} }

// PublicKey returns a copy of the verified key's SSH wire bytes.
func (p Peer) PublicKey() []byte { return []byte(p.key) }

// Key parses a fresh copy of the verified key for trusted composition, which
// passes it to agentwire.ServerConfig.Peer. A zero Peer has no key.
func (p Peer) Key() (ssh.PublicKey, error) {
	if p.key == "" {
		return nil, ErrConfig
	}
	return rawEd25519Bytes([]byte(p.key))
}

// Reason is the first cause of teardown: the local owner's Close, its context,
// the hard deadline (grant on the server, local on the client), revocation of
// the canonical Authority, a handshake or admission that did not finish in
// budget, a peer key or channel binding mismatch, an unexpected channel,
// request or extended data, a failed or late heartbeat, a read or write stall,
// or the end of the transport or agent channel.
type Reason uint8

const (
	ReasonNone Reason = iota
	ReasonClosed
	ReasonCanceled
	ReasonHardDeadline
	ReasonRevoked
	ReasonHandshake
	ReasonRejected
	ReasonViolation
	ReasonHeartbeat
	ReasonReadStall
	ReasonWriteStall
	ReasonTransport
)

var reasonNames = [...]string{"none", "closed", "canceled", "hard-deadline", "revoked", "handshake", "rejected", "violation", "heartbeat", "read-stall", "write-stall", "transport"}

func (r Reason) String() string {
	if int(r) < len(reasonNames) {
		return reasonNames[r]
	}
	return "unknown"
}

// Error reports why a channel was not handed out or why it ended.
type Error struct {
	Reason Reason
	Err    error
}

func (e *Error) Error() string { return e.Err.Error() + ": " + e.Reason.String() }
func (e *Error) Unwrap() error { return e.Err }

// Channel owns one supplied net.Conn, its SSH connection and its single agent
// channel. Only this owner closes that conn; no failure here closes any other
// connection. Teardown is irreversible: on the server it revokes the bound
// Session before the conn is closed, and there is no reconnect or resume.
//
// Observable order of one teardown: the first Reason is recorded, then (on the
// server) the Session is revoked, then Done closes and Read/Write errors are
// returned, and only then is the owned conn closed. Close additionally waits
// for the conn close and every worker. The peer learns of it later, through
// the closed conn or its own heartbeat or stall budget, never synchronously.
type Channel struct {
	raw       *guardedConn
	budgets   Budgets
	deadline  time.Time
	handshake time.Time
	binding   Binding
	session   *agentexec.Session // server only; its canonical Authority is the only grant source

	revokeOnce sync.Once
	revokeDone chan struct{}
	ownRevoke  atomic.Bool // this channel, not an outside owner, revoked the Session
	stop       chan struct{}
	joined     chan struct{}

	// active counts workers plus the Accept/Open call itself while it runs;
	// answered counts our heartbeats that the peer answered.
	mu         sync.Mutex
	active     int
	stopped    bool
	joinClosed bool
	reason     Reason
	conn       ssh.Conn
	ch         ssh.Channel
	peer       Peer
	answered   int
	beatWindow time.Time
	peerBeats  int
}

func newChannel(conn net.Conn, b Budgets, deadline time.Time, binding Binding, session *agentexec.Session) *Channel {
	c := &Channel{
		budgets: b, deadline: deadline, binding: binding, session: session,
		revokeDone: make(chan struct{}), stop: make(chan struct{}), joined: make(chan struct{}), active: 1,
	}
	c.handshake = time.Now().Add(b.Handshake)
	if deadline.Before(c.handshake) {
		c.handshake = deadline
	}
	c.raw = &guardedConn{
		Conn: conn, hard: deadline, handshake: c.handshake, readStall: b.ReadStall, writeStall: b.WriteStall,
		onStall: c.teardown, beforeClose: c.beforeClose,
	}
	return c
}

// revoked is the canonical Session's revocation signal, or nil on the client.
func (c *Channel) revoked() <-chan struct{} {
	if c.session == nil {
		return nil
	}
	return c.session.Done()
}

// beforeClose runs whenever something other than teardown closes the owned
// conn, normally the SSH library after its transport failed. After admission
// that is a transport end: the reason is recorded before the revoke, so a
// watcher can never see our own revoke first and mislabel it. During the
// handshake Accept or Open itself records the classified reason, so only the
// revoke happens here. Either way the Session is revoked before the close.
func (c *Channel) beforeClose(admitted bool) {
	if admitted {
		c.teardown(ReasonTransport)
	}
	c.revokeFirst()
}

// spawn starts a tracked worker. Callers either hold a worker slot themselves
// or run before teardown, so the join signal can never fire early.
func (c *Channel) spawn(f func()) {
	c.mu.Lock()
	c.active++
	c.mu.Unlock()
	go func() {
		defer c.release()
		f()
	}()
}

func (c *Channel) release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active--
	if c.stopped && c.active == 0 && !c.joinClosed {
		c.joinClosed = true
		close(c.joined)
	}
}

// teardown records the first reason and starts the one shutdown worker. It
// never blocks, so stall timers and SSH callbacks may call it.
func (c *Channel) teardown(r Reason) {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	// Peer expiry can close transport before our timer is scheduled. Match
	// the Session's deadline-first stop classification without delaying or
	// changing revoke/close, and never replace an already recorded reason.
	if r == ReasonTransport && !time.Now().Before(c.deadline) {
		r = ReasonHardDeadline
	}
	c.stopped, c.reason = true, r
	c.active++
	c.mu.Unlock()
	close(c.stop)
	go func() {
		defer c.release()
		c.revokeFirst()
		close(c.revokeDone)
		<-c.raw.attempt()
	}()
}

// revokeFirst revokes the bound grant through the canonical Session before
// any close of the owned conn, including a close made by the SSH library.
func (c *Channel) revokeFirst() {
	if c.session != nil {
		c.revokeOnce.Do(func() {
			c.ownRevoke.Store(true)
			// At or past the hard deadline the Session records the deadline
			// as its stop reason first; otherwise this is a transport loss.
			c.session.ObserveDeadline()
			c.session.TransportLost()
		})
	}
}

// live is false once torn down, past the hard deadline or after revocation.
func (c *Channel) live() bool {
	c.mu.Lock()
	stopped := c.stopped
	c.mu.Unlock()
	if stopped || !time.Now().Before(c.deadline) {
		return false
	}
	select {
	case <-c.revoked():
		return false
	default:
	}
	return true
}

// deadReason names why a channel that is no longer live ended: the hard
// deadline, our own revoke after the transport ended, or an outside revoke.
func (c *Channel) deadReason() Reason {
	switch {
	case !time.Now().Before(c.deadline):
		return ReasonHardDeadline
	case c.ownRevoke.Load() && c.raw.isAdmitted():
		return ReasonTransport
	case c.ownRevoke.Load():
		return ReasonHandshake
	}
	return ReasonRevoked
}

// fail ends a handshake that will not hand out the channel. The Accept/Open
// slot is released first; an unconfirmed cleanup returns this same owner.
func (c *Channel) fail(r Reason, err error) (*Channel, error) {
	c.teardown(r)
	c.release()
	e := &Error{Reason: c.Reason(), Err: err}
	if c.join() != nil {
		return c, errors.Join(e, ErrCleanupUnknown)
	}
	return nil, e
}

// join waits, within one Cleanup budget, for the revoke, a confirmed close of
// the same owned conn (retrying a failed close) and every worker's exit.
func (c *Channel) join() error {
	timer := time.NewTimer(c.budgets.Cleanup)
	defer timer.Stop()
	select {
	case <-c.revokeDone:
	case <-timer.C:
		return ErrCleanupUnknown
	}
	if !c.raw.isClosed() {
		select {
		case <-c.raw.attempt():
		case <-timer.C:
			return ErrCleanupUnknown
		}
		if !c.raw.isClosed() {
			return ErrCleanupUnknown
		}
	}
	select {
	case <-c.joined:
		return nil
	case <-timer.C:
		return ErrCleanupUnknown
	}
}

// Close tears the channel down and reports whether the conn close and every
// worker were confirmed within Budgets.Cleanup. ErrCleanupUnknown keeps the
// same owner; calling Close again retries only this channel's conn and join.
func (c *Channel) Close() error {
	if c == nil || c.raw == nil {
		return ErrConfig
	}
	c.teardown(ReasonClosed)
	return c.join()
}

// Done is closed after teardown started and, on the server, after the bound
// Session was revoked. It does not wait for the conn close or the workers;
// Close confirms those.
func (c *Channel) Done() <-chan struct{} { return c.revokeDone }

// Reason is the first teardown reason, or ReasonNone while live.
func (c *Channel) Reason() Reason {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

// Peer is the verified remote key; Binding is the grant this channel serves.
func (c *Channel) Peer() Peer       { return c.peer }
func (c *Channel) Binding() Binding { return c.binding }

// Read returns agent bytes. A quiet peer is not a failure: only heartbeats,
// stalls, deadlines, revocation and cancellation end the channel.
func (c *Channel) Read(p []byte) (int, error) {
	if c.Reason() != ReasonNone {
		return 0, c.closedError(ErrClosed)
	}
	n, err := c.ch.Read(p)
	if err != nil {
		c.teardown(ReasonTransport)
		return n, c.closedError(err)
	}
	return n, nil
}

// Write sends agent bytes. One call that the peer does not drain within
// WriteStall tears the channel down, which unblocks the call.
func (c *Channel) Write(p []byte) (int, error) {
	if !c.live() {
		c.teardown(c.deadReason())
		return 0, c.closedError(ErrClosed)
	}
	if len(p) == 0 {
		return 0, nil
	}
	stall := time.AfterFunc(c.budgets.WriteStall, func() { c.teardown(ReasonWriteStall) })
	n, err := c.ch.Write(p)
	stall.Stop()
	if err != nil {
		c.teardown(ReasonTransport)
		return n, c.closedError(err)
	}
	return n, nil
}

// closedError keeps a clean end of stream as io.EOF and reports every other
// end with its reason, so a revoke or stall never looks like a normal close.
// After teardown it first waits, within the Cleanup budget, for the revoke,
// so a caller that sees the error also sees the revoked grant.
func (c *Channel) closedError(err error) error {
	r := c.Reason()
	if r == ReasonNone {
		return err
	}
	timer := time.NewTimer(c.budgets.Cleanup)
	select {
	case <-c.revokeDone:
	case <-timer.C:
	}
	timer.Stop()
	if r == ReasonTransport && errors.Is(err, io.EOF) {
		return err
	}
	return &Error{Reason: r, Err: ErrClosed}
}

// attach records the SSH connection and starts the workers every handshake
// needs: the transport waiter and the global request handler.
func (c *Channel) attach(conn ssh.Conn, reqs <-chan *ssh.Request) <-chan struct{} {
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	done := make(chan struct{})
	c.spawn(func() {
		_ = conn.Wait()
		close(done)
	})
	c.spawn(func() { c.serveGlobal(reqs) })
	return done
}

// admit hands out the accepted agent channel after starting its workers and
// switching the transport from the handshake deadline to the stall budgets.
func (c *Channel) admit(ctx context.Context, revoked <-chan struct{}, admitted chan struct{}, transport <-chan struct{}, ch ssh.Channel, reqs <-chan *ssh.Request, peer Peer) (*Channel, error) {
	c.mu.Lock()
	c.ch, c.peer = ch, peer
	c.mu.Unlock()
	c.spawn(func() { c.serveChannelRequests(reqs) })
	c.spawn(func() { c.watchExtended(ch) })
	c.raw.admit()
	c.spawn(func() { c.monitor(ctx, revoked, transport) })
	close(admitted)
	if !c.live() {
		return c.fail(c.deadReason(), ErrRejected)
	}
	c.release()
	return c, nil
}

// watchAdmission bounds the handshake and admission by the handshake deadline
// and stops them on cancellation or revocation.
func (c *Channel) watchAdmission(ctx context.Context, revoked, admitted <-chan struct{}) {
	timer := time.NewTimer(time.Until(c.handshake))
	defer timer.Stop()
	select {
	case <-admitted:
	case <-c.stop:
	case <-ctx.Done():
		c.teardown(ReasonCanceled)
	case <-revoked:
		if c.ownRevoke.Load() {
			return // the handshake failed; Accept records its own reason
		}
		c.teardown(c.deadReason())
	case <-timer.C:
		if time.Now().Before(c.deadline) {
			c.teardown(ReasonHandshake)
		} else {
			c.teardown(ReasonHardDeadline)
		}
	}
}
