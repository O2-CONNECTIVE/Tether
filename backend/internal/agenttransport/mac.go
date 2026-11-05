package agenttransport

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

// Owner is the recipient's approved owner; *maclaunch.Approved implements it.
// Serve takes the stream and serves the one approved Session over it; Close
// revokes and retries only that owner's unconfirmed cleanup.
type Owner interface {
	Dispatch() agentchannel.Dispatch
	Serve(ctx context.Context, conn net.Conn) error
	Close(ctx context.Context) error
}

// ClientConfig is chosen by the recipient-launched composition, never by a
// command, MCP request or the gateway. Endpoint is the operator-chosen exact
// wss URL (the same string the gateway is configured with). HostKey is the
// same signer given to maclaunch.Config.HostKey; it must match the dispatch.
// HostLabel and PrincipalLabel are the fixed host/member mapping enrolled at
// the gateway; they select its configuration and cannot widen it.
type ClientConfig struct {
	Endpoint       string
	HostLabel      string
	PrincipalLabel string
	HostKey        ssh.Signer
	Budgets        Budgets
	ResidentIntent *ResidentFixedIntent
}

// Client registers one approved owner once. It never reconnects, retries
// with a new identity or resumes: Run is single-use and a new approval needs
// a new Client.
type Client struct {
	endpoint, host, principal string
	signer                    ssh.Signer
	public                    ssh.PublicKey
	dispatch                  agentgateway.Dispatch
	resident                  *ResidentFixedIntent
	owner                     Owner
	budgets                   Budgets
	clock                     func() time.Time
	// transport builds the one HTTP round tripper for this Client around its
	// transport owner. Tests replace it with an in-memory transport;
	// production uses nativeTransport.
	transport func(acquirer) http.RoundTripper

	mu       sync.Mutex
	started  bool // Run or Close was called
	ran      bool // Run was called; finished closes when it returns
	cancel   context.CancelFunc
	finished chan struct{}

	cleanMu         sync.Mutex
	link            *closer // the transport, retained until confirmed
	ownerDone       bool    // the owner's Close returned nil
	cleanupObserved bool    // completed original cleanup result, not Run error classification
	cleanupErr      error
}

// NewClient validates the configuration against the owner's dispatch. It
// performs no I/O and does not take the owner: on error the caller still
// owns it and must Close it.
func NewClient(cfg ClientConfig, owner Owner) (*Client, error) {
	if owner == nil || cfg.HostKey == nil || !validLabel(cfg.HostLabel) || !validLabel(cfg.PrincipalLabel) || !cfg.Budgets.valid() {
		return nil, ErrConfig
	}
	if _, err := parseEndpoint(cfg.Endpoint); err != nil {
		return nil, ErrConfig
	}
	public, err := rawEd25519(cfg.HostKey.PublicKey().Marshal())
	if err != nil {
		return nil, ErrConfig
	}
	md := owner.Dispatch()
	d := agentgateway.Dispatch{Binding: md.Binding, Deadline: md.Deadline, HostKey: append([]byte(nil), md.HostKey...)}
	if !bytes.Equal(d.HostKey, public.Marshal()) || checkDispatch(d, time.Now()) != nil {
		return nil, ErrConfig
	}
	var intent *ResidentFixedIntent
	if cfg.ResidentIntent != nil {
		i := *cfg.ResidentIntent
		if i.OriginalContext() == nil || i.OriginalContext().Err() != nil || i.Receiver() == nil || i.Endpoint() != cfg.Endpoint || i.HostLabel() != cfg.HostLabel || i.PrincipalLabel() != cfg.PrincipalLabel || i.HostKeyFingerprint() != ssh.FingerprintSHA256(public) || d.Deadline.After(i.Deadline()) {
			return nil, ErrConfig
		}
		intent = &i
	}
	return &Client{
		endpoint: cfg.Endpoint, host: cfg.HostLabel, principal: cfg.PrincipalLabel, signer: cfg.HostKey, public: public,
		dispatch: d, resident: intent, owner: owner, budgets: cfg.Budgets, clock: time.Now, transport: nativeTransport, finished: make(chan struct{}),
	}, nil
}

// Run connects outbound once, proves the host key for this connection, and
// then serves the owner over the same stream until it ends. On every return
// the transport has been closed and the owner closed (revoked) within the
// cleanup budget. Run never returns nil: the stream always ends, and an owner
// whose Serve returns nil is reported as ErrClosed. If the error matches
// ErrCleanupUnknown, keep the Client and retry Close a bounded number of
// times; Close retries only this transport and this owner. If it stays
// unknown, report it to the local operator rather than retrying forever or
// declaring the cleanup clean.
func (c *Client) Run(ctx context.Context) error {
	if c == nil || c.owner == nil || ctx == nil {
		return ErrConfig
	}
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return ErrUsed
	}
	life, cancel := context.WithCancel(ctx)
	c.started, c.ran, c.cancel = true, true, cancel
	c.mu.Unlock()
	defer close(c.finished)
	served := c.attempt(life, cancel)
	cleanup, stop := context.WithTimeout(context.Background(), c.budgets.Cleanup)
	defer stop()
	return errors.Join(served, c.retry(cleanup))
}

func (c *Client) attempt(life context.Context, cancel context.CancelFunc) error {
	if err := checkDispatch(c.dispatch, c.clock()); err != nil {
		cancel()
		return err
	}
	approval := time.AfterFunc(c.budgets.Approval, cancel)
	defer approval.Stop()

	// The owner exists before the dial. link.attach accepts exactly one
	// transport connection during the upgrade; a second one, or a dial that
	// completes after the close started or after the upgrade ended, is closed
	// at once and its close result kept. A dial still running (even after its
	// round trip was abandoned) keeps the owner unconfirmed until it ends.
	link := newCloser(cancel)
	c.cleanMu.Lock()
	c.link = link
	c.cleanMu.Unlock()
	context.AfterFunc(life, link.start)
	hc := &http.Client{
		Transport:     c.transport(link),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	ws, resp, err := websocket.Dial(life, c.endpoint, &websocket.DialOptions{
		HTTPClient: hc, Subprotocols: []string{Subprotocol}, CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil && resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	hc.CloseIdleConnections() // none are kept; an upgraded conn is not idle
	link.setWS(ws)
	transported := link.settle()
	if err != nil {
		link.start()
		return errors.Join(ErrRefused, err)
	}
	if !transported {
		// The WebSocket did not come over the one observed connection: refuse
		// it, and do not claim its closure from CloseNow alone.
		link.start()
		return errors.Join(ErrRefused, errNoTransport)
	}
	registration := time.AfterFunc(c.budgets.Registration, cancel)
	err = c.register(life, ws)
	if !registration.Stop() || !approval.Stop() || err != nil {
		link.start()
		return errors.Join(ErrRefused, err)
	}
	if err := c.owner.Serve(life, newStream(life, ws, link, c.budgets.Cleanup)); err != nil {
		return err
	}
	// The stream always ends; an owner reporting success is not a success.
	return ErrClosed
}

func (c *Client) register(ctx context.Context, ws *websocket.Conn) error {
	if ws.Subprotocol() != Subprotocol {
		return ErrRefused
	}
	ws.SetReadLimit(MaxRegistrationMessage)
	typ, msg, err := ws.Read(ctx)
	if err != nil || typ != websocket.MessageText || len(msg) > MaxRegistrationMessage {
		return ErrRefused
	}
	nonce, err := decodeChallenge(msg)
	if err != nil {
		return err
	}
	data := transcript(c.endpoint, c.host, c.principal, c.dispatch, nonce)
	if c.resident != nil {
		data = workTranscript(c.endpoint, c.host, c.principal, c.dispatch, nonce, c.resident.workBinding())
	}
	sig, err := sign(c.signer, c.public, data)
	if err != nil {
		return err
	}
	msg = encodeRegister(c.host, c.principal, c.dispatch, sig)
	if c.resident != nil {
		msg = encodeWorkRegister(c.host, c.principal, c.dispatch, sig, c.resident.workBinding())
	}
	if err := ws.Write(ctx, websocket.MessageText, msg); err != nil {
		return ErrRefused
	}
	typ, msg, err = ws.Read(ctx)
	if err != nil || typ != websocket.MessageText || len(msg) > MaxRegistrationMessage || !isAccepted(msg) {
		return ErrRefused
	}
	return nil
}

// retry closes the owner (revoking it) and then waits for the transport. A
// confirmed owner is not closed again; an unconfirmed one is retried as the
// same owner, never replaced.
func (c *Client) retry(ctx context.Context) error {
	c.cleanMu.Lock()
	defer c.cleanMu.Unlock()
	var errs []error
	if !c.ownerDone {
		if err := c.owner.Close(ctx); err != nil {
			errs = append(errs, err)
		} else {
			c.ownerDone = true
		}
	}
	if c.link != nil {
		if err := c.link.wait(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	c.cleanupErr = errors.Join(errs...)
	c.cleanupObserved = true
	return c.cleanupErr
}

// CleanupResult observes the last completed cleanup without retrying any owner,
// transport or process operation. A running Run is not joined evidence, even if
// an earlier cleanup observation exists. false means unknown/not yet observed.
// The returned error is the original cleanup result, never inferred from Run.
func (c *Client) CleanupResult() (error, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	ran := c.ran
	c.mu.Unlock()
	if ran {
		select {
		case <-c.finished:
		default:
			return nil, false
		}
	}
	if !c.cleanMu.TryLock() {
		return nil, false
	}
	defer c.cleanMu.Unlock()
	return c.cleanupErr, c.cleanupObserved
}

// Close ends a running attempt, waits within ctx for Run to return and then
// retries the owner and transport cleanup. Before Run it closes the owner and
// makes Run refuse. Calling it again retries only what is unconfirmed.
func (c *Client) Close(ctx context.Context) error {
	if c == nil || c.owner == nil || ctx == nil {
		return ErrConfig
	}
	c.mu.Lock()
	c.started = true
	ran, cancel := c.ran, c.cancel
	c.mu.Unlock()
	if ran {
		cancel()
		select {
		case <-c.finished:
		case <-ctx.Done():
			select {
			case <-c.finished:
			default:
				return errors.Join(ErrCleanupUnknown, ctx.Err())
			}
		}
	}
	return c.retry(ctx)
}

// nativeTransport dials exactly the endpoint host with normal TLS
// verification: no proxy (environment proxies and their credentials are not
// read), no HTTP/2, no keep-alive reuse, no compression. Redirects are refused
// by the http.Client and cookies are never stored. net/http may finish a dial
// after the round trip that started it was abandoned; each dial is therefore
// bracketed by the owner's begin/end (bounded by the dialer timeout) and its
// connection goes through attach, never around the owner.
func nativeTransport(own acquirer) http.RoundTripper {
	dialer := nativeDialer()
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if !own.begin() {
				return nil, ErrRefused
			}
			defer own.end()
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return own.attach(conn)
		},
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxConnsPerHost:        1,
		MaxResponseHeaderBytes: 16 << 10,
		ForceAttemptHTTP2:      false,
		TLSNextProto:           map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
}

// nativeDialer owns the resolver choice for this outbound transport only.
// On Darwin the default libc resolver can retain an inheritable socket, which
// the Mac runtime correctly refuses before spawning. Go's resolver avoids that
// producer; the FD guard stays unchanged. Do not fall back to the system
// resolver, alter GODEBUG, or repair/ignore unknown process descriptors.
// This intentionally does not promise macOS split-DNS or mDNS compatibility.
func nativeDialer() *net.Dialer {
	return &net.Dialer{Timeout: 10 * time.Second, Resolver: &net.Resolver{PreferGo: true}}
}

// StopTerminalUnknown prevents Run/reconnect and seals this transport only.
// Unlike Close, it never retries owner/process cleanup.
func (c *Client) StopTerminalUnknown(ctx context.Context) error {
	if c == nil || ctx == nil {
		return ErrConfig
	}
	c.mu.Lock()
	c.started = true
	ran, cancel := c.ran, c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if ran {
		select {
		case <-c.finished:
		case <-ctx.Done():
			return errors.Join(ErrCleanupUnknown, ctx.Err())
		}
	}
	if !c.cleanMu.TryLock() {
		return ErrCleanupUnknown
	}
	link := c.link
	c.cleanMu.Unlock()
	if link != nil {
		link.start()
		return link.wait(ctx)
	}
	return nil
}
