package agenttransport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
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

// residentOccupiedLimit bounds retrying the gateway's occupancy refusal (4013),
// counted from the first refusal since the last authentication. The gateway
// notices a lost control within about 20s (heartbeat interval plus ping
// timeout); this is three times that, so a real duplicate still stops.
const residentOccupiedLimit = 60 * time.Second

type ResidentFailureReason string

const (
	ResidentFailureIdentityRefused    ResidentFailureReason = "identity_refused"
	ResidentFailureConnectionOccupied ResidentFailureReason = "connection_occupied"
	ResidentFailureGatewayRefused     ResidentFailureReason = "gateway_refused"
)

// ResidentNeedsActionError marks a terminal connection refusal after the
// original transport cleanup was confirmed. The reason is a stable code for
// the owner status; callers supply their own user-facing wording.
type ResidentNeedsActionError struct {
	Reason ResidentFailureReason
	Err    error
}

func (e *ResidentNeedsActionError) Error() string {
	if e == nil {
		return "agenttransport: resident connection needs action"
	}
	return "agenttransport: resident connection needs action: " + string(e.Reason)
}

func (e *ResidentNeedsActionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func ResidentNeedsActionReason(err error) (ResidentFailureReason, bool) {
	var needs *ResidentNeedsActionError
	if !errors.As(err, &needs) || needs == nil || !validResidentFailureReason(needs.Reason) {
		return "", false
	}
	return needs.Reason, true
}

func validResidentFailureReason(reason ResidentFailureReason) bool {
	return reason == ResidentFailureIdentityRefused || reason == ResidentFailureConnectionOccupied || reason == ResidentFailureGatewayRefused
}

type residentRetryResult uint8

const (
	residentRetryElapsed residentRetryResult = iota
	residentRetryWoken
	residentRetryCanceled
)

type residentRetryWait func(context.Context, time.Duration, <-chan struct{}) residentRetryResult

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
	random                              func() float64
	waitRetry                           residentRetryWait
	nudges                              chan struct{}
	mu                                  sync.Mutex
	started, connected, cleanupObserved bool
	retrying, reconnecting              bool
	retryWaitID                         uint64
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
	return &ResidentClient{cfg: cfg, transport: nativeTransport, clock: time.Now, retryDelay: 5 * time.Second, random: rand.Float64, waitRetry: waitResidentRetry, nudges: make(chan struct{}, 1), done: make(chan struct{}), updates: make(chan struct{}, 1)}, nil
}

// ResidentControlSnapshot is transport observation, never standby/work authority.
// Cleanup describes the last original transport attempt; outer Run join and
// authoritative host OFF remain the caller's separate responsibilities.
type ResidentControlSnapshot struct {
	Revision                                                uint64
	Generation                                              string
	Connected, RunFinished, CleanupObserved, CleanupUnknown bool
	RetryWaiting, Reconnecting                              bool
	RetryWaitID                                             uint64
}

func (c *ResidentClient) Snapshot() ResidentControlSnapshot {
	if c == nil {
		return ResidentControlSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return ResidentControlSnapshot{Revision: c.revision, Generation: c.generation, Connected: c.connected, RunFinished: c.finished, CleanupObserved: c.cleanupObserved, CleanupUnknown: c.cleanupObserved && c.cleanupErr != nil, RetryWaiting: c.retrying, Reconnecting: c.reconnecting, RetryWaitID: c.retryWaitID}
}

// Nudge interrupts a resident backoff only while the client is waiting to
// retry. It never closes an active connection or queues a stale wakeup.
func (c *ResidentClient) Nudge() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.retrying {
		return
	}
	select {
	case c.nudges <- struct{}{}:
	default:
	}
}

func (c *ResidentClient) setRetrying(retrying bool) {
	c.mu.Lock()
	if c.retrying != retrying {
		c.retrying = retrying
		if retrying && c.retryWaitID < ^uint64(0) {
			c.retryWaitID++
		}
		c.changedLocked()
	}
	c.mu.Unlock()
}

func (c *ResidentClient) setReconnecting(reconnecting bool) {
	c.mu.Lock()
	if c.reconnecting != reconnecting {
		c.reconnecting = reconnecting
		c.changedLocked()
	}
	c.mu.Unlock()
}

func waitResidentRetry(ctx context.Context, delay time.Duration, nudge <-chan struct{}) residentRetryResult {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return residentRetryCanceled
	case <-nudge:
		return residentRetryWoken
	case <-timer.C:
		return residentRetryElapsed
	}
}

func residentJitteredDelay(delay time.Duration, random func() float64) time.Duration {
	if random == nil {
		random = rand.Float64
	}
	minimum := delay * 4 / 5
	spread := delay - minimum
	v := random()
	if v < 0 {
		v = 0
	} else if v > 1 {
		v = 1
	}
	return minimum + time.Duration(float64(spread)*v)
}

func nextResidentRetryDelay(delay time.Duration) time.Duration {
	if delay < 30*time.Second {
		delay *= 2
		if delay > 30*time.Second {
			return 30 * time.Second
		}
	}
	return delay
}

func residentOccupiedExpired(since, now time.Time) bool {
	return now.Sub(since) > residentOccupiedLimit
}

func residentHTTPRetryable(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable
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
		c.retrying = false
		c.reconnecting = false
		c.finished = true
		c.changedLocked()
		close(c.done)
		c.mu.Unlock()
	}()
	defer cancel()
	delay := c.retryDelay
	var occupiedSince time.Time
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
		occupiedExpired := false
		if authenticated {
			occupiedSince = time.Time{}
		}
		if !authenticated && websocket.CloseStatus(e) == residentOccupiedClose {
			// The gateway still holds an earlier control of this identity.
			if now := c.clock(); occupiedSince.IsZero() {
				occupiedSince = now
			} else if residentOccupiedExpired(occupiedSince, now) {
				occupiedExpired = true
			}
		}
		retry, reason := residentFailureDisposition(e, authenticated, occupiedExpired)
		if !retry {
			c.setReconnecting(false)
			return &ResidentNeedsActionError{Reason: reason, Err: errors.Join(ErrRefused, e)}
		}
		c.setReconnecting(true)
		if authenticated {
			delay = c.retryDelay
		} // successful control resets transient backoff
		c.setRetrying(true)
		waited := c.waitRetry(life, residentJitteredDelay(delay, c.random), c.nudges)
		c.setRetrying(false)
		select {
		case <-c.nudges:
		default:
		}
		if err := life.Err(); err != nil {
			return err
		}
		if waited == residentRetryCanceled {
			return context.Canceled
		}
		delay = nextResidentRetryDelay(delay)
	}
}

func residentFailureDisposition(e error, authenticated, occupiedExpired bool) (bool, ResidentFailureReason) {
	status := websocket.CloseStatus(e)
	if status == residentOccupiedClose {
		if occupiedExpired {
			return false, ResidentFailureConnectionOccupied
		}
		return true, ""
	}
	if status == websocket.StatusPolicyViolation {
		return false, ResidentFailureIdentityRefused
	}
	if residentRetryable(e, authenticated) {
		return true, ""
	}
	return false, ResidentFailureGatewayRefused
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
		if resp != nil && residentHTTPRetryable(resp.StatusCode) {
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
	c.reconnecting = false
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
		refused, e := residentEncodeRefusal(b, intent.Correlation(), c.cfg.HostKey)
		if e != nil {
			return e, nil, true
		}
		write, stop := context.WithDeadline(life, intent.Correlation().Deadline())
		e = residentWrite(write, ws, refused)
		stop()
		receiver.Release(intent.Correlation())
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
