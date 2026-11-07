package agenttransport

import (
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// memConn is a buffered in-memory net.Conn with deadlines, for tests only: no
// socket, listener, dial or process. A black hole models a half-open tunnel:
// writes vanish and a close is never seen by the other end. A close gate
// models a transport close that does not return until released.
type memConn struct {
	rd, wr *pipeBuf
	link   *memLink

	mu        sync.Mutex
	readDL    time.Time
	writeDL   time.Time
	dlWake    chan struct{}
	closed    bool
	closedCh  chan struct{}
	closeGate chan struct{}
	closes    int
	// Set before use to hold the peer's EOF observation independently of Close.
	readEOFGate <-chan struct{}
}

type memLink struct {
	mu        sync.Mutex
	blackhole bool
}

func (l *memLink) setBlackhole() { l.mu.Lock(); l.blackhole = true; l.mu.Unlock() }

func (l *memLink) isBlackhole() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.blackhole
}

type pipeBuf struct {
	mu   sync.Mutex
	data []byte
	eof  bool
	wake chan struct{}
}

func (b *pipeBuf) notifyLocked() {
	close(b.wake)
	b.wake = make(chan struct{})
}

func memPair() (*memConn, *memConn) {
	link := &memLink{}
	ab, ba := &pipeBuf{wake: make(chan struct{})}, &pipeBuf{wake: make(chan struct{})}
	a := &memConn{rd: ba, wr: ab, link: link, dlWake: make(chan struct{}), closedCh: make(chan struct{})}
	b := &memConn{rd: ab, wr: ba, link: link, dlWake: make(chan struct{}), closedCh: make(chan struct{})}
	return a, b
}

const memLimit = 256 << 10

func wait(wake, dlWake, closed <-chan struct{}, deadline time.Time) error {
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

func (c *memConn) state() (bool, time.Time, time.Time, chan struct{}) {
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
		if len(c.rd.data) > 0 {
			n := copy(p, c.rd.data)
			c.rd.data = c.rd.data[n:]
			c.rd.notifyLocked()
			c.rd.mu.Unlock()
			return n, nil
		}
		if c.rd.eof {
			c.rd.mu.Unlock()
			if c.readEOFGate != nil {
				<-c.readEOFGate
			}
			return 0, io.EOF
		}
		wake := c.rd.wake
		c.rd.mu.Unlock()
		if err := wait(wake, dlWake, c.closedCh, dl); err != nil {
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
		if c.wr.eof {
			c.wr.mu.Unlock()
			return written, io.ErrClosedPipe
		}
		if space := memLimit - len(c.wr.data); space > 0 {
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
		if err := wait(wake, dlWake, c.closedCh, dl); err != nil {
			return written, err
		}
	}
}

// Close marks the conn closed at once (so blocked I/O returns) but, with a
// close gate, does not return until the gate is released.
func (c *memConn) Close() error {
	c.mu.Lock()
	c.closes++
	gate := c.closeGate
	if !c.closed {
		c.closed = true
		close(c.closedCh)
		if !c.link.isBlackhole() {
			for _, b := range []*pipeBuf{c.wr, c.rd} {
				b.mu.Lock()
				b.eof = true
				b.notifyLocked()
				b.mu.Unlock()
			}
		}
	}
	c.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return nil
}

func (c *memConn) setGate(gate chan struct{}) { c.mu.Lock(); c.closeGate = gate; c.mu.Unlock() }

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

func (c *memConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *memConn) closeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closes
}
