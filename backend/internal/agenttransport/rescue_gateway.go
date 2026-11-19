package agenttransport

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/coder/websocket"
)

// RescueSubprotocol is the only subprotocol accepted by the rescue reception route.
// See docs/rescue-connection-start.md, 受付の登録.
const RescueSubprotocol = "rescue-agent-reception.v1"

// RescueRegistry stores rescue receptions; *agentgateway.Gateway implements it.
// See docs/rescue-connection-start.md, 受付の登録 and 受付 OFF のホストへの要求.
type RescueRegistry interface {
	AdmitRescue() error
	OpenRescue(agentgateway.RescueRequest) (agentgateway.RescueReception, error)
	EndRescue([]byte)
	AnswerRescue([]byte, agentgateway.RescueAnswer, net.Conn) error
}

// RescueHandler serves the gateway end of the rescue reception route.
// See docs/rescue-connection-start.md, 受付の登録 and 受付 OFF のホストへの要求.
//
// Like the registration Handler, it keeps each connection until its close is
// confirmed, so Close reports a close still in progress.
type RescueHandler struct {
	reg        RescueRegistry
	mu         sync.Mutex
	closed     bool
	serving    int                // ServeHTTP calls past admission
	active     map[*closer][]byte // serving or unconfirmed; the id while the reception is open
	idle       chan struct{}
	idleClosed bool
	ctx        context.Context
	cancel     context.CancelFunc
}

// NewRescueHandler creates a handler backed by the given reception registry.
// See docs/rescue-connection-start.md, 受付の登録.
func NewRescueHandler(reg RescueRegistry) (*RescueHandler, error) {
	if reg == nil {
		return nil, ErrConfig
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &RescueHandler{reg: reg, active: map[*closer][]byte{}, idle: make(chan struct{}), ctx: ctx, cancel: cancel}, nil
}

// ServeHTTP accepts and serves one host reception.
// See docs/rescue-connection-start.md, 受付の登録.
func (h *RescueHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.reg == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	status := 0
	switch {
	case r.Method != http.MethodGet:
		status = http.StatusMethodNotAllowed
	case r.URL == nil || r.URL.Path != agentgateway.RescuePath:
		status = http.StatusNotFound
	case r.URL.RawQuery != "" || r.URL.ForceQuery:
		status = http.StatusBadRequest
	case len(r.Header.Values("Origin")) != 0 || len(r.Header.Values("Cookie")) != 0 || len(r.Header.Values("Authorization")) != 0 || len(r.Header.Values("Proxy-Authorization")) != 0:
		status = http.StatusForbidden
	case !slices.Equal(r.Header.Values("Sec-WebSocket-Protocol"), []string{RescueSubprotocol}):
		status = http.StatusBadRequest
	}
	if status != 0 {
		if status == 405 {
			w.Header().Set("Allow", "GET")
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	if err := h.reg.AdmitRescue(); err != nil {
		if errors.Is(err, agentgateway.ErrRescueBusy) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		} else if errors.Is(err, agentgateway.ErrRescueRate) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
		} else {
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		}
		return
	}
	life, cancel := context.WithCancel(h.ctx)
	c := newCloser(cancel)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		cancel()
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	h.prune()
	h.serving++
	h.active[c] = nil
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		id := h.active[c]
		h.active[c] = nil
		h.mu.Unlock()
		// End the registry entry before waiting for transport close confirmation.
		if id != nil {
			h.reg.EndRescue(id)
		}
		c.start()
		cleanup, stop := context.WithTimeout(context.Background(), DefaultBudgets().Cleanup)
		_ = c.wait(cleanup)
		stop()
		h.mu.Lock()
		h.prune()
		h.serving--
		if h.closed && h.serving == 0 {
			h.markIdle()
		}
		h.mu.Unlock()
	}()
	timer := time.AfterFunc(DefaultBudgets().Registration, cancel)
	ws, err := websocket.Accept(&hijackWriter{ResponseWriter: w, closer: c}, r, &websocket.AcceptOptions{Subprotocols: []string{RescueSubprotocol}, CompressionMode: websocket.CompressionDisabled})
	c.setWS(ws)
	transported := c.settle()
	if err != nil || !transported {
		timer.Stop()
		return
	}
	ws.SetReadLimit(MaxRegistrationMessage)
	typ, msg, err := ws.Read(life)
	if err != nil || typ != websocket.MessageText || len(msg) > MaxRegistrationMessage {
		rescueClose(ws)
		return
	}
	m, ok := stringFields(msg, "type", "nonce")
	if !ok || m["type"] != "hello" {
		rescueClose(ws)
		return
	}
	hostNonce, ok := decodeB64(m["nonce"])
	if !ok || len(hostNonce) != NonceSize {
		rescueClose(ws)
		return
	}
	defer clear(hostNonce)
	nonce, err := newNonce()
	if err != nil {
		rescueClose(ws)
		return
	}
	defer clear(nonce)
	write := func(v any) error {
		b, e := json.Marshal(v)
		if e != nil || len(b) > MaxRegistrationMessage {
			return ErrRefused
		}
		return ws.Write(life, websocket.MessageText, b)
	}
	if write(map[string]string{"type": "challenge", "nonce": b64.EncodeToString(nonce)}) != nil {
		rescueClose(ws)
		return
	}
	typ, msg, err = ws.Read(life)
	if err != nil || typ != websocket.MessageText || len(msg) > MaxRegistrationMessage {
		rescueClose(ws)
		return
	}
	m, ok = stringFields(msg, "type", "purpose", "key", "signature")
	if !ok {
		m, ok = stringFields(msg, "type", "purpose", "key", "signature", "reception")
	}
	if !ok || m["type"] != "proof" {
		rescueClose(ws)
		return
	}
	key, kok := decodeB64(m["key"])
	sig, sok := decodeB64(m["signature"])
	purpose := m["purpose"]
	var reception []byte
	if x, exists := m["reception"]; exists {
		reception, ok = decodeB64(x)
		if !ok {
			rescueClose(ws)
			return
		}
	}
	if !kok || !sok || len(sig) != ed25519.SignatureSize || len(key) == 0 {
		rescueClose(ws)
		return
	}
	res, err := h.reg.OpenRescue(agentgateway.RescueRequest{Key: key, HostNonce: hostNonce, GatewayNonce: nonce, Purpose: purpose, Reception: reception, Proof: sig})
	clear(key)
	clear(sig)
	clear(reception)
	if err != nil {
		rescueClose(ws)
		return
	}
	h.mu.Lock()
	h.active[c] = append([]byte(nil), res.ID...)
	h.mu.Unlock()
	if write(map[string]string{"type": "reception", "reception": b64.EncodeToString(res.ID), "code": res.Code, "signature": b64.EncodeToString(res.Signature)}) != nil {
		rescueClose(ws)
		return
	}
	if !timer.Stop() {
		rescueClose(ws)
		return
	}
	type readResult struct {
		typ websocket.MessageType
		msg []byte
		err error
	}
	read := make(chan readResult, 1)
	go func() {
		typ, msg, err := ws.Read(life)
		read <- readResult{typ: typ, msg: msg, err: err}
	}()
	request := res.Request
	for {
		select {
		case <-res.Done:
			rescueClose(ws)
			return
		case rr := <-read:
			// Until the identity is delivered, any host frame ends this reception.
			if rr.err == nil || rr.typ == websocket.MessageText || len(rr.msg) > MaxRegistrationMessage {
				rescueClose(ws)
			}
			return
		case identity, ok := <-request:
			if !ok {
				request = nil
				continue
			}
			frame := encodeRescueRequest(identity)
			if len(frame) > MaxRegistrationMessage || ws.Write(life, websocket.MessageText, frame) != nil {
				rescueClose(ws)
				return
			}
			var rr readResult
			select {
			case <-res.Done:
				rescueClose(ws)
				return
			case rr = <-read:
			case <-life.Done():
				return
			}
			if rr.err != nil {
				rescueClose(ws)
				return
			}
			answer, err := decodeRescueAnswer(rr.msg)
			if err != nil || rr.typ != websocket.MessageText || len(rr.msg) > MaxRegistrationMessage ||
				(answer.Answer == agentgateway.RescueAllow && checkDispatch(answer.Dispatch, time.Now()) != nil) {
				// A frame the transport cannot accept is still the host's answer:
				// the empty answer never verifies, so the gateway records failed
				// (timeout after RescueRequestLifetime) instead of ended.
				answer = agentgateway.RescueAnswer{}
			}
			stream := newStream(life, ws, c, DefaultBudgets().Cleanup)
			_ = h.reg.AnswerRescue(res.ID, answer, stream)
			select {
			case <-res.Done:
				c.start()
				<-life.Done()
			case <-life.Done():
			}
			return
		case <-life.Done():
			return
		}
	}
}

// Close turns the reception route OFF and ends every active reception.
// See docs/rescue-connection-start.md, 受付 OFF のホストへの要求.
func (h *RescueHandler) Close(ctx context.Context) error {
	if h == nil || ctx == nil {
		return ErrConfig
	}
	h.mu.Lock()
	h.closed = true
	if h.serving == 0 {
		h.markIdle()
	}
	var ids [][]byte
	var active []*closer
	for c, id := range h.active {
		if id != nil {
			ids = append(ids, id)
			h.active[c] = nil
		}
		active = append(active, c)
	}
	h.mu.Unlock()
	h.cancel()
	for _, id := range ids {
		h.reg.EndRescue(id)
	}
	for _, c := range active {
		c.start()
	}
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
	active = active[:0]
	for c := range h.active {
		active = append(active, c)
	}
	h.mu.Unlock()
	var errs []error
	for _, c := range active {
		if err := c.wait(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	h.mu.Lock()
	h.prune()
	h.mu.Unlock()
	return errors.Join(errs...)
}

// markIdle closes idle once. Caller holds h.mu.
func (h *RescueHandler) markIdle() {
	if !h.idleClosed {
		h.idleClosed = true
		close(h.idle)
	}
}

// prune drops connections whose close is confirmed and that no ServeHTTP
// still serves. Caller holds h.mu.
func (h *RescueHandler) prune() {
	for c, id := range h.active {
		if id == nil && c.confirmed() {
			delete(h.active, c)
		}
	}
}

func rescueClose(ws *websocket.Conn) {
	if ws != nil {
		_ = ws.Close(websocket.StatusPolicyViolation, "")
	}
}
