package residentclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

// Composition is deliberately private until the real credential-input/trust
// design is reviewed. The CI overlay supplies anonymous fixture bytes only.
// One client has one SID, one principal and one server-owned immutable binding.
type client struct {
	mu                     sync.Mutex
	http                   *http.Client
	transport              *http.Transport
	resource, bearer, sid  string
	sequence               uint64
	opened, failed, closed bool
	dictionary             map[string]bool
}

func newClient(resource string, bearer []byte, roots *x509.CertPool) (*client, error) {
	u, e := url.Parse(resource)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.String() != resource || len(resource) > 4096 || roots == nil || len(bearer) == 0 || len(bearer) > 8192 {
		return nil, ErrUnavailable
	}
	for _, b := range bearer {
		if b < 0x21 || b > 0x7e {
			return nil, ErrUnavailable
		}
	}
	tr := &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots.Clone()},
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  30 * time.Second,
		MaxResponseHeaderBytes: 16384,
		DisableKeepAlives:      true, // no stale-connection replay of an uncertain request
	}
	c := &client{resource: resource, bearer: string(bearer), transport: tr}
	c.http = &http.Client{Transport: tr, Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrUnavailable }}
	return c, nil
}

// exchange never exposes remote errors, headers or body fragments to the CLI.
// Every failed exchange poisons future calls. Only original-SID Close remains.
func (c *client) exchange(ctx context.Context, method string, body []byte, status int) ([]byte, http.Header, error) {
	if ctx == nil {
		c.failed = true
		return nil, nil, ErrUnavailable
	}
	r, e := http.NewRequestWithContext(ctx, method, c.resource, bytes.NewReader(body))
	if e != nil {
		c.failed = true
		return nil, nil, ErrUnavailable
	}
	r.GetBody = nil
	r.Header.Set("Authorization", "Bearer "+c.bearer)
	r.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/json")
	}
	if c.sid != "" {
		r.Header.Set(agentgateway.HeaderSession, c.sid)
		r.Header.Set(agentgateway.HeaderProtocol, mcpadapter.ProtocolVersion)
	}
	res, e := c.http.Do(r)
	if e != nil {
		c.failed = true
		return nil, nil, ErrUnavailable
	}
	raw, readErr := io.ReadAll(io.LimitReader(res.Body, mcpadapter.MaxLine+1))
	closeErr := res.Body.Close()
	if readErr != nil || closeErr != nil || len(raw) > mcpadapter.MaxLine || res.StatusCode != status || res.TLS == nil || len(res.TLS.VerifiedChains) == 0 || (status == http.StatusOK && res.Header.Get("Content-Type") != "application/json") || (status != http.StatusOK && len(raw) != 0) {
		c.failed = true
		if res.TLS != nil && len(res.TLS.VerifiedChains) > 0 && res.StatusCode == http.StatusOK {
			return nil, res.Header, ErrUnavailable
		}
		return nil, nil, ErrUnavailable
	}
	return raw, res.Header, nil
}

func (c *client) rpc(ctx context.Context, method string, params any) (json.RawMessage, http.Header, error) {
	if c.sequence >= maxInteger {
		c.failed = true
		return nil, nil, ErrUnavailable
	}
	c.sequence++
	b, e := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.sequence, "method": method, "params": params})
	if e != nil {
		c.failed = true
		return nil, nil, ErrProtocol
	}
	raw, h, e := c.exchange(ctx, http.MethodPost, b, http.StatusOK)
	if e != nil {
		return nil, h, e
	}
	m, e := fields(raw, "jsonrpc", "id", "result")
	id, ok := integer(m["id"])
	if e != nil || !exact(m, "jsonrpc", "id", "result") || !enum(m["jsonrpc"], "2.0") || !ok || id != c.sequence {
		c.failed = true
		return nil, h, ErrProtocol
	}
	return m["result"], h, nil
}

func (c *client) open(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.opened || c.failed || c.closed {
		return ErrUnavailable
	}
	c.opened = true
	v, h, e := c.rpc(ctx, "initialize", map[string]any{"protocolVersion": mcpadapter.ProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "tether-work", "version": "0.0.0-unreleased"}})
	// Capture only a valid original SID before checking the payload, so failed
	// initialization can still abandon that session without another initialize.
	ids := h.Values(agentgateway.HeaderSession)
	if len(ids) == 1 && agentwire.ValidCommandID(ids[0]) {
		c.sid = ids[0]
	}
	if e != nil {
		return e
	}
	if len(ids) != 1 || !agentwire.ValidCommandID(ids[0]) {
		c.failed = true
		return ErrProtocol
	}
	if !validInitialize(v) {
		c.failed = true
		return ErrProtocol
	}
	_, h, e = c.exchange(ctx, http.MethodPost, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), http.StatusAccepted)
	if e != nil {
		return e
	}
	if !c.sameSession(h) {
		c.failed = true
		return ErrProtocol
	}
	return nil
}

func (c *client) sameSession(h http.Header) bool {
	ids := h.Values(agentgateway.HeaderSession)
	return len(ids) == 0 || (len(ids) == 1 && ids[0] == c.sid)
}

func (c *client) Invoke(ctx context.Context, op, id string) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.opened || c.sid == "" || c.failed || c.closed || operation(op) == "" || !agentwire.ValidCommandID(id) {
		return nil, ErrUnavailable
	}
	raw, h, e := c.rpc(ctx, "tools/call", map[string]any{"name": operation(op), "arguments": map[string]any{"request_id": id}})
	if e != nil {
		return nil, e
	}
	if !c.sameSession(h) {
		c.failed = true
		return nil, ErrProtocol
	}
	v, e := decodeTool(raw, op, id)
	if e != nil {
		c.failed = true
	}
	return v, e
}

func (c *client) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrUnavailable
	}
	c.closed = true
	defer func() { c.bearer = ""; c.transport.CloseIdleConnections() }()
	if c.sid == "" {
		return ErrUnavailable
	}
	_, h, e := c.exchange(ctx, http.MethodDelete, nil, http.StatusNoContent)
	if e != nil || !c.sameSession(h) {
		return ErrUnavailable
	}
	return nil
}

func validInitialize(raw []byte) bool {
	m, e := fields(raw, "protocolVersion", "capabilities", "serverInfo", "instructions")
	if e != nil || !exact(m, "protocolVersion", "capabilities", "serverInfo", "instructions") || !enum(m["protocolVersion"], mcpadapter.ProtocolVersion) {
		return false
	}
	c, e := fields(m["capabilities"], "tools")
	if e != nil || !exact(c, "tools") {
		return false
	}
	t, e := fields(c["tools"], "listChanged")
	if e != nil || !exact(t, "listChanged") || string(t["listChanged"]) != "false" {
		return false
	}
	s, e := fields(m["serverInfo"], "name", "version")
	if e != nil || !exact(s, "name", "version") || !enum(s["name"], "rescue-mcp") || !enum(s["version"], "0.0.0-unreleased") {
		return false
	}
	i, ok := str(m["instructions"])
	return ok && len(i) <= 8192 && !strings.ContainsRune(i, 0)
}
