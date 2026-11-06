package agenttransport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

// ResidentClientConfig uses the existing enrolled key/pin and exact WSS URL.
// It contains neither a grant nor consent, scope expansion or a worker factory.
var errResidentUnavailable = errors.New("agenttransport: resident transport unavailable")

type ResidentClientConfig struct {
	Endpoint, HostLabel, PrincipalLabel string
	HostKey                             ssh.Signer
	PrincipalKey                        ssh.PublicKey
	Budgets                             Budgets
}

// ResidentClient owns only identity/control connections. Run reauthenticates on
// recoverable connection loss after original transport cleanup is confirmed.
// It cannot issue/refresh grants, resume work or execute a received intent.
type ResidentClient struct {
	cfg                                 ResidentClientConfig
	transport                           func(acquirer) http.RoundTripper
	clock                               func() time.Time
	retryDelay                          time.Duration
	mu                                  sync.Mutex
	started, connected, cleanupObserved bool
	cancel                              context.CancelFunc
	done                                chan struct{}
	cleanupErr                          error
	revision                            uint64
	generation                          string
	finished                            bool
	updates                             chan struct{}
	sources                             chan *ResidentSourceLease
	consumerBudget                      time.Duration
	accepting                           bool
	held                                *ResidentSourceLease
}

func NewResidentClient(cfg ResidentClientConfig) (*ResidentClient, error) {
	if _, e := parseEndpoint(cfg.Endpoint); e != nil {
		return nil, ErrConfig
	}
	if cfg.HostKey == nil || cfg.PrincipalKey == nil || !validLabel(cfg.HostLabel) || !validLabel(cfg.PrincipalLabel) || cfg.HostLabel == cfg.PrincipalLabel || !cfg.Budgets.valid() {
		return nil, ErrConfig
	}
	host, e := rawEd25519(cfg.HostKey.PublicKey().Marshal())
	if e != nil {
		return nil, ErrConfig
	}
	principal, e := rawEd25519(cfg.PrincipalKey.Marshal())
	if e != nil || bytes.Equal(host.Marshal(), principal.Marshal()) {
		return nil, ErrConfig
	}
	cfg.PrincipalKey = principal
	return &ResidentClient{cfg: cfg, transport: nativeTransport, clock: time.Now, retryDelay: 5 * time.Second, done: make(chan struct{}), updates: make(chan struct{}, 1)}, nil
}

// ResidentControlSnapshot is transport observation, never standby/work authority.
// Cleanup describes the last original transport attempt; outer Run join and
// authoritative host OFF remain the caller's separate responsibilities.
type ResidentControlSnapshot struct {
	Revision                                                uint64
	Generation                                              string
	Connected, RunFinished, CleanupObserved, CleanupUnknown bool
}

func (c *ResidentClient) Snapshot() ResidentControlSnapshot {
	if c == nil {
		return ResidentControlSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return ResidentControlSnapshot{Revision: c.revision, Generation: c.generation, Connected: c.connected, RunFinished: c.finished, CleanupObserved: c.cleanupObserved, CleanupUnknown: c.cleanupObserved && c.cleanupErr != nil}
}

// Changes is coalesced notification to re-read Snapshot, not a complete journal.
func (c *ResidentClient) Changes() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.updates
}
func (c *ResidentClient) changedLocked() {
	if c.revision < ^uint64(0) {
		c.revision++
	} else if c.cancel != nil {
		c.cancel()
	}
	select {
	case c.updates <- struct{}{}:
	default:
	}
}
func (c *ResidentClient) Connected() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}
func (c *ResidentClient) CleanupResult() (error, bool) {
	if c == nil {
		return ErrCleanupUnknown, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cleanupErr, c.cleanupObserved
}
func (c *ResidentClient) Run(ctx context.Context) error {
	if c == nil || ctx == nil {
		return ErrConfig
	}
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return ErrUsed
	}
	life, cancel := context.WithCancel(ctx)
	c.started = true
	c.cancel = cancel
	c.changedLocked()
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.connected = false
		c.finished = true
		c.changedLocked()
		close(c.done)
		c.mu.Unlock()
	}()
	defer cancel()
	delay := c.retryDelay
	for {
		if life.Err() != nil {
			c.mu.Lock()
			c.cleanupObserved = true
			c.changedLocked()
			c.mu.Unlock()
			return life.Err()
		}
		e, clean, authenticated := c.attempt(life)
		c.mu.Lock()
		c.connected = false
		c.cleanupErr = clean
		c.cleanupObserved = true
		c.changedLocked()
		c.mu.Unlock()
		if errors.Is(e, ErrCleanupUnknown) {
			c.mu.Lock()
			c.cleanupErr = errors.Join(clean, e)
			c.changedLocked()
			c.mu.Unlock()
			return errors.Join(e, clean)
		}
		if clean != nil {
			return errors.Join(e, ErrCleanupUnknown, clean)
		}
		if life.Err() != nil {
			return life.Err()
		}
		if !residentRetryable(e, authenticated) {
			return errors.Join(ErrRefused, e)
		}
		if authenticated {
			delay = c.retryDelay
		} // successful control resets transient backoff
		timer := time.NewTimer(delay)
		select {
		case <-life.Done():
			timer.Stop()
			return life.Err()
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}
func residentRetryable(e error, authenticated bool) bool {
	if errors.Is(e, errResidentUnavailable) {
		return true
	}
	if errors.Is(e, ErrClosed) {
		return true
	}
	if e == nil || errors.Is(e, ErrRefused) || errors.Is(e, ErrConfig) {
		return false
	}
	status := websocket.CloseStatus(e)
	if status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway || status == websocket.StatusInternalError || status == 4000 {
		return true
	}
	if status != -1 {
		return false
	}
	if errors.Is(e, io.EOF) || errors.Is(e, io.ErrClosedPipe) || errors.Is(e, io.ErrUnexpectedEOF) || errors.Is(e, net.ErrClosed) || errors.Is(e, syscall.ECONNREFUSED) || errors.Is(e, syscall.ECONNRESET) || errors.Is(e, syscall.ENETUNREACH) || errors.Is(e, syscall.EHOSTUNREACH) {
		return true
	}
	var n net.Error
	if errors.As(e, &n) && (n.Timeout() || n.Temporary()) {
		return true
	}
	return authenticated && (errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded))
}
func (c *ResidentClient) attempt(parent context.Context) (result, cleanup error, authenticated bool) {
	c.mu.Lock()
	c.cleanupObserved = false
	c.changedLocked()
	c.mu.Unlock()
	life, cancel := context.WithCancel(parent)
	link := newCloser(cancel)
	stopLife := context.AfterFunc(life, link.start)
	var ping <-chan struct{}
	defer func() {
		c.mu.Lock()
		c.connected = false
		c.changedLocked()
		c.mu.Unlock()
		stopLife()
		cancel()
		link.settle()
		link.start()
		cleanup = residentCleanup(link, ping, c.cfg.Budgets.Cleanup)
	}()
	authTimer := time.AfterFunc(c.cfg.Budgets.Approval, cancel)
	defer authTimer.Stop()
	hc := &http.Client{Transport: c.transport(link), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ws, resp, e := websocket.Dial(life, c.cfg.Endpoint, &websocket.DialOptions{HTTPClient: hc, Subprotocols: []string{ResidentSubprotocol}, CompressionMode: websocket.CompressionDisabled})
	if e != nil && resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	hc.CloseIdleConnections()
	link.setWS(ws)
	observed := link.settle()
	if e != nil {
		if resp != nil && (resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests) {
			return errResidentUnavailable, nil, false
		}
		return e, nil, false
	}
	if !observed {
		return errNoTransport, nil, false
	}
	ws.SetReadLimit(MaxRegistrationMessage)
	receiver, e := newResidentReceiver(life, c.cfg, ws)
	if e != nil {
		if !errors.Is(e, ErrRefused) && parent.Err() == nil && life.Err() != nil {
			return errResidentUnavailable, nil, false
		}
		return e, nil, false
	}
	if !authTimer.Stop() || life.Err() != nil {
		return errResidentUnavailable, nil, false
	}
	defer receiver.Close()
	b := receiver.binding
	c.mu.Lock()
	c.connected = true
	c.generation = b.generation
	c.changedLocked()
	c.mu.Unlock()
	authenticated = true
	heartbeat := make(chan struct{})
	ping = heartbeat
	go residentHeartbeat(life, ws, cancel, heartbeat)
	if c.sources != nil {
		return c.handoff(life, receiver, link), nil, true
	}
	for {
		intent, e := receiver.readFixed()
		if e != nil {
			return e, nil, true
		}
		// Connection-only safety floor: no Request/Authority/Session is created.
		refused, e := residentEncodeRefusal(b, intent, c.cfg.HostKey)
		if e != nil {
			return e, nil, true
		}
		write, stop := context.WithDeadline(life, intent.deadline)
		e = residentWrite(write, ws, refused)
		stop()
		receiver.Release(intent)
		if e != nil {
			return e, nil, true
		}
	}
}
func (c *ResidentClient) authenticate(ctx context.Context, ws *websocket.Conn) (residentBinding, error) {
	b := residentBinding{endpoint: c.cfg.Endpoint, host: c.cfg.HostLabel, principal: c.cfg.PrincipalLabel, hostKey: c.cfg.HostKey.PublicKey().Marshal(), principalKey: c.cfg.PrincipalKey.Marshal()}
	if ws.Subprotocol() != ResidentSubprotocol {
		return b, ErrRefused
	}
	nonce, e := newNonce()
	if e != nil {
		return b, e
	}
	b.clientNonce = nonce
	if e = residentWrite(ctx, ws, residentJSON(map[string]string{"type": "resident_hello", "host": b.host, "principal": b.principal, "client_nonce": b64.EncodeToString(nonce)})); e != nil {
		return b, e
	}
	msg, e := residentRead(ctx, ws)
	if e != nil {
		return b, e
	}
	m, ok := residentFields(msg, "resident_challenge", "generation", "server_nonce", "signature")
	if !ok {
		return b, ErrRefused
	}
	b.generation = m["generation"]
	b.serverNonce, ok = decodeB64(m["server_nonce"])
	sig, okSig := residentSignature(m)
	if !ok || !okSig || !residentHex(b.generation, 32) || len(b.serverNonce) != NonceSize || verify(c.cfg.PrincipalKey, residentTranscript(b, "gateway-challenge"), sig) != nil {
		return b, ErrRefused
	}
	sig, e = sign(c.cfg.HostKey, c.cfg.HostKey.PublicKey(), residentTranscript(b, "host-proof"))
	if e != nil {
		return b, e
	}
	if e = residentWrite(ctx, ws, residentJSON(map[string]string{"type": "resident_proof", "signature": b64.EncodeToString(sig)})); e != nil {
		return b, e
	}
	msg, e = residentRead(ctx, ws)
	if e != nil {
		return b, e
	}
	m, ok = residentFields(msg, "resident_accepted", "signature")
	sig, okSig = residentSignature(m)
	if !ok || !okSig || verify(c.cfg.PrincipalKey, residentTranscript(b, "gateway-accepted"), sig) != nil {
		return b, ErrRefused
	}
	return b, nil
}

// Close seals this resident process, cancels reconnect and observes its original
// cleanup. It never closes a work owner or retries a failed transport close.
func (c *ResidentClient) Close(ctx context.Context) error {
	if c == nil || ctx == nil {
		return ErrConfig
	}
	if _, ok := ctx.Deadline(); !ok {
		return ErrConfig
	}
	c.mu.Lock()
	if !c.started {
		c.started = true
		c.cleanupObserved = true
		c.finished = true
		c.changedLocked()
		close(c.done)
		c.mu.Unlock()
		return nil
	}
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if e := waitFor(ctx, c.done); e != nil {
		return e
	}
	e, ok := c.CleanupResult()
	if !ok {
		return ErrCleanupUnknown
	}
	return e
}
