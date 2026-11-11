package mcpadapter

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

// HTTPConfig binds one MCP Streamable HTTP session to one backend that trusted
// composition already bound to one approved grant. Slots, when non-nil, is a
// bound shared by every session of the same backend; nil gives this session
// its own MaxConcurrent slots.
type HTTPConfig struct {
	Backend     *agentwire.Client
	CallTimeout time.Duration // bounds one wire round trip; zero selects the default
	Slots       chan struct{}
}

// HTTPReplyKind tells the HTTP owner how to answer one POSTed message.
type HTTPReplyKind int

const (
	// HTTPResponse carries one JSON-RPC response (result or error) to a request.
	HTTPResponse HTTPReplyKind = iota + 1
	// HTTPAccepted is a notification or a client response that was accepted;
	// there is no body.
	HTTPAccepted
	// HTTPRejected is input that is not an acceptable message. Body is a
	// JSON-RPC error whose id is the parsed one or null.
	HTTPRejected
	// HTTPAbandoned is a request abandoned by closing the session. No JSON-RPC response exists for it; the remote
	// command, if any started, is not stopped by this.
	HTTPAbandoned
	// HTTPRequiresBinding is a valid command request on a permanently unbound
	// discovery session. Nothing was dispatched; the HTTP owner must end this
	// session and require a new initialize, never attach a backend here.
	HTTPRequiresBinding
)

// HTTPReply is the result of one Handle call.
type HTTPReply struct {
	Kind HTTPReplyKind
	Body []byte
	// Error is true when Body is a JSON-RPC error response.
	Error bool
	// Initialized is true only for a successful initialize response; the HTTP
	// owner assigns its session ID to that reply and to nothing else.
	Initialized bool
}

// HTTPSession applies the same lifecycle, four tools, strict parsing, result
// bounds and error mapping as Serve to one message at a time, so that each
// HTTP POST is answered on its own response. It has no transport, reads no
// header and never authenticates: the HTTP owner must authenticate the caller
// and route it to the session bound to that caller's approved grant.
type HTTPSession struct {
	backend           *agentwire.Client
	resident          ResidentBackend
	residentDiscovery bool // Immutable catalog-only mode, with no authority/backend.
	timeout           time.Duration
	slots             chan struct{}
	ctx               context.Context
	cancel            context.CancelFunc
	mu                sync.Mutex
	state             int
	calls             map[string]*call
}

// NewHTTPSession creates one session in the pre-initialize state.
func NewHTTPSession(cfg HTTPConfig) (*HTTPSession, error) {
	return newHTTPSession(cfg, false)
}

// NewDiscoveryHTTPSession serves only static protocol/catalog information. It
// never acquires a backend, even when a grant is attached later. Authentication
// and member authorization remain the HTTP owner's responsibility per request.
func NewDiscoveryHTTPSession(timeout time.Duration) (*HTTPSession, error) {
	return newHTTPSession(HTTPConfig{CallTimeout: timeout}, true)
}

func newHTTPSession(cfg HTTPConfig, discovery bool) (*HTTPSession, error) {
	if (!discovery && cfg.Backend == nil) || (discovery && cfg.Backend != nil) || cfg.CallTimeout < 0 || (cfg.Slots != nil && cap(cfg.Slots) == 0) {
		return nil, ErrUnconfigured
	}
	if cfg.CallTimeout == 0 {
		cfg.CallTimeout = DefaultCallTimeout
	}
	if cfg.Slots == nil {
		cfg.Slots = make(chan struct{}, MaxConcurrent)
	}
	// The session context has no parent so that an abandoned session holds no
	// registration in a long-lived context; Close cancels it.
	ctx, cancel := context.WithCancel(context.Background())
	return &HTTPSession{backend: cfg.Backend, timeout: cfg.CallTimeout, slots: cfg.Slots, ctx: ctx, cancel: cancel, calls: map[string]*call{}}, nil
}

// Close cancels every in-flight call without a reply. It does not stop remote
// commands, revoke the grant or close Backend, which other sessions may share.
func (s *HTTPSession) Close() {
	if s != nil {
		s.cancel()
	}
}

// Handle processes exactly one JSON-RPC message. Concurrent calls are allowed:
// a tool call blocks only its own caller, at most for CallTimeout. Explicit
// request cancellation receives a JSON-RPC cancellation error for HTTP framing;
// this does not claim that a remote command stopped. Disconnection
// of the HTTP caller is not a cancellation; only notifications/cancelled and
// Close abandon a call.
func (s *HTTPSession) Handle(message []byte) HTTPReply {
	if len(message) > MaxLine {
		return rejected(nil, codeInvalidRequest, "message exceeds the size limit")
	}
	if agentwire.CheckJSON(message, true) != nil {
		return rejected(nil, codeParse, "parse error")
	}
	if firstByte(message) == '[' {
		return rejected(nil, codeInvalidRequest, "batch messages are not supported")
	}
	m, err := agentwire.Fields(message, "jsonrpc", "id", "method", "params", "result", "error")
	if err != nil {
		return rejected(nil, codeInvalidRequest, "invalid request")
	}
	var id *requestID
	rawID, hasID := m["id"]
	if hasID {
		var ok bool
		if id, ok = parseID(rawID); !ok {
			return rejected(nil, codeInvalidRequest, "id must be a string or an integer")
		}
	}
	var version, method string
	if !isString(m["jsonrpc"]) || json.Unmarshal(m["jsonrpc"], &version) != nil || version != "2.0" {
		return rejected(id, codeInvalidRequest, "jsonrpc must be \"2.0\"")
	}
	_, hasResult := m["result"]
	_, hasError := m["error"]
	rawMethod, hasMethod := m["method"]
	if !hasMethod {
		if hasID && hasResult != hasError {
			return HTTPReply{Kind: HTTPAccepted} // a response; this server sends no requests
		}
		return rejected(id, codeInvalidRequest, "invalid request")
	}
	params, hasParams := m["params"]
	if hasResult || hasError || !isString(rawMethod) || json.Unmarshal(rawMethod, &method) != nil || (hasParams && firstByte(params) != '{') {
		return rejected(id, codeInvalidRequest, "invalid request")
	}
	if !hasID {
		s.notification(method, params)
		return HTTPReply{Kind: HTTPAccepted}
	}
	return s.request(id, method, params)
}

func (s *HTTPSession) notification(method string, params json.RawMessage) {
	switch method {
	case "notifications/initialized":
		s.mu.Lock()
		if s.state == stateInitializing {
			s.state = stateReady
		}
		s.mu.Unlock()
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
}

func (s *HTTPSession) request(id *requestID, method string, params json.RawMessage) HTTPReply {
	s.mu.Lock()
	state := s.state
	initialized := method == "initialize" && state == stateNew && validInitialize(params)
	if initialized {
		s.state = stateInitializing
	}
	s.mu.Unlock()
	switch method {
	case "ping":
		return response(id, map[string]any{})
	case "initialize":
		switch {
		case state != stateNew:
			return failed(id, codeInvalidRequest, "initialize was already received")
		case !initialized:
			return failed(id, codeInvalidParams, "initialize requires protocolVersion, capabilities and clientInfo")
		}
		text := instructions
		if s.resident != nil {
			text = residentInstructions
		} else if s.residentDiscovery {
			text = residentDiscoveryInstructions
		}
		r := response(id, map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
			"instructions":    text,
		})
		r.Initialized = !r.Error
		return r
	case "tools/list":
		if state != stateReady {
			return failed(id, codeInvalidRequest, "server not initialized")
		}
		if s.resident != nil {
			return response(id, map[string]any{"tools": residentToolList(s.resident)})
		}
		if s.residentDiscovery {
			return response(id, map[string]any{"tools": residentCatalog(true, true, true, true)})
		}
		return response(id, map[string]any{"tools": toolList()})
	case "tools/call":
		if state != stateReady {
			return failed(id, codeInvalidRequest, "server not initialized")
		}
		return s.toolCall(id, params)
	}
	return failed(id, codeMethodNotFound, "method not found")
}

// toolCall runs one tool on the caller's goroutine with the same parsing,
// slot bound, in-flight ID refusal, timeout and suppression rules as run.
func (s *HTTPSession) toolCall(id *requestID, params json.RawMessage) HTTPReply {
	if s.residentDiscovery {
		return failed(id, codeInvalidRequest, residentDiscoveryInstructions)
	}
	if s.resident != nil {
		return s.residentToolCall(id, params)
	}
	m, err := agentwire.Fields(params, "name", "arguments", "_meta")
	var name string
	if err != nil || !isString(m["name"]) || json.Unmarshal(m["name"], &name) != nil {
		return failed(id, codeInvalidParams, "tools/call requires name and arguments")
	}
	t := findTool(name)
	if t == nil {
		return failed(id, codeInvalidParams, "unknown tool: "+name)
	}
	args, ok := m["arguments"]
	if !ok {
		return failed(id, codeInvalidParams, "arguments are required")
	}
	invoke, err := t.parse(args)
	if err != nil {
		return failed(id, codeInvalidParams, err.Error())
	}
	if s.backend == nil {
		return HTTPReply{Kind: HTTPRequiresBinding}
	}
	return s.invokeHTTP(id, func(ctx context.Context) map[string]any { return invoke(ctx, s.backend) })
}

func (s *HTTPSession) invokeHTTP(id *requestID, invoke func(context.Context) map[string]any) HTTPReply {
	select {
	case s.slots <- struct{}{}:
	default:
		return failed(id, codeServerBusy, "too many concurrent tool calls")
	}
	defer func() { <-s.slots }()
	s.mu.Lock()
	if s.calls[id.key] != nil {
		s.mu.Unlock()
		return failed(id, codeInvalidRequest, "request id is already in flight; this message was not executed, "+
			"but the original call with this id may still execute and will be answered under the same id")
	}
	ctx, cancel := context.WithCancel(s.ctx)
	c := &call{cancel: cancel}
	s.calls[id.key] = c
	s.mu.Unlock()
	var result map[string]any
	panicked := func() (panicked bool) {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		callCtx, stop := context.WithTimeout(ctx, s.timeout)
		defer stop()
		result = invoke(callCtx)
		return false
	}()
	s.mu.Lock()
	delete(s.calls, id.key)
	cancelled := c.cancelled
	suppressed := s.ctx.Err() != nil
	s.mu.Unlock()
	cancel()
	switch {
	case suppressed:
		return HTTPReply{Kind: HTTPAbandoned}
	case cancelled:
		return failed(id, -32800, "request cancelled; remote delivery may be uncertain; use the same request_id to inspect the original start, not a new id")
	case panicked:
		return failed(id, codeInternal, "internal error")
	}
	return response(id, result)
}

func response(id *requestID, result any) HTTPReply {
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id.raw, "result": result})
	if err != nil {
		return failed(id, codeInternal, "internal error")
	}
	return HTTPReply{Kind: HTTPResponse, Body: b}
}

func errorBody(id *requestID, code int, message string) []byte {
	raw := json.RawMessage("null")
	if id != nil {
		raw = id.raw
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": raw, "error": map[string]any{"code": code, "message": message}})
	return b
}

func failed(id *requestID, code int, message string) HTTPReply {
	return HTTPReply{Kind: HTTPResponse, Body: errorBody(id, code, message), Error: true}
}

func rejected(id *requestID, code int, message string) HTTPReply {
	return HTTPReply{Kind: HTTPRejected, Body: errorBody(id, code, message), Error: true}
}
