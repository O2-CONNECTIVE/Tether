package agenttransport

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

// ResidentPrincipal is supplied by the protected normal gateway composition.
// A signer enables identity-only resident authentication, never an execution grant.
type ResidentPrincipal struct {
	Label  string
	Signer ssh.Signer
}
type residentPending struct {
	intent ResidentFixedIntent
	reply  chan error
}
type residentPeer struct {
	handler   *Handler
	binding   residentBinding
	ctx       context.Context
	cancel    context.CancelFunc
	ws        *websocket.Conn
	link      *closer
	key       ssh.PublicKey
	signer    ssh.Signer
	gate      chan struct{}
	mu        sync.Mutex
	sequence  uint64
	seen      map[string]bool
	pending   *residentPending
	intents   map[string]ResidentFixedIntent
	ready     bool // guarded by Handler.mu
	finished  chan struct{}
	registrar Registrar
}

func residentMapKey(host, principal string) string { return host + "/" + principal }

func (h *Handler) serveResident(w http.ResponseWriter, r *http.Request, a *admission) {
	life, cancel := context.WithCancel(h.ctx)
	c := newCloser(cancel)
	h.track(c, a)
	stopLife := context.AfterFunc(life, c.start)
	defer stopLife()
	registration := time.AfterFunc(h.budgets.Approval, cancel)
	var ping <-chan struct{}
	var peer *residentPeer
	defer func() {
		registration.Stop()
		cancel()
		c.settle()
		c.start()
		clean := residentCleanup(c, ping, h.budgets.Cleanup)
		if peer != nil {
			defer close(peer.finished)
			peer.mu.Lock()
			var last ResidentFixedIntent
			for _, intent := range peer.intents {
				if intent.sequence > last.sequence {
					last = intent
				}
			}
			peer.mu.Unlock()
			if last.sequence != 0 {
				h.mu.Lock()
				if h.residentReceipts == nil {
					h.residentReceipts = map[string]residentReceipt{}
				}
				h.residentReceipts[residentMapKey(peer.binding.host, peer.binding.principal)] = residentReceipt{binding: peer.binding, intent: last}
				h.mu.Unlock()
			}
		}
		if peer != nil && clean == nil {
			h.mu.Lock()
			k := residentMapKey(peer.binding.host, peer.binding.principal)
			if h.residents[k] == peer {
				delete(h.residents, k)
			}
			h.mu.Unlock()
		}
	}()
	ws, e := websocket.Accept(&hijackWriter{ResponseWriter: w, closer: c}, r, &websocket.AcceptOptions{Subprotocols: []string{ResidentSubprotocol}, CompressionMode: websocket.CompressionDisabled})
	c.setWS(ws)
	if !c.settle() || e != nil {
		return
	}
	ws.SetReadLimit(MaxRegistrationMessage)
	auth, stop := context.WithTimeout(life, h.budgets.Registration)
	b, key, signer, e := h.authenticateResident(auth, ws)
	stop()
	if e != nil {
		return
	}
	peer = &residentPeer{handler: h, registrar: h.registrar, binding: b, ctx: life, cancel: cancel, ws: ws, link: c, key: key, signer: signer, gate: make(chan struct{}, 1), seen: map[string]bool{}, intents: map[string]ResidentFixedIntent{}, finished: make(chan struct{})}
	peer.gate <- struct{}{}
	reserve, stopReserve := context.WithTimeout(life, h.budgets.Registration)
	e = h.reserveResident(reserve, peer)
	stopReserve()
	if e != nil {
		_ = ws.Close(websocket.StatusPolicyViolation, "resident identity refused")
		peer = nil
		return
	}
	sig, e := sign(signer, signer.PublicKey(), residentTranscript(b, "gateway-accepted"))
	if e != nil {
		return
	}
	accepted, stopAccepted := context.WithTimeout(life, h.budgets.Registration)
	e = residentWrite(accepted, ws, residentJSON(map[string]string{"type": "resident_accepted", "signature": b64.EncodeToString(sig)}))
	stopAccepted()
	if e != nil {
		return
	}
	if !registration.Stop() || life.Err() != nil {
		return
	}
	h.mu.Lock()
	peer.ready = !h.closed && life.Err() == nil
	h.mu.Unlock()
	h.releasePending(a)
	heartbeat := make(chan struct{})
	ping = heartbeat
	go residentHeartbeat(life, ws, cancel, heartbeat)
	_ = peer.readReceipts()
}

func (h *Handler) authenticateResident(ctx context.Context, ws *websocket.Conn) (bound residentBinding, hostKey ssh.PublicKey, principalSigner ssh.Signer, result error) {
	defer func() {
		if errors.Is(result, ErrRefused) {
			_ = ws.Close(websocket.StatusPolicyViolation, "resident identity refused")
		}
	}()
	var b residentBinding
	if ws.Subprotocol() != ResidentSubprotocol {
		return b, nil, nil, ErrRefused
	}
	msg, e := residentRead(ctx, ws)
	if e != nil {
		return b, nil, nil, e
	}
	m, ok := residentFields(msg, "resident_hello", "host", "principal", "client_nonce")
	if !ok {
		return b, nil, nil, ErrRefused
	}
	host := h.hosts[m["host"]]
	signer := h.residentSigners[m["principal"]]
	nonce, ok := decodeB64(m["client_nonce"])
	if host == nil || signer == nil || !host.principals[m["principal"]] || !ok || len(nonce) != NonceSize {
		return b, nil, nil, ErrRefused
	}
	server, e := newNonce()
	if e != nil {
		return b, nil, nil, e
	}
	generation, e := newNonce()
	if e != nil {
		return b, nil, nil, e
	}
	b = residentBinding{endpoint: h.endpoint, host: m["host"], principal: m["principal"], generation: hex.EncodeToString(generation), clientNonce: nonce, serverNonce: server, hostKey: host.wire, principalKey: signer.PublicKey().Marshal()}
	sig, e := sign(signer, signer.PublicKey(), residentTranscript(b, "gateway-challenge"))
	if e != nil {
		return b, nil, nil, e
	}
	if e = residentWrite(ctx, ws, residentJSON(map[string]string{"type": "resident_challenge", "generation": b.generation, "server_nonce": b64.EncodeToString(server), "signature": b64.EncodeToString(sig)})); e != nil {
		return b, nil, nil, e
	}
	msg, e = residentRead(ctx, ws)
	if e != nil {
		return b, nil, nil, e
	}
	m, ok = residentFields(msg, "resident_proof", "signature")
	if !ok {
		return b, nil, nil, ErrRefused
	}
	sig, ok = residentSignature(m)
	if !ok || verify(host.key, residentTranscript(b, "host-proof"), sig) != nil {
		return b, nil, nil, ErrRefused
	}
	return b, host.key, signer, nil
}

func (p *residentPeer) readReceipts() error {
	for {
		msg, e := residentRead(p.ctx, p.ws)
		if e != nil {
			return e
		}
		if m, ok := residentFields(msg, "resident_collected", "generation", "sequence", "request_id", "cleanup", "signature"); ok {
			p.mu.Lock()
			i, found := p.intents[m["request_id"]]
			binding := p.binding
			p.mu.Unlock()
			if !found {
				k := residentMapKey(p.binding.host, p.binding.principal)
				h := p.handler
				h.mu.Lock()
				r, ok := h.residentReceipts[k]
				if ok && m["request_id"] == r.intent.requestID && m["generation"] == r.intent.generation && m["sequence"] == strconv.FormatUint(r.intent.sequence, 10) && bytes.Equal(r.binding.hostKey, p.binding.hostKey) && r.binding.host == p.binding.host && r.binding.principal == p.binding.principal {
					i, binding, found = r.intent, r.binding, true
				}
				h.mu.Unlock()
			}
			if !found || m["generation"] != i.generation || m["sequence"] != strconv.FormatUint(i.sequence, 10) {
				return ErrRefused
			}
			proof := collectionProof(binding, i, m["cleanup"])
			proof.Signature, _ = decodeB64(m["signature"])
			if verify(p.key, agentgateway.ResidentCollectionTranscript(proof), proof.Signature) != nil {
				return ErrRefused
			}
			registrar, ok := p.registrar.(interface {
				ResidentCollected(agentgateway.ResidentCollectionProof) error
			})
			if !ok || registrar.ResidentCollected(proof) != nil {
				return ErrRefused
			}
			if binding.generation != p.binding.generation {
				h := p.handler
				h.mu.Lock()
				delete(h.residentReceipts, residentMapKey(binding.host, binding.principal))
				h.mu.Unlock()
			}
			continue
		}
		p.mu.Lock()
		pending := p.pending
		if pending == nil {
			p.mu.Unlock()
			return ErrRefused
		}
		e = residentVerifyRefusal(p.binding, pending.intent, msg, p.key)
		if !errors.Is(e, ErrWorkUnconfigured) {
			e = residentVerifyPending(p.binding, pending.intent, msg, p.key)
		}
		if !errors.Is(e, ErrWorkUnconfigured) && !errors.Is(e, ErrWorkPending) {
			p.mu.Unlock()
			return ErrRefused
		}
		p.pending = nil
		p.mu.Unlock()
		pending.reply <- e
	}
}

// ResidentConnected observes an authenticated control transport only. It is
// never an authority/admission check, a native-host probe or a cleanup receipt.
func (h *Handler) ResidentConnected(host, principal string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.residents[residentMapKey(host, principal)]
	return !h.closed && p != nil && p.ready && p.ctx.Err() == nil
}

// ProposeResidentFixed is a trusted composition port, NOT an HTTP authorization
// API. A future authenticated MCP adapter must retain principal/host mapping.
// No adapter is enabled here. The connection-only host always refuses work.
func (h *Handler) ProposeResidentFixed(ctx context.Context, host, principal, requestID string, deadline time.Time) error {
	return h.proposeResident(ctx, host, principal, "", requestID, deadline, residentRunScope{})
}

// ProposeResidentRun sends a command-run intent on the same link, sequence and
// replay set as the fixed operation. A scope the Mac would refuse is refused
// here, before signing, without spending the request id or the link. Like
// ProposeResidentFixed it is a trusted composition port, NOT an HTTP
// authorization API, and no adapter calls it here.
func (h *Handler) ProposeResidentRun(ctx context.Context, host, principal, requestID string, deadline time.Time, commandSHA256, cwd string, timeoutMillis uint32) error {
	run := residentRunScope{commandSHA256: commandSHA256, cwd: cwd, timeoutMillis: timeoutMillis}
	if run == (residentRunScope{}) {
		return ErrRefused
	}
	return h.proposeResident(ctx, host, principal, "", requestID, deadline, run)
}
func (h *Handler) proposeResident(ctx context.Context, host, principal, generation, requestID string, deadline time.Time, run residentRunScope) error {
	if h == nil || ctx == nil || ctx.Err() != nil || !residentHex(requestID, 16) {
		return ErrRefused
	}
	now := h.clock()
	limit := ResidentFixedTimeout
	if run != (residentRunScope{}) {
		limit = time.Duration(run.timeoutMillis) * time.Millisecond
	}
	if !now.Before(deadline) || deadline.After(now.Add(limit)) || deadline.Nanosecond()%int(time.Millisecond) != 0 {
		return ErrRefused
	}
	ctx, stop := context.WithDeadline(ctx, deadline)
	defer stop()
	h.mu.Lock()
	p := h.residents[residentMapKey(host, principal)]
	closed := h.closed || (p != nil && (!p.ready || (generation != "" && generation != p.binding.generation)))
	h.mu.Unlock()
	if closed || p == nil {
		return ErrWorkUnconfigured
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ctx.Done():
		return ErrClosed
	case <-p.gate:
	}
	defer func() { p.gate <- struct{}{} }()
	p.mu.Lock()
	if p.sequence >= residentMaxIntents || p.seen[requestID] {
		p.mu.Unlock()
		return ErrRefused
	}
	i := ResidentFixedIntent{generation: p.binding.generation, sequence: p.sequence + 1, requestID: requestID, deadline: deadline, run: run}
	if i.isRun() && !i.runWellFormed(p.binding) {
		p.mu.Unlock()
		return ErrRefused
	}
	p.sequence++
	p.seen[requestID] = true
	i.endpoint, i.host, i.principal = p.binding.endpoint, p.binding.host, p.binding.principal
	p.intents[requestID] = i
	pending := &residentPending{intent: i, reply: make(chan error, 1)}
	p.pending = pending
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		if p.pending == pending {
			p.pending = nil
		}
		p.mu.Unlock()
	}()
	encode := residentEncodeIntent
	if i.isRun() {
		encode = residentEncodeRun
	}
	msg, e := encode(p.binding, i, p.signer)
	if e != nil {
		p.cancel()
		return e
	}
	if e = residentWrite(ctx, p.ws, msg); e != nil {
		p.cancel()
		return e
	}
	select {
	case e := <-pending.reply:
		return e
	case <-ctx.Done():
		p.cancel()
		return ctx.Err()
	case <-p.ctx.Done():
		return ErrClosed
	}
}

func (h *Handler) configureResidents(ps []ResidentPrincipal) error {
	if len(ps) > 4 {
		return ErrConfig
	}
	h.residentSigners = map[string]ssh.Signer{}
	h.residents = map[string]*residentPeer{}
	for _, p := range ps {
		if !validLabel(p.Label) || p.Signer == nil || h.residentSigners[p.Label] != nil {
			return ErrConfig
		}
		key, e := rawEd25519(p.Signer.PublicKey().Marshal())
		if e != nil {
			return ErrConfig
		}
		mapped := false
		for _, host := range h.hosts {
			if bytes.Equal(host.wire, key.Marshal()) {
				return ErrConfig
			}
			mapped = mapped || host.principals[p.Label]
		}
		if !mapped {
			return ErrConfig
		}
		h.residentSigners[p.Label] = p.Signer
	}
	return nil
}

func (h *Handler) reserveResident(ctx context.Context, p *residentPeer) error {
	k := residentMapKey(p.binding.host, p.binding.principal)
	h.mu.Lock()
	old := h.residents[k]
	if !h.closed && old == nil && len(h.residents) < MaxHosts {
		h.residents[k] = p
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()
	if old == nil || old.ctx.Err() == nil {
		return ErrRefused
	}
	if e := waitFor(ctx, old.finished); e != nil {
		return e
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.residents[k] != nil || len(h.residents) >= MaxHosts {
		return ErrRefused
	}
	h.residents[k] = p
	return nil
}
