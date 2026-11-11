package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/coder/websocket"
)

// The fixed probe surface. Nothing here is read from the document or from a
// request; the Worker forwards to these exact paths only.
const (
	HTTPPath    = "/tether-probe/v1/echo"
	WSPath      = "/tether-probe/v1/ws"
	Subprotocol = "tether-probe.v1"
	echoType    = "application/octet-stream"
	probeName   = "tether-echo-probe/v1"
	probeNote   = "commandless route probe only; nothing is executed and this route grants no command access"
)

var errProbeConfig = errors.New("tether-echo-probe: refused configuration")

// limits bound one probe. Production uses probeLimits only; tests shorten
// them. A WebSocket lifetime starts before the upgrade and nothing extends it.
type limits struct {
	Body     int           // HTTP echo request body bytes
	Message  int64         // one WebSocket message
	Frames   int           // echoed messages per WebSocket
	HTTP     int           // concurrent HTTP echo requests
	Conns    int           // concurrent WebSockets
	Lifetime time.Duration // one WebSocket
}

func probeLimits() limits {
	return limits{Body: 1024, Message: 1024, Frames: 32, HTTP: 8, Conns: 4, Lifetime: 30 * time.Second}
}

// probe serves the two echo routes to verified, allowlisted Access subjects.
// It holds no signer, grant, channel or runtime and executes nothing.
type probe struct {
	access    *agentgateway.AccessVerifier
	subjects  map[string]bool
	limits    limits
	httpSlots chan struct{}
	wsSlots   chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc

	mu     sync.Mutex
	closed bool
	active sync.WaitGroup // admitted requests; Add only under mu while open
}

// newProbe accepts only a parsed cloudflare_access document whose resource is
// the probe's HTTP echo URL, so the real gateway document (an MCP resource) is
// refused, with no allowed origins. The subject allowlist is exactly the
// document's principals (one to four); email is never consulted. This bounded
// probe has no refresh owner and therefore accepts only the static key mode.
func newProbe(c agentgateway.FileConfig, l limits) (*probe, error) {
	if c.Mode != agentgateway.ModeCloudflareAccess || c.AccessKeySource != "" || len(c.AllowedOrigins) != 0 || probePath(c.Resource) != HTTPPath ||
		len(c.Principals) == 0 || len(c.Principals) > agentgateway.MaxPrincipals ||
		l.Body <= 0 || l.Message <= 0 || l.Frames <= 0 || l.HTTP <= 0 || l.Conns <= 0 || l.Lifetime <= 0 {
		return nil, errProbeConfig
	}
	cfg, err := c.GatewayConfig()
	if err != nil || cfg.Access == nil || cfg.Verifier != nil {
		return nil, errProbeConfig
	}
	subjects := map[string]bool{}
	for _, p := range c.Principals {
		if p.Subject == "" || subjects[p.Subject] {
			return nil, errProbeConfig
		}
		subjects[p.Subject] = true
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &probe{
		access: cfg.Access, subjects: subjects, limits: l,
		httpSlots: make(chan struct{}, l.HTTP), wsSlots: make(chan struct{}, l.Conns), ctx: ctx, cancel: cancel,
	}, nil
}

func probePath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawPath != "" {
		return ""
	}
	return u.Path
}

// ServeHTTP dispatches exact paths and methods only: no prefix match, path
// cleaning, query, Origin or redirect. Every request is authenticated before
// its body or upgrade is touched. Nothing about the request is logged.
func (p *probe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || r.URL.RawPath != "" || (r.URL.Path != HTTPPath && r.URL.Path != WSPath) {
		plain(w, http.StatusNotFound, "not found")
		return
	}
	method := http.MethodPost
	if r.URL.Path == WSPath {
		method = http.MethodGet
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		plain(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		plain(w, http.StatusBadRequest, "query parameters are not accepted")
		return
	}
	if len(r.Header.Values("Origin")) != 0 {
		plain(w, http.StatusForbidden, "origin not allowed")
		return
	}
	if !p.authorize(w, r) {
		return
	}
	if r.URL.Path == HTTPPath {
		p.echoHTTP(w, r)
	} else {
		p.echoWS(w, r)
	}
}

// authorize requires exactly one Access assertion, verifies it with the
// existing AccessVerifier and admits only an allowlisted verified "sub". A
// valid signature for any other subject is refused; header presence proves
// nothing.
func (p *probe) authorize(w http.ResponseWriter, r *http.Request) bool {
	values := r.Header.Values(agentgateway.HeaderAccessAssertion)
	if len(values) != 1 {
		plain(w, http.StatusForbidden, "exactly one Cloudflare Access assertion is required")
		return false
	}
	id, err := p.access.Verify(values[0])
	if err != nil {
		plain(w, http.StatusForbidden, "the Cloudflare Access assertion is not valid")
		return false
	}
	if !p.subjects[id.Subject] {
		plain(w, http.StatusForbidden, "the authenticated subject is not allowed for this probe")
		return false
	}
	return true
}

type echoReply struct {
	Probe         string `json:"probe"`
	CommandAccess bool   `json:"command_access"`
	Note          string `json:"note"`
	EchoBytes     int    `json:"echo_bytes"`
	EchoBase64    string `json:"echo_base64"`
}

func (p *probe) echoHTTP(w http.ResponseWriter, r *http.Request) {
	if !p.admit(p.httpSlots) {
		w.Header().Set("Retry-After", "1")
		plain(w, http.StatusServiceUnavailable, "too many concurrent probe requests")
		return
	}
	defer p.release(p.httpSlots)
	if types := r.Header.Values("Content-Type"); len(types) != 1 || types[0] != echoType {
		plain(w, http.StatusUnsupportedMediaType, "Content-Type must be "+echoType)
		return
	}
	if r.ContentLength > int64(p.limits.Body) {
		plain(w, http.StatusRequestEntityTooLarge, "probe body exceeds the size limit")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(p.limits.Body)+1))
	if err != nil {
		plain(w, http.StatusBadRequest, "request body could not be read")
		return
	}
	if len(body) > p.limits.Body {
		plain(w, http.StatusRequestEntityTooLarge, "probe body exceeds the size limit")
		return
	}
	out, _ := json.Marshal(echoReply{Probe: probeName, Note: probeNote, EchoBytes: len(body), EchoBase64: base64.StdEncoding.EncodeToString(body)})
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// echoWS upgrades only an offer of exactly the fixed subprotocol, then echoes
// at most Frames messages of at most Message bytes, preserving text/binary.
// The lifetime is fixed before the upgrade, as the context deadline and the
// connection deadlines; its end (or Close) tears the connection down even
// during a close handshake.
func (p *probe) echoWS(w http.ResponseWriter, r *http.Request) {
	if offers := r.Header.Values("Sec-WebSocket-Protocol"); len(offers) != 1 || offers[0] != Subprotocol {
		plain(w, http.StatusBadRequest, "the fixed probe subprotocol is required")
		return
	}
	if !p.admit(p.wsSlots) {
		w.Header().Set("Retry-After", "1")
		plain(w, http.StatusServiceUnavailable, "too many concurrent probe connections")
		return
	}
	defer p.release(p.wsSlots)
	// net/http leaves the server's ReadTimeout/WriteTimeout deadlines on a
	// hijacked connection, so before the upgrade the connection gets exactly
	// the fixed lifetime instead. A connection that cannot take it is refused.
	end := time.Now().Add(p.limits.Lifetime)
	rc := http.NewResponseController(w)
	if rc.SetReadDeadline(end) != nil || rc.SetWriteDeadline(end) != nil {
		plain(w, http.StatusInternalServerError, "probe connection deadline could not be set")
		return
	}
	life, cancel := context.WithDeadline(p.ctx, end)
	defer cancel()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{Subprotocol}, CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return // the library already answered with an HTTP error
	}
	defer ws.CloseNow()
	stop := context.AfterFunc(life, func() { _ = ws.CloseNow() })
	defer stop()
	if ws.Subprotocol() != Subprotocol {
		_ = ws.Close(websocket.StatusPolicyViolation, "probe subprotocol required")
		return
	}
	ws.SetReadLimit(p.limits.Message)
	for range p.limits.Frames {
		typ, msg, err := ws.Read(life)
		if err != nil {
			return
		}
		if err := ws.Write(life, typ, msg); err != nil {
			return
		}
	}
	_ = ws.Close(websocket.StatusNormalClosure, "probe frame budget reached")
}

// admit reserves one slot without waiting; a closed probe admits nothing.
func (p *probe) admit(slots chan struct{}) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	select {
	case slots <- struct{}{}:
		p.active.Add(1)
		return true
	default:
		return false
	}
}

func (p *probe) release(slots chan struct{}) {
	<-slots
	p.active.Done()
}

// Close refuses new requests, ends every WebSocket and waits within ctx for
// admitted requests to return. Upgraded connections are not tracked by the
// HTTP server, so this is their only owner.
func (p *probe) Close(ctx context.Context) error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.cancel()
	done := make(chan struct{})
	go func() {
		p.active.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func plain(w http.ResponseWriter, code int, message string) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, message+"\n")
}
