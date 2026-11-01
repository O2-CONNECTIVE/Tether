package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// WebSocket tests run the production http.Server over net.Pipe connections;
// every frame is a real coder/websocket frame, but no socket is opened.

func readCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func echo(t *testing.T, ws *websocket.Conn, typ websocket.MessageType, msg []byte) {
	t.Helper()
	ctx := readCtx(t)
	if err := ws.Write(ctx, typ, msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, data, err := ws.Read(ctx)
	if err != nil || got != typ || !bytes.Equal(data, msg) {
		t.Fatalf("echo = %v %q, %v", got, data, err)
	}
}

// A verified, allowlisted subject gets the fixed subprotocol and an echo that
// preserves text and binary; a client close ends the server side.
func TestWebSocketEchoPreservesTypeAndSubprotocol(t *testing.T) {
	p := newTestProbe(t, testLimits())
	ln := serveInMemory(t, p)
	ws, resp, err := dialWS(t, ln, assertion(t, subject1), Subprotocol)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols || ws.Subprotocol() != Subprotocol {
		t.Fatalf("dial = %v, %v", resp, err)
	}
	echo(t, ws, websocket.MessageText, []byte("synthetic-text"))
	echo(t, ws, websocket.MessageBinary, []byte{0, 1, 2, 0xff})
	echo(t, ws, websocket.MessageText, []byte{})
	if err := ws.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	eventually(t, "server connection end", func() bool { return slotsInUse(p) == 0 })
}

// The server's own short read/write timeouts must not end an upgraded
// connection early: after the upgrade only the fixed probe lifetime applies.
func TestWebSocketOutlivesServerTimeouts(t *testing.T) {
	l := testLimits()
	l.Lifetime = 3 * time.Second
	p := newTestProbe(t, l)
	ln := serveWith(t, p, func(s *http.Server) {
		s.ReadTimeout, s.WriteTimeout = 100*time.Millisecond, 100*time.Millisecond
	})
	ws, _, err := dialWS(t, ln, assertion(t, subject1), Subprotocol)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	echo(t, ws, websocket.MessageText, []byte("before the server timeouts"))
	time.Sleep(400 * time.Millisecond)
	echo(t, ws, websocket.MessageBinary, []byte{9, 8, 7})
	echo(t, ws, websocket.MessageText, []byte("after the server timeouts"))
	if err := ws.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	eventually(t, "server connection end", func() bool { return slotsInUse(p) == 0 })
}

// Every refusal is an HTTP status before the upgrade: no WebSocket exists
// without a verified allowlisted subject and the exact subprotocol offer.
func TestWebSocketRefusesBeforeUpgrade(t *testing.T) {
	p := newTestProbe(t, testLimits())
	ln := serveInMemory(t, p)
	expired := claims(subject1)
	past := time.Now().Add(-11 * time.Minute)
	expired["iat"], expired["nbf"], expired["exp"] = past.Unix(), past.Unix(), time.Now().Add(-time.Minute).Unix()
	for _, tc := range []struct {
		name      string
		token     string
		protocols []string
		status    int
	}{
		{"unauthenticated", "", []string{Subprotocol}, http.StatusForbidden},
		{"forged key", signWith(t, claims(subject1), testKey(t, 1)), []string{Subprotocol}, http.StatusForbidden},
		{"valid signature, subject not allowlisted", assertion(t, outsider), []string{Subprotocol}, http.StatusForbidden},
		{"expired", signWith(t, expired, testKey(t, 0)), []string{Subprotocol}, http.StatusForbidden},
		{"no subprotocol", assertion(t, subject1), nil, http.StatusBadRequest},
		{"other subprotocol", assertion(t, subject1), []string{"tether-agent.v1"}, http.StatusBadRequest},
		{"extra subprotocol offer", assertion(t, subject1), []string{Subprotocol, "other"}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, resp, err := dialWS(t, ln, tc.token, tc.protocols...)
			if err == nil {
				_ = ws.CloseNow()
				t.Fatal("upgraded")
			}
			if resp == nil || resp.StatusCode != tc.status {
				t.Fatalf("response = %v, %v", resp, err)
			}
		})
	}
	if slotsInUse(p) != 0 {
		t.Fatal("a refused upgrade held a slot")
	}
}

func closeStatus(t *testing.T, ws *websocket.Conn) websocket.StatusCode {
	t.Helper()
	_, _, err := ws.Read(readCtx(t))
	if err == nil {
		t.Fatal("read succeeded after the server should have closed")
	}
	return websocket.CloseStatus(err)
}

func TestWebSocketMessageTooBigCloses(t *testing.T) {
	p := newTestProbe(t, testLimits())
	ws, _, err := dialWS(t, serveInMemory(t, p), assertion(t, subject1), Subprotocol)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	echo(t, ws, websocket.MessageBinary, bytes.Repeat([]byte{7}, 64))
	if err := ws.Write(readCtx(t), websocket.MessageBinary, bytes.Repeat([]byte{7}, 65)); err != nil {
		t.Fatal(err)
	}
	if got := closeStatus(t, ws); got != websocket.StatusMessageTooBig {
		t.Fatalf("close status = %v", got)
	}
	eventually(t, "server connection end", func() bool { return slotsInUse(p) == 0 })
}

func TestWebSocketFrameBudgetCloses(t *testing.T) {
	l := testLimits()
	l.Frames = 2
	p := newTestProbe(t, l)
	ws, _, err := dialWS(t, serveInMemory(t, p), assertion(t, subject1), Subprotocol)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	echo(t, ws, websocket.MessageText, []byte("one"))
	echo(t, ws, websocket.MessageText, []byte("two"))
	if got := closeStatus(t, ws); got != websocket.StatusNormalClosure {
		t.Fatalf("close status = %v", got)
	}
	eventually(t, "server connection end", func() bool { return slotsInUse(p) == 0 })
}

// An idle connection ends at the lifetime.
func TestWebSocketIdleLifetimeEnds(t *testing.T) {
	l := testLimits()
	l.Lifetime = 200 * time.Millisecond
	p := newTestProbe(t, l)
	ws, _, err := dialWS(t, serveInMemory(t, p), assertion(t, subject1), Subprotocol)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	start := time.Now()
	_, _, err = ws.Read(readCtx(t))
	if err == nil || errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 4*time.Second {
		t.Fatalf("idle read = %v after %v", err, time.Since(start))
	}
	eventually(t, "server connection end", func() bool { return slotsInUse(p) == 0 })
}

// Traffic never extends the lifetime: a connection kept busy still ends.
func TestWebSocketLifetimeIsNotExtended(t *testing.T) {
	l := testLimits()
	l.Lifetime, l.Frames = 300*time.Millisecond, 1000
	p := newTestProbe(t, l)
	ws, _, err := dialWS(t, serveInMemory(t, p), assertion(t, subject1), Subprotocol)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	start := time.Now()
	echoes := 0
	for time.Since(start) < 4*time.Second {
		ctx := readCtx(t)
		if ws.Write(ctx, websocket.MessageText, []byte("busy")) != nil {
			break
		}
		if _, _, err := ws.Read(ctx); err != nil {
			break
		}
		echoes++
		time.Sleep(20 * time.Millisecond)
	}
	if elapsed := time.Since(start); echoes == 0 || elapsed >= 4*time.Second {
		t.Fatalf("busy connection: %d echoes, ended after %v", echoes, elapsed)
	}
	eventually(t, "server connection end", func() bool { return slotsInUse(p) == 0 })
}

// Concurrent WebSockets are bounded without queueing; a closed slot is reused.
func TestWebSocketConcurrencyBound(t *testing.T) {
	l := testLimits()
	l.Conns = 1
	p := newTestProbe(t, l)
	ln := serveInMemory(t, p)
	token := assertion(t, subject1)
	first, _, err := dialWS(t, ln, token, Subprotocol)
	if err != nil {
		t.Fatal(err)
	}
	if ws, resp, err := dialWS(t, ln, token, Subprotocol); err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		if ws != nil {
			_ = ws.CloseNow()
		}
		t.Fatalf("second concurrent WebSocket = %v, %v", resp, err)
	}
	if err := first.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "slot release", func() bool { return slotsInUse(p) == 0 })
	again, _, err := dialWS(t, ln, token, Subprotocol)
	if err != nil {
		t.Fatal(err)
	}
	_ = again.Close(websocket.StatusNormalClosure, "")
}

// Close ends upgraded connections, which the HTTP server does not track, and
// refuses new work.
func TestCloseEndsWebSockets(t *testing.T) {
	p := newTestProbe(t, testLimits())
	ln := serveInMemory(t, p)
	ws, _, err := dialWS(t, ln, assertion(t, subject1), Subprotocol)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Close(ctx); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if _, _, err := ws.Read(readCtx(t)); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("read after Close = %v", err)
	}
	if rec := post(p, assertion(t, subject1), "x", nil); rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "echo_base64") {
		t.Fatalf("HTTP after Close = %d", rec.Code)
	}
	if ws, resp, err := dialWS(t, ln, assertion(t, subject1), Subprotocol); err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		if ws != nil {
			_ = ws.CloseNow()
		}
		t.Fatalf("WebSocket after Close = %v, %v", resp, err)
	}
}
