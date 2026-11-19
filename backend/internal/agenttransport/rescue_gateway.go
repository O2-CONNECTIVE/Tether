package agenttransport

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
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
}

// RescueHandler serves the gateway end of the rescue reception route.
// See docs/rescue-connection-start.md, 受付の登録 and 受付 OFF のホストへの要求.
type RescueHandler struct {
	reg    RescueRegistry
	mu     sync.Mutex
	closed bool
	active map[*closer][]byte
	idle   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
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
	h.active[c] = nil
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		id := h.active[c]
		delete(h.active, c)
		if h.closed && len(h.active) == 0 {
			close(h.idle)
		}
		h.mu.Unlock()
		// End the registry entry before waiting for transport close confirmation.
		if id != nil {
			h.reg.EndRescue(id)
		}
		c.start()
		cleanup, stop := context.WithTimeout(context.Background(), DefaultBudgets().Cleanup)
		_ = c.wait(cleanup)
		stop()
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
	_, _, _ = ws.Read(life)
}

// Close turns the reception route OFF and ends every active reception.
// See docs/rescue-connection-start.md, 受付 OFF のホストへの要求.
func (h *RescueHandler) Close(ctx context.Context) error {
	if h == nil || ctx == nil {
		return ErrConfig
	}
	h.mu.Lock()
	h.closed = true
	if len(h.active) == 0 {
		select {
		case <-h.idle:
		default:
			close(h.idle)
		}
	}
	active := make([]*closer, 0, len(h.active))
	ids := make([][]byte, 0, len(h.active))
	for c, id := range h.active {
		active = append(active, c)
		ids = append(ids, id)
		h.active[c] = nil
	}
	h.mu.Unlock()
	h.cancel()
	for i, c := range active {
		if i < len(ids) && ids[i] != nil {
			h.reg.EndRescue(ids[i])
		}
		c.start()
	}
	select {
	case <-h.idle:
	case <-ctx.Done():
		return ctx.Err()
	}
	var errs []error
	for _, c := range active {
		if err := c.wait(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func rescueClose(ws *websocket.Conn) {
	if ws != nil {
		_ = ws.Close(websocket.StatusPolicyViolation, "")
	}
}
