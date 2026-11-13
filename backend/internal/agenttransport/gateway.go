package agenttransport

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

// Registrar is the gateway's registration contract; *agentgateway.Gateway
// implements it. Connect owns conn on every call and closes it on refusal.
type Registrar interface {
	Connect(label string, d agentgateway.Dispatch, conn net.Conn) error
}

// EnrolledHost is one operator-enrolled recipient host: its label, its raw
// Ed25519 host public key and the principal labels it may serve. A request
// can select an entry by label but never supply or replace its key.
type EnrolledHost struct {
	Label      string
	PublicKey  ssh.PublicKey
	Principals []string
}

// HandlerConfig is chosen by the trusted gateway composition, never a request.
// Endpoint is the exact public wss URL of this handler (behind the existing
// named tunnel) and is the resource identity every proof is bound to.
type HandlerConfig struct {
	Endpoint           string
	Hosts              []EnrolledHost
	Registrar          Registrar
	Budgets            Budgets
	Clock              func() time.Time    // wall clock for dispatch freshness; nil is time.Now
	ResidentPrincipals []ResidentPrincipal // opt-in identity-only control; no grant producer
}

type enrolled struct {
	key        ssh.PublicKey
	wire       []byte
	principals map[string]bool
}

// Handler accepts host registrations and hands each verified stream to the
// Registrar. It does not listen; the operator composition mounts it.
type Handler struct {
	endpoint        string
	path            string
	hosts           map[string]*enrolled
	registrar       Registrar
	residentSigners map[string]ssh.Signer
	residents       map[string]*residentPeer
	budgets         Budgets
	clock           func() time.Time
	slots           chan struct{}
	ctx             context.Context
	cancel          context.CancelFunc

	mu         sync.Mutex
	closed     bool
	owned      []*closer // every transport not yet confirmed closed
	reserved   int       // admitted requests that have not tracked a transport yet
	inflight   int       // admitted requests whose ServeHTTP has not returned
	idle       chan struct{}
	idleClosed bool
}

// admission is one admitted request's reservation. tracked is set, under
// Handler.mu, exactly when the reservation becomes an owned transport, so a
// panic later in the request cannot return the reservation twice.
type admission struct{ tracked, pending bool }

// NewHandler validates the fixed configuration. It opens nothing.
func NewHandler(cfg HandlerConfig) (*Handler, error) {
	u, err := parseEndpoint(cfg.Endpoint)
	if err != nil || cfg.Registrar == nil || len(cfg.Hosts) == 0 || len(cfg.Hosts) > MaxHosts || !cfg.Budgets.valid() {
		return nil, ErrConfig
	}
	h := &Handler{endpoint: cfg.Endpoint, path: u.Path, hosts: map[string]*enrolled{}, registrar: cfg.Registrar,
		budgets: cfg.Budgets, clock: cfg.Clock, slots: make(chan struct{}, MaxPending), idle: make(chan struct{})}
	if h.clock == nil {
		h.clock = time.Now
	}
	keys := map[string]bool{}
	for _, e := range cfg.Hosts {
		if !validLabel(e.Label) || h.hosts[e.Label] != nil || e.PublicKey == nil || len(e.Principals) == 0 || len(e.Principals) > agentgateway.MaxPrincipals {
			return nil, ErrConfig
		}
		key, err := rawEd25519(e.PublicKey.Marshal())
		if err != nil || keys[string(key.Marshal())] {
			return nil, ErrConfig
		}
		keys[string(key.Marshal())] = true
		entry := &enrolled{key: key, wire: key.Marshal(), principals: map[string]bool{}}
		for _, p := range e.Principals {
			if !validLabel(p) || entry.principals[p] {
				return nil, ErrConfig
			}
			entry.principals[p] = true
		}
		h.hosts[e.Label] = entry
	}
	if err := h.configureResidents(cfg.ResidentPrincipals); err != nil {
		return nil, err
	}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	return h, nil
}

// ServeHTTP refuses before upgrading anything that is not an exact, credential
// free registration request: another method, path or query, any Origin (no
// browser registration), Cookie or Authorization (no ambient credential), or a
// subprotocol other than exactly Subprotocol (or the separately configured
// identity-only ResidentSubprotocol). At most MaxPending registrations
// run at once; the rest get 503 without an upgrade.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.registrar == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	if status := h.precheck(r); status != 0 {
		if status == http.StatusMethodNotAllowed {
			w.Header().Set("Allow", http.MethodGet)
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	if !h.admit() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	a := &admission{pending: true}
	defer h.release(a)
	if r.Header.Get("Sec-WebSocket-Protocol") == ResidentSubprotocol {
		h.serveResident(w, r, a)
		return
	}
	h.serve(w, r, a)
}

func (h *Handler) precheck(r *http.Request) int {
	switch {
	case r.Method != http.MethodGet:
		return http.StatusMethodNotAllowed
	case r.URL == nil || r.URL.Path != h.path:
		return http.StatusNotFound
	case r.URL.RawQuery != "" || r.URL.ForceQuery:
		return http.StatusBadRequest
	case len(r.Header.Values("Origin")) != 0 || len(r.Header.Values("Cookie")) != 0 ||
		len(r.Header.Values("Authorization")) != 0 || len(r.Header.Values("Proxy-Authorization")) != 0:
		return http.StatusForbidden
	case !slices.Equal(r.Header.Values("Sec-WebSocket-Protocol"), []string{Subprotocol}) && !(len(h.residentSigners) > 0 && slices.Equal(r.Header.Values("Sec-WebSocket-Protocol"), []string{ResidentSubprotocol})):
		return http.StatusBadRequest
	}
	return 0
}

// admit takes a pending slot and reserves one tracked transport, so pending,
// live and unconfirmed transports together never exceed MaxTracked.
func (h *Handler) admit() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prune()
	if h.closed || len(h.owned)+h.reserved >= MaxTracked {
		return false
	}
	select {
	case h.slots <- struct{}{}:
	default:
		return false
	}
	h.reserved++
	h.inflight++
	return true
}

// release frees the pending slot; an unused reservation is returned. The last
// request after Close marks the Handler idle; no goroutine waits for it.
func (h *Handler) release(a *admission) {
	h.mu.Lock()
	if !a.tracked {
		h.reserved--
	}
	h.inflight--
	pending := a.pending
	a.pending = false
	if h.closed && h.inflight == 0 {
		h.markIdle()
	}
	h.mu.Unlock()
	if pending {
		<-h.slots
	}
}

// releasePending returns only the completed authentication slot. The live
// control remains tracked/inflight for bounded ownership and shutdown.
func (h *Handler) releasePending(a *admission) {
	h.mu.Lock()
	pending := a.pending
	a.pending = false
	h.mu.Unlock()
	if pending {
		<-h.slots
	}
}

// markIdle closes idle once. Caller holds h.mu.
func (h *Handler) markIdle() {
	if !h.idleClosed {
		h.idleClosed = true
		close(h.idle)
	}
}

// prune drops transports whose close is confirmed. Caller holds h.mu.
func (h *Handler) prune() {
	h.owned = slices.DeleteFunc(h.owned, func(c *closer) bool { return c.confirmed() })
}

// track turns the request's reservation into an owned transport.
func (h *Handler) track(c *closer, a *admission) {
	h.mu.Lock()
	h.reserved--
	a.tracked = true
	h.owned = append(h.owned, c)
	h.mu.Unlock()
}

// serve owns the transport from before the upgrade: the closer, the
// transport context and both budgets exist before Accept, so a stalled or
// partial upgrade is closed by the same owner as soon as the hijack hands the
// connection over. Every path that does not end in a successful Connect,
// including a panic (which still propagates), closes that transport.
func (h *Handler) serve(w http.ResponseWriter, r *http.Request, a *admission) {
	life, cancel := context.WithCancel(h.ctx)
	c := newCloser(cancel)
	h.track(c, a)
	// Any end of the transport context (budget, Handler.Close, the owner's
	// Close) starts the same single close.
	context.AfterFunc(life, c.start)
	registration := time.AfterFunc(h.budgets.Registration, cancel)
	approval := time.AfterFunc(h.budgets.Approval, cancel)
	committed := false
	defer func() {
		if !committed {
			registration.Stop()
			approval.Stop()
			c.settle()
			c.start()
		}
	}()
	ws, err := websocket.Accept(&hijackWriter{ResponseWriter: w, closer: c}, r, &websocket.AcceptOptions{
		Subprotocols: []string{Subprotocol}, CompressionMode: websocket.CompressionDisabled,
	})
	c.setWS(ws)
	if !c.settle() || err != nil {
		return // without a hijack the library already answered with an HTTP error
	}
	label, d, resident, err := h.register(life, ws)
	if !registration.Stop() || err != nil {
		return
	}
	// Connect owns the stream from here; a refusal closes it, and a late
	// budget expiry ends it. Either way the same closer is the only owner.
	stream := newStream(life, ws, c, h.budgets.Cleanup)
	if resident != nil {
		if registrar, ok := h.registrar.(interface {
			ConnectResident(string, agentgateway.Dispatch, agentgateway.ResidentWorkBinding, net.Conn) error
		}); ok {
			err = registrar.ConnectResident(label, d, *resident, stream)
		} else {
			_ = stream.Close()
			err = ErrRefused
		}
	} else {
		err = h.registrar.Connect(label, d, stream)
	}
	committed = approval.Stop() && err == nil
}

// register runs the challenge and verifies the proof. The nonce is fresh for
// this connection, used for exactly one registration message and then cleared.
// Labels select configuration; the key is always the enrolled one.
func (h *Handler) register(ctx context.Context, ws *websocket.Conn) (string, agentgateway.Dispatch, *agentgateway.ResidentWorkBinding, error) {
	if ws.Subprotocol() != Subprotocol {
		return "", agentgateway.Dispatch{}, nil, ErrRefused
	}
	ws.SetReadLimit(MaxRegistrationMessage)
	nonce, err := newNonce()
	if err != nil {
		return "", agentgateway.Dispatch{}, nil, ErrRefused
	}
	defer clear(nonce)
	if err := ws.Write(ctx, websocket.MessageText, encodeChallenge(nonce)); err != nil {
		return "", agentgateway.Dispatch{}, nil, ErrRefused
	}
	typ, msg, err := ws.Read(ctx)
	// The library limit is a backstop; the bound is checked here exactly.
	if err != nil || typ != websocket.MessageText || len(msg) > MaxRegistrationMessage {
		return "", agentgateway.Dispatch{}, nil, ErrRefused
	}
	reg, err := decodeRegister(msg)
	if err != nil {
		reg, err = decodeWorkRegister(msg)
	}
	if err != nil {
		return "", agentgateway.Dispatch{}, nil, ErrRefused
	}
	host := h.hosts[reg.host]
	data := transcript(h.endpoint, reg.host, reg.principal, reg.dispatch, nonce)
	if reg.resident != nil {
		current, ok := h.currentWork(*reg.resident)
		if !ok || reg.dispatch.Deadline.After(time.UnixMilli(reg.resident.DeadlineMillis)) {
			return "", agentgateway.Dispatch{}, nil, ErrRefused
		}
		*reg.resident = current
		data = workTranscript(h.endpoint, reg.host, reg.principal, reg.dispatch, nonce, *reg.resident)
	}
	if host == nil || !host.principals[reg.principal] || !bytes.Equal(reg.dispatch.HostKey, host.wire) ||
		checkDispatch(reg.dispatch, h.clock()) != nil ||
		verify(host.key, data, reg.signature) != nil {
		return "", agentgateway.Dispatch{}, nil, ErrRefused
	}
	if err := ws.Write(ctx, websocket.MessageText, encodeAccepted()); err != nil {
		return "", agentgateway.Dispatch{}, nil, ErrRefused
	}
	return reg.principal, reg.dispatch, reg.resident, nil
}

// Close refuses new registrations, ends every pending registration and every
// stream this Handler accepted (the gateway routes then end and the remote
// hosts revoke), waits for in-flight requests and then for each transport's
// close within ctx. Unconfirmed transports stay owned (and keep their
// MaxTracked slots); a later Close waits on the same attempts again. A request
// stuck inside net/http before its hijack returns is reported, not waited for
// by an extra goroutine. It does not close the Registrar.
func (h *Handler) Close(ctx context.Context) error {
	if h == nil || h.cancel == nil || ctx == nil {
		return ErrConfig
	}
	h.mu.Lock()
	h.closed = true
	if h.inflight == 0 {
		h.markIdle()
	}
	h.mu.Unlock()
	h.cancel()
	select {
	case <-h.idle:
	case <-ctx.Done():
		select {
		case <-h.idle:
		default:
			return errors.Join(ErrCleanupUnknown, ctx.Err())
		}
	}
	h.mu.Lock()
	owned := slices.Clone(h.owned)
	h.mu.Unlock()
	var errs []error
	for _, c := range owned {
		if err := c.wait(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	h.mu.Lock()
	h.prune()
	h.mu.Unlock()
	return errors.Join(errs...)
}

// hijackWriter hands the hijacked connection to the request's closer, so its
// close is owned and observed independently of the WebSocket library. If the
// close already started (budget or Handler.Close during a stalled upgrade),
// the connection is closed at once and the hijack fails.
type hijackWriter struct {
	http.ResponseWriter
	closer   *closer
	hijacked bool
}

func (w *hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.hijacked {
		return nil, nil, http.ErrHijacked
	}
	w.hijacked = true
	conn, brw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	raw, err := w.closer.attach(conn)
	if err != nil {
		return nil, nil, err
	}
	// net/http may leave its server read/write deadlines on a hijacked conn;
	// they would cut a live stream. The registration and channel budgets
	// bound every wait from here on.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		w.closer.start()
		return nil, nil, err
	}
	return raw, brw, nil
}
