package agentgateway

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// The rescue reception (docs/rescue-connection-start.md, 受付の登録,
// 受付 OFF のホストへの要求 and 回数の制限). Each test keeps a positive
// control, so a gateway that refuses everything fails it too.

var rescueIdentity = mustRescueSigner(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x49}, 32)))

func mustRescueSigner(k any) ssh.Signer {
	s, err := ssh.NewSignerFromKey(k)
	if err != nil {
		panic(err)
	}
	return s
}

// rescueHostKey is a distinct K per n.
func rescueHostKey(n int) ssh.Signer {
	seed := make([]byte, 32)
	binary.BigEndian.PutUint64(seed, uint64(n)+1)
	seed[31] = 0x4b
	return mustRescueSigner(ed25519.NewKeyFromSeed(seed))
}

func rescueReceptionConfig(t *testing.T, now *time.Time) Config {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x52}, 32))
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "test", "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}}})
	verifier, err := NewVerifier(VerifierConfig{Issuer: "https://issuer.example.invalid/", Audience: "https://example.test/mcp", Scope: "scope", Types: []string{"at+jwt"}, JWKS: jwks})
	if err != nil {
		t.Fatal(err)
	}
	return Config{Verifier: verifier, Clock: func() time.Time { return *now }, RescueSigner: rescueIdentity, Principals: []Principal{
		{Label: "op1", Subject: "s1", Roles: []Role{RoleRescueOperator}},
	}}
}

func rescueReceptionGateway(t *testing.T, now *time.Time) *Gateway {
	t.Helper()
	g, err := New(rescueReceptionConfig(t, now))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

type rescueReceptionTestClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (c *rescueReceptionTestClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *rescueReceptionTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func rescueNonce(b byte) []byte { return bytes.Repeat([]byte{b}, RescueNonceSize) }

// newRescueRequest is a valid "new" registration of k on g.
func newRescueRequest(t *testing.T, g *Gateway, k ssh.Signer) RescueRequest {
	t.Helper()
	r := RescueRequest{Key: k.PublicKey().Marshal(), HostNonce: rescueNonce(0x68), GatewayNonce: rescueNonce(0x67), Purpose: RescuePurposeNew}
	r.Proof = rescueSign(t, k, RescueTranscript(g.RescueEndpoint(), r.Key, r.GatewayNonce, r.Purpose, r.Reception))
	return r
}

func rescueSign(t *testing.T, k ssh.Signer, data []byte) []byte {
	t.Helper()
	sig, err := k.Sign(rand.Reader, data)
	if err != nil || sig.Format != ssh.KeyAlgoED25519 {
		t.Fatalf("sign: %v %v", sig, err)
	}
	return sig.Blob
}

func openRescue(t *testing.T, g *Gateway, n int) RescueReception {
	t.Helper()
	res, err := g.OpenRescue(newRescueRequest(t, g, rescueHostKey(n)))
	if err != nil {
		t.Fatalf("OpenRescue #%d: %v", n, err)
	}
	return res
}

func TestRescueEndpointComesFromTheConfiguredResource(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	if got := rescueReceptionGateway(t, &now).RescueEndpoint(); got != "wss://example.test/tether-agent/v1/rescue" {
		t.Fatalf("RescueEndpoint = %q", got)
	}
	cfg := rescueReceptionConfig(t, &now)
	cfg.Resource = "https://gw.example.test:8443/mcp"
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := g.RescueEndpoint(); got != "wss://gw.example.test:8443/tether-agent/v1/rescue" {
		t.Fatalf("RescueEndpoint with a port = %q", got)
	}
}

// Only the holder of K gets a reception, and the result it gets is signed over
// the six values the host checks.
func TestRescueReceptionIssuesASignedCodeOnlyForTheHolderOfK(t *testing.T) {
	k := rescueHostKey(0)
	other := rescueHostKey(1)
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *Gateway, *RescueRequest)
		ok   bool
	}{
		{"the proof of K", nil, true},
		{"a proof by another key for K", func(t *testing.T, g *Gateway, r *RescueRequest) {
			r.Proof = rescueSign(t, other, RescueTranscript(g.RescueEndpoint(), r.Key, r.GatewayNonce, r.Purpose, nil))
		}, false},
		{"a proof for another endpoint", func(t *testing.T, g *Gateway, r *RescueRequest) {
			r.Proof = rescueSign(t, k, RescueTranscript("wss://other.example.test/tether-agent/v1/rescue", r.Key, r.GatewayNonce, r.Purpose, nil))
		}, false},
		{"a proof for another gateway nonce", func(t *testing.T, g *Gateway, r *RescueRequest) {
			r.Proof = rescueSign(t, k, RescueTranscript(g.RescueEndpoint(), r.Key, rescueNonce(0x01), r.Purpose, nil))
		}, false},
		{"a proof for another purpose", func(t *testing.T, g *Gateway, r *RescueRequest) {
			r.Proof = rescueSign(t, k, RescueTranscript(g.RescueEndpoint(), r.Key, r.GatewayNonce, RescuePurposeResume, nil))
		}, false},
		{"a short host nonce", func(t *testing.T, g *Gateway, r *RescueRequest) { r.HostNonce = r.HostNonce[1:] }, false},
		{"a short gateway nonce", func(t *testing.T, g *Gateway, r *RescueRequest) {
			r.GatewayNonce = r.GatewayNonce[1:]
			r.Proof = rescueSign(t, k, RescueTranscript(g.RescueEndpoint(), r.Key, r.GatewayNonce, r.Purpose, nil))
		}, false},
		{"a reception id on a new reception", func(t *testing.T, g *Gateway, r *RescueRequest) {
			r.Reception = bytes.Repeat([]byte{0x72}, RescueReceptionSize)
			r.Proof = rescueSign(t, k, RescueTranscript(g.RescueEndpoint(), r.Key, r.GatewayNonce, r.Purpose, r.Reception))
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1_800_000_000, 0)
			g := rescueReceptionGateway(t, &now)
			r := newRescueRequest(t, g, k)
			if tc.edit != nil {
				tc.edit(t, g, &r)
			}
			res, err := g.OpenRescue(r)
			if !tc.ok {
				if err == nil || res.Code != "" || len(g.rescueCodes.codes) != 0 {
					t.Fatalf("OpenRescue = %+v, %v with %d codes; want a refusal and no code", res, err, len(g.rescueCodes.codes))
				}
				return
			}
			if err != nil {
				t.Fatalf("OpenRescue: %v", err)
			}
			if len(res.ID) != RescueReceptionSize || bytes.Equal(res.ID, make([]byte, RescueReceptionSize)) || !regexp.MustCompile(`^[0-9]{12}$`).MatchString(res.Code) {
				t.Fatalf("result = %x %q", res.ID, res.Code)
			}
			data := RescueResultTranscript("wss://example.test/tether-agent/v1/rescue", r.HostNonce, r.Key, res.ID, res.Code)
			if err := rescueIdentity.PublicKey().Verify(data, &ssh.Signature{Format: ssh.KeyAlgoED25519, Blob: res.Signature}); err != nil {
				t.Fatalf("the result is not signed by the identity key over the six values: %v", err)
			}
			if _, err := g.rescueCodes.redeem(g.byLabel["op1"], res.Code); err != nil {
				t.Fatalf("the signed code is not the live code: %v", err)
			}
		})
	}
}

// rescueTorsionPoints are the canonical encodings of the eight points of
// order 1, 2, 4 and 8 on edwards25519 (the same y values as libsodium's
// has_small_order list).
var rescueTorsionPoints = []string{
	"0100000000000000000000000000000000000000000000000000000000000000", // order 1
	"ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", // order 2, y = p-1
	"0000000000000000000000000000000000000000000000000000000000000000", // order 4, y = 0
	"0000000000000000000000000000000000000000000000000000000000000080",
	"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05", // order 8
	"26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85",
	"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a",
	"c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa",
}

// A small-order K lets anyone make, without a private key, a proof that
// crypto/ed25519 accepts: with S = 0 and R one of the torsion points, the
// check [S]B = R + [k]A holds once R = -[k]A. A reception on such a K would
// later let anyone resume it and pass the host key pin, so the gateway takes
// no small-order K, in any encoding Go decodes (PR #429's review, #425).
func TestRescueReceptionRefusesASmallOrderK(t *testing.T) {
	keys := append([]string{
		"edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", // y = p, so 0
		"edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		"eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f", // y = p+1, so 1
		"eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		"0100000000000000000000000000000000000000000000000000000000000080", // x = -0
		"ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	}, rescueTorsionPoints...)
	for _, h := range keys {
		t.Run(h[:8]+".."+h[62:], func(t *testing.T) {
			now := time.Unix(1_800_000_000, 0)
			g := rescueReceptionGateway(t, &now)
			raw, _ := hex.DecodeString(h)
			pub, err := ssh.NewPublicKey(ed25519.PublicKey(raw))
			if err != nil {
				t.Fatal(err)
			}
			r := RescueRequest{Key: pub.Marshal(), HostNonce: rescueNonce(0x68), Purpose: RescuePurposeNew}
		forge:
			for n := range 256 {
				r.GatewayNonce = rescueNonce(byte(n))
				msg := RescueTranscript(g.RescueEndpoint(), r.Key, r.GatewayNonce, r.Purpose, nil)
				for _, rh := range rescueTorsionPoints {
					sig, _ := hex.DecodeString(rh + "0000000000000000000000000000000000000000000000000000000000000000")
					if ed25519.Verify(ed25519.PublicKey(raw), msg, sig) {
						r.Proof = sig
						break forge
					}
				}
			}
			if r.Proof == nil {
				t.Fatal("crypto/ed25519 accepts no forged proof for this K; the threat is gone, so re-read this test")
			}
			res, err := g.OpenRescue(r)
			if !errors.Is(err, ErrRefused) || res.Code != "" || len(g.rescueCodes.codes) != 0 || len(g.rescue.records) != 0 {
				t.Fatalf("OpenRescue with a small-order K and a forged proof = code %q, %v with %d records; want ErrRefused and no reception", res.Code, err, len(g.rescue.records))
			}
			openRescue(t, g, 0) // a K of prime order still opens
		})
	}
}

// The transcripts are the length-prefixed values of the design, in order, so a
// value cannot slide into its neighbour.
func TestRescueTranscriptsAreLengthPrefixedInTheDesignOrder(t *testing.T) {
	field := func(out []byte, v []byte) []byte {
		return append(binary.BigEndian.AppendUint32(out, uint32(len(v))), v...)
	}
	var proof []byte
	for _, v := range [][]byte{[]byte("rescue-host.agentgateway.rescue-reception\x00v1\x00"), []byte("wss://e/x"), []byte("K"), []byte("gn"), []byte("resume"), []byte("id")} {
		proof = field(proof, v)
	}
	if got := RescueTranscript("wss://e/x", []byte("K"), []byte("gn"), "resume", []byte("id")); !bytes.Equal(got, proof) {
		t.Fatalf("RescueTranscript = %q", got)
	}
	newProof := proof[:len(proof)-len("id")-4-len("resume")-4]
	newProof = field(newProof, []byte("new"))
	if got := RescueTranscript("wss://e/x", []byte("K"), []byte("gn"), "new", nil); !bytes.Equal(got, newProof) {
		t.Fatalf("RescueTranscript for new carries no reception id: %q", got)
	}
	var result []byte
	for _, v := range [][]byte{[]byte("rescue-host.agentgateway.rescue-reception-result\x00v1\x00"), []byte("wss://e/x"), []byte("hn"), []byte("K"), []byte("id"), []byte("123412341234")} {
		result = field(result, v)
	}
	if got := RescueResultTranscript("wss://e/x", []byte("hn"), []byte("K"), []byte("id"), "123412341234"); !bytes.Equal(got, result) {
		t.Fatalf("RescueResultTranscript = %q", got)
	}
}

// 受付 OFF and a dropped reception connection both end in EndRescue: the code
// is invalid at once, and only that reception's code.
func TestRescueReceptionEndInvalidatesItsCodeAtOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	g := rescueReceptionGateway(t, &now)
	ended := openRescue(t, g, 0)
	kept := openRescue(t, g, 1)
	g.EndRescue(ended.ID)
	op := g.byLabel["op1"]
	if _, err := g.rescueCodes.redeem(op, ended.Code); !errors.Is(err, errRescueCodeInvalid) {
		t.Fatalf("the code of an ended reception = %v, want invalid", err)
	}
	if _, err := g.rescueCodes.redeem(op, kept.Code); err != nil {
		t.Fatalf("another reception's code = %v, want live", err)
	}
}

// resume is accepted as wire only: the proof binds the reception id, and the
// gateway refuses it without touching that reception (#425 resumes).
func TestRescueReceptionRefusesResumeUntilGraceExists(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	g := rescueReceptionGateway(t, &now)
	k := rescueHostKey(0)
	live, err := g.OpenRescue(newRescueRequest(t, g, k))
	if err != nil {
		t.Fatalf("OpenRescue: %v", err)
	}
	r := RescueRequest{Key: k.PublicKey().Marshal(), HostNonce: rescueNonce(0x68), GatewayNonce: rescueNonce(0x67), Purpose: RescuePurposeResume, Reception: live.ID}
	r.Proof = rescueSign(t, k, RescueTranscript(g.RescueEndpoint(), r.Key, r.GatewayNonce, r.Purpose, r.Reception))
	if res, err := g.OpenRescue(r); err == nil || res.Code != "" {
		t.Fatalf("resume = %+v, %v; want a refusal", res, err)
	}
	if len(g.rescueCodes.codes) != 1 {
		t.Fatalf("resume changed the codes: %d", len(g.rescueCodes.codes))
	}
	if _, err := g.rescueCodes.redeem(g.byLabel["op1"], live.Code); err != nil {
		t.Fatalf("resume ended the live reception: %v", err)
	}
}

func TestRescueReceptionLimits(t *testing.T) {
	t.Run("100 valid codes refuse a new reception with busy", func(t *testing.T) {
		now := time.Unix(1_800_000_000, 0)
		g := rescueReceptionGateway(t, &now)
		for i := range 100 { // the design value: maxRescueCodes
			openRescue(t, g, i)
		}
		if err := g.AdmitRescue(); !errors.Is(err, ErrRescueBusy) {
			t.Fatalf("AdmitRescue with 100 valid codes = %v, want busy", err)
		}
		if _, err := g.OpenRescue(newRescueRequest(t, g, rescueHostKey(100))); !errors.Is(err, ErrRescueBusy) {
			t.Fatalf("OpenRescue with 100 valid codes = %v, want busy", err)
		}
		now = now.Add(5 * time.Minute)
		if err := g.AdmitRescue(); err != nil {
			t.Fatalf("AdmitRescue after the codes expired = %v", err)
		}
	})
	t.Run("1000 receptions refuse a new reception with busy", func(t *testing.T) {
		now := time.Unix(1_800_000_000, 0)
		g := rescueReceptionGateway(t, &now)
		// Since #423 a reception without a request ends with its code, so only
		// requested ones (in session now, in grace with #425) outlive their
		// codes and can fill the records.
		first := openRescue(t, g, 0)
		g.rescue.mu.Lock()
		for i := 1; i < 1000; i++ { // the design value: maxRescueReceptions
			g.rescue.records[fmt.Sprintf("session-%d", i)] = &rescueReceptionRecord{principal: &principal{}, answered: true, done: make(chan struct{})}
		}
		g.rescue.mu.Unlock()
		if err := g.AdmitRescue(); !errors.Is(err, ErrRescueBusy) {
			t.Fatalf("AdmitRescue with 1000 receptions and no valid code = %v, want busy", err)
		}
		if _, err := g.OpenRescue(newRescueRequest(t, g, rescueHostKey(1000))); !errors.Is(err, ErrRescueBusy) {
			t.Fatalf("OpenRescue with 1000 receptions = %v, want busy", err)
		}
		g.EndRescue(first.ID)
		if err := g.AdmitRescue(); err != nil {
			t.Fatalf("AdmitRescue after one reception ended = %v", err)
		}
	})
	t.Run("the 31st new reception in a minute is rate limited", func(t *testing.T) {
		now := time.Unix(1_800_000_000, 0)
		clock := &rescueReceptionTestClock{now: now}
		cfg := rescueReceptionConfig(t, &now)
		cfg.Clock = clock.Now
		g, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = g.Close(context.Background()) })
		for i := range 30 { // the design value: rescueAdmitLimit
			if err := g.AdmitRescue(); err != nil {
				t.Fatalf("AdmitRescue #%d = %v", i+1, err)
			}
			clock.Advance(time.Second)
		}
		if err := g.AdmitRescue(); !errors.Is(err, ErrRescueRate) {
			t.Fatalf("AdmitRescue #31 within a minute = %v, want rate", err)
		}
		clock.Advance(time.Minute - 30*time.Second + time.Second)
		if err := g.AdmitRescue(); err != nil {
			t.Fatalf("AdmitRescue once the first left the window = %v", err)
		}
	})
}

// The reception is closed without the identity key, and the key is a raw
// Ed25519 key of a gateway that registers anything at all.
func TestRescueReceptionNeedsAnEd25519IdentityKey(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	if err := rescueReceptionGateway(t, &now).AdmitRescue(); err != nil {
		t.Fatalf("AdmitRescue with the identity key = %v", err)
	}
	cfg := rescueReceptionConfig(t, &now)
	cfg.RescueSigner = nil
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.AdmitRescue(); err == nil {
		t.Fatal("AdmitRescue without the identity key was admitted")
	}
	if _, err := g.OpenRescue(newRescueRequest(t, g, rescueHostKey(0))); err == nil {
		t.Fatal("OpenRescue without the identity key was opened")
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg = rescueReceptionConfig(t, &now)
	cfg.RescueSigner = mustRescueSigner(ec)
	if _, err := New(cfg); !errors.Is(err, ErrConfig) {
		t.Fatalf("New with an ECDSA identity key = %v, want ErrConfig", err)
	}
	cfg = rescueReceptionConfig(t, &now)
	cfg.DiscoveryOnly = true
	cfg.RescueSigner = nil
	if _, err := New(cfg); err != nil {
		t.Fatalf("New in discovery only = %v", err)
	}
	cfg.RescueSigner = rescueIdentity
	if _, err := New(cfg); !errors.Is(err, ErrConfig) {
		t.Fatalf("New with an identity key in discovery only = %v, want ErrConfig", err)
	}
}

// A closed gateway takes no reception, as every other gateway operation
// refuses once Close began.
func TestRescueReceptionClosesWithTheGateway(t *testing.T) {
	now := time.Now()
	g := rescueReceptionGateway(t, &now)
	if err := g.AdmitRescue(); err != nil {
		t.Fatalf("AdmitRescue before Close = %v", err)
	}
	if _, err := g.OpenRescue(newRescueRequest(t, g, rescueHostKey(0))); err != nil {
		t.Fatalf("OpenRescue before Close = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.Close(ctx); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if err := g.AdmitRescue(); !errors.Is(err, ErrClosed) {
		t.Fatalf("AdmitRescue after Close = %v, want ErrClosed", err)
	}
	if _, err := g.OpenRescue(newRescueRequest(t, g, rescueHostKey(1))); !errors.Is(err, ErrClosed) {
		t.Fatalf("OpenRescue after Close = %v, want ErrClosed", err)
	}
}
