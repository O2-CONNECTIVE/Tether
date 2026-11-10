// rescue-gateway is the remote MCP (Streamable HTTP) gateway entry for the
// agent command tools. It reads one local composition document named by
// "-config <path>" through agentkeys.ReadTrustConfig (protected like a key,
// although its contents are public), loads each principal's enrolled raw
// Ed25519 signer with agentkeys.LoadCurrentUser from the referenced path and
// fingerprint, and only then listens on the configured address: loopback by
// default, or the document's explicit container_private address. It
// serves the MCP resource with agentgateway and host registrations with
// agenttransport at exact paths. No credential is read from argv, the
// environment or the document; see docs/agent-cli.md.
//
// "-pilot-config <path>" is the same start bounded as a whole by the fixed
// pilotMaxLifetime, which begins before the document read; "-config" is not
// time-limited.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
	"golang.org/x/crypto/ssh"
)

const (
	readBudget     = 10 * time.Second // one protected document or key read
	shutdownBudget = 5 * time.Second
	// pilotMaxLifetime bounds one whole -pilot-config run. It is fixed: no
	// argument, document member or environment value sets or extends it.
	pilotMaxLifetime = 15 * time.Minute
)

// deps are the native operations. Production always uses nativeDeps; tests
// substitute fakes so that no file, key or listener is touched.
type deps struct {
	readTrust func(context.Context, string) ([]byte, error)
	loadKey   func(context.Context, agentkeys.Config) (ssh.Signer, error)
	listen    func(network, addr string) (net.Listener, error)
	// pilotLifetime applies to -pilot-config only. Production fixes it at
	// pilotMaxLifetime; a test may shorten it but never exceed the maximum.
	pilotLifetime time.Duration
}

func nativeDeps() deps {
	return deps{
		readTrust:     agentkeys.ReadTrustConfig,
		loadKey:       agentkeys.LoadCurrentUser,
		listen:        net.Listen,
		pilotLifetime: pilotMaxLifetime,
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(execute(ctx, os.Args[1:], os.Stderr, nativeDeps()))
}

// execute validates the whole document, loads every principal signer, builds
// the gateway and registration handler, and listens last. Any refusal before
// the listener normally exits 2; an expired constructed pilot closes owners
// and exits 1. Diagnostics are fixed messages; no
// token, header, subject, label, fingerprint or path is printed.
func execute(ctx context.Context, args []string, diagnostic io.Writer, d deps) int {
	if ctx == nil || d.readTrust == nil || d.loadKey == nil || d.listen == nil || len(args) != 2 ||
		(args[0] != "-config" && args[0] != "-pilot-config") || args[1] == "" {
		report(diagnostic, "使い方: rescue-gateway -config <path> | -pilot-config <path>")
		return 2
	}
	pilot := args[0] == "-pilot-config"
	ctx, stopLifetime, ok := lifetime(ctx, pilot, d.pilotLifetime)
	if !ok {
		report(diagnostic, "試行期限が不正です")
		return 2
	}
	defer stopLifetime()
	read, cancel := context.WithTimeout(ctx, readBudget)
	raw, err := d.readTrust(read, args[1])
	cancel()
	if err != nil {
		wipe(raw)
		report(diagnostic, "設定を読めませんでした")
		return 2
	}
	cfg, err := parseLocalConfig(raw, args[1])
	wipe(raw)
	if err != nil {
		report(diagnostic, "設定が不正または不完全です")
		return 2
	}
	// Every principal needs its own enrolled key; one refusal stops the start
	// instead of substituting another member's key or serving a partial team.
	principals := make([]agentgateway.Principal, 0, len(cfg.gateway.Principals))
	for i, p := range cfg.gateway.Principals {
		load, cancel := context.WithTimeout(ctx, readBudget)
		s, err := d.loadKey(load, cfg.keys[i])
		cancel()
		if err != nil || s == nil || s.PublicKey().Type() != ssh.KeyAlgoED25519 || ssh.FingerprintSHA256(s.PublicKey()) != cfg.keys[i].Fingerprint {
			report(diagnostic, "署名鍵を取得できませんでした。待受しません")
			return 2
		}
		p.Signer = s
		principals = append(principals, p)
	}
	// The document's one auth_mode selects the bearer or Access verifier; the
	// other is never built.
	// FileConfig intentionally leaves CallTimeout zero: the default is 30s,
	// bounded below the native Worker upstream budget of 35s. No document
	// member can extend it; keep that contract when adding future settings.
	gwConfig, err := cfg.gateway.GatewayConfig()
	if err != nil {
		report(diagnostic, "設定が不正または不完全です")
		return 2
	}
	gwConfig.Principals, gwConfig.Budgets = principals, agentgateway.ChannelBudgets()
	gw, err := agentgateway.New(gwConfig)
	if err != nil {
		report(diagnostic, "設定が不正または不完全です")
		return 2
	}
	// Resident signers come only from explicitly selected protected entries.
	// Legacy host/key enrollment alone never enables the resident protocol.
	var residentPrincipals []agenttransport.ResidentPrincipal
	for _, entry := range cfg.resident {
		for _, p := range principals {
			if p.Label == entry.PrincipalLabel {
				residentPrincipals = append(residentPrincipals, agenttransport.ResidentPrincipal{Label: p.Label, Signer: p.Signer})
			}
		}
	}
	registration, err := agenttransport.NewHandler(agenttransport.HandlerConfig{
		Endpoint: cfg.endpoint, Hosts: cfg.hosts, Registrar: gw, Budgets: agenttransport.DefaultBudgets(),
		ResidentPrincipals: residentPrincipals,
	})
	if err != nil {
		report(diagnostic, "設定が不正または不完全です")
		return closeUnserved(nil, gw.Close, diagnostic, 2)
	}
	// One-time composition happens before the listener can initialize any MCP
	// session. Handler and gateway share the same pinned mapping and signer.
	if len(cfg.resident) > 0 {
		if err := gw.ConfigureResident(cfg.endpoint, registration, cfg.resident); err != nil {
			report(diagnostic, "常駐構成を開始できませんでした。待受しません")
			return closeUnserved(registration.Close, gw.Close, diagnostic, 2)
		}
	}
	// A pilot whose lifetime ended during the reads or construction never
	// listens; the owners built so far are closed like a listen failure.
	if pilot && ctx.Err() != nil {
		report(diagnostic, "試行期限内に待受を開始できませんでした")
		return closeUnserved(registration.Close, gw.Close, diagnostic, 1)
	}
	// The network comes from the same parsed profile as the address.
	ln, err := d.listen(cfg.gateway.ListenNetwork(), cfg.gateway.Listen)
	if err != nil || ln == nil {
		if ln != nil {
			_ = ln.Close()
		}
		report(diagnostic, "待受を開始できませんでした")
		return closeUnserved(registration.Close, gw.Close, diagnostic, 1)
	}
	mcpPath, registrationPath := pathOf(cfg.gateway.Resource), pathOf(cfg.endpoint)
	metadata := cfg.gateway.Mode == agentgateway.ModeOAuthBearer
	code := serve(ctx, ln, routes(mcpPath, registrationPath, metadata, gw, registration), registration.Close, gw.Close, diagnostic)
	if pilot && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		report(diagnostic, "試行期限に達したため停止しました")
	}
	return code
}

// lifetime bounds a -pilot-config run as a whole from before the document
// read: the read and key-load deadlines derive from it, and its end finishes
// serving through serve like an interrupt. Each Mac grant keeps its own
// shorter limit. A lifetime outside (0, pilotMaxLifetime] is refused, never
// clamped. -config keeps the caller's ctx unchanged.
func lifetime(ctx context.Context, pilot bool, limit time.Duration) (context.Context, context.CancelFunc, bool) {
	if !pilot {
		return ctx, func() {}, true
	}
	if limit <= 0 || limit > pilotMaxLifetime {
		return nil, nil, false
	}
	bounded, stop := context.WithTimeout(ctx, limit)
	return bounded, stop, true
}

// closeUnserved releases owners that never received a connection.
func closeUnserved(closeRegistration, closeGateway func(context.Context) error, diagnostic io.Writer, code int) int {
	cleanup, stop := context.WithTimeout(context.Background(), 2*shutdownBudget)
	defer stop()
	var errs []error
	if closeRegistration != nil {
		errs = append(errs, closeRegistration(cleanup))
	}
	if errors.Join(append(errs, closeGateway(cleanup))...) != nil {
		report(diagnostic, "Owned shutdown could not be confirmed")
		return 1
	}
	return code
}

// routes dispatches exact paths only: the registration endpoint to the
// transport handler, the MCP resource and, in OAuth bearer mode only, its
// protected resource metadata to the gateway. In Access mode discovery is the
// Access edge's, so the metadata paths are 404 here. No prefix match, path
// cleaning or redirect applies.
func routes(mcpPath, registrationPath string, metadata bool, gw, registration http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL == nil || r.URL.RawPath != "" {
			http.NotFound(w, r)
			return
		}
		switch p := r.URL.Path; {
		case p == registrationPath:
			registration.ServeHTTP(w, r)
		case p == mcpPath, metadata && (p == agentgateway.MetadataPrefix || p == agentgateway.MetadataPrefix+mcpPath):
			gw.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

// serve runs the HTTP server on the already validated listener until ctx ends
// or serving fails, then finishes every owner. Upgraded registration streams
// are owned by the registration handler, not by the HTTP server.
func serve(ctx context.Context, ln net.Listener, h http.Handler, closeRegistration, closeGateway func(context.Context) error, diagnostic io.Writer) int {
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
	if err := finishOwned(owned{srv: srv, served: served, closeRegistration: closeRegistration, closeGateway: closeGateway}, shutdownBudget); err != nil {
		report(diagnostic, "Owned shutdown could not be confirmed")
		code = 1
	}
	return code
}

// newHTTPServer bounds every phase. WriteTimeout exceeds one tool call;
// the error log is discarded so that no request detail is logged.
func newHTTPServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30*time.Second + mcpadapter.DefaultCallTimeout + 15*time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0),
	}
}

func report(w io.Writer, message string) {
	if w != nil {
		fmt.Fprintln(w, message)
	}
}

// shutdownServer admits a fake for ownership tests without a listener.
type shutdownServer interface {
	Shutdown(context.Context) error
	Close() error
}

// owned is everything one serving run must finish: the HTTP server and its
// Serve goroutine, the registration handler (hijacked transports) and the
// gateway (routes and agent channels).
type owned struct {
	srv               shutdownServer
	served            <-chan struct{}
	closeRegistration func(context.Context) error
	closeGateway      func(context.Context) error
}

// finishOwned stops admission (Shutdown closes the listener; both handlers
// refuse once closed), closes the registration handler and its transports
// before the gateway, then joins the HTTP drain and Serve. A timed-out drain
// is force-closed and still reported. Every phase is finite and nothing is
// retried; an unknown owner is never reported as clean. The drain spans the
// owner cleanup so that requests ended by the gateway close still finish.
func finishOwned(o owned, budget time.Duration) error {
	drain, stopDrain := context.WithTimeout(context.Background(), 3*budget)
	defer stopDrain()
	drained := make(chan error, 1)
	go func() { drained <- o.srv.Shutdown(drain) }()
	var errs []error
	cleanup, stop := context.WithTimeout(context.Background(), 2*budget)
	errs = append(errs, o.closeRegistration(cleanup), o.closeGateway(cleanup))
	stop()
	join, stopJoin := context.WithTimeout(context.Background(), budget)
	defer stopJoin()
	select {
	case err := <-drained:
		if err != nil {
			errs = append(errs, err, o.srv.Close())
		}
	case <-join.Done():
		errs = append(errs, join.Err(), o.srv.Close())
	}
	select {
	case <-o.served:
	case <-join.Done():
		errs = append(errs, join.Err())
	}
	return errors.Join(errs...)
}

func pathOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Path
}
