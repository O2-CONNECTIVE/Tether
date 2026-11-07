//go:build ci_mcp_loopback

package agentgateway_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

// This explicit lane owns a real IPv4 loopback TCP/TLS endpoint. Trust is
// confined to one client and an ephemeral certificate; no OS trust is changed.
// Behind the production HTTP handler, the existing harness still uses memory
// SSH and a fake Mac runtime. This is not producer, GUI or native execution proof.
type loopbackMCP struct {
	t           *testing.T
	client      *http.Client
	transport   *http.Transport
	url         string
	bearer, sid string
	next        int
}

func newLoopbackMCP(t *testing.T, handler http.Handler) *loopbackMCP {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "ephemeral loopback test"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("owned loopback listener: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	address := listener.Addr().(*net.TCPAddr)
	if !address.IP.Equal(net.ParseIP("127.0.0.1")) || address.Port < 1024 {
		t.Fatalf("unexpected listener address: %v", address)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second}
	done := make(chan error, 1)
	secured := tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	go func() { done <- server.Serve(secured) }()
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 5 * time.Second,
		DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
			if target != address.String() {
				return nil, errors.New("test refuses non-owned destination")
			}
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp4", target)
		}} // Proxy is nil: environment proxies are never used.
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("test refuses redirects") }}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
			t.Errorf("owned HTTP server shutdown: %v", err)
		}
		select {
		case err := <-done:
			if !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("HTTP Serve: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("owned HTTP server did not join")
		}
	})
	return &loopbackMCP{t: t, client: client, transport: transport,
		url: "https://" + address.String() + "/mcp", bearer: token(t, subjects[0])}
}

func (c *loopbackMCP) do(method, bearer, sid, body string) reply {
	c.t.Helper()
	req, err := http.NewRequest(method, c.url, strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if sid != "" {
		req.Header.Set(agentgateway.HeaderSession, sid)
		req.Header.Set(agentgateway.HeaderProtocol, mcpadapter.ProtocolVersion)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	res, err := c.client.Do(req)
	if err != nil {
		c.t.Fatalf("loopback MCP request: %v", err)
	}
	defer res.Body.Close()
	if res.TLS == nil || len(res.TLS.VerifiedChains) == 0 {
		c.t.Fatal("response did not use verified TLS")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, mcpadapter.MaxLine+1))
	if err != nil || len(data) > mcpadapter.MaxLine {
		c.t.Fatalf("response size/read: %d, %v", len(data), err)
	}
	out := reply{status: res.StatusCode, header: res.Header, body: data}
	if strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(data, &out.message); err != nil {
			c.t.Fatal(err)
		}
	}
	return out
}

func (c *loopbackMCP) call(method string, params any) map[string]any {
	c.t.Helper()
	c.next++
	res := c.do(http.MethodPost, c.bearer, c.sid, rpc(c.next, method, params))
	result, ok := res.message["result"].(map[string]any)
	if res.status != http.StatusOK || !ok || res.message["id"] != float64(c.next) {
		c.t.Fatalf("%s response = %d %s", method, res.status, res.body)
	}
	if method == "initialize" {
		c.sid = res.header.Get(agentgateway.HeaderSession)
		if c.sid == "" || result["protocolVersion"] != mcpadapter.ProtocolVersion {
			c.t.Fatal("invalid initialization")
		}
	}
	return result
}

func (c *loopbackMCP) tool(name string, args map[string]any) map[string]any {
	c.t.Helper()
	result := c.call("tools/call", map[string]any{"name": name, "arguments": args})
	content, ok := result["structuredContent"].(map[string]any)
	if !ok || result["isError"] != false {
		c.t.Fatalf("%s = %v", name, result)
	}
	return content
}

func (c *loopbackMCP) terminal(id string) map[string]any {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := c.tool("rescue_status", map[string]any{"command_id": id})
		if st["terminal"] == true {
			return st
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("terminal status timeout: %v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
