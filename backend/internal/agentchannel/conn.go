package agentchannel

import (
	"errors"
	"net"
	"os"
	"sync"
	"time"
)

// guardedConn wraps exactly the conn the owner supplied. Until admission every
// read and write uses the handshake deadline; afterwards each call gets its
// own stall budget. The hard deadline caps both. The supplied conn must
// support deadlines: a deadline error fails the call instead of waiting.
type guardedConn struct {
	net.Conn
	hard        time.Time
	readStall   time.Duration
	writeStall  time.Duration
	onStall     func(Reason)
	beforeClose func(admitted bool)

	mu        sync.Mutex
	handshake time.Time // zero after admission

	closeMu sync.Mutex
	closed  bool
	closing chan struct{} // the close attempt in flight, if any
}

func (g *guardedConn) Read(p []byte) (int, error) {
	if err := g.Conn.SetReadDeadline(g.deadline(g.readStall)); err != nil {
		return 0, err
	}
	n, err := g.Conn.Read(p)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		g.onStall(g.stallReason(ReasonReadStall))
	}
	return n, err
}

func (g *guardedConn) Write(p []byte) (int, error) {
	if err := g.Conn.SetWriteDeadline(g.deadline(g.writeStall)); err != nil {
		return 0, err
	}
	n, err := g.Conn.Write(p)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		g.onStall(g.stallReason(ReasonWriteStall))
	}
	return n, err
}

func (g *guardedConn) deadline(stall time.Duration) time.Time {
	g.mu.Lock()
	d := g.handshake
	g.mu.Unlock()
	if d.IsZero() {
		d = time.Now().Add(stall)
	}
	if g.hard.Before(d) {
		d = g.hard
	}
	return d
}

func (g *guardedConn) stallReason(live Reason) Reason {
	if !time.Now().Before(g.hard) {
		return ReasonHardDeadline
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.handshake.IsZero() {
		return ReasonHandshake
	}
	return live
}

func (g *guardedConn) admit() {
	g.mu.Lock()
	g.handshake = time.Time{}
	g.mu.Unlock()
}

func (g *guardedConn) isAdmitted() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.handshake.IsZero()
}

// Close is what the SSH library calls when a handshake fails or its transport
// ends. The bound grant is revoked first on every path (after recording the
// transport reason once admitted). It does not wait for the close, so a conn
// whose Close hangs cannot hold the handshake; the owner's join confirms.
func (g *guardedConn) Close() error {
	g.beforeClose(g.isAdmitted())
	g.attempt()
	return nil
}

// attempt starts at most one Close of the owned conn at a time and returns
// its completion. Success is final; a failure leaves the same conn owned so
// the next attempt retries it and never any other connection.
func (g *guardedConn) attempt() <-chan struct{} {
	g.closeMu.Lock()
	defer g.closeMu.Unlock()
	if g.closing != nil {
		return g.closing
	}
	done := make(chan struct{})
	if g.closed {
		close(done)
		return done
	}
	g.closing = done
	go func() {
		err := g.Conn.Close()
		g.closeMu.Lock()
		g.closed, g.closing = err == nil, nil
		g.closeMu.Unlock()
		close(done)
	}()
	return done
}

func (g *guardedConn) isClosed() bool {
	g.closeMu.Lock()
	defer g.closeMu.Unlock()
	return g.closed
}
