package agentchannel

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// memConn is a buffered in-memory net.Conn for tests only: no socket,
// listener, dial, process or OS signal. Each direction holds at most limit
// bytes, so a peer that stops reading stalls the writer like a congested relay.
type memConn struct {
	rd, wr *pipeBuf
	link   *memLink

	mu        sync.Mutex
	readDL    time.Time
	writeDL   time.Time
	dlWake    chan struct{}
	closed    bool
	closedCh  chan struct{}
	gate      chan struct{} // Close waits for it when set
	failures  int           // Close calls that fail before any succeeds
	onClose   func()        // observes the moment Close is entered
	closeCall int
}

// memLink is shared by both ends. A black hole is a half-open relay: writes
// vanish and a close on one end is never seen by the other.
type memLink struct {
	mu        sync.Mutex
	blackhole bool
}

func (l *memLink) setBlackhole() {
	l.mu.Lock()
	l.blackhole = true
	l.mu.Unlock()
}

func (l *memLink) isBlackhole() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.blackhole
}

type pipeBuf struct {
	mu     sync.Mutex
	data   []byte
	limit  int
	eof    bool // the writing end closed
	broken bool // the reading end closed
	frozen bool // the reading end stopped reading
	wake   chan struct{}
}

func (b *pipeBuf) notifyLocked() {
	close(b.wake)
	b.wake = make(chan struct{})
}

func (b *pipeBuf) setFrozen(v bool) {
	b.mu.Lock()
	b.frozen = v
	b.notifyLocked()
	b.mu.Unlock()
}

func memPair() (*memConn, *memConn) {
	link := &memLink{}
	ab := &pipeBuf{limit: 64 << 10, wake: make(chan struct{})}
	ba := &pipeBuf{limit: 64 << 10, wake: make(chan struct{})}
	a := &memConn{rd: ba, wr: ab, link: link, dlWake: make(chan struct{}), closedCh: make(chan struct{})}
	b := &memConn{rd: ab, wr: ba, link: link, dlWake: make(chan struct{}), closedCh: make(chan struct{})}
	return a, b
}

func waitFor(wake, dlWake, closed <-chan struct{}, deadline time.Time) error {
	var timeout <-chan time.Time
	if !deadline.IsZero() {
		d := time.Until(deadline)
		if d <= 0 {
			return os.ErrDeadlineExceeded
		}
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-wake:
	case <-dlWake:
	case <-closed:
	case <-timeout:
		return os.ErrDeadlineExceeded
	}
	return nil
}

func (c *memConn) state() (closed bool, rdl, wdl time.Time, dlWake chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed, c.readDL, c.writeDL, c.dlWake
}

func (c *memConn) Read(p []byte) (int, error) {
	for {
		closed, dl, _, dlWake := c.state()
		if closed {
			return 0, net.ErrClosed
		}
		c.rd.mu.Lock()
		if !c.rd.frozen && len(c.rd.data) > 0 {
			n := copy(p, c.rd.data)
			c.rd.data = c.rd.data[n:]
			c.rd.notifyLocked()
			c.rd.mu.Unlock()
			return n, nil
		}
		if !c.rd.frozen && c.rd.eof {
			c.rd.mu.Unlock()
			return 0, io.EOF
		}
		wake := c.rd.wake
		c.rd.mu.Unlock()
		if err := waitFor(wake, dlWake, c.closedCh, dl); err != nil {
			return 0, err
		}
	}
}

func (c *memConn) Write(p []byte) (int, error) {
	written := 0
	for {
		closed, _, dl, dlWake := c.state()
		if closed {
			return written, net.ErrClosed
		}
		if c.link.isBlackhole() {
			return len(p), nil
		}
		c.wr.mu.Lock()
		if c.wr.broken {
			c.wr.mu.Unlock()
			return written, io.ErrClosedPipe
		}
		if space := c.wr.limit - len(c.wr.data); space > 0 {
			n := min(space, len(p)-written)
			c.wr.data = append(c.wr.data, p[written:written+n]...)
			written += n
			c.wr.notifyLocked()
		}
		if written == len(p) {
			c.wr.mu.Unlock()
			return written, nil
		}
		wake := c.wr.wake
		c.wr.mu.Unlock()
		if err := waitFor(wake, dlWake, c.closedCh, dl); err != nil {
			return written, err
		}
	}
}

func (c *memConn) Close() error {
	c.mu.Lock()
	c.closeCall++
	hook, gate := c.onClose, c.gate
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	if gate != nil {
		<-gate
	}
	c.mu.Lock()
	if c.failures > 0 {
		c.failures--
		c.mu.Unlock()
		return errors.New("injected close failure")
	}
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.closedCh)
	c.mu.Unlock()
	if !c.link.isBlackhole() {
		c.wr.mu.Lock()
		c.wr.eof = true
		c.wr.notifyLocked()
		c.wr.mu.Unlock()
		c.rd.mu.Lock()
		c.rd.broken = true
		c.rd.notifyLocked()
		c.rd.mu.Unlock()
	}
	return nil
}

func (c *memConn) setDeadlines(read, write bool, t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if read {
		c.readDL = t
	}
	if write {
		c.writeDL = t
	}
	close(c.dlWake)
	c.dlWake = make(chan struct{})
	return nil
}

func (c *memConn) SetDeadline(t time.Time) error      { return c.setDeadlines(true, true, t) }
func (c *memConn) SetReadDeadline(t time.Time) error  { return c.setDeadlines(true, false, t) }
func (c *memConn) SetWriteDeadline(t time.Time) error { return c.setDeadlines(false, true, t) }

type memAddr struct{}

func (memAddr) Network() string { return "memory" }
func (memAddr) String() string  { return "memory" }

func (c *memConn) LocalAddr() net.Addr  { return memAddr{} }
func (c *memConn) RemoteAddr() net.Addr { return memAddr{} }

func (c *memConn) setGate(g chan struct{}) { c.mu.Lock(); c.gate = g; c.mu.Unlock() }
func (c *memConn) setFailures(n int)       { c.mu.Lock(); c.failures = n; c.mu.Unlock() }
func (c *memConn) setOnClose(f func())     { c.mu.Lock(); c.onClose = f; c.mu.Unlock() }

func (c *memConn) closeCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeCall
}

func (c *memConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}
