package agenttransport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentlink"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
	"golang.org/x/crypto/ssh"
)

// The host's side of a request on its reception (#424): the identity proof is
// verified before anything reaches the state machine, and the answer is signed
// by K. See docs/rescue-connection-start.md, 受付口の wire.

var answerReception = bytes.Repeat([]byte{0x72}, agentgateway.RescueReceptionSize)

// answerRegistry is a gateway with one reception and a correctly signed
// result. The test supplies the request; the host's answer and stream come
// back to the test, which owns the stream.
type answerRegistry struct {
	key                  chan []byte // K as the host proved it
	opened               chan agentgateway.RescueRequest
	request              chan agentgateway.RescueIdentity
	done                 chan struct{}
	once                 sync.Once
	ended                chan []byte
	answered             chan rescueAnswerCall
	disconnected         chan []byte
	observeStream        bool
	preserveOnDisconnect bool
	streamRead           chan struct{}
}

func newAnswerRegistry() *answerRegistry {
	return &answerRegistry{key: make(chan []byte, 1), request: make(chan agentgateway.RescueIdentity, 1), done: make(chan struct{}),
		opened: make(chan agentgateway.RescueRequest, 4), ended: make(chan []byte, 1), answered: make(chan rescueAnswerCall, 1), disconnected: make(chan []byte, 1), streamRead: make(chan struct{}, 1)}
}

func (*answerRegistry) AdmitRescue() error { return nil }
func (r *answerRegistry) OpenRescue(q agentgateway.RescueRequest) (agentgateway.RescueReception, error) {
	r.key <- append([]byte(nil), q.Key...)
	r.opened <- agentgateway.RescueRequest{Key: append([]byte(nil), q.Key...), HostNonce: append([]byte(nil), q.HostNonce...),
		GatewayNonce: append([]byte(nil), q.GatewayNonce...), Purpose: q.Purpose, Reception: append([]byte(nil), q.Reception...), Proof: append([]byte(nil), q.Proof...)}
	sig, err := rescueIdentity.Sign(rand.Reader, agentgateway.RescueResultTranscript(rescueEndpoint, q.HostNonce, q.Key, answerReception, "123456789012"))
	if err != nil {
		return agentgateway.RescueReception{}, err
	}
	return agentgateway.RescueReception{ID: answerReception, AttemptID: 1, Code: "123456789012", Signature: sig.Blob, Request: r.request, Done: r.done}, nil
}
func (r *answerRegistry) EndRescue(id []byte) {
	select {
	case r.ended <- append([]byte(nil), id...):
	default:
	}
	r.end()
}
func (r *answerRegistry) EndRescueAttempt(id []byte, _ uint64) { r.EndRescue(id) }
func (r *answerRegistry) DisconnectRescue(id []byte, _ uint64) {
	if r.preserveOnDisconnect {
		select {
		case r.disconnected <- append([]byte(nil), id...):
		default:
		}
		return
	}
	r.EndRescue(id)
}
func (*answerRegistry) TouchRescue([]byte, uint64) {}
func (r *answerRegistry) end()                     { r.once.Do(func() { close(r.done) }) }
func (r *answerRegistry) AnswerRescue(id []byte, _ uint64, a agentgateway.RescueAnswer, conn net.Conn, _ func()) error {
	r.answered <- rescueAnswerCall{id: append([]byte(nil), id...), a: a, conn: conn}
	if r.observeStream {
		go func() {
			r.streamRead <- struct{}{}
			_, _ = io.Copy(io.Discard, conn)
		}()
	}
	return nil
}

func (r *answerRegistry) answer(t *testing.T) rescueAnswerCall {
	t.Helper()
	select {
	case call := <-r.answered:
		t.Cleanup(func() { _ = call.conn.Close() })
		return call
	case <-time.After(3 * time.Second):
		t.Fatal("the gateway received no answer")
		return rescueAnswerCall{}
	}
}

func (r *answerRegistry) requireNoAnswer(t *testing.T) {
	t.Helper()
	select {
	case call := <-r.answered:
		t.Fatalf("the gateway received answer %q", call.a.Answer)
	case <-time.After(200 * time.Millisecond):
	}
}

// openAnswer opens a pinned host on reg and returns it with K as the gateway
// saw it and K's private bytes, captured before any end clears them.
func openAnswer(t *testing.T, reg *answerRegistry) (*RescueClient, []byte, ed25519.PrivateKey) {
	t.Helper()
	c := newRescueClient(t, []ssh.PublicKey{rescueIdentity.PublicKey()}, newMemTransport(newRescueHandler(t, reg)))
	k := c.key
	if _, err := openRescue(t, c); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(reg.end)
	return c, <-reg.key, k
}

func openAnswerWithCodeExpiry(t *testing.T, reg *answerRegistry) (*RescueClient, []byte, chan time.Time) {
	t.Helper()
	c := newRescueClient(t, []ssh.PublicKey{rescueIdentity.PublicKey()}, newMemTransport(newRescueHandler(t, reg)))
	expires := make(chan time.Time)
	c.after = func(time.Duration) <-chan time.Time { return expires }
	if _, err := openRescue(t, c); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(reg.end)
	k := <-reg.key
	if opened := <-reg.opened; opened.Purpose != agentgateway.RescuePurposeNew {
		t.Fatalf("initial purpose = %q", opened.Purpose)
	}
	return c, k, expires
}

// identityValues are the values one identity proof binds.
type identityValues struct {
	endpoint                          string
	reception, hostKey, nonce, client []byte
	name, org, profile                string
}

var answerC = rescueKey(0x43) // the 担当者's one-request key C

func valuesFor(k []byte) identityValues {
	return identityValues{endpoint: rescueEndpoint, reception: answerReception, hostKey: k,
		nonce: bytes.Repeat([]byte{0x6e}, agentgateway.RescueNonceSize), client: answerC.PublicKey().Marshal(),
		name: "山田（サポート）", org: "O2 CONNECTIVE", profile: agentgateway.RescueProfile}
}

func (v identityValues) transcript() []byte {
	return agentgateway.RescueRequestTranscript(v.endpoint, v.reception, v.hostKey, v.nonce, v.client, v.name, v.org, v.profile)
}

func (v identityValues) sign(t *testing.T, s ssh.Signer) []byte {
	t.Helper()
	sig, err := s.Sign(rand.Reader, v.transcript())
	if err != nil {
		t.Fatal(err)
	}
	return sig.Blob
}

// identity is the request frame's content: these values with sig.
func (v identityValues) identity(sig []byte) agentgateway.RescueIdentity {
	return agentgateway.RescueIdentity{Nonce: v.nonce, Client: v.client, DisplayName: v.name, Organization: v.org, Profile: v.profile, Signature: sig}
}

func signedByK(k, data, sig []byte) bool {
	pk, err := ssh.ParsePublicKey(k)
	return err == nil && pk.Verify(data, &ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: sig}) == nil
}

// dispatchFor is a well-formed dispatch whose host key is K.
func dispatchFor(k []byte) agentgateway.Dispatch {
	d := fixtureDispatch(rescueKey(0x4b), "launch-rescue", time.Now().Add(time.Hour))
	d.HostKey = append([]byte(nil), k...)
	return d
}

func receiveRequest(t *testing.T, c *RescueClient) VerifiedRescueIdentity {
	t.Helper()
	select {
	case v := <-c.Request():
		return v
	case <-c.Done():
		t.Fatal("the reception ended instead of passing the request on")
	case <-time.After(3 * time.Second):
		t.Fatal("the host passed no request on")
	}
	return VerifiedRescueIdentity{}
}

// The popup may show only what the gateway's pinned identity key signed for
// this reception: any value signed differently, or a proof the host cannot
// tie to its own URL, reception id and K, is not a request.
func TestVerifyRescueRequestTakesOnlyValuesSignedForThisReception(t *testing.T) {
	k := rescueKey(0x4b).PublicKey().Marshal()
	pins := []ssh.PublicKey{rescueIdentity.PublicKey(), rescueNext.PublicKey()}
	sent := valuesFor(k)
	for _, s := range []ssh.Signer{rescueIdentity, rescueNext} {
		frame := valuesFor(k)
		frame.client = append([]byte(nil), frame.client...)
		v, err := VerifyRescueRequest(rescueEndpoint, answerReception, k, pins, frame.identity(frame.sign(t, s)))
		if err != nil {
			t.Fatalf("a proof by a pinned key: %v", err)
		}
		// The grant allows this C long after the frame's buffers are cleared,
		// so the verified identity must not share them.
		clear(frame.client)
		if v.Client() == nil || !bytes.Equal(v.Client().Marshal(), sent.client) || v.DisplayName() != sent.name ||
			v.Organization() != sent.org || v.Profile() != sent.profile {
			t.Fatalf("verified %+v, want the signed values", v)
		}
		// The state machine takes only a request verified for its own K.
		if !bytes.Equal(v.HostKey(), k) {
			t.Fatal("the verified request does not carry the K it was verified for")
		}
	}
	for _, tc := range []struct {
		name   string
		signed func(*identityValues) // what the identity key signed
		sent   func(*identityValues) // what the frame carries
		by     ssh.Signer
	}{
		{name: "another URL", signed: func(v *identityValues) { v.endpoint = "wss://other.example.invalid" + agentgateway.RescuePath }},
		{name: "another reception id", signed: func(v *identityValues) { v.reception = flipped(v.reception) }},
		{name: "another K", signed: func(v *identityValues) { v.hostKey = rescueKey(0x4c).PublicKey().Marshal() }},
		{name: "another nonce", signed: func(v *identityValues) { v.nonce = flipped(v.nonce) }},
		{name: "another C", signed: func(v *identityValues) { v.client = rescueKey(0x44).PublicKey().Marshal() }},
		{name: "another display name", signed: func(v *identityValues) { v.name = "鈴木（サポート）" }},
		{name: "another organization", signed: func(v *identityValues) { v.org = "Other Org" }},
		{name: "another profile", signed: func(v *identityValues) { v.profile = "mac_guest" }},
		{name: "a signed profile other than mac_current_user", signed: func(v *identityValues) { v.profile = "mac_guest" }, sent: func(v *identityValues) { v.profile = "mac_guest" }},
		{name: "a signed short nonce", signed: func(v *identityValues) { v.nonce = v.nonce[:16] }, sent: func(v *identityValues) { v.nonce = v.nonce[:16] }},
		{name: "a signed C that is no Ed25519 key", signed: func(v *identityValues) { v.client = []byte("not a key") }, sent: func(v *identityValues) { v.client = []byte("not a key") }},
		{name: "an identity key the host never pinned", by: rescueStranger},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signed, frame := valuesFor(k), valuesFor(k)
			if tc.signed != nil {
				tc.signed(&signed)
			}
			if tc.sent != nil {
				tc.sent(&frame)
			}
			by := tc.by
			if by == nil {
				by = rescueIdentity
			}
			v, err := VerifyRescueRequest(rescueEndpoint, answerReception, k, pins, frame.identity(signed.sign(t, by)))
			if err == nil || v.Client() != nil {
				t.Fatalf("verified %+v, %v; want a refusal", v, err)
			}
		})
	}
	t.Run("a broken signature", func(t *testing.T) {
		if v, err := VerifyRescueRequest(rescueEndpoint, answerReception, k, pins, sent.identity(flipped(sent.sign(t, rescueIdentity)))); err == nil || v.Client() != nil {
			t.Fatalf("verified %+v, %v; want a refusal", v, err)
		}
	})
	t.Run("no proof", func(t *testing.T) {
		if v, err := VerifyRescueRequest(rescueEndpoint, answerReception, k, pins, agentgateway.RescueIdentity{}); err == nil || v.Client() != nil {
			t.Fatalf("verified %+v, %v; want a refusal", v, err)
		}
	})
}

// On the wire the host checks the proof against its own URL, reception id and
// K. A proof that does not verify never reaches the state machine: the
// reception ends and K is discarded.
func TestRescueHostPassesOnOnlyARequestItVerified(t *testing.T) {
	t.Run("verified", func(t *testing.T) {
		reg := newAnswerRegistry()
		c, k, _ := openAnswer(t, reg)
		sent := valuesFor(k)
		reg.request <- sent.identity(sent.sign(t, rescueIdentity))
		v := receiveRequest(t, c)
		if v.Client() == nil || !bytes.Equal(v.Client().Marshal(), sent.client) || v.DisplayName() != sent.name ||
			v.Organization() != sent.org || v.Profile() != sent.profile {
			t.Fatalf("passed on %+v, want the signed values", v)
		}
		select {
		case <-c.Done():
			t.Fatal("the reception ended with a verified request")
		case <-time.After(200 * time.Millisecond):
		}
	})
	for _, tc := range []struct {
		name   string
		signed func(*identityValues)
		by     ssh.Signer
	}{
		{name: "an identity key the host never pinned", by: rescueStranger},
		{name: "signed for another reception id", signed: func(v *identityValues) { v.reception = flipped(v.reception) }},
		{name: "signed for another K", signed: func(v *identityValues) { v.hostKey = rescueKey(0x4c).PublicKey().Marshal() }},
		{name: "signed for another URL", signed: func(v *identityValues) { v.endpoint = "wss://other.example.invalid" + agentgateway.RescuePath }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newAnswerRegistry()
			c, kWire, k := openAnswer(t, reg)
			signed := valuesFor(kWire)
			if tc.signed != nil {
				tc.signed(&signed)
			}
			by := tc.by
			if by == nil {
				by = rescueIdentity
			}
			reg.request <- valuesFor(kWire).identity(signed.sign(t, by))
			expectDone(t, c)
			select {
			case v := <-c.Request():
				t.Fatalf("passed on %+v", v)
			default:
			}
			expectDropped(t, k)
			reg.requireNoAnswer(t)
		})
	}
}

// The answer is K's signature over the reception, the request nonce, the
// proof's digest and the answer; an allow also binds the dispatch. The host
// answers one request once.
func TestRescueHostSignsItsOneAnswerWithK(t *testing.T) {
	for _, tc := range []struct {
		name     string
		timedOut bool
		answer   string
	}{
		{"deny", false, agentgateway.RescueDeny},
		{"timeout", true, agentgateway.RescueTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newAnswerRegistry()
			c, k, _ := openAnswer(t, reg)
			sent := valuesFor(k)
			reg.request <- sent.identity(sent.sign(t, rescueIdentity))
			receiveRequest(t, c)
			if err := c.Refuse(tc.timedOut); err != nil {
				t.Fatalf("Refuse: %v", err)
			}
			call := reg.answer(t)
			digest := sha256.Sum256(sent.transcript())
			if call.a.Answer != tc.answer || !bytes.Equal(call.id, answerReception) ||
				!signedByK(k, agentgateway.RescueAnswerTranscript(answerReception, sent.nonce, digest[:], tc.answer, agentgateway.Dispatch{}), call.a.Signature) {
				t.Fatalf("answer %q on %x is not K's signed %q", call.a.Answer, call.id, tc.answer)
			}
			if err := c.Refuse(false); err == nil {
				t.Fatal("a second Refuse was sent")
			}
			if _, err := c.Allow(dispatchFor(k)); err == nil {
				t.Fatal("an Allow after the answer was sent")
			}
		})
	}
	t.Run("allow", func(t *testing.T) {
		reg := newAnswerRegistry()
		c, k, _ := openAnswer(t, reg)
		sent := valuesFor(k)
		reg.request <- sent.identity(sent.sign(t, rescueIdentity))
		receiveRequest(t, c)
		d := dispatchFor(k)
		conn, err := c.Allow(d)
		if err != nil || conn == nil {
			t.Fatalf("Allow = %v, %v", conn, err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		call := reg.answer(t)
		digest := sha256.Sum256(sent.transcript())
		if call.a.Answer != agentgateway.RescueAllow || !sameDispatch(call.a.Dispatch, d) ||
			!signedByK(k, agentgateway.RescueAnswerTranscript(answerReception, sent.nonce, digest[:], agentgateway.RescueAllow, d), call.a.Signature) {
			t.Fatalf("answer %q with %+v is not K's signed allow of the dispatch", call.a.Answer, call.a.Dispatch)
		}
		// The same WSS now carries the channel both ways, from the gateway's
		// first message on.
		if _, err := call.conn.Write([]byte("gateway first")); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len("gateway first"))
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := io.ReadFull(conn, got); err != nil || string(got) != "gateway first" {
			t.Fatalf("host read %q, %v", got, err)
		}
		if _, err := conn.Write([]byte("host reply")); err != nil {
			t.Fatal(err)
		}
		got = make([]byte, len("host reply"))
		_ = call.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := io.ReadFull(call.conn, got); err != nil || string(got) != "host reply" {
			t.Fatalf("gateway read %q, %v", got, err)
		}
		if err := c.Refuse(false); err == nil {
			t.Fatal("a Refuse after the allow was sent")
		}
	})
	t.Run("before a request", func(t *testing.T) {
		reg := newAnswerRegistry()
		c, k, _ := openAnswer(t, reg)
		if err := c.Refuse(false); err == nil {
			t.Fatal("Refuse without a request was sent")
		}
		if _, err := c.Allow(dispatchFor(k)); err == nil {
			t.Fatal("Allow without a request was sent")
		}
		reg.requireNoAnswer(t)
	})
}

// If the gateway goes while the popup is up, the reception ends at once, so
// the state machine refuses; a late answer is not sent.
func TestRescueHostEndsWhenTheGatewayGoesWhileConfirming(t *testing.T) {
	reg := newAnswerRegistry()
	c, kWire, k := openAnswer(t, reg)
	sent := valuesFor(kWire)
	reg.request <- sent.identity(sent.sign(t, rescueIdentity))
	receiveRequest(t, c)
	reg.end()
	expectDone(t, c)
	expectDropped(t, k)
	if _, err := c.Allow(dispatchFor(kWire)); err == nil {
		t.Fatal("Allow after the reception ended was sent")
	}
	if err := c.Refuse(false); err == nil {
		t.Fatal("Refuse after the reception ended was sent")
	}
}

// The code is used once a request arrives: its 5 minutes no longer end the
// reception, as on the gateway, so a request at 4:59 still gets its popup.
func TestRescueRequestOutlivesTheCodeExpiry(t *testing.T) {
	reg := newAnswerRegistry()
	c := newRescueClient(t, []ssh.PublicKey{rescueIdentity.PublicKey()}, newMemTransport(newRescueHandler(t, reg)))
	fire := make(chan time.Time, 1)
	c.after = func(time.Duration) <-chan time.Time { return fire }
	if _, err := openRescue(t, c); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(reg.end)
	k := <-reg.key
	sent := valuesFor(k)
	reg.request <- sent.identity(sent.sign(t, rescueIdentity))
	receiveRequest(t, c)
	fire <- time.Now()
	select {
	case <-c.Done():
		t.Fatal("the code's expiry ended a reception with a request")
	case <-time.After(200 * time.Millisecond):
	}
	if err := c.Refuse(false); err != nil {
		t.Fatalf("Refuse after the code's expiry: %v", err)
	}
	reg.answer(t)
}

func TestAcceptedRescueRequestOutlivesCodeExpiryDuringDisconnect(t *testing.T) {
	reg := newAnswerRegistry()
	reg.preserveOnDisconnect = true
	c, kWire, expires := openAnswerWithCodeExpiry(t, reg)
	values := valuesFor(kWire)
	reg.request <- values.identity(values.sign(t, rescueIdentity))
	receiveRequest(t, c)
	hostConn, err := c.Allow(dispatchFor(kWire))
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	first := reg.answer(t)
	if err := first.conn.Close(); err != nil {
		t.Fatalf("close stream before SSH bytes: %v", err)
	}
	if _, err := hostConn.Read(make([]byte, 1)); err == nil {
		t.Fatal("host stream read after transport close succeeded")
	}
	select {
	case <-reg.disconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("the gateway did not observe the channel transport loss")
	}
	expires <- time.Now()
	select {
	case <-c.Done():
		t.Fatal("the first accepted request did not outlive code expiry while disconnected")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := c.HostKey().Sign(rand.Reader, []byte("still live")); err != nil {
		t.Fatalf("K was discarded during the reconnect grace: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Resume(ctx); err != nil {
		t.Fatalf("Resume after code expiry: %v", err)
	}
}

func TestAcceptedRescueRequestOutlivesCodeExpiryBeforeResumeIdentity(t *testing.T) {
	reg := newAnswerRegistry()
	reg.preserveOnDisconnect = true
	c, kWire, expires := openAnswerWithCodeExpiry(t, reg)
	values := valuesFor(kWire)
	reg.request <- values.identity(values.sign(t, rescueIdentity))
	receiveRequest(t, c)
	hostConn, err := c.Allow(dispatchFor(kWire))
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	first := reg.answer(t)
	if err := first.conn.Close(); err != nil {
		t.Fatalf("close stream before SSH bytes: %v", err)
	}
	if _, err := hostConn.Read(make([]byte, 1)); err == nil {
		t.Fatal("host stream read after transport close succeeded")
	}
	select {
	case <-reg.disconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("the gateway did not observe the channel transport loss")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Resume(ctx); err != nil {
		t.Fatalf("Resume before the next identity request: %v", err)
	}
	if attempt := <-reg.opened; attempt.Purpose != agentgateway.RescuePurposeResume {
		t.Fatalf("resume purpose = %q", attempt.Purpose)
	}
	expires <- time.Now()
	select {
	case <-c.Done():
		t.Fatal("the first accepted request did not outlive code expiry before the resumed identity")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := c.HostKey().Sign(rand.Reader, []byte("still live")); err != nil {
		t.Fatalf("K was discarded before the resumed identity: %v", err)
	}
	reg.request <- values.identity(values.sign(t, rescueIdentity))
	receiveRequest(t, c)
	if _, err := c.Allow(dispatchFor(kWire)); err != nil {
		t.Fatalf("Allow after resume: %v", err)
	}
	reg.answer(t)
}

// The grant's host key is K itself, so the channel's host key dies with the
// reception.
func TestRescueHostKeyIsKUntilTheReceptionEnds(t *testing.T) {
	reg := newAnswerRegistry()
	c, k, _ := openAnswer(t, reg)
	s := c.HostKey()
	if s == nil || !bytes.Equal(s.PublicKey().Marshal(), k) {
		t.Fatal("HostKey is not K")
	}
	sig, err := s.Sign(rand.Reader, []byte("channel"))
	if err != nil || !signedByK(k, []byte("channel"), sig.Blob) {
		t.Fatalf("HostKey signature: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sign(rand.Reader, []byte("channel")); err == nil {
		t.Fatal("HostKey signed after the reception ended")
	}
}

func TestRescueClientCloseSendsEndFrameAfterAllow(t *testing.T) {
	reg := newAnswerRegistry()
	reg.observeStream = true
	reg.preserveOnDisconnect = true
	c, kWire, k := openAnswer(t, reg)
	sent := valuesFor(kWire)
	reg.request <- sent.identity(sent.sign(t, rescueIdentity))
	receiveRequest(t, c)
	if _, err := c.Allow(dispatchFor(kWire)); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	reg.answer(t)
	select {
	case <-reg.streamRead:
	case <-time.After(3 * time.Second):
		t.Fatal("the gateway did not start reading the channel stream")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case id := <-reg.ended:
		if !bytes.Equal(id, answerReception) {
			t.Fatalf("ended reception %x, want %x", id, answerReception)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not send an end frame to the gateway")
	}
	select {
	case <-reg.disconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("the gateway did not observe the WebSocket close")
	}
	expectDone(t, c)
	expectDropped(t, k)
}

func TestRescueClientKeepsTheReceptionKeyAfterChannelTransportLoss(t *testing.T) {
	reg := newAnswerRegistry()
	reg.preserveOnDisconnect = true
	c, kWire, _ := openAnswer(t, reg)
	values := valuesFor(kWire)
	reg.request <- values.identity(values.sign(t, rescueIdentity))
	receiveRequest(t, c)
	if _, err := c.Allow(dispatchFor(kWire)); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	first := reg.answer(t)
	if err := first.conn.Close(); err != nil {
		t.Fatalf("close first channel: %v", err)
	}
	select {
	case <-reg.disconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("the gateway did not observe the channel transport loss")
	}
	resumer, ok := any(c).(interface{ Resume(context.Context) error })
	if !ok {
		t.Fatal("RescueClient does not support resuming the same reception")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := resumer.Resume(ctx); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := <-reg.key; !bytes.Equal(got, kWire) {
		t.Fatal("resume proof used a different K")
	}
	firstOpen, resumedOpen := <-reg.opened, <-reg.opened
	if firstOpen.Purpose != agentgateway.RescuePurposeNew || len(firstOpen.Reception) != 0 {
		t.Fatalf("initial purpose/reception = %q/%x", firstOpen.Purpose, firstOpen.Reception)
	}
	if resumedOpen.Purpose != agentgateway.RescuePurposeResume || !bytes.Equal(resumedOpen.Reception, answerReception) || !bytes.Equal(resumedOpen.Key, kWire) {
		t.Fatalf("resume proof did not bind the same K and reception: purpose=%q reception=%x", resumedOpen.Purpose, resumedOpen.Reception)
	}
	transcript := agentgateway.RescueTranscript(rescueEndpoint, resumedOpen.Key, resumedOpen.GatewayNonce, agentgateway.RescuePurposeResume, answerReception)
	if err := c.HostKey().PublicKey().Verify(transcript, &ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: resumedOpen.Proof}); err != nil {
		t.Fatalf("resume proof: %v", err)
	}
	clear(transcript)
	select {
	case <-c.Done():
		t.Fatal("the reception ended with the channel transport loss")
	default:
	}
	host := c.HostKey()
	if host == nil || !bytes.Equal(host.PublicKey().Marshal(), kWire) {
		t.Fatal("the host key changed while the reception was in its reconnect grace")
	}
	sig, err := host.Sign(rand.Reader, []byte("resume"))
	if err != nil || host.PublicKey().Verify([]byte("resume"), sig) != nil {
		t.Fatalf("K was not retained during the reconnect grace: %v", err)
	}
	reg.request <- values.identity(values.sign(t, rescueIdentity))
	receiveRequest(t, c)
	secondConn, err := c.Allow(dispatchFor(kWire))
	if err != nil {
		t.Fatalf("Allow after resume: %v", err)
	}
	second := reg.answer(t)
	if !bytes.Equal(second.id, answerReception) || !bytes.Equal(second.a.Dispatch.HostKey, kWire) {
		t.Fatal("resumed answer did not use the same reception and K")
	}
	_ = secondConn.Close()
}

func TestRescueClientResumesWhenStreamEndsAfterInitialSSHData(t *testing.T) {
	reg := newAnswerRegistry()
	reg.preserveOnDisconnect = true
	c, kWire, _ := openAnswer(t, reg)
	values := valuesFor(kWire)
	reg.request <- values.identity(values.sign(t, rescueIdentity))
	receiveRequest(t, c)
	hostConn, err := c.Allow(dispatchFor(kWire))
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	first := reg.answer(t)
	initial := []byte("initial SSH transport bytes")
	if _, err := first.conn.Write(initial); err != nil {
		t.Fatalf("write initial SSH bytes: %v", err)
	}
	got := make([]byte, len(initial))
	if _, err := io.ReadFull(hostConn, got); err != nil || !bytes.Equal(got, initial) {
		t.Fatalf("initial SSH bytes = %q, %v", got, err)
	}
	if err := first.conn.Close(); err != nil {
		t.Fatalf("close stream after initial SSH bytes: %v", err)
	}
	if _, err := hostConn.Read(make([]byte, 1)); err == nil {
		t.Fatal("host stream read after transport close succeeded")
	}
	select {
	case <-reg.disconnected:
	case <-time.After(3 * time.Second):
		t.Fatal("the gateway did not observe the channel transport loss")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.Resume(ctx); err != nil {
		t.Fatalf("Resume after the established stream ended: %v", err)
	}
	if got := <-reg.key; !bytes.Equal(got, kWire) {
		t.Fatal("resume proof used a different K")
	}
	initialOpen, resumedOpen := <-reg.opened, <-reg.opened
	if initialOpen.Purpose != agentgateway.RescuePurposeNew ||
		resumedOpen.Purpose != agentgateway.RescuePurposeResume ||
		!bytes.Equal(resumedOpen.Reception, answerReception) || !bytes.Equal(resumedOpen.Key, kWire) {
		t.Fatalf("open attempts = %q/%q, resumed reception/key = %x/%x", initialOpen.Purpose, resumedOpen.Purpose, resumedOpen.Reception, resumedOpen.Key)
	}
}

func TestRescueClientRefusesResumeAfterReceptionClose(t *testing.T) {
	reg := newAnswerRegistry()
	c, kWire, _ := openAnswer(t, reg)
	values := valuesFor(kWire)
	reg.request <- values.identity(values.sign(t, rescueIdentity))
	receiveRequest(t, c)
	if _, err := c.Allow(dispatchFor(kWire)); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Resume(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("Resume after close = %v, want ErrClosed", err)
	}
	if _, err := c.HostKey().Sign(rand.Reader, []byte("resume")); err == nil {
		t.Fatal("the reception key signed after Close")
	}
}

func TestRescueClientCloseBoundsWaitForWriteLock(t *testing.T) {
	reg := newAnswerRegistry()
	c, kWire, _ := openAnswer(t, reg)
	sent := valuesFor(kWire)
	reg.request <- sent.identity(sent.sign(t, rescueIdentity))
	receiveRequest(t, c)
	if _, err := c.Allow(dispatchFor(kWire)); err != nil {
		t.Fatalf("Allow: %v", err)
	}

	c.wsWriteMu.Lock()
	started := time.Now()
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
		if elapsed := time.Since(started); elapsed > 350*time.Millisecond {
			t.Fatalf("Close took %v while the WebSocket write lock was held", elapsed)
		}
	case <-time.After(350 * time.Millisecond):
		c.wsWriteMu.Unlock()
		select {
		case <-closed:
		case <-time.After(3 * time.Second):
			t.Fatal("Close remained blocked after the WebSocket write lock was released")
		}
		t.Fatal("Close did not apply its 250ms bound while waiting for the WebSocket write lock")
	}
	c.wsWriteMu.Unlock()
}

// Between the request and the answer the gateway sends nothing; a frame there
// is not the channel's, so the reception ends rather than hand it to a grant.
func TestRescueHostEndsWhenTheGatewaySpeaksBeforeTheAnswer(t *testing.T) {
	reg := newAnswerRegistry()
	m := newMemTransport(newRescueHandler(t, reg))
	servers := make(chan *memConn, 1)
	m.prepare = func(_, server *memConn) { servers <- server }
	c := newRescueClient(t, []ssh.PublicKey{rescueIdentity.PublicKey()}, m)
	k := c.key
	if _, err := openRescue(t, c); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(reg.end)
	kWire := <-reg.key
	sent := valuesFor(kWire)
	reg.request <- sent.identity(sent.sign(t, rescueIdentity))
	receiveRequest(t, c)
	// One unmasked binary WebSocket frame from the gateway's side.
	if _, err := (<-servers).Write([]byte{0x82, 5, 'e', 'a', 'r', 'l', 'y'}); err != nil {
		t.Fatal(err)
	}
	expectDone(t, c)
	expectDropped(t, k)
	if _, err := c.Allow(dispatchFor(kWire)); err == nil {
		t.Fatal("Allow after an early gateway frame was sent")
	}
	reg.requireNoAnswer(t)
}

func TestRescueHostAnswersTheRealGatewayOverItsReception(t *testing.T) {
	pub := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	set, err := json.Marshal(map[string]any{"keys": []any{map[string]any{
		"kty": "OKP", "crv": "Ed25519", "kid": "synthetic-ed", "alg": "EdDSA", "use": "sig", "x": base64.RawURLEncoding.EncodeToString(pub),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := agentgateway.NewVerifier(agentgateway.VerifierConfig{
		Issuer: "https://issuer.example.invalid/", Audience: "https://gateway.example.invalid/mcp", Scope: "rescue.agent",
		Types: []string{"at+jwt"}, JWKS: set,
	})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := agentgateway.New(agentgateway.Config{
		Verifier: verifier, Resource: "https://gateway.example.invalid/mcp", Organization: "O2 CONNECTIVE",
		CallTimeout: 5 * time.Second, RescueSigner: rescueIdentity, Budgets: chanBudgets(),
		Principals: []agentgateway.Principal{{Label: "operator", Subject: "synthetic-subject-1", DisplayName: "山田（サポート）", Roles: []agentgateway.Role{agentgateway.RoleRescueOperator}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := gateway.RescueEndpoint(); got != rescueEndpoint {
		t.Fatalf("RescueEndpoint = %q, want %q", got, rescueEndpoint)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := gateway.Close(ctx); err != nil {
			t.Errorf("Gateway.Close: %v", err)
		}
	})
	h := newRescueHandler(t, gateway)
	c := newRescueClient(t, []ssh.PublicKey{rescueIdentity.PublicKey()}, newMemTransport(h))
	code, err := openRescue(t, c)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	requester := newVerticalMCP(t, gateway)
	pending, failed := requester.tool("rescue_request", map[string]any{"code": code})
	if failed || pending["state"] != "pending" {
		t.Fatalf("rescue_request = %v, isError %v; want pending", pending, failed)
	}
	v := receiveRequest(t, c)
	hostKey := c.HostKey()
	if v.Client() == nil || v.DisplayName() != "山田（サポート）" || v.Organization() != "O2 CONNECTIVE" ||
		!bytes.Equal(v.HostKey(), hostKey.PublicKey().Marshal()) {
		t.Fatalf("verified request = %+v; want gateway identity and this reception's K", v)
	}

	runtime := &verticalRuntime{procs: make(chan *verticalProc, 1)}
	digest, err := runbackend.KeySetDigest([]ssh.PublicKey{v.Client()})
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := ephemeral.NewCurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	challengeOwner, err := issuer.Prepare(ephemeral.ApprovedPolicy{
		PolicyDigest: agentexec.MacCurrentUserPolicyDigest(), KeySetDigest: digest,
		GuestBoundary: supervisor.CurrentUserNoIsolation, HardDeadline: time.Now().Add(30 * time.Second), BackendLeaseCeiling: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := issuer.Launch(context.Background(), challengeOwner, time.Now, func(context.Context, *supervisor.Authority) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	session, err := agentexec.New(agentexec.Config{Issuer: issuer, Authority: authority, Runtime: runtime, Clock: time.Now, CleanupTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	host, err := agentlink.NewHost(agentlink.HostConfig{Session: session, AllowedKeys: []ssh.PublicKey{v.Client()}, HostKey: hostKey, Budgets: chanBudgets()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := errors.Join(issuer.Close(), host.Close(ctx)); err != nil {
			t.Errorf("close rescue host: %v", err)
		}
	})
	d := session.Grant()
	dispatch := agentgateway.Dispatch{Binding: agentchannel.Binding{Grant: d.Binding, ConnectionGeneration: 1, KeySetDigest: d.KeySetDigest, Profile: session.Profile()},
		Deadline: d.HardDeadline, HostKey: hostKey.PublicKey().Marshal()}
	conn, err := c.Allow(dispatch)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	serveCtx, serveCancel := context.WithCancel(context.Background())
	t.Cleanup(serveCancel)
	served := make(chan error, 1)
	go func() { served <- host.Serve(serveCtx, conn) }()
	status, failed := requester.tool("rescue_request_status", map[string]any{})
	if failed || status["state"] != "allow" {
		t.Fatalf("rescue_request_status = %v, isError %v; want allow", status, failed)
	}

	fresh := newVerticalMCP(t, gateway)
	started, failed := fresh.start("rescue-route", "printf ready\n\n")
	if failed {
		t.Fatalf("rescue_start failed: %v", started)
	}
	commandID := verticalID(t, started)
	proc := verticalProcNext(t, runtime)
	if proc.input != "printf ready\n\n" {
		t.Fatalf("route command = %q, want one command", proc.input)
	}
	proc.output <- []byte("ready\n")
	close(proc.output)
	close(proc.exited)
	if terminal := fresh.terminal(commandID); terminal["phase"] != "exited" {
		t.Fatalf("route command terminal state = %v; want exited", terminal)
	}
	output, failed := fresh.tool("rescue_output", map[string]any{"command_id": commandID, "cursor": 0, "max_bytes": 1024})
	chunks, ok := output["chunks"].([]any)
	if failed || !ok || len(chunks) != 1 {
		t.Fatalf("rescue_output = %v, isError %v; want one output chunk", output, failed)
	}
	chunk, ok := chunks[0].(map[string]any)
	if !ok || chunk["stream"] != "stdout" || chunk["encoding"] != "utf-8" || chunk["data"] != "ready\n" {
		t.Fatalf("rescue_output chunk = %v; want stdout ready newline", chunks[0])
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("agentlink.Host.Serve did not end within 5s of the reception closing")
	}
}

type rescueActivityPath struct {
	gateway   *agentgateway.Gateway
	recorder  *rescueRecorder
	client    *RescueClient
	conn      net.Conn
	ready     <-chan struct{}
	requests  <-chan *ssh.Request
	served    <-chan error
	reception agentgateway.RescueReception
}

func newRescueActivityPath(t *testing.T, budgets agentchannel.Budgets) rescueActivityPath {
	t.Helper()
	now := time.Now()
	gateway := newRescueGatewayWithBudgets(t, &now, budgets)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := gateway.Close(ctx); err != nil {
			t.Errorf("Gateway.Close: %v", err)
		}
	})
	recorder := newRescueRecorder(gateway)
	handler := newRescueHandler(t, recorder)
	client := newRescueClient(t, []ssh.PublicKey{rescueIdentity.PublicKey()}, newMemTransport(handler))
	code, err := openRescue(t, client)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	reception := <-recorder.opened
	requester := newVerticalMCP(t, gateway)
	if pending, failed := requester.tool("rescue_request", map[string]any{"code": code}); failed || pending["state"] != "pending" {
		t.Fatalf("rescue_request = %v, isError %v; want pending", pending, failed)
	}
	identity := receiveRequest(t, client)
	hostKey := client.HostKey()
	dispatch := fixtureDispatch(hostKey, "rescue-activity", time.Now().Add(time.Hour))
	digest, err := runbackend.KeySetDigest([]ssh.PublicKey{identity.Client()})
	if err != nil {
		t.Fatal(err)
	}
	dispatch.Binding.KeySetDigest = digest
	conn, err := client.Allow(dispatch)
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	ready, requests, served := serveRescueActivitySSH(t, conn, hostKey, identity.Client(), dispatch.Binding.Grant.Identity.LaunchID)
	return rescueActivityPath{gateway: gateway, recorder: recorder, client: client, conn: conn, ready: ready,
		requests: requests, served: served, reception: reception}
}

func serveRescueActivitySSH(t *testing.T, conn net.Conn, hostKey ssh.Signer, clientKey ssh.PublicKey, launchID string) (<-chan struct{}, <-chan *ssh.Request, <-chan error) {
	t.Helper()
	ready := make(chan struct{})
	requestsOut := make(chan *ssh.Request, 8)
	served := make(chan error, 1)
	go func() {
		config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != launchID || !bytes.Equal(key.Marshal(), clientKey.Marshal()) {
				return nil, ErrRefused
			}
			return nil, nil
		}}
		config.AddHostKey(hostKey)
		server, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			served <- err
			return
		}
		defer server.Close()
		go func() {
			for request := range requests {
				requestsOut <- request
			}
		}()
		for open := range channels {
			if open.ChannelType() != agentchannel.ChannelType {
				_ = open.Reject(ssh.UnknownChannelType, "unsupported")
				continue
			}
			channel, channelRequests, err := open.Accept()
			if err != nil {
				served <- err
				return
			}
			close(ready)
			go io.Copy(io.Discard, channel)
			go func() {
				for request := range channelRequests {
					if request.WantReply {
						_ = request.Reply(false, nil)
					}
				}
			}()
		}
		served <- nil
	}()
	return ready, requestsOut, served
}

func TestRescueHandlerTouchesOnlyAuthenticatedChannelActivity(t *testing.T) {
	t.Run("positive heartbeat reply", func(t *testing.T) {
		path := newRescueActivityPath(t, chanBudgets())
		select {
		case <-path.ready:
		case <-time.After(3 * time.Second):
			t.Fatal("SSH channel did not open")
		}
		select {
		case call := <-path.recorder.touched:
			t.Fatalf("TouchRescue before authenticated channel activity: attempt %d", call.attemptID)
		default:
		}
		var heartbeat *ssh.Request
		select {
		case heartbeat = <-path.requests:
		case <-time.After(2 * time.Second):
			t.Fatal("gateway did not send an SSH heartbeat")
		}
		if heartbeat.Type != agentchannel.HeartbeatRequest || !heartbeat.WantReply || len(heartbeat.Payload) != 0 {
			t.Fatalf("heartbeat = %+v", heartbeat)
		}
		if err := heartbeat.Reply(true, nil); err != nil {
			t.Fatalf("reply to SSH heartbeat: %v", err)
		}
		select {
		case call := <-path.recorder.touched:
			if !bytes.Equal(call.id, path.reception.ID) || call.attemptID != path.reception.AttemptID {
				t.Fatalf("TouchRescue event = %x/%d; want %x/%d", call.id, call.attemptID, path.reception.ID, path.reception.AttemptID)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("authenticated heartbeat reply did not call TouchRescue through WSS")
		}
	})

	t.Run("malformed SSH bytes", func(t *testing.T) {
		budgets := chanBudgets()
		budgets.HeartbeatInterval = agentchannel.MaxHeartbeatInterval
		budgets.ReadStall = budgets.HeartbeatInterval + budgets.HeartbeatTimeout + time.Second
		path := newRescueActivityPath(t, budgets)
		select {
		case <-path.ready:
		case <-time.After(3 * time.Second):
			t.Fatal("SSH channel did not open")
		}
		select {
		case call := <-path.recorder.touched:
			t.Fatalf("TouchRescue before authenticated channel activity: attempt %d", call.attemptID)
		default:
		}
		if _, err := path.conn.Write([]byte("not an authenticated SSH packet")); err != nil {
			t.Fatalf("write malformed SSH bytes over WSS: %v", err)
		}
		select {
		case <-path.served:
		case <-time.After(3 * time.Second):
			t.Fatal("malformed SSH bytes did not end the channel")
		}
		select {
		case call := <-path.recorder.touched:
			t.Fatalf("malformed SSH bytes advanced lastFrame through TouchRescue for attempt %d", call.attemptID)
		default:
		}
	})
}
