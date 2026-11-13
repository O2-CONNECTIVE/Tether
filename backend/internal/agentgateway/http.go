package agentgateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

const (
	HeaderSession  = "Mcp-Session-Id"
	HeaderProtocol = "Mcp-Protocol-Version"
	// MetadataPrefix is the RFC 9728 protected resource metadata location.
	MetadataPrefix = "/.well-known/oauth-protected-resource"
	sessionIDBytes = 16
)

func protectedResourceMetadata(resource *url.URL, v *Verifier) (string, []byte, error) {
	u := url.URL{Scheme: resource.Scheme, Host: resource.Host, Path: MetadataPrefix + resource.Path}
	body, err := json.Marshal(map[string]any{
		"resource": v.Audience(), "authorization_servers": []string{v.Issuer()},
		"scopes_supported": []string{v.Scope()}, "bearer_methods_supported": []string{"header"},
	})
	if err != nil {
		return "", nil, ErrConfig
	}
	return u.String(), body, nil
}

// ServeHTTP serves one MCP endpoint (the resource URI path) and, in OAuth
// bearer mode only, its protected resource metadata. Only POST (one JSON-RPC
// message, answered with one application/json body) and DELETE (end the MCP
// session) are supported; GET server-sent event streams are refused with 405,
// never emulated.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case g.inflight <- struct{}{}:
		defer func() { <-g.inflight }()
	default:
		w.Header().Set("Retry-After", "1")
		plain(w, http.StatusServiceUnavailable, "too many concurrent requests")
		return
	}
	switch {
	case g.metadata != nil && (r.URL.Path == MetadataPrefix || r.URL.Path == MetadataPrefix+g.path):
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			plain(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, g.metadata)
	case r.URL.Path == g.path:
		g.serveMCP(w, r)
	default:
		plain(w, http.StatusNotFound, "not found")
	}
}

func (g *Gateway) serveMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "POST, DELETE")
		plain(w, http.StatusMethodNotAllowed, "only POST and DELETE are supported; server-sent event streams are not offered")
		return
	}
	if origins := r.Header.Values("Origin"); len(origins) > 0 && (len(origins) != 1 || !g.origins[origins[0]]) {
		plain(w, http.StatusForbidden, "origin not allowed")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		plain(w, http.StatusBadRequest, "query parameters are not accepted; credentials are read from one request header only")
		return
	}
	p := g.authorize(w, r)
	if p == nil {
		return
	}
	if versions := r.Header.Values(HeaderProtocol); len(versions) > 1 || (len(versions) == 1 && versions[0] != mcpadapter.ProtocolVersion) {
		plain(w, http.StatusBadRequest, "unsupported "+HeaderProtocol+"; only "+mcpadapter.ProtocolVersion+" is served")
		return
	}
	ids := r.Header.Values(HeaderSession)
	if len(ids) > 1 {
		plain(w, http.StatusBadRequest, "one "+HeaderSession+" header is allowed")
		return
	}
	if r.Method == http.MethodDelete {
		g.terminate(w, r, p, ids)
		return
	}
	if !mediaJSON(r.Header.Values("Content-Type")) {
		plain(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	if !acceptsJSON(r.Header.Values("Accept")) {
		plain(w, http.StatusNotAcceptable, "Accept must allow application/json")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, mcpadapter.MaxLine+1))
	if err != nil {
		plain(w, http.StatusBadRequest, "request body could not be read")
		return
	}
	if len(body) > mcpadapter.MaxLine {
		plain(w, http.StatusRequestEntityTooLarge, "message exceeds the size limit")
		return
	}
	if len(ids) == 0 {
		g.initialize(w, p, body)
		return
	}
	// After initialize the version header is required: the pinned version is
	// the only one served, so no older default is assumed.
	if r.Header.Get(HeaderProtocol) == "" {
		plain(w, http.StatusBadRequest, HeaderProtocol+" is required")
		return
	}
	s := g.lookup(p, ids[0])
	if s == nil {
		plain(w, http.StatusNotFound, "unknown or ended MCP session; initialize a new one")
		return
	}
	reply := s.mcp.Handle(body)
	switch reply.Kind {
	case mcpadapter.HTTPRequiresBinding:
		g.mu.Lock()
		g.dropLocked(ids[0], s)
		g.mu.Unlock()
		plain(w, http.StatusNotFound, "discovery-only MCP session ended; initialize a new session before calling tools")
	case mcpadapter.HTTPResponse:
		writeJSON(w, http.StatusOK, reply.Body)
	case mcpadapter.HTTPRejected:
		writeJSON(w, http.StatusBadRequest, reply.Body)
	case mcpadapter.HTTPAbandoned:
		if !g.live(ids[0], s) {
			plain(w, http.StatusNotFound, "the MCP session ended")
			return
		}
		w.WriteHeader(http.StatusAccepted)
	default:
		w.WriteHeader(http.StatusAccepted)
	}
}

// authorize authenticates the bearer token (401) and then authorizes its
// verified subject as one configured principal (403). Session IDs, request
// IDs and other headers never identify the caller. In Access mode only the
// Access assertion is read; see authorizeAccess.
func (g *Gateway) authorize(w http.ResponseWriter, r *http.Request) *principal {
	if g.access != nil {
		return g.authorizeAccess(w, r)
	}
	values := r.Header.Values("Authorization")
	if len(values) == 0 {
		g.challenge(w, http.StatusUnauthorized, "")
		return nil
	}
	scheme, token, found := strings.Cut(values[0], " ")
	if len(values) != 1 || !found || !strings.EqualFold(scheme, "Bearer") || !token68(token) {
		g.challenge(w, http.StatusUnauthorized, "invalid_token")
		return nil
	}
	id, err := g.verifier.Verify(token)
	switch {
	case errors.Is(err, ErrScope):
		g.challenge(w, http.StatusForbidden, "insufficient_scope")
		return nil
	case err != nil:
		g.challenge(w, http.StatusUnauthorized, "invalid_token")
		return nil
	}
	p := g.bySubject[id.Subject]
	if p == nil {
		plain(w, http.StatusForbidden, "the authenticated subject is not a configured principal")
		return nil
	}
	return p
}

// authorizeAccess requires exactly one valid Access assertion and maps its
// verified user "sub" to a configured principal. A request reaching the origin
// without one did not pass the Access edge, so the origin answers 403 and
// sends no WWW-Authenticate challenge: the edge owns discovery and the OAuth
// challenge, and the origin never names an issuer of its own. Authorization
// (the client's opaque edge token) and cookies are not read, so no bearer
// verification or other fallback exists in this mode.
func (g *Gateway) authorizeAccess(w http.ResponseWriter, r *http.Request) *principal {
	values := r.Header.Values(HeaderAccessAssertion)
	if len(values) != 1 {
		plain(w, http.StatusForbidden, "exactly one Cloudflare Access assertion is required")
		return nil
	}
	id, err := g.access.Verify(values[0])
	if err != nil {
		plain(w, http.StatusForbidden, "the Cloudflare Access assertion is not valid")
		return nil
	}
	p := g.bySubject[id.Subject]
	if p == nil {
		plain(w, http.StatusForbidden, "the authenticated subject is not a configured principal")
		return nil
	}
	return p
}

func (g *Gateway) challenge(w http.ResponseWriter, status int, code string) {
	v := `Bearer resource_metadata="` + g.metadataURL + `"`
	if code != "" {
		v += `, error="` + code + `"`
	}
	if code == "insufficient_scope" {
		v += `, scope="` + g.verifier.Scope() + `"`
	}
	w.Header().Set("WWW-Authenticate", v)
	plain(w, status, http.StatusText(status))
}

// initialize binds a new session to the current route when one is present.
// Otherwise it creates a permanently unbound, authenticated discovery session.
// No later request can bind that session to a grant.
func (g *Gateway) initialize(w http.ResponseWriter, p *principal, body []byte) {
	g.mu.Lock()
	g.expireLocked()
	rt, full, closed := p.route, p.sessions >= MaxSessionsPerPrincipal, g.closed
	var resident *residentSession
	residentDiscovery := false
	if g.resident != nil {
		if entry := g.resident.entries[p]; entry != nil {
			if gen := g.resident.control.ResidentGeneration(entry.enrollment.HostLabel, p.label); gen != "" {
				resident = &residentSession{gateway: g, principal: p, entry: entry, generation: gen}
				rt = nil
			} else if g.residentOnly {
				residentDiscovery = true
				rt = nil
			}
		}
	}
	if rt != nil && ended(rt) {
		rt = nil
	}
	if full && resident != nil {
		if _, s := g.replaceableLocked(p, resident.generation); s != nil {
			full = false
		}
	}
	g.mu.Unlock()
	switch {
	case closed:
		writeJSON(w, http.StatusServiceUnavailable, rpcError("the gateway is closed"))
		return
	case g.residentOnly && resident == nil && !residentDiscovery:
		writeJSON(w, http.StatusServiceUnavailable, rpcError("resident control unavailable"))
		return
	case full:
		writeJSON(w, http.StatusTooManyRequests, rpcError("too many MCP sessions for this principal; DELETE an unused session"))
		return
	}
	var mcp *mcpadapter.HTTPSession
	var err error
	if resident != nil {
		mcp, err = mcpadapter.NewResidentHTTPSession(resident, g.callTimeout, nil)
	} else if residentDiscovery {
		mcp, err = mcpadapter.NewResidentDiscoveryHTTPSession(g.callTimeout)
	} else if rt == nil {
		mcp, err = mcpadapter.NewDiscoveryHTTPSession(g.callTimeout)
	} else {
		mcp, err = mcpadapter.NewHTTPSession(mcpadapter.HTTPConfig{Backend: rt.client, CallTimeout: g.callTimeout, Slots: rt.slots})
	}
	if err != nil {
		plain(w, http.StatusInternalServerError, "internal error")
		return
	}
	reply := mcp.Handle(body)
	if !reply.Initialized {
		mcp.Close()
		if reply.Error {
			writeJSON(w, http.StatusBadRequest, reply.Body)
		} else {
			writeJSON(w, http.StatusBadRequest, rpcError(HeaderSession+" is required; send initialize first"))
		}
		return
	}
	raw := make([]byte, sessionIDBytes)
	if _, err := rand.Read(raw); err != nil {
		mcp.Close()
		plain(w, http.StatusInternalServerError, "internal error")
		return
	}
	id := hex.EncodeToString(raw)
	g.mu.Lock()
	ok := !g.closed && (rt == nil || (p.route == rt && !ended(rt))) && g.sessions[id] == nil
	if ok && resident != nil {
		if g.resident.control.ResidentGeneration(resident.entry.enrollment.HostLabel, p.label) != resident.generation {
			ok = false
		} else if p.sessions >= MaxSessionsPerPrincipal {
			if old, s := g.replaceableLocked(p, resident.generation); s != nil {
				g.dropLocked(old, s)
			}
		}
	}
	if ok && p.sessions < MaxSessionsPerPrincipal {
		g.sessions[id] = &session{owner: p, route: rt, mcp: mcp, lastUsed: g.clock(), resident: resident}
		p.sessions++
	} else {
		// A concurrent initialize can take the slot seen free above.
		full, ok = ok, false
	}
	g.mu.Unlock()
	if !ok {
		mcp.Close()
		if full {
			writeJSON(w, http.StatusTooManyRequests, rpcError("too many MCP sessions for this principal; DELETE an unused session"))
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, rpcError("the approved grant ended during initialize"))
		return
	}
	w.Header().Set(HeaderSession, id)
	writeJSON(w, http.StatusOK, reply.Body)
}

// lookup returns the session only for the same principal, bound to that
// principal's current live route (or permanently unbound discovery) and not idle
// beyond SessionIdle. Another
// principal's session ID is answered exactly like an unknown one.
func (g *Gateway) lookup(p *principal, id string) *session {
	if len(id) != 2*sessionIDBytes || strings.Trim(id, "0123456789abcdef") != "" {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.sessions[id]
	if s == nil || s.owner != p {
		return nil
	}
	now := g.clock()
	if now.Sub(s.lastUsed) > SessionIdle || now.Before(s.lastUsed) || (s.route != nil && (p.route != s.route || ended(s.route))) {
		g.dropLocked(id, s)
		return nil
	}
	s.lastUsed = now
	return s
}

func (g *Gateway) live(id string, s *session) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sessions[id] == s
}

// terminate ends one MCP session. In-flight calls are abandoned without a
// reply; started remote commands keep running and the grant stays live.
func (g *Gateway) terminate(w http.ResponseWriter, r *http.Request, p *principal, ids []string) {
	if len(ids) != 1 || r.Header.Get(HeaderProtocol) == "" {
		plain(w, http.StatusBadRequest, HeaderSession+" and "+HeaderProtocol+" are required")
		return
	}
	s := g.lookup(p, ids[0])
	if s == nil {
		plain(w, http.StatusNotFound, "unknown or ended MCP session")
		return
	}
	g.mu.Lock()
	g.dropLocked(ids[0], s)
	g.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (g *Gateway) dropLocked(id string, s *session) {
	if g.sessions[id] == s {
		delete(g.sessions, id)
		s.owner.sessions--
		s.mcp.Close()
	}
}

// replaceableLocked returns the resident session of p that a resident
// initialize at MaxSessionsPerPrincipal ends to make room: the least recently
// used one of another generation, which can no longer start work, or else the
// least recently used one of generation current. Resident work is read by
// generation, not by session, so a later session of the same generation still
// reads it. Discovery sessions and sessions bound to an approved route are
// never returned; they keep the bound.
func (g *Gateway) replaceableLocked(p *principal, current string) (string, *session) {
	var id string
	var victim *session
	victimSuperseded := false
	for k, s := range g.sessions {
		if s.owner != p || s.route != nil || s.resident == nil {
			continue
		}
		superseded := s.resident.generation != current
		if victim == nil || (superseded && !victimSuperseded) ||
			(superseded == victimSuperseded && s.lastUsed.Before(victim.lastUsed)) {
			id, victim, victimSuperseded = k, s, superseded
		}
	}
	return id, victim
}

func (g *Gateway) expireLocked() {
	now := g.clock()
	for id, s := range g.sessions {
		if now.Sub(s.lastUsed) > SessionIdle || now.Before(s.lastUsed) {
			g.dropLocked(id, s)
		}
	}
}

func ended(r *route) bool {
	select {
	case <-r.ended:
		return true
	default:
		return false
	}
}

func token68(s string) bool {
	if s == "" || len(s) > MaxTokenBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-._~+/=", c) >= 0) {
			return false
		}
	}
	return true
}

func mediaJSON(values []string) bool {
	if len(values) != 1 {
		return false
	}
	mt, params, err := mime.ParseMediaType(values[0])
	if err != nil || mt != "application/json" {
		return false
	}
	for k, v := range params {
		if k != "charset" || !strings.EqualFold(v, "utf-8") {
			return false
		}
	}
	return true
}

func acceptsJSON(values []string) bool {
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			mt, params, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err != nil {
				continue
			}
			if q, ok := params["q"]; ok {
				if f, err := strconv.ParseFloat(q, 64); err != nil || !(f > 0) {
					continue
				}
			}
			if mt == "application/json" || mt == "application/*" || mt == "*/*" {
				return true
			}
		}
	}
	return false
}

func rpcError(message string) []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32001, "message": message}})
	return b
}

func writeJSON(w http.ResponseWriter, status int, body []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func plain(w http.ResponseWriter, status int, message string) {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, message+"\n")
}
