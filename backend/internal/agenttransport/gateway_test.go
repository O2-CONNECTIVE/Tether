package agenttransport

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"golang.org/x/crypto/ssh"
)

// Handler-level tests: a hand-written WebSocket client sends real frames to
// the real Handler over a memory conn. The registrar here only records what
// the Handler hands over (or that it handed nothing); the composed path with
// the real gateway and Mac host is in mac_test.go.

const member3 = "member-3"

type gatewayFixture struct {
	host1, host2, attacker ssh.Signer
	rec                    *recordingRegistrar
	h                      *Handler
}

func newGatewayFixture(t *testing.T, inner Registrar, budgets Budgets) *gatewayFixture {
	t.Helper()
	f := &gatewayFixture{host1: fixtureSigner(t, 0x11), host2: fixtureSigner(t, 0x12), attacker: fixtureSigner(t, 0x31)}
	f.rec = newRecorder(inner)
	f.h = newHandler(t, f.rec, budgets,
		EnrolledHost{Label: hostLabel, PublicKey: f.host1.PublicKey(), Principals: []string{member1, member2}},
		EnrolledHost{Label: otherHost, PublicKey: f.host2.PublicKey(), Principals: []string{member3}})
	return f
}

func (f *gatewayFixture) dispatch(launch string) agentgateway.Dispatch {
	return fixtureDispatch(f.host1, launch, time.Now().Add(time.Minute))
}

func TestNewHandlerRefusesUntrustedConfiguration(t *testing.T) {
	host1, host2 := fixtureSigner(t, 0x11), fixtureSigner(t, 0x12)
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecdsaPub, err := ssh.NewPublicKey(&ecdsaKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	good := func() HandlerConfig {
		return HandlerConfig{Endpoint: testEndpoint, Registrar: newRecorder(nil), Budgets: testBudgets(),
			Hosts: []EnrolledHost{{Label: hostLabel, PublicKey: host1.PublicKey(), Principals: []string{member1}}}}
	}
	five := func() (hs []EnrolledHost) {
		for i := range 5 {
			hs = append(hs, EnrolledHost{Label: "mac-" + string(rune('1'+i)), PublicKey: fixtureSigner(t, byte(0x40+i)).PublicKey(), Principals: []string{member1}})
		}
		return hs
	}
	for _, tc := range []struct {
		name string
		edit func(*HandlerConfig)
	}{
		{"plaintext ws endpoint", func(c *HandlerConfig) { c.Endpoint = "ws://gateway.example.invalid/agent/register" }},
		{"https endpoint", func(c *HandlerConfig) { c.Endpoint = "https://gateway.example.invalid/agent/register" }},
		{"endpoint with query", func(c *HandlerConfig) { c.Endpoint = testEndpoint + "?host=x" }},
		{"no registrar", func(c *HandlerConfig) { c.Registrar = nil }},
		{"no hosts", func(c *HandlerConfig) { c.Hosts = nil }},
		{"five hosts", func(c *HandlerConfig) { c.Hosts = five() }},
		{"duplicate label", func(c *HandlerConfig) {
			c.Hosts = append(c.Hosts, EnrolledHost{Label: hostLabel, PublicKey: host2.PublicKey(), Principals: []string{member1}})
		}},
		{"same key for two hosts", func(c *HandlerConfig) {
			c.Hosts = append(c.Hosts, EnrolledHost{Label: otherHost, PublicKey: host1.PublicKey(), Principals: []string{member1}})
		}},
		{"non-Ed25519 key", func(c *HandlerConfig) { c.Hosts[0].PublicKey = ecdsaPub }},
		{"no key", func(c *HandlerConfig) { c.Hosts[0].PublicKey = nil }},
		{"bad host label", func(c *HandlerConfig) { c.Hosts[0].Label = "Mac 1" }},
		{"no principals", func(c *HandlerConfig) { c.Hosts[0].Principals = nil }},
		{"bad principal label", func(c *HandlerConfig) { c.Hosts[0].Principals = []string{"Member-1"} }},
		{"duplicate principal", func(c *HandlerConfig) { c.Hosts[0].Principals = []string{member1, member1} }},
		{"zero budgets", func(c *HandlerConfig) { c.Budgets = Budgets{} }},
		{"approval not above registration", func(c *HandlerConfig) { c.Budgets.Approval = c.Budgets.Registration }},
		{"registration too long", func(c *HandlerConfig) {
			c.Budgets = Budgets{Registration: MaxRegistration + 1, Approval: MaxApproval, Cleanup: time.Second}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := good()
			tc.edit(&cfg)
			if _, err := NewHandler(cfg); !errors.Is(err, ErrConfig) {
				t.Fatalf("NewHandler = %v", err)
			}
		})
	}
}

// Requests that are not exact, credential-free registrations are refused
// before any upgrade or hijack, and nothing reaches the registrar.
func TestRequestsAreRefusedBeforeUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*http.Request)
		status int
	}{
		{"POST", func(r *http.Request) { r.Method = http.MethodPost }, http.StatusMethodNotAllowed},
		{"other path", func(r *http.Request) { r.URL.Path = "/agent/other" }, http.StatusNotFound},
		{"query", func(r *http.Request) { r.URL.RawQuery = "host=mac-1" }, http.StatusBadRequest},
		{"browser Origin", func(r *http.Request) { r.Header.Set("Origin", "https://gateway.example.invalid") }, http.StatusForbidden},
		{"ambient cookie", func(r *http.Request) { r.Header.Set("Cookie", "session=x") }, http.StatusForbidden},
		{"bearer token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer x") }, http.StatusForbidden},
		{"proxy credential", func(r *http.Request) { r.Header.Set("Proxy-Authorization", "Basic eA==") }, http.StatusForbidden},
		{"no subprotocol", func(r *http.Request) { r.Header.Del("Sec-WebSocket-Protocol") }, http.StatusBadRequest},
		{"extra subprotocol", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Protocol", Subprotocol+", chat") }, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGatewayFixture(t, nil, testBudgets())
			r, resp := upgrade(t, f.h, tc.edit)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
			<-r.served
			if f.rec.count.Load() != 0 || !r.server.isClosed() {
				t.Fatal("a refused request reached the registrar or kept its conn")
			}
		})
	}
}

// The valid proof hands exactly the signed dispatch and principal label to
// the registrar, and the stream is binary-only, chunked and bounded.
func TestValidRegistrationHandsOverTheSignedDispatchAsABinaryStream(t *testing.T) {
	f := newGatewayFixture(t, nil, testBudgets())
	r := upgraded(t, f.h)
	d := f.dispatch("launch-1")
	r.text(signedRegister(t, testEndpoint, hostLabel, member1, d, r.challenge(), f.host1))
	r.expectAccepted()
	call := f.rec.next(t)
	if call.label != member1 || !sameDispatch(call.d, d) {
		t.Fatalf("registrar got %q %+v", call.label, call.d)
	}
	conn := call.conn

	// Gateway to Mac: one write larger than a message arrives as bounded
	// binary messages with the same bytes.
	payload := bytes.Repeat([]byte("0123456789abcdef"), 70000/16)
	go func() { _, _ = conn.Write(payload) }()
	var got []byte
	for len(got) < len(payload) {
		op, p, err := r.next(2 * time.Second)
		if err != nil || op != opBinary || len(p) > MaxStreamMessage || len(p) == 0 {
			t.Fatalf("stream frame: op %d len %d err %v", op, len(p), err)
		}
		got = append(got, p...)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("stream bytes differ")
	}

	// Mac to gateway: a binary message is delivered as bytes.
	if err := r.send(true, opBinary, []byte("ssh-bytes")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 9)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ssh-bytes" {
		t.Fatalf("read %q %v", buf, err)
	}

	// A message above the restored read limit ends the stream and is never
	// delivered whole.
	big := bytes.Repeat([]byte{7}, MaxStreamMessage+1024)
	if err := r.send(true, opBinary, big); err != nil {
		t.Fatal(err)
	}
	if delivered := endsWithin(t, conn, 8*time.Second); delivered >= len(big) {
		t.Fatalf("oversized message delivered whole (%d bytes)", delivered)
	}
	if f.rec.count.Load() != 1 {
		t.Fatal("registrar called more than once")
	}
}

// drain reads until an error. Its own read deadline is longer than any
// budget under test, so callers distinguish an ended stream by elapsed time.
func drain(conn net.Conn) (int, error) {
	total := 0
	buf := make([]byte, 4096)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		n, err := conn.Read(buf)
		total += n
		if err != nil {
			return total, err
		}
	}
}

// endsWithin requires the stream to end (not merely time out) within d and
// returns the bytes delivered before the end.
func endsWithin(t *testing.T, conn net.Conn, d time.Duration) int {
	t.Helper()
	start := time.Now()
	n, err := drain(conn)
	if err == nil || time.Since(start) > d {
		t.Fatalf("stream did not end within %v (err %v)", d, err)
	}
	return n
}

// A text message after registration is the wrong type for the stream.
func TestTextMessageInStreamModeEndsTheStream(t *testing.T) {
	f := newGatewayFixture(t, nil, testBudgets())
	r := upgraded(t, f.h)
	r.text(signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch("launch-1"), r.challenge(), f.host1))
	r.expectAccepted()
	conn := f.rec.next(t).conn
	r.text([]byte(`{"type":"register"}`))
	// The stream refuses a non-binary message before delivering any byte and
	// starts its owner's close.
	if n := endsWithin(t, conn, 8*time.Second); n != 0 {
		t.Fatalf("text in stream mode delivered %d bytes", n)
	}
}

// Every forged, stale, swapped or malformed proof is refused on its own
// stream and nothing reaches the registrar.
func TestForgedStaleOrMalformedProofsAreRefused(t *testing.T) {
	type sendFunc func(t *testing.T, f *gatewayFixture, r *rawWS, nonce []byte)
	text := func(build func(t *testing.T, f *gatewayFixture, nonce []byte) []byte) sendFunc {
		return func(t *testing.T, f *gatewayFixture, r *rawWS, nonce []byte) { r.text(build(t, f, nonce)) }
	}
	now := time.Now()
	for _, tc := range []struct {
		name string
		send sendFunc
	}{
		{"unknown host label", text(func(t *testing.T, f *gatewayFixture, n []byte) []byte {
			return signedRegister(t, testEndpoint, "mac-9", member1, f.dispatch("l"), n, f.host1)
		})},
		{"self-enrolled key", text(func(t *testing.T, f *gatewayFixture, n []byte) []byte {
			return signedRegister(t, testEndpoint, hostLabel, member1, fixtureDispatch(f.attacker, "l", now.Add(time.Minute)), n, f.attacker)
		})},
		{"another enrolled host's key", text(func(t *testing.T, f *gatewayFixture, n []byte) []byte {
			return signedRegister(t, testEndpoint, hostLabel, member1, fixtureDispatch(f.host2, "l", now.Add(time.Minute)), n, f.host2)
		})},
		{"cross-host swap", text(func(t *testing.T, f *gatewayFixture, n []byte) []byte {
			d := fixtureDispatch(f.host2, "l", now.Add(time.Minute))
			sig, _ := sign(f.host2, f.host2.PublicKey(), transcript(testEndpoint, otherHost, member3, d, n))
			return encodeRegister(hostLabel, member3, d, sig)
		})},
		{"principal not associated with host", text(func(t *testing.T, f *gatewayFixture, n []byte) []byte {
			return signedRegister(t, testEndpoint, hostLabel, member3, f.dispatch("l"), n, f.host1)
		})},
		{"principal swapped after signing", text(func(t *testing.T, f *gatewayFixture, n []byte) []byte {
			d := f.dispatch("l")
			sig, _ := sign(f.host1, f.host1.PublicKey(), transcript(testEndpoint, hostLabel, member1, d, n))
			return encodeRegister(hostLabel, member2, d, sig)
		})},
		{"other resource identity", text(func(t *testing.T, f *gatewayFixture, n []byte) []byte {
			return signedRegister(t, "wss://other.example.invalid/agent/register", hostLabel, member1, f.dispatch("l"), n, f.host1)
		})},
		{"launch changed after signing", text(func(t *testing.T, f *gatewayFixture, n []byte) []byte {
			d := f.dispatch("l")
			sig, _ := sign(f.host1, f.host1.PublicKey(), transcript(testEndpoint, hostLabel, member1, d, n))
			d.Binding.Grant.Identity.LaunchID = "other"
			return encodeRegister(hostLabel, member1, d, sig)
		})},
		{"deadline changed after signing", text(func(t *testing.T, f *gatewayFixture, n []byte) []byte {
			d := f.dispatch("l")
			sig, _ := sign(f.host1, f.host1.PublicKey(), transcript(testEndpoint, hostLabel, member1, d, n))
			d.Deadline = d.Deadline.Add(time.Hour)
			return encodeRegister(hostLabel, member1, d, sig)
		})},
		{"expired dispatch", text(func(t *testing.T, f *gatewayFixture, n []byte) []byte {
			return signedRegister(t, testEndpoint, hostLabel, member1, fixtureDispatch(f.host1, "l", now.Add(-time.Second)), n, f.host1)
		})},
		{"dispatch beyond 24 hours", text(func(t *testing.T, f *gatewayFixture, n []byte) []byte {
			return signedRegister(t, testEndpoint, hostLabel, member1, fixtureDispatch(f.host1, "l", now.Add(25*time.Hour)), n, f.host1)
		})},
		{"Windows policy", text(func(t *testing.T, f *gatewayFixture, n []byte) []byte {
			d := f.dispatch("l")
			d.Binding.Grant.Identity.PolicyDigest = agentexec.FullPolicyDigest()
			return signedRegister(t, testEndpoint, hostLabel, member1, d, n, f.host1)
		})},
		{"zero nonce", text(func(t *testing.T, f *gatewayFixture, n []byte) []byte {
			return signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch("l"), make([]byte, NonceSize), f.host1)
		})},
		{"unknown member", text(func(t *testing.T, f *gatewayFixture, n []byte) []byte {
			msg := signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch("l"), n, f.host1)
			return append(msg[:len(msg)-1], []byte(`,"origin":"x"}`)...)
		})},
		{"duplicate member", text(func(t *testing.T, f *gatewayFixture, n []byte) []byte {
			msg := signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch("l"), n, f.host1)
			return append(msg[:len(msg)-1], []byte(`,"host":"mac-1"}`)...)
		})},
		{"binary registration", func(t *testing.T, f *gatewayFixture, r *rawWS, n []byte) {
			_ = r.send(true, opBinary, signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch("l"), n, f.host1))
		}},
		// The next two are valid proofs padded with JSON whitespace, so only
		// the message bound can refuse them.
		{"oversized registration", func(t *testing.T, f *gatewayFixture, r *rawWS, n []byte) {
			_ = r.send(true, opText, padded(signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch("l"), n, f.host1)))
		}},
		{"fragments beyond the bound", func(t *testing.T, f *gatewayFixture, r *rawWS, n []byte) {
			msg := padded(signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch("l"), n, f.host1))
			_ = r.send(false, opText, msg[:len(msg)/2])
			_ = r.send(true, opContinuation, msg[len(msg)/2:])
		}},
		{"close instead of registration", func(t *testing.T, f *gatewayFixture, r *rawWS, n []byte) {
			_ = r.send(true, opClose, []byte{0x03, 0xe8})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGatewayFixture(t, nil, testBudgets())
			r := upgraded(t, f.h)
			tc.send(t, f, r, r.challenge())
			r.expectRefused(3 * time.Second)
			if f.rec.count.Load() != 0 {
				t.Fatal("a refused proof reached the registrar")
			}
		})
	}
}

// padded appends whitespace (valid around a JSON value) past the bound.
func padded(msg []byte) []byte {
	return append(msg, bytes.Repeat([]byte(" "), MaxRegistrationMessage+64-len(msg))...)
}

// A proof made for one stream's nonce is refused on another stream; the
// original stream can still use it once.
func TestProofIsBoundToItsOwnStream(t *testing.T) {
	f := newGatewayFixture(t, nil, testBudgets())
	a, b := upgraded(t, f.h), upgraded(t, f.h)
	nonceA, nonceB := a.challenge(), b.challenge()
	if bytes.Equal(nonceA, nonceB) {
		t.Fatal("two connections got the same nonce")
	}
	proof := signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch("launch-1"), nonceA, f.host1)
	b.text(proof)
	b.expectRefused(3 * time.Second)
	a.text(proof)
	a.expectAccepted()
	f.rec.next(t)
	if f.rec.count.Load() != 1 {
		t.Fatal("swapped proof reached the registrar")
	}
}

// A captured registration replayed on a new connection is refused.
func TestReplayedRegistrationIsRefused(t *testing.T) {
	f := newGatewayFixture(t, nil, testBudgets())
	first := upgraded(t, f.h)
	proof := signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch("launch-1"), first.challenge(), f.host1)
	first.text(proof)
	first.expectAccepted()
	f.rec.next(t)
	replay := upgraded(t, f.h)
	replay.challenge()
	replay.text(proof)
	replay.expectRefused(3 * time.Second)
	if f.rec.count.Load() != 1 {
		t.Fatal("replay reached the registrar")
	}
}

// Fragmentation is bounded by the message, not by frames: a valid proof in
// two fragments within the bound is accepted.
func TestFragmentedRegistrationWithinTheBound(t *testing.T) {
	f := newGatewayFixture(t, nil, testBudgets())
	r := upgraded(t, f.h)
	proof := signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch("launch-1"), r.challenge(), f.host1)
	half := len(proof) / 2
	if err := r.send(false, opText, proof[:half]); err != nil {
		t.Fatal(err)
	}
	if err := r.send(true, opContinuation, proof[half:]); err != nil {
		t.Fatal(err)
	}
	r.expectAccepted()
	f.rec.next(t)
}

// At most MaxPending registrations run; the next request gets 503 without an
// upgrade. Stalled registrations end within the budget and free their slots.
func TestPendingRegistrationsAreBoundedAndStallsEnd(t *testing.T) {
	f := newGatewayFixture(t, nil, testBudgets())
	start := time.Now()
	var pending []*rawWS
	for range MaxPending {
		r := upgraded(t, f.h)
		r.challenge()
		pending = append(pending, r)
	}
	extra, resp := upgrade(t, f.h, nil)
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("over the pending cap: %d", resp.StatusCode)
	}
	<-extra.served
	for _, r := range pending {
		r.expectRefused(3 * time.Second)
	}
	if elapsed := time.Since(start); elapsed > testBudgets().Registration+time.Second {
		t.Fatalf("stalled registrations held for %v", elapsed)
	}
	// The freed slots accept a new upgrade; nothing reached the registrar.
	upgraded(t, f.h).challenge()
	if f.rec.count.Load() != 0 {
		t.Fatal("registrar reached")
	}
}

// A peer that sends half a frame header and stops is closed at the budget.
func TestPartialFrameStallIsClosed(t *testing.T) {
	f := newGatewayFixture(t, nil, testBudgets())
	r := upgraded(t, f.h)
	r.challenge()
	if _, err := r.conn.Write([]byte{0x81}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r.expectRefused(3 * time.Second)
	if time.Since(start) > testBudgets().Registration+time.Second {
		t.Fatal("partial frame held the registration")
	}
}

// Closing the Handler during a registration ends it, confirms the close and
// refuses later upgrades.
func TestHandlerCloseDuringRegistration(t *testing.T) {
	f := newGatewayFixture(t, nil, Budgets{Registration: 10 * time.Second, Approval: 20 * time.Second, Cleanup: time.Second})
	r := upgraded(t, f.h)
	r.challenge()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.h.Close(ctx); err != nil {
		t.Fatalf("Close = %v", err)
	}
	r.expectRefused(time.Second)
	if _, resp := upgrade(t, f.h, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("after Close: %d", resp.StatusCode)
	}
}

// Closing the Handler also ends the streams it handed over.
func TestHandlerCloseEndsAcceptedStreams(t *testing.T) {
	f := newGatewayFixture(t, nil, testBudgets())
	r := upgraded(t, f.h)
	r.text(signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch("launch-1"), r.challenge(), f.host1))
	r.expectAccepted()
	conn := f.rec.next(t).conn
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.h.Close(ctx); err != nil {
		t.Fatalf("Close = %v", err)
	}
	endsWithin(t, conn, 3*time.Second)
	if !r.server.isClosed() {
		t.Fatal("hijacked conn not closed")
	}
}

type registrarFunc func(string, agentgateway.Dispatch, net.Conn) error

func (f registrarFunc) Connect(l string, d agentgateway.Dispatch, c net.Conn) error {
	return f(l, d, c)
}

// A registrar that refuses closes the same stream; the Handler confirms it.
func TestRefusingRegistrarClosesTheSameStream(t *testing.T) {
	f := newGatewayFixture(t, registrarFunc(func(_ string, _ agentgateway.Dispatch, c net.Conn) error {
		_ = c.Close()
		return agentgateway.ErrRefused
	}), testBudgets())
	r := upgraded(t, f.h)
	r.text(signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch("launch-1"), r.challenge(), f.host1))
	r.expectAccepted()
	if call := f.rec.next(t); !errors.Is(call.err, agentgateway.ErrRefused) {
		t.Fatalf("registrar = %v", call.err)
	}
	r.expectRefused(3 * time.Second)
}

// A registrar that never finishes the channel handshake is cut off by the
// approval budget: the stream closes and the call returns.
func TestApprovalBudgetEndsAStalledConnect(t *testing.T) {
	budgets := Budgets{Registration: 500 * time.Millisecond, Approval: time.Second, Cleanup: time.Second}
	f := newGatewayFixture(t, registrarFunc(func(_ string, _ agentgateway.Dispatch, c net.Conn) error {
		_, err := drain(c) // a peer that never speaks SSH
		return errors.Join(agentgateway.ErrRefused, err)
	}), budgets)
	start := time.Now()
	r := upgraded(t, f.h)
	r.text(signedRegister(t, testEndpoint, hostLabel, member1, f.dispatch("launch-1"), r.challenge(), f.host1))
	r.expectAccepted()
	if call := f.rec.next(t); call.err == nil {
		t.Fatal("stalled Connect returned success")
	}
	r.expectRefused(3 * time.Second)
	if elapsed := time.Since(start); elapsed > budgets.Approval+time.Second {
		t.Fatalf("stalled Connect held %v", elapsed)
	}
}
