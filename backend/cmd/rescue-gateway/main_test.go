package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"golang.org/x/crypto/ssh"
)

// execute is driven only with fake readers, loaders and an in-memory listener;
// no test opens a file, reads a key or reaches net.Listen.

type fakeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newFakeListener() *fakeListener {
	return &fakeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *fakeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *fakeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *fakeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8787} }

// recorder logs every native step in order and serves synthetic values.
type recorder struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []string
	doc      []byte
	readErr  error
	signers  map[string]ssh.Signer // by fingerprint
	failKey  string                // fingerprint whose load fails
	listener *fakeListener
	listened chan struct{}
	network  string        // the network passed to listen; read after listened closes
	lifetime time.Duration // deps.pilotLifetime
	hold     string        // the read or load step that waits for its ctx to end
	bounds   []time.Time   // every read and load ctx deadline, in call order
}

func newRecorder(t *testing.T, doc []byte) *recorder {
	return &recorder{t: t, doc: doc, listened: make(chan struct{}), lifetime: pilotMaxLifetime, signers: map[string]ssh.Signer{
		fingerprint(t, 0x11): signerOf(t, 0x11), fingerprint(t, 0x12): signerOf(t, 0x12),
	}}
}

func (r *recorder) log(s string) {
	r.mu.Lock()
	r.calls = append(r.calls, s)
	r.mu.Unlock()
}

func (r *recorder) trace() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.calls, " ")
}

// bounded logs one read or load step with its deadline and, at the held
// step, blocks until that ctx ends as a slow protected read would.
func (r *recorder) bounded(ctx context.Context, step string) {
	deadline, ok := ctx.Deadline()
	if !ok {
		r.t.Error("unbounded " + step)
	}
	r.mu.Lock()
	r.calls, r.bounds = append(r.calls, step), append(r.bounds, deadline)
	r.mu.Unlock()
	if step == r.hold {
		<-ctx.Done()
	}
}

func (r *recorder) deadlines() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.bounds...)
}

func (r *recorder) deps() deps {
	return deps{
		pilotLifetime: r.lifetime,
		readTrust: func(ctx context.Context, path string) ([]byte, error) {
			if path != configPath {
				r.t.Error("unexpected document read")
			}
			r.bounded(ctx, "read")
			if r.hold == "read" {
				return r.doc, ctx.Err()
			}
			return r.doc, r.readErr
		},
		loadKey: func(ctx context.Context, cfg agentkeys.Config) (ssh.Signer, error) {
			r.bounded(ctx, "load:"+cfg.Path[strings.LastIndex(cfg.Path, `\`)+1:])
			if cfg.Fingerprint == r.failKey {
				return nil, agentkeys.ErrDenied
			}
			return r.signers[cfg.Fingerprint], nil
		},
		listen: func(network, addr string) (net.Listener, error) {
			r.log("listen:" + addr)
			r.network = network
			defer close(r.listened)
			if r.listener == nil {
				return nil, errors.New("synthetic listen failure")
			}
			return r.listener, nil
		},
	}
}

func checkDiagnostic(t *testing.T, out string) {
	t.Helper()
	for _, secret := range []string{"synthetic", "member-", "mac-", "SHA256:", "example.invalid", `C:\`, "127.0.0.1"} {
		if strings.Contains(out, secret) {
			t.Fatalf("diagnostic leaks %q: %q", secret, out)
		}
	}
}

func TestEntryRefusesUsageBeforeReading(t *testing.T) {
	for _, args := range [][]string{
		nil, {"-config"}, {"-config", ""}, {"-config", configPath, "-token"}, {"-key", configPath},
		{"-pilot-config"}, {"-pilot-config", ""}, {"-pilot-config", configPath, "-config"}, {"-config", configPath, "-pilot-config", configPath},
		{"--pilot-config", configPath}, {"-pilot-config=" + configPath}, {"-Pilot-Config", configPath}, {"-pilot", configPath},
	} {
		r := newRecorder(t, encode(t, localDoc(t)))
		var diagnostic bytes.Buffer
		if code := execute(context.Background(), args, &diagnostic, r.deps()); code != 2 || r.trace() != "" {
			t.Fatalf("args %q: exit=%d trace=%q", args, code, r.trace())
		}
		checkDiagnostic(t, diagnostic.String())
	}
}

func TestEntryValidatesBeforeLoadingOrListening(t *testing.T) {
	invalid := localDoc(t)
	item(invalid, "hosts", 0)["label"] = "member-1"
	for _, tc := range []struct {
		name    string
		doc     []byte
		readErr error
		failKey func(t *testing.T) string
		want    string
	}{
		{"unreadable", nil, agentkeys.ErrDenied, nil, "read"},
		{"read failure with bytes", encode(t, localDoc(t)), agentkeys.ErrCleanup, nil, "read"},
		{"invalid document", encode(t, invalid), nil, nil, "read"},
		{"second key refused", encode(t, localDoc(t)), nil, func(t *testing.T) string { return fingerprint(t, 0x12) }, "read load:member-1 load:member-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRecorder(t, tc.doc)
			r.readErr = tc.readErr
			if tc.failKey != nil {
				r.failKey = tc.failKey(t)
			}
			var diagnostic bytes.Buffer
			if code := execute(context.Background(), []string{"-config", configPath}, &diagnostic, r.deps()); code != 2 || r.trace() != tc.want {
				t.Fatalf("exit=%d trace=%q", code, r.trace())
			}
			if !bytes.Equal(tc.doc, make([]byte, len(tc.doc))) {
				t.Fatal("document bytes were not wiped")
			}
			checkDiagnostic(t, diagnostic.String())
		})
	}
}

func TestEntryRefusesLoadedKeyOtherThanReferenced(t *testing.T) {
	r := newRecorder(t, encode(t, localDoc(t)))
	r.signers[fingerprint(t, 0x12)] = signerOf(t, 0x21) // the loader returned the host key instead
	var diagnostic bytes.Buffer
	if code := execute(context.Background(), []string{"-config", configPath}, &diagnostic, r.deps()); code != 2 || strings.Contains(r.trace(), "listen") {
		t.Fatalf("exit=%d trace=%q", code, r.trace())
	}
	checkDiagnostic(t, diagnostic.String())
}

func TestEntryListenFailureClosesOwners(t *testing.T) {
	r := newRecorder(t, encode(t, localDoc(t)))
	var diagnostic bytes.Buffer
	if code := execute(context.Background(), []string{"-config", configPath}, &diagnostic, r.deps()); code != 1 ||
		r.trace() != "read load:member-1 load:member-2 listen:127.0.0.1:8787" || r.network != "tcp" {
		t.Fatalf("exit=%d trace=%q network=%q", code, r.trace(), r.network)
	}
	checkDiagnostic(t, diagnostic.String())
}

// Only the document's explicit container_private profile reaches listen with
// a non-loopback address, and then over "tcp4" so that 0.0.0.0 never becomes
// a dual-stack IPv6 socket. The same address without the profile, or with
// the loopback profile, is refused before any key is loaded.
func TestEntryContainerProfileListensIPv4Only(t *testing.T) {
	for _, tc := range []struct {
		name    string
		gateway func(t *testing.T) map[string]any
	}{
		{"oauth_bearer_jwt", func(*testing.T) map[string]any { return gatewayDoc() }},
		{"cloudflare_access", accessGatewayDoc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, listen := range []string{"0.0.0.0:8787", "172.18.0.2:8787"} {
				gw := tc.gateway(t)
				gw["deployment_profile"], gw["listen"] = "container_private", listen
				doc := localDoc(t)
				doc["gateway"] = gw
				r := newRecorder(t, encode(t, doc))
				var diagnostic bytes.Buffer
				if code := execute(context.Background(), []string{"-config", configPath}, &diagnostic, r.deps()); code != 1 ||
					r.trace() != "read load:member-1 load:member-2 listen:"+listen || r.network != "tcp4" {
					t.Fatalf("%s: exit=%d trace=%q network=%q", listen, code, r.trace(), r.network)
				}
				checkDiagnostic(t, diagnostic.String())
			}
			for _, profile := range []any{nil, "loopback"} {
				gw := tc.gateway(t)
				gw["listen"] = "0.0.0.0:8787"
				if profile != nil {
					gw["deployment_profile"] = profile
				}
				doc := localDoc(t)
				doc["gateway"] = gw
				r := newRecorder(t, encode(t, doc))
				var diagnostic bytes.Buffer
				if code := execute(context.Background(), []string{"-config", configPath}, &diagnostic, r.deps()); code != 2 || r.trace() != "read" {
					t.Fatalf("profile %v: exit=%d trace=%q", profile, code, r.trace())
				}
				checkDiagnostic(t, diagnostic.String())
			}
		})
	}
}

// roundTrip sends one request over an in-memory connection accepted by the
// fake listener and returns the status and body.
func roundTrip(t *testing.T, l *fakeListener, request string) (int, string) {
	t.Helper()
	code, _, body := exchange(t, l, request)
	return code, body
}

// exchange is roundTrip that also returns the response header.
func exchange(t *testing.T, l *fakeListener, request string) (int, http.Header, string) {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	select {
	case l.conns <- server:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not accept")
	}
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(client, request); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(body)
}

func TestEntryMountsBothHandlersAfterValidation(t *testing.T) {
	doc := encode(t, localDoc(t))
	r := newRecorder(t, doc)
	r.listener = newFakeListener()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var diagnostic bytes.Buffer
	exited := make(chan int, 1)
	go func() { exited <- execute(ctx, []string{"-config", configPath}, &diagnostic, r.deps()) }()
	select {
	case <-r.listened:
	case <-time.After(5 * time.Second):
		t.Fatal("never listened")
	}
	if got := r.trace(); got != "read load:member-1 load:member-2 listen:127.0.0.1:8787" {
		t.Fatalf("trace = %q", got)
	}
	if !bytes.Equal(doc, make([]byte, len(doc))) {
		t.Fatal("document bytes were not wiped before serving")
	}
	const host = "Host: gateway.example.invalid\r\nConnection: close\r\n"
	if code, body := roundTrip(t, r.listener, "GET /.well-known/oauth-protected-resource/mcp HTTP/1.1\r\n"+host+"\r\n"); code != http.StatusOK ||
		!strings.Contains(body, `"resource":"https://gateway.example.invalid/mcp"`) {
		t.Fatalf("metadata: %d %q", code, body)
	}
	if code, _ := roundTrip(t, r.listener, "POST /mcp HTTP/1.1\r\n"+host+"Content-Type: application/json\r\nContent-Length: 2\r\n\r\n{}"); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated MCP: %d", code)
	}
	// OAuth bearer mode never reads an Access assertion.
	if code, _ := roundTrip(t, r.listener, "POST /mcp HTTP/1.1\r\n"+host+"Cf-Access-Jwt-Assertion: synthetic\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}"); code != http.StatusUnauthorized {
		t.Fatalf("Access assertion in OAuth mode: %d", code)
	}
	// The registration handler answers its own prechecks; no upgrade happens.
	if code, _ := roundTrip(t, r.listener, "GET /agent-register HTTP/1.1\r\n"+host+"\r\n"); code != http.StatusBadRequest {
		t.Fatalf("registration without subprotocol: %d", code)
	}
	if code, _ := roundTrip(t, r.listener, "GET /agent-register HTTP/1.1\r\n"+host+"Sec-WebSocket-Protocol: "+agenttransport.Subprotocol+"\r\nAuthorization: Bearer synthetic\r\n\r\n"); code != http.StatusForbidden {
		t.Fatalf("registration with ambient credential: %d", code)
	}
	for _, path := range []string{"/", "/mcp/", "/agent-register/", "/agent-register/x", "//mcp", "/mc%70", "/agent-regist%65r"} {
		if code, _ := roundTrip(t, r.listener, "GET "+path+" HTTP/1.1\r\n"+host+"\r\n"); code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, code)
		}
	}
	cancel()
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit = %d (%q)", code, diagnostic.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	select {
	case <-r.listener.closed:
	default:
		t.Fatal("listener left open")
	}
	checkDiagnostic(t, diagnostic.String())
}

func TestNativeDepsAreTheProtectedLoaders(t *testing.T) {
	d := nativeDeps()
	if reflect.ValueOf(d.listen).Pointer() != reflect.ValueOf(net.Listen).Pointer() ||
		reflect.ValueOf(d.readTrust).Pointer() != reflect.ValueOf(agentkeys.ReadTrustConfig).Pointer() ||
		reflect.ValueOf(d.loadKey).Pointer() != reflect.ValueOf(agentkeys.LoadCurrentUser).Pointer() {
		t.Fatal("production composition is not the protected loader")
	}
}

// The pilot lifetime is the fixed 15-minute maximum in production, and only
// -pilot-config receives a deadline; -config keeps the caller's ctx.
func TestPilotLifetimeIsNativeFixedMaximum(t *testing.T) {
	if pilotMaxLifetime != 15*time.Minute || nativeDeps().pilotLifetime != pilotMaxLifetime {
		t.Fatalf("pilot lifetime = %v, native = %v", pilotMaxLifetime, nativeDeps().pilotLifetime)
	}
	before := time.Now()
	ctx, stop, ok := lifetime(context.Background(), true, nativeDeps().pilotLifetime)
	if !ok {
		t.Fatal("native pilot lifetime refused")
	}
	defer stop()
	if deadline, has := ctx.Deadline(); !has || deadline.Before(before.Add(pilotMaxLifetime)) || deadline.After(time.Now().Add(pilotMaxLifetime)) {
		t.Fatalf("pilot deadline = %v (%v)", deadline, has)
	}
	plain, stopPlain, ok := lifetime(context.Background(), false, nativeDeps().pilotLifetime)
	if !ok {
		t.Fatal("-config refused")
	}
	defer stopPlain()
	if _, has := plain.Deadline(); has {
		t.Fatal("-config was time-limited")
	}
}

// A zero, negative or overlong lifetime refuses -pilot-config before any read
// instead of clamping; -config ignores it and starts unchanged.
func TestEntryRefusesPilotLifetimeOutsideMaximum(t *testing.T) {
	for _, limit := range []time.Duration{0, -time.Second, pilotMaxLifetime + time.Nanosecond, 24 * time.Hour} {
		r := newRecorder(t, encode(t, localDoc(t)))
		r.lifetime = limit
		var diagnostic bytes.Buffer
		if code := execute(context.Background(), []string{"-pilot-config", configPath}, &diagnostic, r.deps()); code != 2 || r.trace() != "" {
			t.Fatalf("%v: exit=%d trace=%q", limit, code, r.trace())
		}
		checkDiagnostic(t, diagnostic.String())
		r = newRecorder(t, encode(t, localDoc(t)))
		r.lifetime = limit
		diagnostic.Reset()
		if code := execute(context.Background(), []string{"-config", configPath}, &diagnostic, r.deps()); code != 1 ||
			r.trace() != "read load:member-1 load:member-2 listen:127.0.0.1:8787" {
			t.Fatalf("-config with %v: exit=%d trace=%q", limit, code, r.trace())
		}
		checkDiagnostic(t, diagnostic.String())
	}
}

// awaitListen waits for the fake listen and fails at once if execute returned
// without listening.
func awaitListen(t *testing.T, r *recorder, exited <-chan int, diagnostic *bytes.Buffer) {
	t.Helper()
	select {
	case <-r.listened:
	case code := <-exited:
		t.Fatalf("exited %d before listening (%q)", code, diagnostic.String())
	case <-time.After(30 * time.Second):
		t.Fatal("never listened")
	}
}

// -config is not silently time-limited even when deps carries a 1ns pilot
// lifetime: every read keeps the full readBudget and serving continues.
func TestEntryConfigIsNotTimeLimited(t *testing.T) {
	r := newRecorder(t, encode(t, localDoc(t)))
	r.listener, r.lifetime = newFakeListener(), time.Nanosecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	var diagnostic bytes.Buffer
	exited := make(chan int, 1)
	go func() { exited <- execute(ctx, []string{"-config", configPath}, &diagnostic, r.deps()) }()
	awaitListen(t, r, exited, &diagnostic)
	for _, deadline := range r.deadlines() {
		if deadline.Before(start.Add(readBudget)) {
			t.Fatalf("read deadline shortened to %v", deadline.Sub(start))
		}
	}
	if code, _ := roundTrip(t, r.listener, "GET / HTTP/1.1\r\nHost: gateway.example.invalid\r\nConnection: close\r\n\r\n"); code != http.StatusNotFound {
		t.Fatalf("serving: %d", code)
	}
	select {
	case code := <-exited:
		t.Fatalf("-config ended without interrupt: %d", code)
	default:
	}
	cancel()
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit = %d (%q)", code, diagnostic.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("shutdown did not finish")
	}
}

// An idle pilot serves through the real handlers until its lifetime ends,
// then finishes like an interrupt: the listener and an idle keep-alive
// connection are closed and the exit is 0. The lifetime leaves ample room for
// the in-memory parse before listen.
func TestEntryPilotLifetimeEndsIdleServing(t *testing.T) {
	r := newRecorder(t, encode(t, localDoc(t)))
	r.listener, r.lifetime = newFakeListener(), 2*time.Second
	start := time.Now()
	var diagnostic bytes.Buffer
	exited := make(chan int, 1)
	go func() {
		exited <- execute(context.Background(), []string{"-pilot-config", configPath}, &diagnostic, r.deps())
	}()
	awaitListen(t, r, exited, &diagnostic)
	limit := time.Now().Add(r.lifetime)
	for _, deadline := range r.deadlines() {
		if deadline.After(limit) {
			t.Fatalf("read deadline %v beyond the pilot lifetime", deadline.Sub(start))
		}
	}
	client, server := net.Pipe()
	defer client.Close()
	select {
	case r.listener.conns <- server:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not accept")
	}
	_ = client.SetDeadline(start.Add(r.lifetime + 30*time.Second))
	if _, err := io.WriteString(client, "GET / HTTP/1.1\r\nHost: gateway.example.invalid\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("serving: %d", resp.StatusCode)
	}
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit = %d (%q)", code, diagnostic.String())
		}
	case <-time.After(r.lifetime + 30*time.Second):
		t.Fatal("pilot lifetime did not end serving")
	}
	if elapsed := time.Since(start); elapsed < r.lifetime {
		t.Fatalf("ended after %v, before its lifetime", elapsed)
	}
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("idle connection after lifetime: %v", err)
	}
	select {
	case <-r.listener.closed:
	default:
		t.Fatal("listener left open")
	}
	if diagnostic.Len() == 0 {
		t.Fatal("lifetime end not reported")
	}
	checkDiagnostic(t, diagnostic.String())
}

// A protected read still pending at the pilot lifetime ends with it, well
// inside readBudget: the returned bytes are wiped and nothing listens.
func TestEntryPilotLifetimeBoundsSlowRead(t *testing.T) {
	doc := encode(t, localDoc(t))
	r := newRecorder(t, doc)
	r.listener, r.lifetime, r.hold = newFakeListener(), 300*time.Millisecond, "read"
	start := time.Now()
	var diagnostic bytes.Buffer
	if code := execute(context.Background(), []string{"-pilot-config", configPath}, &diagnostic, r.deps()); code != 2 || r.trace() != "read" {
		t.Fatalf("exit=%d trace=%q", code, r.trace())
	}
	if elapsed := time.Since(start); elapsed < r.lifetime || elapsed >= readBudget {
		t.Fatalf("read ended after %v", elapsed)
	}
	if !bytes.Equal(doc, make([]byte, len(doc))) {
		t.Fatal("document bytes were not wiped")
	}
	checkDiagnostic(t, diagnostic.String())
}

// A loader that returns only after the lifetime ended still yields no listen:
// the built handler and gateway are closed and the exit is 1.
func TestEntryPilotExpiredBeforeListenClosesOwners(t *testing.T) {
	doc := encode(t, localDoc(t))
	r := newRecorder(t, doc)
	r.listener, r.lifetime, r.hold = newFakeListener(), 300*time.Millisecond, "load:member-2"
	var diagnostic bytes.Buffer
	if code := execute(context.Background(), []string{"-pilot-config", configPath}, &diagnostic, r.deps()); code != 1 ||
		r.trace() != "read load:member-1 load:member-2" {
		t.Fatalf("exit=%d trace=%q", code, r.trace())
	}
	if !bytes.Equal(doc, make([]byte, len(doc))) {
		t.Fatal("document bytes were not wiped")
	}
	if diagnostic.String() != "試行期限内に待受を開始できませんでした\n" {
		t.Fatal("expiry did not confirm unserved owner cleanup")
	}
	checkDiagnostic(t, diagnostic.String())
}

// serve on a pilot ctx ends at its deadline through the existing finish:
// registration (with its streams) before the gateway, the listener closed,
// and an unconfirmed registration cleanup reported as exit 1.
func TestServeEndsAtPilotLifetimeAndFinishesOwners(t *testing.T) {
	for _, tc := range []struct {
		name            string
		registrationErr error
		want            int
	}{
		{"confirmed", nil, 0},
		{"registration unconfirmed", agenttransport.ErrCleanupUnknown, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, trace := ownedFixture(t, nil, tc.registrationErr)
			ln := newFakeListener()
			ctx, stop := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer stop()
			var diagnostic bytes.Buffer
			exited := make(chan int, 1)
			go func() {
				exited <- serve(ctx, ln, http.NotFoundHandler(), o.closeRegistration, o.closeGateway, &diagnostic)
			}()
			select {
			case code := <-exited:
				if code != tc.want || trace() != "registration gateway" {
					t.Fatalf("exit=%d order=%q", code, trace())
				}
			case <-time.After(30 * time.Second):
				t.Fatal("serve outlived its lifetime")
			}
			select {
			case <-ln.closed:
			default:
				t.Fatal("listener left open")
			}
			if (tc.want != 0) != (diagnostic.Len() != 0) {
				t.Fatalf("diagnostic = %q", diagnostic.String())
			}
		})
	}
}

func TestHTTPServerBounds(t *testing.T) {
	srv := newHTTPServer(nil)
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= srv.ReadTimeout || srv.IdleTimeout <= 0 || srv.MaxHeaderBytes <= 0 || srv.ErrorLog == nil {
		t.Fatalf("server bounds = %+v", srv)
	}
}
