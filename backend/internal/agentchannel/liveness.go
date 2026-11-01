package agentchannel

import (
	"context"
	"time"

	"golang.org/x/crypto/ssh"
)

// WallRecheck bounds how long a channel can outlive its hard deadline when the
// monotonic timer is late. The server's Session has its own owner with the
// same bound (agentexec.DeadlineRecheck) and revokes the grant itself.
const WallRecheck = time.Second

// monitor is the only liveness owner after admission. At most one heartbeat
// is in flight: the next is scheduled only after the previous reply. A sender
// blocked in SendRequest is unblocked by teardown closing the same owned conn
// and is then joined like every other worker. Agent data is not inspected, so
// a quiet long-running command never ends the channel by itself.
func (c *Channel) monitor(ctx context.Context, revoked, transport <-chan struct{}) {
	hard := time.NewTimer(time.Until(c.deadline))
	defer hard.Stop()
	// The monotonic timer above can fire late after a sleep or a forward
	// wall-clock jump; the wall clock is re-read at this bounded cadence so the
	// hard deadline still ends the channel within it without agent traffic.
	wall := time.NewTicker(WallRecheck)
	defer wall.Stop()
	next := time.NewTimer(c.budgets.HeartbeatInterval)
	defer next.Stop()
	var reply chan Reason
	var expire <-chan time.Time
	var expireTimer *time.Timer
	defer func() {
		if expireTimer != nil {
			expireTimer.Stop()
		}
	}()
	for {
		select {
		case <-c.stop:
			return
		case <-ctx.Done():
			c.teardown(ReasonCanceled)
			return
		case <-revoked:
			c.teardown(c.deadReason()) // a no-op after our own teardown
			return
		case <-hard.C:
			c.teardown(ReasonHardDeadline)
			return
		case <-wall.C:
			if !time.Now().Before(c.deadline) {
				c.teardown(ReasonHardDeadline)
				return
			}
		case <-transport:
			c.teardown(ReasonTransport)
			return
		case <-next.C:
			out := make(chan Reason, 1)
			reply = out
			c.spawn(func() { out <- c.heartbeat() })
			expireTimer = time.NewTimer(c.budgets.HeartbeatTimeout)
			expire = expireTimer.C
		case failed := <-reply:
			expireTimer.Stop()
			reply, expire = nil, nil
			if failed != ReasonNone {
				c.teardown(failed)
				return
			}
			c.mu.Lock()
			c.answered++
			c.mu.Unlock()
			next.Reset(c.budgets.HeartbeatInterval)
		case <-expire:
			c.teardown(ReasonHeartbeat)
			return
		}
	}
}

// heartbeat accepts only an exact positive reply with an empty payload. A
// refusal, as from a stock SSH server, is a failure and there is no fallback.
// A send error means the transport already ended and is reported as such.
func (c *Channel) heartbeat() Reason {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	ok, payload, err := conn.SendRequest(HeartbeatRequest, true, nil)
	switch {
	case err != nil:
		return ReasonTransport
	case !ok || len(payload) != 0:
		return ReasonHeartbeat
	}
	return ReasonNone
}

// serveGlobal answers only well-formed peer heartbeats within the rate bound.
// A heartbeat that arrives while the channel is ending gets no answer, so a
// torn-down channel whose conn close is still pending never looks alive; the
// close (or the peer's own timeout) tells the peer. Every other global
// request, including forwarding and keepalive requests, is refused and ends
// the connection. It drains until the transport ends so the SSH library
// never blocks on this queue.
func (c *Channel) serveGlobal(reqs <-chan *ssh.Request) {
	for req := range reqs {
		if req.Type == HeartbeatRequest && req.WantReply && len(req.Payload) == 0 {
			if !c.live() {
				continue
			}
			if c.admitPeerBeat() {
				_ = req.Reply(true, nil)
				continue
			}
		}
		_ = req.Reply(false, nil)
		c.teardown(ReasonViolation)
	}
}

// admitPeerBeat bounds the work a peer can cause. Answering a peer heartbeat
// moves no deadline and confirms nothing about our own liveness.
func (c *Channel) admitPeerBeat() bool {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Sub(c.beatWindow) >= time.Second {
		c.beatWindow, c.peerBeats = now, 0
	}
	c.peerBeats++
	return c.peerBeats <= MaxPeerHeartbeats
}

// serveChannels passes the first channel open to the server's admission (a
// nil first refuses every open, as on the client) and refuses any later one.
// There is no destination routing: no channel type carries a host or port.
func (c *Channel) serveChannels(chans <-chan ssh.NewChannel, first chan<- ssh.NewChannel) {
	for open := range chans {
		if first != nil {
			first <- open
			first = nil
			continue
		}
		_ = open.Reject(ssh.Prohibited, "one agent channel per connection")
		c.teardown(ReasonViolation)
	}
}

// serveChannelRequests refuses every channel request (exec, shell, pty-req,
// env, subsystem and all others). The queue ends when the channel ends.
func (c *Channel) serveChannelRequests(reqs <-chan *ssh.Request) {
	for req := range reqs {
		if req.WantReply {
			_ = req.Reply(false, nil)
		}
		c.teardown(ReasonViolation)
	}
	c.teardown(ReasonTransport)
}

// watchExtended treats any extended (stderr) data as a violation instead of
// letting it fill the window unread. It returns when the channel ends.
func (c *Channel) watchExtended(ch ssh.Channel) {
	var one [1]byte
	if n, _ := ch.Stderr().Read(one[:]); n > 0 {
		c.teardown(ReasonViolation)
	}
}
