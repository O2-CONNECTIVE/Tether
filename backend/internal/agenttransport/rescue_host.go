package agenttransport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

// RescueClientConfig selects the public reception endpoint and pinned gateway identity keys.
// See docs/rescue-connection-start.md, 受付の登録.
type RescueClientConfig struct {
	Endpoint string
	Pins     []ssh.PublicKey
}

// RescueClient owns one reception from 受付 ON until it ends.
// Its connection lifetime is independent of the context used to open it; see
// docs/rescue-connection-start.md, 受付の登録 and 受付 OFF のホストへの要求.
type RescueClient struct {
	transport           func(acquirer) http.RoundTripper
	after               func(time.Duration) <-chan time.Time
	endpoint            string
	pins                []ssh.PublicKey
	key                 ed25519.PrivateKey
	done                chan struct{}
	mu                  sync.Mutex
	used, closed, ended bool
	cancel              context.CancelFunc
	ws                  *websocket.Conn
	closer              *closer
}

// NewRescueClient validates the endpoint and pins and creates a fresh reception key.
// See docs/rescue-connection-start.md, 受付の登録.
func NewRescueClient(cfg RescueClientConfig) (*RescueClient, error) {
	u, endpointErr := parseEndpoint(cfg.Endpoint)
	if endpointErr != nil || u.Path != agentgateway.RescuePath || len(cfg.Pins) < 1 || len(cfg.Pins) > 2 {
		return nil, ErrConfig
	}
	pins := make([]ssh.PublicKey, 0, len(cfg.Pins))
	seen := map[string]bool{}
	for _, p := range cfg.Pins {
		if p == nil || p.Type() != ssh.KeyAlgoED25519 || seen[string(p.Marshal())] {
			return nil, ErrConfig
		}
		seen[string(p.Marshal())] = true
		pins = append(pins, p)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, ErrConfig
	}
	_ = pub
	return &RescueClient{endpoint: cfg.Endpoint, pins: pins, key: key, done: make(chan struct{}), transport: nativeTransport}, nil
}

// Open registers the reception and returns its code after verifying the signed result.
// ctx bounds only the handshake; the established reception lasts until Close, code expiry, or peer closure.
func (c *RescueClient) Open(ctx context.Context) (string, error) {
	if c == nil || ctx == nil {
		return "", ErrConfig
	}
	c.mu.Lock()
	if c.used || c.closed {
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return "", ErrClosed
		}
		return "", ErrUsed
	}
	c.used = true
	// Keep the reception alive after Open returns; ctx is attached only for the handshake.
	life, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.mu.Unlock()
	link := newCloser(cancel)
	c.mu.Lock()
	c.closer = link
	c.mu.Unlock()
	stopHandshake := context.AfterFunc(ctx, cancel)
	defer stopHandshake()
	context.AfterFunc(life, link.start)
	hc := &http.Client{Transport: c.transport(link), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ws, resp, err := websocket.Dial(life, c.endpoint, &websocket.DialOptions{HTTPClient: hc, Subprotocols: []string{RescueSubprotocol}, CompressionMode: websocket.CompressionDisabled})
	if err != nil && resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	hc.CloseIdleConnections()
	link.setWS(ws)
	transported := link.settle()
	c.mu.Lock()
	c.ws = ws
	c.mu.Unlock()
	if err != nil || !transported || ws.Subprotocol() != RescueSubprotocol {
		c.end()
		return "", ErrRefused
	}
	reg := time.AfterFunc(DefaultBudgets().Registration, cancel)
	defer reg.Stop()
	ws.SetReadLimit(MaxRegistrationMessage)
	write := func(v any) error {
		b, e := json.Marshal(v)
		if e != nil || len(b) > MaxRegistrationMessage {
			return ErrRefused
		}
		return ws.Write(life, websocket.MessageText, b)
	}
	hostNonce, e := newNonce()
	if e != nil {
		c.end()
		return "", ErrRefused
	}
	defer clear(hostNonce)
	if e = write(map[string]string{"type": "hello", "nonce": b64.EncodeToString(hostNonce)}); e != nil {
		c.end()
		return "", ErrRefused
	}
	typ, msg, e := ws.Read(life)
	if e != nil || typ != websocket.MessageText || len(msg) > MaxRegistrationMessage {
		c.end()
		return "", ErrRefused
	}
	m, ok := stringFields(msg, "type", "nonce")
	if !ok || m["type"] != "challenge" {
		c.end()
		return "", ErrRefused
	}
	gatewayNonce, ok := decodeB64(m["nonce"])
	if !ok || len(gatewayNonce) != NonceSize {
		c.end()
		return "", ErrRefused
	}
	defer clear(gatewayNonce)
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		c.end()
		return "", ErrClosed
	}
	pub := c.key.Public().(ed25519.PublicKey)
	keyWire, err := ssh.NewPublicKey(pub)
	if err != nil {
		c.mu.Unlock()
		c.end()
		return "", ErrRefused
	}
	key := keyWire.Marshal()
	transcript := agentgateway.RescueTranscript(c.endpoint, key, gatewayNonce, agentgateway.RescuePurposeNew, nil)
	sig := ed25519.Sign(c.key, transcript)
	c.mu.Unlock()
	clear(transcript)
	if e = write(map[string]string{"type": "proof", "purpose": agentgateway.RescuePurposeNew, "key": b64.EncodeToString(key), "signature": b64.EncodeToString(sig)}); e != nil {
		clear(sig)
		c.end()
		return "", ErrRefused
	}
	clear(sig)
	expiry := c.after
	if expiry == nil {
		expiry = time.After
	}
	expires := expiry(agentgateway.RescueCodeLifetime)
	go func() {
		select {
		case <-expires:
			c.end()
		case <-c.done:
		}
	}()
	typ, msg, e = ws.Read(life)
	if e != nil || typ != websocket.MessageText || len(msg) > MaxRegistrationMessage {
		c.end()
		return "", ErrRefused
	}
	m, ok = stringFields(msg, "type", "reception", "code", "signature")
	if !ok || m["type"] != "reception" {
		c.end()
		return "", ErrRefused
	}
	id, iok := decodeB64(m["reception"])
	signature, sok := decodeB64(m["signature"])
	if !iok || len(id) != 16 || !sok || len(signature) != ed25519.SignatureSize || len(m["code"]) != 12 {
		c.end()
		return "", ErrRefused
	}
	for _, r := range m["code"] {
		if r < '0' || r > '9' {
			c.end()
			return "", ErrRefused
		}
	}
	data := agentgateway.RescueResultTranscript(c.endpoint, hostNonce, key, id, m["code"])
	valid := false
	for _, pin := range c.pins {
		pk, ok := pin.(ssh.CryptoPublicKey)
		if ok {
			ed, ok := pk.CryptoPublicKey().(ed25519.PublicKey)
			if ok && ed25519.Verify(ed, data, signature) {
				valid = true
				break
			}
		}
	}
	clear(data)
	clear(signature)
	if !valid {
		c.end()
		return "", ErrRefused
	}
	if !stopHandshake() || !reg.Stop() {
		c.end()
		return "", ErrRefused
	}
	c.mu.Lock()
	ended := c.ended
	c.mu.Unlock()
	if ended { // 受付 OFF came while the result was verified
		return "", ErrClosed
	}
	go func() { _, _, _ = ws.Read(life); c.end() }()
	return m["code"], nil
}

// Done closes when the reception ends and its private key is discarded.
func (c *RescueClient) Done() <-chan struct{} { return c.done }
func (c *RescueClient) end() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return
	}
	c.ended = true
	ws := c.ws
	link := c.closer
	cancel := c.cancel
	clear(c.key)
	c.key = nil
	close(c.done)
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if ws != nil {
		_ = ws.CloseNow()
	}
	if link != nil {
		link.start()
	}
}

// Close turns reception OFF and ends the connection.
func (c *RescueClient) Close() error {
	if c == nil {
		return ErrConfig
	}
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.end()
	return nil
}
