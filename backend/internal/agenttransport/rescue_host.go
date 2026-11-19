package agenttransport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net"
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
	hostPublic          ssh.PublicKey
	reception, hostKey  []byte
	nonce               []byte
	digest              [32]byte
	request             chan VerifiedRescueIdentity
	stream              *stream
	life                context.Context
	done                chan struct{}
	mu                  sync.Mutex
	used, closed, ended bool
	requested, answered bool
	expired             bool
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
	hostPublic, err := ssh.NewPublicKey(pub)
	if err != nil {
		clear(key)
		return nil, ErrConfig
	}
	return &RescueClient{endpoint: cfg.Endpoint, pins: pins, key: key, hostPublic: hostPublic,
		done: make(chan struct{}), request: make(chan VerifiedRescueIdentity, 1), transport: nativeTransport}, nil
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
	c.life = life
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
			c.mu.Lock()
			requested := c.requested
			c.expired = !requested // serve passes on no request after this
			c.mu.Unlock()
			if !requested {
				c.end()
			}
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
	off := c.ended || c.closed
	if !off {
		c.reception = append([]byte(nil), id...)
		c.hostKey = append([]byte(nil), key...)
	}
	c.mu.Unlock()
	if off { // 受付 OFF came, or began, while the result was verified
		c.end()
		return "", ErrClosed
	}
	go c.serve()
	return m["code"], nil
}

func (c *RescueClient) serve() {
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return
	}
	life, ws, link := c.life, c.ws, c.closer
	reception, hostKey := append([]byte(nil), c.reception...), append([]byte(nil), c.hostKey...)
	c.mu.Unlock()

	typ, msg, err := ws.Read(life)
	if err != nil || typ != websocket.MessageText || len(msg) > MaxRegistrationMessage {
		c.end()
		return
	}
	id, err := decodeRescueRequest(msg)
	if err != nil {
		c.end()
		return
	}
	defer clear(id.Nonce)
	defer clear(id.Client)
	defer clear(id.Signature)
	verified, digest, err := verifyRescueRequest(c.endpoint, reception, hostKey, c.pins, id)
	if err != nil {
		c.end()
		return
	}
	stream := newStream(life, ws, link, DefaultBudgets().Cleanup)
	c.mu.Lock()
	if c.ended || c.expired {
		c.mu.Unlock()
		return
	}
	c.requested = true
	c.nonce = append([]byte(nil), id.Nonce...)
	c.digest = digest
	c.stream = stream
	c.request <- verified
	c.mu.Unlock()

	if err := stream.Conn.(*msgConn).fill(); err != nil {
		c.end()
		return
	}
	c.mu.Lock()
	answered := c.answered
	c.mu.Unlock()
	if !answered {
		c.end()
	}
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
	clear(c.reception)
	c.reception = nil
	clear(c.hostKey)
	c.hostKey = nil
	clear(c.nonce)
	c.nonce = nil
	clear(c.digest[:])
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

// VerifiedRescueIdentity is an identity proof VerifyRescueRequest verified
// for one reception. Its zero value is not verified and has no Client.
type VerifiedRescueIdentity struct {
	client                             ssh.PublicKey
	displayName, organization, profile string
	hostKey                            []byte
}

func (v VerifiedRescueIdentity) Client() ssh.PublicKey { return v.client }
func (v VerifiedRescueIdentity) DisplayName() string   { return v.displayName }
func (v VerifiedRescueIdentity) Organization() string  { return v.organization }
func (v VerifiedRescueIdentity) Profile() string       { return v.profile }

// HostKey is K (SSH wire form) of the reception that verified the request.
func (v VerifiedRescueIdentity) HostKey() []byte { return append([]byte(nil), v.hostKey...) }

// VerifyRescueRequest verifies the identity proof of a request on reception
// id at endpoint, whose host key is K (hostKey, SSH wire form): one of pins
// signed RescueRequestTranscript over exactly these values, the nonce is
// RescueNonceSize bytes, C is an Ed25519 key and the profile is RescueProfile.
// See docs/rescue-connection-start.md, 受付口の wire.
func VerifyRescueRequest(endpoint string, reception, hostKey []byte, pins []ssh.PublicKey, id agentgateway.RescueIdentity) (VerifiedRescueIdentity, error) {
	verified, _, err := verifyRescueRequest(endpoint, reception, hostKey, pins, id)
	return verified, err
}

func verifyRescueRequest(endpoint string, reception, hostKey []byte, pins []ssh.PublicKey, id agentgateway.RescueIdentity) (VerifiedRescueIdentity, [32]byte, error) {
	if id.Profile != agentgateway.RescueProfile || len(id.Nonce) != agentgateway.RescueNonceSize || len(id.Signature) != ed25519.SignatureSize {
		return VerifiedRescueIdentity{}, [32]byte{}, ErrRefused
	}
	client, err := ssh.ParsePublicKey(append([]byte(nil), id.Client...)) // the key aliases its input; the caller may clear id
	if err != nil || client.Type() != ssh.KeyAlgoED25519 || !bytes.Equal(client.Marshal(), id.Client) {
		return VerifiedRescueIdentity{}, [32]byte{}, ErrRefused
	}
	transcript := agentgateway.RescueRequestTranscript(endpoint, reception, hostKey, id.Nonce, id.Client, id.DisplayName, id.Organization, id.Profile)
	defer clear(transcript)
	valid := false
	for _, pin := range pins {
		cryptoKey, ok := pin.(ssh.CryptoPublicKey)
		if !ok {
			continue
		}
		publicKey, ok := cryptoKey.CryptoPublicKey().(ed25519.PublicKey)
		if ok && ed25519.Verify(publicKey, transcript, id.Signature) {
			valid = true
			break
		}
	}
	if !valid {
		return VerifiedRescueIdentity{}, [32]byte{}, ErrRefused
	}
	return VerifiedRescueIdentity{client: client, displayName: id.DisplayName, organization: id.Organization, profile: id.Profile, hostKey: append([]byte(nil), hostKey...)}, sha256.Sum256(transcript), nil
}

// Request delivers the reception's one request once it verifies; a request
// that does not verify ends the reception instead.
func (c *RescueClient) Request() <-chan VerifiedRescueIdentity {
	if c == nil {
		return nil
	}
	return c.request
}

// Refuse answers the request with a K-signed deny, or timeout when timedOut.
func (c *RescueClient) Refuse(timedOut bool) error {
	answer := agentgateway.RescueDeny
	if timedOut {
		answer = agentgateway.RescueTimeout
	}
	_, err := c.answer(answer, agentgateway.Dispatch{})
	return err
}

// Allow answers the request with a K-signed allow carrying d and returns the
// reception's stream, which now carries the channel.
func (c *RescueClient) Allow(d agentgateway.Dispatch) (net.Conn, error) {
	return c.answer(agentgateway.RescueAllow, d)
}

// HostKey signs with K until the reception ends. The grant answered by Allow
// uses it as its host key, so K and the grant end together.
func (c *RescueClient) HostKey() ssh.Signer {
	if c == nil {
		return nil
	}
	return &rescueSigner{client: c, public: c.hostPublic}
}

func (c *RescueClient) answer(answer string, d agentgateway.Dispatch) (net.Conn, error) {
	if c == nil {
		return nil, ErrClosed
	}
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	if !c.requested || c.answered {
		c.mu.Unlock()
		return nil, ErrRefused
	}
	c.answered = true
	transcript := agentgateway.RescueAnswerTranscript(c.reception, c.nonce, c.digest[:], answer, d)
	signature := ed25519.Sign(c.key, transcript)
	clear(transcript)
	msg := encodeRescueAnswer(agentgateway.RescueAnswer{Answer: answer, Dispatch: d, Signature: signature})
	clear(signature)
	ws, life, stream := c.ws, c.life, c.stream
	c.mu.Unlock()
	if len(msg) > MaxRegistrationMessage {
		clear(msg)
		c.end()
		return nil, ErrRefused
	}
	ctx, cancel := context.WithTimeout(life, DefaultBudgets().Registration)
	err := ws.Write(ctx, websocket.MessageText, msg)
	cancel()
	clear(msg)
	if err != nil {
		c.end()
		return nil, err
	}
	if answer == agentgateway.RescueAllow {
		return stream, nil
	}
	return nil, nil
}

type rescueSigner struct {
	client *RescueClient
	public ssh.PublicKey
}

func (s *rescueSigner) PublicKey() ssh.PublicKey { return s.public }

func (s *rescueSigner) Sign(_ io.Reader, data []byte) (*ssh.Signature, error) {
	if s == nil || s.client == nil {
		return nil, ErrClosed
	}
	s.client.mu.Lock()
	defer s.client.mu.Unlock()
	if s.client.key == nil {
		return nil, ErrClosed
	}
	return &ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: ed25519.Sign(s.client.key, data)}, nil
}
