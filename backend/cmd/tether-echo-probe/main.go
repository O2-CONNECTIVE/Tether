// tether-echo-probe is a commandless route probe for the Tether Cloudflare
// path (Worker, VPC Service, Tunnel, container). It reads one public trust
// document named by "-config <path>" through agentkeys.ReadTrustConfig, accepts
// only the existing agentgateway cloudflare_access document whose resource is
// the probe's HTTP echo URL, and only then listens on the document's address:
// loopback by default, or the explicit container_private address. It serves an
// HTTP echo and a WebSocket echo at fixed paths to verified, allowlisted Access
// subjects. It loads no signing key, starts no process, opens no agent
// channel, writes no file and offers no command. Its fixed 10-minute lifetime
// starts before configuration read and ends through bounded owned shutdown; see
// deploy/cloudflare/probe/README.md.
//
// "--deny-all-probe" (exactly this one argument) is a separate reachability
// mode for a nonroot, isolated, dedicated container published on host
// loopback only: it reads no document, listens on the fixed tcp4 0.0.0.0:8787,
// answers parsed requests with the same 403. At 10 minutes it stops accepting
// work, then performs bounded shutdown (up to 10 additional seconds).
// It has no verifier and cannot serve the echo.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
)

const (
	probeLifetime  = 10 * time.Minute // fixed commandless deployment lifetime
	readBudget     = 10 * time.Second // one protected document read
	shutdownBudget = 5 * time.Second
)

// The deny-all mode has no other input: no document, trust, key, verifier or
// listen choice.
const (
	denyAllFlag     = "--deny-all-probe"
	denyAllNetwork  = "tcp4"
	denyAllAddress  = "0.0.0.0:8787"
	denyAllLifetime = 10 * time.Minute
	denyAllMessage  = "tether deny-all probe: reachability only; every request is refused"
)

// deps are the native operations. Production always uses nativeDeps; tests
// substitute fakes so that no file or listener is touched and may shorten,
// never extend, either fixed probe lifetime.
type deps struct {
	readTrust  func(context.Context, string) ([]byte, error)
	listen     func(network, addr string) (net.Listener, error)
	denyAllFor time.Duration
	probeFor   time.Duration
}

func nativeDeps() deps {
	return deps{readTrust: agentkeys.ReadTrustConfig, listen: net.Listen, denyAllFor: denyAllLifetime, probeFor: probeLifetime}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(execute(ctx, os.Args[1:], os.Stderr, nativeDeps()))
}

// execute validates the whole document and builds the probe before listening.
// Any refusal before the listener exits 2 with nothing opened. Diagnostics are
// fixed messages; no token, header, subject, body or path is printed.
func execute(ctx context.Context, args []string, diagnostic io.Writer, d deps) int {
	if ctx != nil && len(args) == 1 && args[0] == denyAllFlag {
		return executeDenyAll(ctx, diagnostic, d)
	}
	if ctx == nil || d.probeFor <= 0 || d.probeFor > probeLifetime || d.readTrust == nil || d.listen == nil || len(args) != 2 || args[0] != "-config" || args[1] == "" {
		report(diagnostic, "使い方: tether-echo-probe -config <path>")
		return 2
	}
	life, endLife := context.WithTimeout(ctx, d.probeFor)
	defer endLife()
	read, cancel := context.WithTimeout(life, readBudget)
	raw, err := d.readTrust(read, args[1])
	cancel()
	if err != nil {
		wipe(raw)
		report(diagnostic, "設定を読めませんでした。待受しません")
		return 2
	}
	c, err := agentgateway.ParseConfig(raw)
	wipe(raw)
	if err != nil {
		report(diagnostic, "設定が不正または不完全です。待受しません")
		return 2
	}
	p, err := newProbe(c, probeLimits())
	if err != nil {
		report(diagnostic, "probe用のCloudflare Access設定ではありません。待受しません")
		return 2
	}
	if life.Err() != nil {
		closing, stop := context.WithTimeout(context.Background(), shutdownBudget)
		_ = p.Close(closing)
		stop()
		report(diagnostic, "試験期限が終了しました。待受しません")
		return 2
	}
	// The network comes from the same parsed profile as the address.
	ln, err := d.listen(c.ListenNetwork(), c.Listen)
	if err != nil || ln == nil {
		if ln != nil {
			_ = ln.Close()
		}
		closing, stop := context.WithTimeout(context.Background(), shutdownBudget)
		_ = p.Close(closing)
		stop()
		report(diagnostic, "待受を開始できませんでした")
		return 1
	}
	return serve(life, ln, p, p.Close, diagnostic)
}

// executeDenyAll listens on the fixed address and refuses every request until
// the fixed lifetime (counted from before the listen) or ctx ends.
func executeDenyAll(ctx context.Context, diagnostic io.Writer, d deps) int {
	if d.listen == nil || d.denyAllFor <= 0 || d.denyAllFor > denyAllLifetime {
		report(diagnostic, "使い方: tether-echo-probe --deny-all-probe")
		return 2
	}
	life, cancel := context.WithTimeout(ctx, d.denyAllFor)
	defer cancel()
	ln, err := d.listen(denyAllNetwork, denyAllAddress)
	if err != nil || ln == nil {
		if ln != nil {
			_ = ln.Close()
		}
		report(diagnostic, "待受を開始できませんでした")
		return 1
	}
	return serve(life, ln, denyAll{}, func(context.Context) error { return nil }, diagnostic)
}

// denyAll answers every method, path and upgrade with the same fixed 403. It
// reads no header value, body, path or query and logs nothing; the
// connection closes after the reply.
type denyAll struct{}

func (denyAll) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Connection", "close")
	plain(w, http.StatusForbidden, denyAllMessage)
}

// serve runs the HTTP server on the validated listener until ctx ends or
// serving fails, then finishes the server and the handler's owned work
// (every upgraded connection in the echo mode).
func serve(ctx context.Context, ln net.Listener, h http.Handler, closeOwned func(context.Context) error, diagnostic io.Writer) int {
	srv := newHTTPServer(h)
	served := make(chan struct{})
	var serveErr error
	go func() {
		defer close(served)
		serveErr = srv.Serve(ln)
	}()
	code := 0
	select {
	case <-ctx.Done():
	case <-served:
		if !errors.Is(serveErr, http.ErrServerClosed) {
			code = 1
		}
	}
	if err := finish(srv, served, closeOwned, shutdownBudget); err != nil {
		report(diagnostic, "Owned shutdown could not be confirmed")
		code = 1
	}
	return code
}

// finish drains the HTTP server while the probe refuses new work and ends its
// WebSockets, force-closes a drain that outlives the budget, and joins Serve.
// Every phase is finite; an unconfirmed owner is never reported as clean.
func finish(srv *http.Server, served <-chan struct{}, closeProbe func(context.Context) error, budget time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	drained := make(chan error, 1)
	go func() { drained <- srv.Shutdown(ctx) }()
	errs := []error{closeProbe(ctx)}
	if err := <-drained; err != nil {
		errs = append(errs, err, srv.Close())
	}
	join, stop := context.WithTimeout(context.Background(), budget)
	defer stop()
	select {
	case <-served:
	case <-join.Done():
		errs = append(errs, join.Err())
	}
	return errors.Join(errs...)
}

// newHTTPServer bounds every phase; an upgraded connection is bounded by the
// probe lifetime instead. The error log is discarded so that no request detail
// is logged.
func newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		DisableGeneralOptionsHandler: true, Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0),
	}
}

func report(w io.Writer, message string) {
	if w != nil {
		fmt.Fprintln(w, message)
	}
}

// wipe clears the document buffer; best effort, it does not reach copies made
// by the runtime.
func wipe(b []byte) {
	clear(b[:cap(b)])
}
