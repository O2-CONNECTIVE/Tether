package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

const configPath = "/synthetic/protected/tether-probe.json"

// fakeDeps records the protected read and the listen without touching a file
// or socket. raw is the buffer handed out by the fake reader.
type fakeDeps struct {
	mu       sync.Mutex
	raw      []byte
	readErr  error
	deadline bool
	listened []string
	ln       net.Listener
	listenFn func(network, addr string) (net.Listener, error)
	reads    int
	denyFor  time.Duration
}

func (f *fakeDeps) deps() deps {
	return deps{
		readTrust: func(ctx context.Context, path string) ([]byte, error) {
			_, has := ctx.Deadline()
			f.mu.Lock()
			defer f.mu.Unlock()
			f.reads++
			f.deadline = has && path == configPath
			return f.raw, f.readErr
		},
		listen: func(network, addr string) (net.Listener, error) {
			f.mu.Lock()
			f.listened = append(f.listened, network+" "+addr)
			f.mu.Unlock()
			if f.listenFn != nil {
				return f.listenFn(network, addr)
			}
			return f.ln, nil
		},
		denyAllFor: f.denyFor,
		probeFor:   probeLifetime,
	}
}

func checkDiagnostic(t *testing.T, s string) {
	t.Helper()
	for _, leak := range []string{configPath, subject1, subject2, testIssuer, testAUD, "eyJ"} {
		if strings.Contains(s, leak) {
			t.Fatalf("diagnostic discloses %q", leak)
		}
	}
}

func zeroed(b []byte) bool { return len(bytes.Trim(b[:cap(b)], "\x00")) == 0 }

// Missing, unreadable or non-probe configuration refuses before any listener
// is requested, and the read buffer is cleared.
func TestExecuteRefusesBeforeListen(t *testing.T) {
	oauth := probeDoc(t, func(d map[string]any) {
		for _, k := range []string{"access_issuer", "access_aud", "access_keys"} {
			delete(d, k)
		}
		d["auth_mode"], d["issuer"], d["scope"], d["token_types"] = "oauth_bearer_jwt", "https://synthetic-issuer.example.invalid", "tether.probe", []string{"at+jwt"}
		d["jwks"] = map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "synthetic-ed", "alg": "EdDSA",
			"x": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}}
	})
	for _, tc := range []struct {
		name string
		args []string
		raw  []byte
		err  error
	}{
		{"no arguments", nil, nil, nil},
		{"no config flag", []string{configPath}, nil, nil},
		{"empty path", []string{"-config", ""}, nil, nil},
		{"unreadable config", []string{"-config", configPath}, nil, errors.New("synthetic denied")},
		{"empty config", []string{"-config", configPath}, []byte{}, nil},
		{"not JSON", []string{"-config", configPath}, []byte("listen=0.0.0.0"), nil},
		{"OAuth bearer document", []string{"-config", configPath}, oauth, nil},
		{"real gateway MCP resource", []string{"-config", configPath}, probeDoc(t, func(d map[string]any) {
			d["resource"] = "https://probe.example.invalid/mcp"
		}), nil},
		{"unset sample values", []string{"-config", configPath}, probeDoc(t, func(d map[string]any) {
			d["listen"], d["access_issuer"], d["access_aud"], d["access_keys"] = "", "", "", map[string]any{"keys": []any{}}
			d["principals"] = []any{map[string]any{"label": "member-1", "subject": ""}}
		}), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDeps{raw: tc.raw, readErr: tc.err}
			var diagnostic bytes.Buffer
			if code := execute(t.Context(), tc.args, &diagnostic, f.deps()); code != 2 {
				t.Fatalf("exit = %d", code)
			}
			if len(f.listened) != 0 {
				t.Fatalf("listened: %v", f.listened)
			}
			if tc.raw != nil && !zeroed(tc.raw) {
				t.Fatal("document buffer not cleared")
			}
			checkDiagnostic(t, diagnostic.String())
		})
	}
}

func TestExecuteListenFailure(t *testing.T) {
	f := &fakeDeps{raw: probeDoc(t, nil), listenFn: func(string, string) (net.Listener, error) { return nil, errors.New("synthetic") }}
	var diagnostic bytes.Buffer
	if code := execute(t.Context(), []string{"-config", configPath}, &diagnostic, f.deps()); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	checkDiagnostic(t, diagnostic.String())
}

// A valid container_private document is read with a deadline, cleared, and
// only then listened on with tcp4 at the validated address. The served probe
// answers an authenticated echo through the production server and stops on
// context cancellation.
func TestExecuteServesProbeAndShutsDown(t *testing.T) {
	raw := probeDoc(t, func(d map[string]any) {
		d["deployment_profile"], d["listen"] = "container_private", "0.0.0.0:8787"
	})
	ln := newPipeListener()
	f := &fakeDeps{raw: raw, ln: ln}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var diagnostic bytes.Buffer
	exited := make(chan int, 1)
	go func() { exited <- execute(ctx, []string{"-config", configPath}, &diagnostic, f.deps()) }()
	eventually(t, "listen", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.listened) == 1
	})
	f.mu.Lock()
	listened, deadline := f.listened[0], f.deadline
	f.mu.Unlock()
	if listened != "tcp4 0.0.0.0:8787" || !deadline || !zeroed(raw) {
		t.Fatalf("listen = %q, read deadline %v, cleared %v", listened, deadline, zeroed(raw))
	}

	req, _ := http.NewRequest(http.MethodPost, "http://probe.example.invalid"+HTTPPath, strings.NewReader("synthetic-route"))
	req.Header.Set("Content-Type", echoType)
	req.Header.Set(agentgateway.HeaderAccessAssertion, assertion(t, subject1))
	resp, err := ln.client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var reply echoReply
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &reply) != nil || reply.CommandAccess || reply.Probe != probeName || reply.EchoBytes != len("synthetic-route") {
		t.Fatalf("echo = %d %s", resp.StatusCode, body)
	}

	cancel()
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit = %d (%q)", code, diagnostic.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	checkDiagnostic(t, diagnostic.String())
}

// Only the exact single argument selects the deny-all mode; its lifetime can
// be shortened by a test but never extended. Every other form refuses without
// a read or a listen.
func TestDenyAllArgumentIsExact(t *testing.T) {
	for _, args := range [][]string{
		{"--deny-all-probe=1"}, {"-deny-all-probe"}, {"--DENY-ALL-PROBE"}, {"--deny-all-probe", "x"},
		{"--deny-all-probe", "0.0.0.0:9999"}, {"--deny-all-probe", "-config", configPath}, {"-config", configPath, "--deny-all-probe"},
	} {
		f := &fakeDeps{raw: probeDoc(t, nil), denyFor: time.Second}
		if code := execute(t.Context(), args, io.Discard, f.deps()); code != 2 || f.reads != 0 || len(f.listened) != 0 {
			t.Fatalf("%q: exit %d, reads %d, listened %v", args, code, f.reads, f.listened)
		}
	}
	for _, lifetime := range []time.Duration{0, -time.Second, denyAllLifetime + time.Nanosecond} {
		f := &fakeDeps{denyFor: lifetime}
		if code := execute(t.Context(), []string{denyAllFlag}, io.Discard, f.deps()); code != 2 || f.reads != 0 || len(f.listened) != 0 {
			t.Fatalf("lifetime %v: exit %d, listened %v", lifetime, code, f.listened)
		}
	}
	if nativeDeps().denyAllFor != 10*time.Minute {
		t.Fatal("the native deny-all lifetime is not the fixed 10 minutes")
	}
}

// countingBody records any read; the deny-all handler never reads input.
type countingBody struct{ reads int }

func (b *countingBody) Read([]byte) (int, error) {
	b.reads++
	return 0, io.EOF
}

func (b *countingBody) Close() error { return nil }

// Every method, path and upgrade, even with a valid synthetic assertion and
// the probe subprotocol, gets the same fixed 403 without any input read.
func TestDenyAllRefusesEverythingWithoutReading(t *testing.T) {
	token := assertion(t, subject1)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions, http.MethodPatch} {
		for _, target := range []string{"/", HTTPPath, WSPath, "/mcp", "/anything?x=1", "/tether-probe/v1/%65cho"} {
			body := &countingBody{}
			req := httptest.NewRequest(method, "http://probe.example.invalid"+target, nil)
			req.Body = body
			for k, v := range map[string]string{
				agentgateway.HeaderAccessAssertion: token, "Origin": "https://probe.example.invalid", "Content-Type": echoType,
				"Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13",
				"Sec-WebSocket-Key": "c3ludGhldGljLWtleS0xNg==", "Sec-WebSocket-Protocol": Subprotocol,
			} {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			denyAll{}.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden || rec.Body.String() != denyAllMessage+"\n" || body.reads != 0 ||
				rec.Header().Get("Connection") != "close" || rec.Header().Get("Sec-WebSocket-Accept") != "" {
				t.Fatalf("%s %s = %d %v %q, reads %d", method, target, rec.Code, rec.Header(), rec.Body.String(), body.reads)
			}
		}
	}
}

// The mode listens only on the fixed tcp4 address without reading any
// document, refuses an echo and an upgrade through the production server, and
// ends by its own lifetime with a context that is never cancelled.
func TestDenyAllModeListensFixedAndEnds(t *testing.T) {
	ln := newPipeListener()
	f := &fakeDeps{ln: ln, denyFor: 2 * time.Second}
	var diagnostic bytes.Buffer
	exited := make(chan int, 1)
	go func() { exited <- execute(context.Background(), []string{denyAllFlag}, &diagnostic, f.deps()) }()
	eventually(t, "listen", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.listened) == 1
	})
	f.mu.Lock()
	listened := f.listened[0]
	f.mu.Unlock()
	if listened != "tcp4 0.0.0.0:8787" {
		t.Fatalf("listen = %q", listened)
	}

	req, _ := http.NewRequest(http.MethodPost, "http://probe.example.invalid"+HTTPPath, strings.NewReader("synthetic-route"))
	req.Header.Set("Content-Type", echoType)
	req.Header.Set(agentgateway.HeaderAccessAssertion, assertion(t, subject1))
	resp, err := ln.client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || string(body) != denyAllMessage+"\n" {
		t.Fatalf("echo in deny-all mode = %d %q", resp.StatusCode, body)
	}
	if ws, resp, err := dialWS(t, ln, assertion(t, subject1), Subprotocol); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		if ws != nil {
			_ = ws.CloseNow()
		}
		t.Fatalf("upgrade in deny-all mode = %v, %v", resp, err)
	}

	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit = %d (%q)", code, diagnostic.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the deny-all lifetime did not end the process")
	}
	if f.reads != 0 {
		t.Fatal("deny-all mode read a document")
	}
	checkDiagnostic(t, diagnostic.String())
}

func TestDenyAllServerRefusesStarOptions(t *testing.T) {
	ln := newPipeListener()
	srv := newHTTPServer(denyAll{})
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close(); <-done }()
	c, err := ln.dial(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(c, "OPTIONS * HTTP/1.1\r\nHost: probe.example.invalid\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodOptions})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden || string(body) != denyAllMessage+"\n" {
		t.Fatalf("OPTIONS * = %d %q", resp.StatusCode, body)
	}
}

func TestAuthenticatedProbeLifetimeIsFinite(t *testing.T) {
	if nativeDeps().probeFor != 10*time.Minute {
		t.Fatal("native probe lifetime changed")
	}
	for _, duration := range []time.Duration{0, -time.Second, probeLifetime + time.Second} {
		f := &fakeDeps{raw: probeDoc(t, nil)}
		d := f.deps()
		d.probeFor = duration
		if execute(t.Context(), []string{"-config", configPath}, io.Discard, d) != 2 || f.reads != 0 || len(f.listened) != 0 {
			t.Fatal("invalid lifetime accepted")
		}
	}
	ln := newPipeListener()
	f := &fakeDeps{raw: probeDoc(t, nil), ln: ln}
	d := f.deps()
	d.probeFor = time.Second
	exited := make(chan int, 1)
	go func() { exited <- execute(context.Background(), []string{"-config", configPath}, io.Discard, d) }()
	eventually(t, "authenticated listen", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.listened) == 1 })
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("idle authenticated probe did not expire")
	}
}

func TestAuthenticatedLifetimeBoundsReadAndRefusesExpiredListen(t *testing.T) {
	raw := probeDoc(t, nil)
	f := &fakeDeps{}
	d := f.deps()
	d.probeFor = 20 * time.Millisecond
	d.readTrust = func(ctx context.Context, _ string) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > d.probeFor {
			t.Fatal("read deadline not bounded by probe lifetime")
		}
		<-ctx.Done()
		return raw, nil
	}
	if code := execute(t.Context(), []string{"-config", configPath}, io.Discard, d); code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if len(f.listened) != 0 || !zeroed(raw) {
		t.Fatal("expired read opened listener or retained config")
	}
}
