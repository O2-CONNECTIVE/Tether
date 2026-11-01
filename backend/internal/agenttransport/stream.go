package agenttransport

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"
)

var (
	errNoTransport  = errors.New("agenttransport: the WebSocket has no observed transport")
	errUpgrading    = errors.New("agenttransport: the upgrade may still attach a transport")
	errAcquiring    = errors.New("agenttransport: a transport acquisition or late close is still in progress")
	errInvalidWrite = errors.New("agenttransport: invalid read or write count")
	errMessageType  = errors.New("agenttransport: non-binary message in stream mode")
	errMessageSize  = errors.New("agenttransport: stream message exceeds MaxStreamMessage")
	errNoProgress   = errors.New("agenttransport: message reader made no progress")
	errStreamClose  = errors.New("agenttransport: close the stream, not its message view")
)

// rawConn observes the first Close of exactly one transport connection (the
// hijacked connection on the gateway, the dialed TCP connection on the Mac).
// Whoever closes it first (this package, the WebSocket library or TLS), the
// result is recorded once and never re-attempted, so a failed close stays
// unconfirmed instead of closing a reused descriptor.
type rawConn struct {
	net.Conn
	once sync.Once
	err  error
}

func (r *rawConn) Close() error {
	r.once.Do(func() { r.err = r.Conn.Close() })
	return r.err
}

// acquirer is the Client's transport owner as seen by an HTTP transport: each
// dial attempt is bracketed by begin/end so that one still running (even after
// its round trip was abandoned) keeps the owner unconfirmed, and its
// connection goes through attach.
type acquirer interface {
	begin() bool
	end()
	attach(net.Conn) (net.Conn, error)
}

// closer owns one transport from before the upgrade until its close is
// confirmed. The upgrade attaches the raw connection (attach) and then the
// WebSocket (setWS); settle ends the upgrade. The single close attempt ends the
// transport context, closes exactly the attached raw connection (its first
// result is authoritative) and only then calls CloseNow to join the library,
// so a library-initiated close handshake cannot hold the raw close. The attempt
// runs in one goroutine; a hanging close is reported as unknown within the
// caller's wait, and later waits observe the same attempt. While a dial or a
// late connection's close is in progress the owner is never reported clean.
type closer struct {
	cancel context.CancelFunc

	mu      sync.Mutex
	ws      *websocket.Conn
	raw     *rawConn
	busy    bool          // the upgrade may still attach a transport
	sealed  bool          // the close attempt started; later attachments are closed at once
	pending int           // dial attempts in flight plus late closes in progress
	quiet   chan struct{} // closed when pending returns to zero
	late    error         // close results of transports attached after sealing or in excess
	err     error         // the attempt's result, written before done is closed

	once sync.Once
	done chan struct{}
}

// newCloser returns an owner that accepts one transport until settle.
func newCloser(cancel context.CancelFunc) *closer {
	return &closer{cancel: cancel, busy: true, done: make(chan struct{})}
}

// addPending and donePending require c.mu.
func (c *closer) addPending() {
	if c.pending == 0 {
		c.quiet = make(chan struct{})
	}
	c.pending++
}

func (c *closer) donePending() {
	c.pending--
	if c.pending == 0 {
		close(c.quiet)
	}
}

// begin admits one dial attempt during the upgrade; end must follow.
func (c *closer) begin() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sealed || !c.busy {
		return false
	}
	c.addPending()
	return true
}

func (c *closer) end() {
	c.mu.Lock()
	c.donePending()
	c.mu.Unlock()
}

// attach takes the one raw connection of the upgrade. After the close
// started, after settle, or for a second connection, it closes conn at once,
// keeps its close result and refuses it; until that close returns the owner
// counts as unconfirmed.
func (c *closer) attach(conn net.Conn) (net.Conn, error) {
	r := &rawConn{Conn: conn}
	c.mu.Lock()
	if !c.sealed && c.busy && c.raw == nil {
		c.raw = r
		c.mu.Unlock()
		return r, nil
	}
	if c.raw == nil {
		c.raw = r // owned for the record; closed below
	}
	c.addPending()
	c.mu.Unlock()
	err := r.Close()
	c.mu.Lock()
	if err != nil {
		c.late = errors.Join(c.late, err)
	}
	c.donePending()
	c.mu.Unlock()
	return nil, ErrRefused
}

// setWS records the upgraded WebSocket. If the close already started, it
// joins the library here; without a raw transport the close stays unknown.
func (c *closer) setWS(ws *websocket.Conn) {
	if ws == nil {
		return
	}
	c.mu.Lock()
	if !c.sealed {
		c.ws = ws
		c.mu.Unlock()
		return
	}
	if c.raw == nil {
		c.late = errors.Join(c.late, errNoTransport)
	}
	c.mu.Unlock()
	_ = ws.CloseNow()
}

// settle ends the upgrade phase and reports whether a raw transport is owned.
func (c *closer) settle() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.busy = false
	return c.raw != nil
}

func (c *closer) start() {
	c.once.Do(func() {
		c.mu.Lock()
		c.sealed = true
		raw, ws := c.raw, c.ws
		c.mu.Unlock()
		go func() {
			c.cancel()
			var err error
			switch {
			case raw != nil:
				err = raw.Close()
			case ws != nil:
				// A WebSocket whose transport was never observed: CloseNow
				// returning proves nothing about the connection under it.
				err = errNoTransport
			}
			if ws != nil {
				_ = ws.CloseNow()
			}
			c.mu.Lock()
			c.err = err
			c.mu.Unlock()
			close(c.done)
		}()
	})
}

// result is meaningful once done is closed.
func (c *closer) result() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.busy:
		return errUpgrading
	case c.pending > 0:
		return errAcquiring
	}
	return errors.Join(c.err, c.late)
}

// waitFor waits for ch within ctx; a ch already closed wins over an ended ctx.
func waitFor(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		select {
		case <-ch:
			return nil
		default:
			return errors.Join(ErrCleanupUnknown, ctx.Err())
		}
	}
}

// wait starts the close if needed and waits within ctx for it and for any
// dial or late close still in progress. A completed attempt reports its
// actual result even if ctx has already ended. A nil result means the raw
// connection's first Close succeeded, CloseNow joined and nothing is pending.
func (c *closer) wait(ctx context.Context) error {
	c.start()
	if err := waitFor(ctx, c.done); err != nil {
		return err
	}
	for {
		c.mu.Lock()
		if c.pending == 0 || c.busy {
			c.mu.Unlock()
			break
		}
		q := c.quiet
		c.mu.Unlock()
		if err := waitFor(ctx, q); err != nil {
			return err
		}
	}
	if err := c.result(); err != nil {
		return errors.Join(ErrCleanupUnknown, err)
	}
	return nil
}

func (c *closer) confirmed() bool {
	select {
	case <-c.done:
		return c.result() == nil
	default:
		return false
	}
}

// stream is the registered WebSocket in binary mode: the opaque net.Conn the
// agent channel owns. Its message view (msgConn) enforces the message type,
// the per-message bound and the deadlines; stream adds chunking and the
// owner's finite Close.
type stream struct {
	net.Conn
	closer  *closer
	cleanup time.Duration
	writeMu sync.Mutex // one Write's chunks are never interleaved with another's
}

// newStream switches ws to binary stream mode through msgConn. It does not use
// websocket.NetConn: observed in memory tests, NetConn delivered a message one
// byte over the restored read limit whole, and reported a pending deadline
// expiry as context cancellation rather than os.ErrDeadlineExceeded. The
// library limit is only a backstop here; msgConn enforces the bound itself.
func newStream(ctx context.Context, ws *websocket.Conn, c *closer, cleanup time.Duration) *stream {
	ws.SetReadLimit(MaxStreamMessage + 1)
	m := &msgConn{ws: ws, fail: c.start, buf: make([]byte, MaxStreamMessage+1)}
	m.readCtx, m.read.cancel = context.WithCancel(ctx)
	m.writeCtx, m.write.cancel = context.WithCancel(ctx)
	return &stream{Conn: m, closer: c, cleanup: cleanup}
}

// Write sends one WebSocket message per MaxStreamMessage bytes, so the peer's
// identical bound is never exceeded by a well-behaved sender. A short or zero
// write ends with io.ErrShortWrite and an impossible count with an error; it
// never loops without progress.
func (s *stream) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n := 0
	for n < len(p) {
		chunk := p[n:min(len(p), n+MaxStreamMessage)]
		m, err := s.Conn.Write(chunk)
		if m < 0 || m > len(chunk) {
			return n, errors.Join(errInvalidWrite, err)
		}
		n += m
		if err != nil {
			return n, err
		}
		if m < len(chunk) {
			return n, io.ErrShortWrite
		}
	}
	return n, nil
}

// Close is finite: it never performs the WebSocket close handshake and waits
// at most the cleanup budget. An unconfirmed result is ErrCleanupUnknown and a
// later Close waits again on the same attempt.
func (s *stream) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.cleanup)
	defer cancel()
	return s.closer.wait(ctx)
}

// msgConn is the bounded net.Conn view of one registered WebSocket, built on
// the library's public Reader and Write. Each Write is exactly one binary
// message. Each incoming message is read whole, before any byte is
// delivered, into one buffer of MaxStreamMessage+1 bytes; it is delivered only
// if it is binary and at most MaxStreamMessage bytes. A violation is sticky
// and starts the owner's close. There is one reader at a time and one writer
// at a time. Deadlines follow net.Conn: an expiry while idle only makes the
// next call fail until the deadline is reset; an expiry during a pending call
// fails it with an error matching os.ErrDeadlineExceeded and, because the
// library then ends the WebSocket, every later call too. Cancellation or loss
// of the transport is reported as such, never as a timeout.
type msgConn struct {
	ws    *websocket.Conn
	fail  func()
	read  deadline
	write deadline

	readCtx, writeCtx context.Context

	readMu  sync.Mutex
	buf     []byte // MaxStreamMessage+1 bytes, reused per message
	rest    []byte // undelivered part of the current message
	readErr error  // sticky

	writeMu sync.Mutex
}

func (m *msgConn) Read(p []byte) (int, error) {
	m.readMu.Lock()
	defer m.readMu.Unlock()
	if m.readErr != nil {
		return 0, m.readErr
	}
	if err := m.read.begin(); err != nil {
		return 0, err
	}
	for len(m.rest) == 0 {
		if len(p) == 0 {
			return 0, m.read.finish(nil)
		}
		msg, err := m.next()
		if err != nil {
			m.readErr = m.read.finish(err)
			return 0, m.readErr
		}
		m.rest = msg // an empty message delivers nothing; read the next one
	}
	n := copy(p, m.rest)
	m.rest = m.rest[n:]
	return n, m.read.finish(nil)
}

// next reads one whole message, validating type and size before delivery.
func (m *msgConn) next() ([]byte, error) {
	typ, r, err := m.ws.Reader(m.readCtx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageBinary {
		m.fail()
		return nil, errMessageType
	}
	n, idle := 0, 0
	for {
		k, err := r.Read(m.buf[n:])
		if k < 0 || k > len(m.buf)-n {
			m.fail()
			return nil, errInvalidWrite
		}
		n += k
		if n > MaxStreamMessage {
			m.fail()
			return nil, errMessageSize
		}
		switch {
		case err == io.EOF:
			return m.buf[:n], nil
		case err != nil:
			return nil, err
		case k == 0:
			if idle++; idle > 100 {
				m.fail()
				return nil, errNoProgress
			}
		}
	}
}

func (m *msgConn) Write(p []byte) (int, error) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	if err := m.write.begin(); err != nil {
		return 0, err
	}
	if err := m.write.finish(m.ws.Write(m.writeCtx, websocket.MessageBinary, p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close is not the owner's close; stream.Close is.
func (m *msgConn) Close() error {
	m.fail()
	return errStreamClose
}

func (m *msgConn) SetDeadline(t time.Time) error {
	m.read.set(t)
	m.write.set(t)
	return nil
}

func (m *msgConn) SetReadDeadline(t time.Time) error  { m.read.set(t); return nil }
func (m *msgConn) SetWriteDeadline(t time.Time) error { m.write.set(t); return nil }

type wsAddr struct{}

func (wsAddr) Network() string { return "websocket" }
func (wsAddr) String() string  { return "websocket" }

func (m *msgConn) LocalAddr() net.Addr  { return wsAddr{} }
func (m *msgConn) RemoteAddr() net.Addr { return wsAddr{} }

// deadline is one direction's net.Conn deadline. Its timer runs only while a
// call is pending; firing then cancels that direction's context (which the
// library answers by ending the WebSocket) and marks the direction timed out.
type deadline struct {
	mu      sync.Mutex
	at      time.Time
	pending bool
	gen     uint64
	timer   *time.Timer
	fired   bool
	cancel  context.CancelFunc
}

func (d *deadline) set(t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.at = t
	if d.pending {
		d.arm()
	}
}

// arm (re)starts the timer for the pending call. Caller holds d.mu.
func (d *deadline) arm() {
	d.gen++
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if d.at.IsZero() {
		return
	}
	gen := d.gen
	d.timer = time.AfterFunc(time.Until(d.at), func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.pending && d.gen == gen {
			d.fired = true
			d.cancel()
		}
	})
}

// begin fails at once, without I/O and without ending the stream, if the
// deadline has already passed; a later set can clear that.
func (d *deadline) begin() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fired || (!d.at.IsZero() && !time.Now().Before(d.at)) {
		return timeoutError{}
	}
	d.pending = true
	d.arm()
	return nil
}

func (d *deadline) finish(err error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending = false
	d.gen++
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	if err != nil && d.fired {
		return timeoutError{cause: err}
	}
	return err
}

// timeoutError matches os.ErrDeadlineExceeded and reports Timeout, like the
// deadline errors of a net.Conn.
type timeoutError struct{ cause error }

func (e timeoutError) Error() string {
	if e.cause != nil {
		return "agenttransport: i/o deadline exceeded: " + e.cause.Error()
	}
	return "agenttransport: i/o deadline exceeded"
}

func (e timeoutError) Unwrap() []error {
	if e.cause != nil {
		return []error{os.ErrDeadlineExceeded, e.cause}
	}
	return []error{os.ErrDeadlineExceeded}
}

func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
