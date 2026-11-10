package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func discoveryDoc() map[string]any {
	return map[string]any{"mode": "discovery_only", "gateway": gatewayDoc()}
}

func TestDiscoveryConfigStrictEnvelope(t *testing.T) {
	for _, access := range []bool{false, true} {
		doc := discoveryDoc()
		if access {
			doc["gateway"] = accessGatewayDoc(t)
		}
		if _, err := parseDiscoveryConfig(encode(t, doc)); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"hosts", "principal_keys", "registration_endpoint", "resident", "timeout", "unknown"} {
		doc := discoveryDoc()
		doc[field] = []any{}
		if _, err := parseDiscoveryConfig(encode(t, doc)); err == nil {
			t.Fatal("mixed field admitted", field)
		}
	}
	for _, raw := range [][]byte{
		nil, []byte(`null`), []byte(`{"mode":"discovery_only","gateway":null}`),
		[]byte(`{"mode":"discovery_only","mode":"discovery_only","gateway":{}}`),
		encode(t, localDoc(t)), []byte(`{"mode":"discovery_only","gateway":{}} {}`),
	} {
		if _, err := parseDiscoveryConfig(raw); err == nil {
			t.Fatal("invalid envelope admitted")
		}
	}
	for _, mode := range []any{nil, "", "ordinary", "discovery", 1} {
		doc := discoveryDoc()
		doc["mode"] = mode
		if _, err := parseDiscoveryConfig(encode(t, doc)); err == nil {
			t.Fatal("invalid mode admitted")
		}
	}
	for _, field := range []string{"auth_mode", "principals", "jwks", "issuer"} {
		doc := discoveryDoc()
		delete(doc["gateway"].(map[string]any), field)
		if _, err := parseDiscoveryConfig(encode(t, doc)); err == nil {
			t.Fatal("missing trust admitted", field)
		}
	}
}

func TestDiscoveryEntryNoSignerAndCleanup(t *testing.T) {
	for _, access := range []bool{false, true} {
		doc := discoveryDoc()
		if access {
			doc["gateway"] = accessGatewayDoc(t)
		}
		r := newRecorder(t, encode(t, doc))
		r.listener = newFakeListener()
		d := r.deps()
		d.loadKey = nil // unlike ordinary modes this is not a dependency
		ctx, stop := context.WithCancel(context.Background())
		done := make(chan int, 1)
		go func() { done <- execute(ctx, []string{"-discovery-pilot-config", configPath}, io.Discard, d) }()
		select {
		case <-r.listened:
		case <-time.After(3 * time.Second):
			stop()
			t.Fatal("discovery did not listen")
		}
		status, _ := roundTrip(t, r.listener, "GET /tether-agent/v1/register HTTP/1.1\r\nHost: example.invalid\r\nConnection: close\r\n\r\n")
		if status != 404 {
			t.Fatal("registration routed", status)
		}
		stop()
		select {
		case code := <-done:
			if code != 0 {
				t.Fatal("cleanup failed", code)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cleanup did not join")
		}
		if strings.Contains(r.trace(), "load") {
			t.Fatal("signer loader reached")
		}
	}
}

func TestDiscoveryEntryRejectsMixedFlagsAndOrdinaryDocuments(t *testing.T) {
	for _, args := range [][]string{
		{"-discovery-pilot-config"}, {"-discovery-pilot-config", ""},
		{"-discovery-pilot-config", configPath, "-config", configPath},
		{"-config", configPath, "-discovery-pilot-config", configPath},
	} {
		r := newRecorder(t, encode(t, discoveryDoc()))
		if execute(context.Background(), args, io.Discard, r.deps()) != 2 || r.trace() != "" {
			t.Fatal("mixed args reached native dependency")
		}
	}
	for _, flag := range []string{"-config", "-pilot-config", "-discovery-pilot-config"} {
		doc := discoveryDoc()
		if flag == "-discovery-pilot-config" {
			doc = localDoc(t)
		}
		r := newRecorder(t, encode(t, doc))
		if execute(context.Background(), []string{flag, configPath}, io.Discard, r.deps()) != 2 || strings.Contains(r.trace(), "load") || strings.Contains(r.trace(), "listen") {
			t.Fatal("mode fallback")
		}
	}
}

func TestDiscoveryDeadlinePrecedesReadAndNeverListensLate(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer stop()
	deadline, _ := ctx.Deadline()
	reads := 0
	d := deps{
		readTrust: func(read context.Context, _ string) ([]byte, error) {
			reads++
			bound, ok := read.Deadline()
			if !ok || bound.After(deadline) {
				t.Error("read lost entry deadline")
			}
			<-read.Done()
			return encode(t, discoveryDoc()), nil // even a late successful read refuses
		},
		listen: func(string, string) (net.Listener, error) {
			t.Error("late listen")
			return nil, errors.New("not allowed")
		},
	}
	if code := execute(ctx, []string{"-discovery-pilot-config", configPath}, io.Discard, d); code != 1 || reads != 1 {
		t.Fatal(code, reads)
	}
	if discoveryMaxLifetime != 5*time.Minute {
		t.Fatal("fixed lifetime changed")
	}
	if code := execute(ctx, []string{"-discovery-pilot-config", configPath}, io.Discard, d); code != 2 || reads != 1 {
		t.Fatal("expired parent read configuration")
	}
}

func TestDiscoveryRoutesExactAuthMetadataAndExpiry(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		ctx, stop := context.WithCancel(context.Background())
		calls := 0
		h := discoveryRoutes(ctx, "/mcp", metadata, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(204) }))
		for _, path := range []string{"/agent-register", "/tether-agent/v1/register", "/mcp/", "/mcp?", "/mcp?token=x", "/%6dcp", "/.well-known/oauth-protected-resource/other"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
			if w.Code != 404 {
				t.Fatal(path, w.Code)
			}
		}
		if calls != 0 {
			t.Fatal("unexpected route")
		}
		for _, path := range []string{"/mcp", "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			want := 204
			if path != "/mcp" && !metadata {
				want = 404
			}
			if w.Code != want {
				t.Fatal(path, w.Code)
			}
		}
		stop()
		before := calls
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/mcp", nil))
		if w.Code != 503 || calls != before {
			t.Fatal("expired admission")
		}
	}
}

func TestDiscoveryListenFailureClosesReturnedListener(t *testing.T) {
	ln := newFakeListener()
	d := deps{
		readTrust: func(context.Context, string) ([]byte, error) { return encode(t, discoveryDoc()), nil },
		listen:    func(string, string) (net.Listener, error) { return ln, errors.New("synthetic failure") },
	}
	if code := execute(context.Background(), []string{"-discovery-pilot-config", configPath}, io.Discard, d); code != 1 {
		t.Fatal(code)
	}
	select {
	case <-ln.closed:
	default:
		t.Fatal("partial listener not closed")
	}
}
