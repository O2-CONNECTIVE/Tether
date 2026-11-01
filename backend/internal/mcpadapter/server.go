// Package mcpadapter is a thin MCP stdio server for the pinned protocol
// version 2025-06-18. It exposes exactly four tools that map one to one onto
// the bound agentwire.Client: rescue_start, rescue_status, rescue_output and
// rescue_cancel. It issues, extends or selects no grant, Binding, host, mode,
// profile or credential, never retries an uncertain start and never reconnects.
// Owner-only cleanup is not a tool. Only JSON-RPC messages are written to out.
package mcpadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

const (
	// ProtocolVersion is the only version this adapter speaks. initialize
	// always answers with it; a client that cannot use it must disconnect.
	ProtocolVersion    = "2025-06-18"
	MaxLine            = 512 << 10
	MaxConcurrent      = 4
	DefaultCallTimeout = 30 * time.Second
	serverName         = "rescue-mcp"
	serverVersion      = "0.0.0-unreleased"
)

var (
	ErrUnconfigured  = errors.New("mcpadapter: agent channel client is not configured")
	ErrChannelClosed = errors.New("mcpadapter: the agent channel ended; the grant is revoked and not resumed")
)

// Config is supplied by trusted composition. Backend is already bound to one
// approved grant through an authenticated agent channel.
type Config struct {
	Backend     *agentwire.Client
	CallTimeout time.Duration // bounds one wire round trip; zero selects the default
}

const (
	stateNew = iota
	stateInitializing
	stateReady
)

type call struct {
	cancel    context.CancelFunc
	cancelled bool
}

type server struct {
	ctx     context.Context
	backend *agentwire.Client
	timeout time.Duration
	w       *writer
	state   int // read and written only by the reading goroutine
	slots   chan struct{}
	wg      sync.WaitGroup
	mu      sync.Mutex
	calls   map[string]*call
}

type lineResult struct {
	line    []byte
	tooLong bool
	err     error
}

// Serve handles newline-delimited JSON-RPC from in until in ends, ctx is done,
// the agent channel ends or out fails. Tool calls run on at most MaxConcurrent
// goroutines so that ping and cancellation stay responsive.
//
// Serve takes ownership of in: it closes in before returning and joins its one
// reader goroutine, so in must unblock a pending Read when closed (pipes and
// files that support interruptible reads do). out stays owned by the caller
// and is never closed. One blocked output Write may outlive Serve, reported as
// ErrOutputUnconfirmed; no further writes start and out must not be reused.
// The command-line caller exits promptly after revoking its channel. When the
// channel ends, in-flight calls still report their transport error and Serve
// returns ErrChannelClosed instead of answering every later call with it. On
// other returns in-flight calls are cancelled without a reply. The caller then
// closes Backend, which the remote side treats as transport loss.
func Serve(ctx context.Context, cfg Config, in io.ReadCloser, out io.Writer) error {
	if ctx == nil || cfg.Backend == nil || in == nil || out == nil || cfg.CallTimeout < 0 {
		if in != nil {
			_ = in.Close()
		}
		return ErrUnconfigured
	}
	if cfg.CallTimeout == 0 {
		cfg.CallTimeout = DefaultCallTimeout
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &server{
		ctx: ctx, backend: cfg.Backend, timeout: cfg.CallTimeout, w: &writer{out: out, ctx: ctx, done: cfg.Backend.Done(), failedCh: make(chan struct{})},
		slots: make(chan struct{}, MaxConcurrent), calls: map[string]*call{},
	}
	lines := make(chan lineResult)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		r := bufio.NewReaderSize(in, 64<<10)
		for {
			line, tooLong, err := readLine(r, MaxLine)
			select {
			case lines <- lineResult{line: line, tooLong: tooLong, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var err error
	channelEnded := false
loop:
	for s.w.failed() == nil {
		select {
		case <-ctx.Done():
			err = ctx.Err()
			break loop
		case <-s.backend.Done():
			channelEnded = true
			break loop
		case <-s.w.failedCh:
			err = s.w.failed()
			break loop
		case lr := <-lines:
			switch {
			case lr.tooLong:
				s.w.fail(nil, codeInvalidRequest, "message exceeds the size limit")
			case lr.err == nil:
				s.message(lr.line)
			}
			if lr.err != nil {
				if !errors.Is(lr.err, io.EOF) {
					err = lr.err
				}
				break loop
			}
		}
	}
	if channelEnded {
		s.wg.Wait() // their transport errors are still reported
		err = ErrChannelClosed
	}
	cancel()
	s.wg.Wait()
	_ = in.Close()
	<-readerDone
	outputErr := s.w.failed()
	if err == nil {
		err = outputErr
	} else if errors.Is(outputErr, ErrOutputUnconfirmed) {
		err = errors.Join(err, outputErr)
	}
	return err
}

func (s *server) message(line []byte) {
	if agentwire.CheckJSON(line, true) != nil {
		s.w.fail(nil, codeParse, "parse error")
		return
	}
	if firstByte(line) == '[' {
		s.w.fail(nil, codeInvalidRequest, "batch messages are not supported")
		return
	}
	m, err := agentwire.Fields(line, "jsonrpc", "id", "method", "params", "result", "error")
	if err != nil {
		s.w.fail(nil, codeInvalidRequest, "invalid request")
		return
	}
	var id *requestID
	rawID, hasID := m["id"]
	if hasID {
		var ok bool
		if id, ok = parseID(rawID); !ok {
			s.w.fail(nil, codeInvalidRequest, "id must be a string or an integer")
			return
		}
	}
	var version, method string
	if !isString(m["jsonrpc"]) || json.Unmarshal(m["jsonrpc"], &version) != nil || version != "2.0" {
		s.w.fail(id, codeInvalidRequest, "jsonrpc must be \"2.0\"")
		return
	}
	_, hasResult := m["result"]
	_, hasError := m["error"]
	rawMethod, hasMethod := m["method"]
	if !hasMethod {
		if hasID && hasResult != hasError {
			return // a response; this server sends no requests, so it is dropped
		}
		s.w.fail(id, codeInvalidRequest, "invalid request")
		return
	}
	params, hasParams := m["params"]
	if hasResult || hasError || !isString(rawMethod) || json.Unmarshal(rawMethod, &method) != nil || (hasParams && firstByte(params) != '{') {
		s.w.fail(id, codeInvalidRequest, "invalid request")
		return
	}
	if !hasID {
		s.notification(method, params)
		return
	}
	s.request(id, method, params)
}

func firstByte(b []byte) byte {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
		default:
			return c
		}
	}
	return 0
}

func (s *server) request(id *requestID, method string, params json.RawMessage) {
	switch method {
	case "ping":
		s.w.result(id, map[string]any{})
	case "initialize":
		switch {
		case s.state != stateNew:
			s.w.fail(id, codeInvalidRequest, "initialize was already received")
		case !validInitialize(params):
			s.w.fail(id, codeInvalidParams, "initialize requires protocolVersion, capabilities and clientInfo")
		default:
			s.state = stateInitializing
			s.w.result(id, map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
				"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
				"instructions":    instructions,
			})
		}
	case "tools/list":
		if s.state != stateReady {
			s.w.fail(id, codeInvalidRequest, "server not initialized")
			return
		}
		s.w.result(id, map[string]any{"tools": toolList()})
	case "tools/call":
		if s.state != stateReady {
			s.w.fail(id, codeInvalidRequest, "server not initialized")
			return
		}
		s.dispatch(id, params)
	default:
		s.w.fail(id, codeMethodNotFound, "method not found")
	}
}

func validInitialize(params json.RawMessage) bool {
	m, err := agentwire.Fields(params)
	if err != nil {
		return false
	}
	var version string
	var info map[string]json.RawMessage
	return isString(m["protocolVersion"]) && json.Unmarshal(m["protocolVersion"], &version) == nil && version != "" &&
		firstByte(m["capabilities"]) == '{' && firstByte(m["clientInfo"]) == '{' && json.Unmarshal(m["clientInfo"], &info) == nil &&
		isString(info["name"])
}

func (s *server) notification(method string, params json.RawMessage) {
	switch method {
	case "notifications/initialized":
		if s.state == stateInitializing {
			s.state = stateReady
		}
	case "notifications/cancelled":
		m, err := agentwire.Fields(params)
		if err != nil {
			return
		}
		id, ok := parseID(m["requestId"])
		if !ok {
			return
		}
		s.mu.Lock()
		if c := s.calls[id.key]; c != nil {
			c.cancelled = true
			c.cancel()
		}
		s.mu.Unlock()
	}
	// Other notifications are ignored; notifications never receive a reply.
}

func (s *server) dispatch(id *requestID, params json.RawMessage) {
	m, err := agentwire.Fields(params, "name", "arguments", "_meta")
	var name string
	if err != nil || !isString(m["name"]) || json.Unmarshal(m["name"], &name) != nil {
		s.w.fail(id, codeInvalidParams, "tools/call requires name and arguments")
		return
	}
	t := findTool(name)
	if t == nil {
		s.w.fail(id, codeInvalidParams, "unknown tool: "+name)
		return
	}
	args, ok := m["arguments"]
	if !ok {
		s.w.fail(id, codeInvalidParams, "arguments are required")
		return
	}
	invoke, err := t.parse(args)
	if err != nil {
		s.w.fail(id, codeInvalidParams, err.Error())
		return
	}
	select {
	case s.slots <- struct{}{}:
	default:
		s.w.fail(id, codeServerBusy, "too many concurrent tool calls")
		return
	}
	s.mu.Lock()
	if s.calls[id.key] != nil {
		s.mu.Unlock()
		<-s.slots
		s.w.fail(id, codeInvalidRequest, "request id is already in flight; this message was not executed, "+
			"but the original call with this id may still execute and will be answered under the same id")
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	c := &call{cancel: cancel}
	s.calls[id.key] = c
	s.mu.Unlock()
	s.wg.Add(1)
	go s.run(ctx, id, c, invoke)
}

// run executes one tool call. A call cancelled by the client or by shutdown
// gets no response, as the cancellation notification requires.
func (s *server) run(ctx context.Context, id *requestID, c *call, invoke func(context.Context, *agentwire.Client) map[string]any) {
	defer s.wg.Done()
	defer func() { <-s.slots }()
	var result map[string]any
	panicked := func() (panicked bool) {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		callCtx, cancel := context.WithTimeout(ctx, s.timeout)
		defer cancel()
		result = invoke(callCtx, s.backend)
		return false
	}()
	s.mu.Lock()
	delete(s.calls, id.key)
	suppressed := c.cancelled || s.ctx.Err() != nil
	s.mu.Unlock()
	c.cancel()
	switch {
	case suppressed:
	case panicked:
		s.w.fail(id, codeInternal, "internal error")
	default:
		s.w.result(id, result)
	}
}
