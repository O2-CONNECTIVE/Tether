package agenttransport

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
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
	key      chan []byte // K as the host proved it
	request  chan agentgateway.RescueIdentity
	done     chan struct{}
	once     sync.Once
	ended    chan []byte
	answered chan rescueAnswerCall
}

func newAnswerRegistry() *answerRegistry {
	return &answerRegistry{key: make(chan []byte, 1), request: make(chan agentgateway.RescueIdentity, 1), done: make(chan struct{}),
		ended: make(chan []byte, 1), answered: make(chan rescueAnswerCall, 1)}
}

func (*answerRegistry) AdmitRescue() error { return nil }
func (r *answerRegistry) OpenRescue(q agentgateway.RescueRequest) (agentgateway.RescueReception, error) {
	r.key <- append([]byte(nil), q.Key...)
	sig, err := rescueIdentity.Sign(rand.Reader, agentgateway.RescueResultTranscript(rescueEndpoint, q.HostNonce, q.Key, answerReception, "123456789012"))
	if err != nil {
		return agentgateway.RescueReception{}, err
	}
	return agentgateway.RescueReception{ID: answerReception, Code: "123456789012", Signature: sig.Blob, Request: r.request, Done: r.done}, nil
}
func (r *answerRegistry) EndRescue(id []byte) {
	select {
	case r.ended <- append([]byte(nil), id...):
	default:
	}
	r.end()
}
func (r *answerRegistry) end() { r.once.Do(func() { close(r.done) }) }
func (r *answerRegistry) AnswerRescue(id []byte, a agentgateway.RescueAnswer, conn net.Conn) error {
	r.answered <- rescueAnswerCall{id: append([]byte(nil), id...), a: a, conn: conn}
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
