package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

// These helpers exercise execute and its concrete handlers over net.Pipe.
// Trust/key reads are synthetic; no TLS, native file, listener or process is
// created. The peer verifies the existing wire transcript independently.
func serveResidentEntry(t *testing.T, doc map[string]any) *fakeListener {
	t.Helper()
	r := newRecorder(t, encode(t, doc))
	r.listener = newFakeListener()
	ctx, cancel := context.WithCancel(context.Background())
	var diagnostic bytes.Buffer
	exited := make(chan int, 1)
	go func() { exited <- execute(ctx, []string{"-config", configPath}, &diagnostic, r.deps()) }()
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-exited:
			if code != 0 {
				t.Errorf("entry shutdown exit=%d", code)
			}
			checkDiagnostic(t, diagnostic.String())
		case <-time.After(20 * time.Second):
			t.Error("entry did not join")
		}
	})
	select {
	case <-r.listened:
	case <-time.After(5 * time.Second):
		t.Fatal("entry never listened")
	}
	return r.listener
}

func residentAuth(t *testing.T, access bool, subject string) string {
	t.Helper()
	if access {
		return "Cf-Access-Jwt-Assertion: " + accessAssertion(t, subject, accessKey(t, 0)) + "\r\n"
	}
	enc := base64.RawURLEncoding
	h := encode(t, map[string]any{"alg": "EdDSA", "kid": "synthetic-ed", "typ": "at+jwt"})
	c := encode(t, map[string]any{"iss": "https://issuer.example.invalid/", "aud": "https://gateway.example.invalid/mcp", "sub": subject, "scope": "rescue.agent", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()})
	input := enc.EncodeToString(h) + "." + enc.EncodeToString(c)
	return "Authorization: Bearer " + input + "." + enc.EncodeToString(ed25519.Sign(seedKey(0x51), []byte(input))) + "\r\n"
}

func residentRPC(t *testing.T, l *fakeListener, auth, sid, method string, params any) (int, http.Header, string) {
	t.Helper()
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if method != "notifications/initialized" {
		m["id"] = 1
	}
	if params != nil {
		m["params"] = params
	}
	body := encode(t, m)
	header := auth
	if sid != "" {
		header += "Mcp-Session-Id: " + sid + "\r\nMcp-Protocol-Version: 2025-06-18\r\n"
	}
	return exchange(t, l, "POST /mcp HTTP/1.1\r\nHost: gateway.example.invalid\r\nConnection: close\r\n"+header+"Content-Type: application/json\r\nAccept: application/json\r\nContent-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"+string(body))
}

func residentInitialize(t *testing.T, l *fakeListener, auth string) string {
	t.Helper()
	code, h, _ := residentRPC(t, l, auth, "", "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "fixture", "version": "1"}})
	sid := h.Get(agentgateway.HeaderSession)
	if code != 200 || sid == "" {
		t.Fatal("initialize failed", code)
	}
	if code, _, _ := residentRPC(t, l, auth, sid, "notifications/initialized", nil); code != 202 {
		t.Fatal("initialized failed", code)
	}
	return sid
}

func residentDelete(t *testing.T, l *fakeListener, auth, sid string) {
	t.Helper()
	code, _ := roundTrip(t, l, "DELETE /mcp HTTP/1.1\r\nHost: gateway.example.invalid\r\nConnection: close\r\n"+auth+"Mcp-Session-Id: "+sid+"\r\nMcp-Protocol-Version: 2025-06-18\r\n\r\n")
	if code != 204 {
		t.Fatal("delete failed", code)
	}
}

func residentPeer(t *testing.T, l *fakeListener) (*websocket.Conn, func(string, ...string) []byte) {
	t.Helper()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
		if addr != "gateway.example.invalid:80" {
			t.Error("non-fixture dial refused")
			return nil, net.ErrClosed
		}
		client, server := net.Pipe()
		select {
		case l.conns <- server:
			return client, nil
		case <-ctx.Done():
			_ = client.Close()
			_ = server.Close()
			return nil, ctx.Err()
		}
	}}
	t.Cleanup(tr.CloseIdleConnections)
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	ws, _, err := websocket.Dial(ctx, "ws://gateway.example.invalid/agent-register", &websocket.DialOptions{HTTPClient: &http.Client{Transport: tr}, Subprotocols: []string{agenttransport.ResidentSubprotocol}})
	if err != nil {
		t.Fatal("resident upgrade refused", err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	enc := base64.RawURLEncoding
	nonce := bytes.Repeat([]byte{7}, 32)
	residentWriteMessage(t, ws, map[string]string{"type": "resident_hello", "host": "mac-1", "principal": "member-1", "client_nonce": enc.EncodeToString(nonce)})
	challenge := residentReadMessage(t, ws)
	server, err := enc.DecodeString(challenge["server_nonce"])
	if err != nil || len(server) != 32 || len(challenge["generation"]) != 64 || challenge["type"] != "resident_challenge" {
		t.Fatal("invalid challenge")
	}
	transcript := func(role string, values ...string) []byte {
		var out []byte
		field := func(v []byte) { out = binary.BigEndian.AppendUint32(out, uint32(len(v))); out = append(out, v...) }
		for _, s := range []string{"rescue-host.agenttransport.resident\x00v1\x00", role, "wss://gateway.example.invalid/agent-register", "mac-1", "member-1", challenge["generation"]} {
			field([]byte(s))
		}
		for _, b := range [][]byte{nonce, server, publicOf(t, 0x21).Marshal(), publicOf(t, 0x11).Marshal()} {
			field(b)
		}
		for _, s := range values {
			field([]byte(s))
		}
		return out
	}
	residentVerifyMessage(t, challenge, transcript("gateway-challenge"))
	sig, err := signerOf(t, 0x21).Sign(nil, transcript("host-proof"))
	if err != nil {
		t.Fatal(err)
	}
	residentWriteMessage(t, ws, map[string]string{"type": "resident_proof", "signature": enc.EncodeToString(ssh.Marshal(sig))})
	accepted := residentReadMessage(t, ws)
	if accepted["type"] != "resident_accepted" {
		t.Fatal("resident proof refused")
	}
	residentVerifyMessage(t, accepted, transcript("gateway-accepted"))
	return ws, transcript
}

func residentReadMessage(t *testing.T, ws *websocket.Conn) map[string]string {
	t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	kind, raw, err := ws.Read(ctx)
	var m map[string]string
	if err != nil || kind != websocket.MessageText || json.Unmarshal(raw, &m) != nil {
		t.Fatal("resident message unavailable", err)
	}
	return m
}

func residentWriteMessage(t *testing.T, ws *websocket.Conn, m map[string]string) {
	t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := ws.Write(ctx, websocket.MessageText, encode(t, m)); err != nil {
		t.Fatal(err)
	}
}

func residentVerifyMessage(t *testing.T, m map[string]string, transcript []byte) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(m["signature"])
	var sig ssh.Signature
	if err != nil || ssh.Unmarshal(raw, &sig) != nil || sig.Format != ssh.KeyAlgoED25519 || len(sig.Rest) != 0 || publicOf(t, 0x11).Verify(transcript, &sig) != nil {
		t.Fatal("gateway signature not pinned")
	}
}

func residentTool(t *testing.T, l *fakeListener, auth, sid, name string, args map[string]any) (int, string) {
	t.Helper()
	code, _, body := residentRPC(t, l, auth, sid, "tools/call", map[string]any{"name": name, "arguments": args})
	return code, body
}

func residentAwaitSession(t *testing.T, l *fakeListener, auth string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sid := residentInitialize(t, l, auth)
		code, _, body := residentRPC(t, l, auth, sid, "tools/list", nil)
		if code == 200 && strings.Contains(body, "tether_fixed_start") && !strings.Contains(body, `"name":"rescue_start"`) {
			return sid
		}
		residentDelete(t, l, auth, sid)
		time.Sleep(time.Millisecond)
	}
	t.Fatal("concrete entry never composed resident MCP")
	return ""
}
