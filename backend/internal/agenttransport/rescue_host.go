package agenttransport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
	attemptDone         chan struct{}
	mu                  sync.Mutex
	prepareMu           sync.Mutex
	prepareDone         chan struct{}
	generateKey         func() (ed25519.PublicKey, ed25519.PrivateKey, error)
	wsWriteMu           sync.Mutex
	closeOnce           sync.Once
	closeFrameDone      chan struct{}
	closeFrameErr       error
	used, closed, ended bool
	active              bool
	everAllowed         bool
	resumeAttempt       bool
	attempt             uint64
	requested, answered bool
	requestedOnce       bool
	expired             bool
	code                string
	cancel              context.CancelFunc
	ws                  *websocket.Conn
	closer              *closer
	cleanup             time.Duration
}

// NewRescueClient validates the endpoint and pins. Prepare creates its fresh reception key.
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
	attemptDone := make(chan struct{})
	close(attemptDone)
	return &RescueClient{endpoint: cfg.Endpoint, pins: pins,
		done: make(chan struct{}), closeFrameDone: make(chan struct{}), attemptDone: attemptDone, request: make(chan VerifiedRescueIdentity, 1), transport: nativeTransport,
		cleanup: DefaultBudgets().Cleanup, generateKey: func() (ed25519.PublicKey, ed25519.PrivateKey, error) {
			return ed25519.GenerateKey(rand.Reader)
		}}, nil
}

// Prepare creates K once, after the host has registered this owner for cleanup.
func (c *RescueClient) Prepare() error {
	if c == nil {
		return ErrConfig
	}
	c.prepareMu.Lock()
	defer c.prepareMu.Unlock()
	c.mu.Lock()
	if c.closed || c.ended {
		c.mu.Unlock()
		return ErrClosed
	}
	if c.key != nil && c.hostPublic != nil {
		c.mu.Unlock()
		return nil
	}
	done := make(chan struct{})
	c.prepareDone = done
	generateKey := c.generateKey
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.prepareDone == done {
			c.prepareDone = nil
		}
		close(done)
		c.mu.Unlock()
	}()
	if generateKey == nil {
		return ErrConfig
	}
	pub, key, err := generateKey()
	if err != nil {
		clear(key)
		return ErrConfig
	}
	hostPublic, err := ssh.NewPublicKey(pub)
	if err != nil {
		clear(key)
		return ErrConfig
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.ended {
		clear(key)
		return ErrClosed
	}
	c.key, c.hostPublic = key, hostPublic
	return nil
}

// Open registers the reception and returns its code after verifying the signed result.
// ctx bounds only the handshake. The reception lasts until Close or code expiry
// before its first request; after an allow, a transport loss leaves it available
// for Resume.
func (c *RescueClient) Open(ctx context.Context) (string, error) {
	if c == nil || ctx == nil {
		return "", ErrConfig
	}
	if err := c.Prepare(); err != nil {
		return "", err
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
	c.mu.Unlock()
	return c.openAttempt(ctx, agentgateway.RescuePurposeNew, nil)
}

// Resume opens the same reception with its K proof. It waits for an in-flight
// attempt to finish, then verifies the gateway returned the same reception and
// code before accepting another request.
func (c *RescueClient) Resume(ctx context.Context) error {
	if c == nil || ctx == nil {
		return ErrConfig
	}
	for {
		c.mu.Lock()
		if c.ended || c.closed {
			c.mu.Unlock()
			return ErrClosed
		}
		if !c.used || !c.everAllowed {
			c.mu.Unlock()
			return ErrRefused
		}
		if c.active {
			attemptDone := c.attemptDone
			c.mu.Unlock()
			select {
			case <-attemptDone:
				continue
			case <-c.done:
				return ErrClosed
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		reception := append([]byte(nil), c.reception...)
		c.mu.Unlock()
		_, err := c.openAttempt(ctx, agentgateway.RescuePurposeResume, reception)
		clear(reception)
		return err
	}
}

func (c *RescueClient) openAttempt(ctx context.Context, purpose string, reception []byte) (string, error) {
	resuming := purpose == agentgateway.RescuePurposeResume
	c.mu.Lock()
	if c.closed || c.ended {
		c.mu.Unlock()
		return "", ErrClosed
	}
	if c.active || (resuming && (!c.everAllowed || !bytes.Equal(reception, c.reception))) {
		c.mu.Unlock()
		return "", ErrRefused
	}
	// Keep the reception alive after Open returns; ctx is attached only for the handshake.
	life, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.life = life
	c.active = true
	c.resumeAttempt = resuming
	c.requested, c.answered, c.expired = false, false, false
	c.stream = nil
	c.attempt++
	attempt := c.attempt
	c.attemptDone = make(chan struct{})
	c.mu.Unlock()
	fail := func() (string, error) {
		if resuming {
			c.disconnectAttempt(attempt)
		} else {
			c.end()
		}
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return "", ErrClosed
		}
		return "", ErrRefused
	}
	link := newRescueCloser(cancel)
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
		return fail()
	}
	reg := time.AfterFunc(DefaultBudgets().Registration, cancel)
	defer reg.Stop()
	ws.SetReadLimit(MaxRegistrationMessage)
	write := func(v any) error {
		b, e := json.Marshal(v)
		if e != nil || len(b) > MaxRegistrationMessage {
			return ErrRefused
		}
		c.wsWriteMu.Lock()
		err := ws.Write(life, websocket.MessageText, b)
		c.wsWriteMu.Unlock()
		return err
	}
	hostNonce, e := newNonce()
	if e != nil {
		return fail()
	}
	defer clear(hostNonce)
	if e = write(map[string]string{"type": "hello", "nonce": b64.EncodeToString(hostNonce)}); e != nil {
		return fail()
	}
	typ, msg, e := ws.Read(life)
	if e != nil || typ != websocket.MessageText || len(msg) > MaxRegistrationMessage {
		return fail()
	}
	m, ok := stringFields(msg, "type", "nonce")
	if !ok || m["type"] != "challenge" {
		return fail()
	}
	gatewayNonce, ok := decodeB64(m["nonce"])
	if !ok || len(gatewayNonce) != NonceSize {
		return fail()
	}
	defer clear(gatewayNonce)
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return fail()
	}
	pub := c.key.Public().(ed25519.PublicKey)
	keyWire, err := ssh.NewPublicKey(pub)
	if err != nil {
		c.mu.Unlock()
		return fail()
	}
	key := keyWire.Marshal()
	transcript := agentgateway.RescueTranscript(c.endpoint, key, gatewayNonce, purpose, reception)
	sig := ed25519.Sign(c.key, transcript)
	c.mu.Unlock()
	clear(transcript)
	proof := map[string]string{"type": "proof", "purpose": purpose, "key": b64.EncodeToString(key), "signature": b64.EncodeToString(sig)}
	if resuming {
		proof["reception"] = b64.EncodeToString(reception)
	}
	if e = write(proof); e != nil {
		clear(sig)
		return fail()
	}
	clear(sig)
	if !resuming {
		expiry := c.after
		if expiry == nil {
			expiry = time.After
		}
		expires := expiry(agentgateway.RescueCodeLifetime)
		go func() {
			select {
			case <-expires:
				c.mu.Lock()
				requested := c.requestedOnce
				c.expired = !requested // serve passes on no accepted request after this
				c.mu.Unlock()
				if !requested {
					c.end()
				}
			case <-c.done:
			}
		}()
	}
	typ, msg, e = ws.Read(life)
	if e != nil || typ != websocket.MessageText || len(msg) > MaxRegistrationMessage {
		return fail()
	}
	m, ok = stringFields(msg, "type", "reception", "code", "signature")
	if !ok || m["type"] != "reception" {
		return fail()
	}
	id, iok := decodeB64(m["reception"])
	signature, sok := decodeB64(m["signature"])
	if !iok || len(id) != 16 || !sok || len(signature) != ed25519.SignatureSize || len(m["code"]) != 12 {
		return fail()
	}
	for _, r := range m["code"] {
		if r < '0' || r > '9' {
			return fail()
		}
	}
	if resuming {
		c.mu.Lock()
		same := bytes.Equal(id, c.reception) && m["code"] == c.code && bytes.Equal(key, c.hostKey)
		c.mu.Unlock()
		if !same {
			clear(signature)
			return fail()
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
		return fail()
	}
	if !stopHandshake() || !reg.Stop() {
		return fail()
	}
	c.mu.Lock()
	off := c.ended || c.closed
	if !off && !resuming {
		c.reception = append([]byte(nil), id...)
		c.hostKey = append([]byte(nil), key...)
		c.code = m["code"]
	}
	c.mu.Unlock()
	if off { // 受付 OFF came, or began, while the result was verified
		return fail()
	}
	go c.serve(attempt)
	return m["code"], nil
}

func (c *RescueClient) serve(attempt uint64) {
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
		c.disconnectAttempt(attempt)
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
	stream := newStreamObserved(life, ws, link, DefaultBudgets().Cleanup, nil, nil, &c.wsWriteMu)
	stream.Conn.(*msgConn).onEnd = func() { c.disconnectAttempt(attempt) }
	c.mu.Lock()
	if c.ended || c.expired {
		c.mu.Unlock()
		return
	}
	c.requested = true
	c.requestedOnce = true
	c.nonce = append([]byte(nil), id.Nonce...)
	c.digest = digest
	c.stream = stream
	c.request <- verified
	c.mu.Unlock()

	if err := stream.Conn.(*msgConn).fill(); err != nil {
		return
	}
	c.mu.Lock()
	answered := c.answered
	c.mu.Unlock()
	if !answered {
		c.disconnectAttempt(attempt)
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
	if c.active {
		c.active = false
		close(c.attemptDone)
	}
	clear(c.key)
	c.key = nil
	clear(c.reception)
	c.reception = nil
	clear(c.hostKey)
	c.hostKey = nil
	clear(c.nonce)
	c.nonce = nil
	clear(c.digest[:])
	c.code = ""
	close(c.done)
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if link != nil {
		link.start()
	} else if ws != nil {
		go func() { _ = ws.CloseNow() }()
	}
}

// disconnectAttempt ends one WSS while retaining K and the reception id after
// an allowed channel or a resume attempt. A reception that never reached an
// allowed channel remains one-shot and is ended on transport loss.
func (c *RescueClient) disconnectAttempt(attempt uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.ended || !c.active || c.attempt != attempt {
		c.mu.Unlock()
		return
	}
	if c.closed || (!c.everAllowed && !c.resumeAttempt) {
		c.mu.Unlock()
		c.end()
		return
	}
	link, cancel := c.closer, c.cancel
	c.ws, c.closer, c.cancel, c.life = nil, nil, nil, nil
	c.active, c.resumeAttempt = false, false
	c.requested, c.answered, c.expired = false, false, false
	c.stream = nil
	clear(c.nonce)
	c.nonce = nil
	clear(c.digest[:])
	close(c.attemptDone)
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if link != nil {
		link.start()
	}
}

// StartClose turns reception OFF, drops K, and starts bounded physical cleanup.
// It does not wait for WebSocket I/O or the underlying transport Close.
func (c *RescueClient) StartClose() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		ws, link, lifeCancel := c.ws, c.closer, c.cancel
		ended := c.ended
		if !ended {
			c.ended = true
			if c.active {
				c.active = false
				close(c.attemptDone)
			}
			clear(c.key)
			c.key = nil
			clear(c.reception)
			c.reception = nil
			clear(c.hostKey)
			c.hostKey = nil
			clear(c.nonce)
			c.nonce = nil
			clear(c.digest[:])
			c.code = ""
			close(c.done)
		}
		c.mu.Unlock()

		go func() {
			var frameErr error
			if !ended && ws != nil {
				budget := c.cleanupBudget()
				frameBudget := min(250*time.Millisecond, budget/2)
				if frameBudget <= 0 {
					frameBudget = budget
				}
				ctx, cancel := context.WithTimeout(context.Background(), frameBudget)
				writeDone := make(chan error, 1)
				go func() {
					if !c.lockWrite(ctx) {
						writeDone <- ctx.Err()
						return
					}
					err := ws.Write(ctx, websocket.MessageText, encodeRescueEnd())
					c.wsWriteMu.Unlock()
					writeDone <- err
				}()
				select {
				case err := <-writeDone:
					if err != nil {
						frameErr = errors.Join(ErrCleanupUnknown, err)
					}
				case <-ctx.Done():
					frameErr = errors.Join(ErrCleanupUnknown, ctx.Err())
				}
				cancel()
			}
			c.mu.Lock()
			c.closeFrameErr = frameErr
			c.mu.Unlock()
			close(c.closeFrameDone)
			if link != nil {
				link.start()
			} else {
				if lifeCancel != nil {
					lifeCancel()
				}
				if ws != nil {
					go func() { _ = ws.CloseNow() }()
				}
			}
		}()
	})
}

func (c *RescueClient) cleanupBudget() time.Duration {
	if c.cleanup > 0 {
		return c.cleanup
	}
	return DefaultBudgets().Cleanup
}

// Close turns reception OFF and reports whether both its end frame and owned
// transport cleanup were confirmed within the cleanup budget.
func (c *RescueClient) Close() error {
	if c == nil {
		return ErrConfig
	}
	budget := c.cleanupBudget()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	c.StartClose()
	c.mu.Lock()
	prepareDone := c.prepareDone
	c.mu.Unlock()
	if prepareDone != nil {
		if err := waitFor(ctx, prepareDone); err != nil {
			return err
		}
	}
	if err := waitFor(ctx, c.closeFrameDone); err != nil {
		return err
	}
	c.mu.Lock()
	frameErr, link, ws := c.closeFrameErr, c.closer, c.ws
	c.mu.Unlock()
	var closeErr error
	if link != nil {
		closeErr = link.wait(ctx)
	} else if ws != nil {
		closeErr = errors.Join(ErrCleanupUnknown, errNoTransport)
	}
	if closeErr != nil {
		return errors.Join(frameErr, closeErr)
	}
	return nil
}

func (c *RescueClient) lockWrite(ctx context.Context) bool {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return false
		}
		if c.wsWriteMu.TryLock() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
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

// HostKey signs with K until the local reception ends, including while its
// channel is disconnected and waiting for Resume.
func (c *RescueClient) HostKey() ssh.Signer {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hostPublic == nil {
		return nil
	}
	return &rescueSigner{client: c, public: c.hostPublic}
}

func (c *RescueClient) answer(answer string, d agentgateway.Dispatch) (net.Conn, error) {
	if c == nil {
		return nil, ErrClosed
	}
	c.mu.Lock()
	if c.ended || c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	if !c.requested || c.answered {
		c.mu.Unlock()
		return nil, ErrRefused
	}
	c.answered = true
	if answer == agentgateway.RescueAllow {
		c.everAllowed = true
	}
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
	c.wsWriteMu.Lock()
	err := ws.Write(ctx, websocket.MessageText, msg)
	c.wsWriteMu.Unlock()
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
