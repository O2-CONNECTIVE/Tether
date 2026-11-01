package agentwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
)

// Client multiplexes bounded requests over one stream with two goroutines. It
// never retries: a request that may have been delivered without a reply is
// reported as ErrUncertain, and reusing the same Start request ID on the same
// live stream is the caller's explicit, idempotent way to learn its outcome.
// Core errors are returned as the agentexec sentinels.
//
// Every reply must answer an outstanding or abandoned request exactly once.
// An unsolicited, duplicate or already answered reply is a protocol violation
// and closes the stream. At most MaxAbandoned requests may wait for a late
// reply; one more abandonment closes the stream rather than growing the set.
type Client struct {
	conn      io.ReadWriteCloser
	slots     chan struct{}
	send      chan []byte
	closed    chan struct{}
	once      sync.Once
	mu        sync.Mutex
	err       error
	last      uint64
	pending   map[uint64]chan map[string]json.RawMessage
	abandoned map[uint64]struct{}
}

// NewClient takes ownership of conn, which must already be the authenticated
// agent channel produced by trusted composition.
func NewClient(conn io.ReadWriteCloser) (*Client, error) {
	if conn == nil {
		return nil, ErrUnconfigured
	}
	c := &Client{
		conn: conn, slots: make(chan struct{}, maxClientCalls), send: make(chan []byte), closed: make(chan struct{}),
		pending: map[uint64]chan map[string]json.RawMessage{}, abandoned: map[uint64]struct{}{},
	}
	go c.readLoop()
	go c.writeLoop()
	return c, nil
}

// Close ends the stream. The server treats this as transport loss.
func (c *Client) Close() error {
	c.fail(ErrClosed)
	return nil
}

// Done is closed once the stream has ended for any reason.
func (c *Client) Done() <-chan struct{} { return c.closed }

func (c *Client) fail(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.closed)
		_ = c.conn.Close()
	})
}

func (c *Client) cause() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Client) writeLoop() {
	for {
		select {
		case frame := <-c.send:
			if err := writeFrame(c.conn, frame); err != nil {
				c.fail(ErrClosed)
				return
			}
		case <-c.closed:
			return
		}
	}
}

func (c *Client) readLoop() {
	for {
		frame, err := readFrame(c.conn)
		if err != nil {
			if errors.Is(err, ErrProtocol) {
				c.fail(ErrProtocol)
			} else {
				c.fail(ErrClosed)
			}
			return
		}
		id, body, ok := decodeEnvelope(frame)
		c.mu.Lock()
		reply, waiting := c.pending[id]
		_, late := c.abandoned[id]
		delete(c.pending, id)
		delete(c.abandoned, id)
		c.mu.Unlock()
		switch {
		case !ok || (!waiting && !late):
			c.fail(ErrProtocol)
			return
		case waiting:
			reply <- body // buffered and answered once
		}
		// A late reply to an abandoned call is consumed and dropped.
	}
}

func decodeEnvelope(frame []byte) (uint64, map[string]json.RawMessage, bool) {
	if CheckJSON(frame, false) != nil {
		return 0, nil, false
	}
	m, err := Fields(frame, "v", "id", "error", "status", "output")
	if err != nil || len(m) != 3 {
		return 0, nil, false
	}
	var v uint16
	var id uint64
	if !field(m, "v", &v) || v != ProtocolVersion || !field(m, "id", &id) || id == 0 {
		return 0, nil, false
	}
	return id, m, true
}

// abandon moves a sent request without a reply to the bounded late set.
func (c *Client) abandon(id uint64) {
	c.mu.Lock()
	_, waiting := c.pending[id]
	delete(c.pending, id)
	overflow := false
	if waiting {
		if len(c.abandoned) >= MaxAbandoned {
			overflow = true
		} else {
			c.abandoned[id] = struct{}{}
		}
	}
	c.mu.Unlock()
	if overflow {
		c.fail(ErrClosed)
	}
}

func (c *Client) forget(id uint64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) call(ctx context.Context, r request) (map[string]json.RawMessage, error) {
	if ctx == nil {
		return nil, invalid()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotSent, err)
	}
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrNotSent, ctx.Err())
	case <-c.closed:
		return nil, fmt.Errorf("%w: %w", ErrNotSent, c.cause())
	}
	defer func() { <-c.slots }()
	reply := make(chan map[string]json.RawMessage, 1)
	c.mu.Lock()
	if c.err != nil {
		defer c.mu.Unlock()
		return nil, fmt.Errorf("%w: %w", ErrNotSent, c.err)
	}
	c.last++
	r.id = c.last
	c.pending[r.id] = reply
	c.mu.Unlock()
	frame, err := r.encode()
	if err != nil {
		c.forget(r.id)
		return nil, invalid()
	}
	select {
	case c.send <- frame:
	case <-ctx.Done():
		c.forget(r.id)
		return nil, fmt.Errorf("%w: %w", ErrNotSent, ctx.Err())
	case <-c.closed:
		c.forget(r.id)
		return nil, fmt.Errorf("%w: %w", ErrNotSent, c.cause())
	}
	select {
	case body := <-reply:
		return body, nil
	case <-ctx.Done():
	case <-c.closed:
	}
	select {
	case body := <-reply:
		return body, nil
	default:
	}
	c.abandon(r.id)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%w: %w", ErrUncertain, ctx.Err())
	}
	return nil, fmt.Errorf("%w: %w", ErrUncertain, c.cause())
}

// protocol closes the stream after the peer sent a well-framed but wrong reply.
func (c *Client) protocol() error {
	c.fail(ErrProtocol)
	return ErrProtocol
}

// remote returns nil unless body is an error reply.
func (c *Client) remote(body map[string]json.RawMessage) error {
	raw, ok := body["error"]
	if !ok {
		return nil
	}
	var code string
	if json.Unmarshal(raw, &code) == nil {
		if code == "internal" {
			return ErrRemoteInternal
		}
		for _, e := range coreErrors {
			if e.code == code {
				return e.err
			}
		}
	}
	return c.protocol()
}

func (c *Client) status(body map[string]json.RawMessage, commandID string) (Status, error) {
	if err := c.remote(body); err != nil {
		return Status{}, err
	}
	st, ok := decodeStatus(body["status"])
	if !ok || (commandID != "" && st.CommandID != commandID) {
		return Status{}, c.protocol()
	}
	return st, nil
}

func invalid() error { return fmt.Errorf("%w: %w", ErrNotSent, agentexec.ErrInvalid) }

// Start submits command bytes for the fixed shell stdin of the Session profile.
// requestID is the caller's idempotency key on this stream. Once the request
// was handed to the stream, an internal error or an unreadable reply means the
// command may have started: it is returned as ErrUncertain, never retried.
func (c *Client) Start(ctx context.Context, requestID string, command []byte, timeoutMillis uint32) (Status, error) {
	if c == nil || !ValidRequestID(requestID) || len(command) == 0 || len(command) > MaxCommandBytes || timeoutMillis == 0 {
		return Status{}, invalid()
	}
	body, err := c.call(ctx, request{op: opStart, requestID: requestID, command: command, timeout: timeoutMillis})
	if err != nil {
		return Status{}, err
	}
	st, err := c.status(body, "")
	if errors.Is(err, ErrRemoteInternal) || errors.Is(err, ErrProtocol) {
		return Status{}, fmt.Errorf("%w: %w", ErrUncertain, err)
	}
	return st, err
}

func (c *Client) Status(ctx context.Context, commandID string) (Status, error) {
	return c.ref(ctx, opStatus, commandID)
}

// Cancel stops only this command; it does not revoke the grant.
func (c *Client) Cancel(ctx context.Context, commandID string) (Status, error) {
	return c.ref(ctx, opCancel, commandID)
}

func (c *Client) ref(ctx context.Context, op, commandID string) (Status, error) {
	if c == nil || !ValidCommandID(commandID) {
		return Status{}, invalid()
	}
	body, err := c.call(ctx, request{op: op, commandID: commandID})
	if err != nil {
		return Status{}, err
	}
	return c.status(body, commandID)
}

// Output reads at most maxBytes from cursor and acknowledges every earlier
// byte of the command.
func (c *Client) Output(ctx context.Context, commandID string, cursor uint64, maxBytes int) (agentexec.Output, error) {
	if c == nil || !ValidCommandID(commandID) || maxBytes <= 0 || maxBytes > MaxOutputBytes {
		return agentexec.Output{}, invalid()
	}
	body, err := c.call(ctx, request{op: opOutput, commandID: commandID, cursor: cursor, maxBytes: maxBytes})
	if err != nil {
		return agentexec.Output{}, err
	}
	if err := c.remote(body); err != nil {
		return agentexec.Output{}, err
	}
	out, ok := decodeOutput(body["output"], cursor, maxBytes)
	if !ok {
		return agentexec.Output{}, c.protocol()
	}
	return out, nil
}
