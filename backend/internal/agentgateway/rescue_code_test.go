package agentgateway

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"testing"
	"time"
)

// rescueCodeGateway builds a gateway whose principals got their roles through
// New, so the role check below is the one #415 configured, not a hand-set flag.
func rescueCodeGateway(t *testing.T, now *time.Time) *Gateway {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x52}, 32))
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "test", "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}}})
	verifier, err := NewVerifier(VerifierConfig{Issuer: "https://issuer.example.invalid/", Audience: "https://example.test/mcp", Scope: "scope", Types: []string{"at+jwt"}, JWKS: jwks})
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(Config{Verifier: verifier, Clock: func() time.Time { return *now }, Principals: []Principal{
		{Label: "op1", Subject: "s1", Roles: []Role{RoleRescueOperator}},
		{Label: "op2", Subject: "s2", Roles: []Role{RoleRescueOperator}},
		{Label: "owner", Subject: "s3", Roles: []Role{RoleResidentOwner}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func issueRescueCode(t *testing.T, g *Gateway, reception string) string {
	t.Helper()
	code, err := g.rescueCodes.issue(reception)
	if err != nil || !twelveDigits.MatchString(code) {
		t.Fatalf("issue(%q) = %q, %v; want 12 digits", reception, code, err)
	}
	return code
}

// wrongRescueCode differs from every code issued in the test: issued codes are
// random, and one hit in 10^12 would only make the test fail, never pass.
const wrongRescueCode = "000000000000"

// The values of docs/rescue-connection-start.md (回数の制限), written out so a
// changed constant fails here instead of moving the tests along with it.
const (
	specFailures   = 5   // failed matches per principal in 10 minutes
	specValidCodes = 100 // N
	specConcurrent = 4   // requests one principal may hold at once
)

// A code that worked once must never work again, for anyone: otherwise a
// second operator (or a replay) reaches the host after its user answered.
func TestRescueCodeRedeemsOnlyOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	g := rescueCodeGateway(t, &now)
	code := issueRescueCode(t, g, "reception-a")
	if got, err := g.rescueCodes.redeem(g.byLabel["op1"], code); err != nil || got != "reception-a" {
		t.Fatalf("first redeem = %q, %v; want reception-a", got, err)
	}
	for _, label := range []string{"op1", "op2"} {
		if got, err := g.rescueCodes.redeem(g.byLabel[label], code); !errors.Is(err, errRescueCodeInvalid) || got != "" {
			t.Fatalf("%s redeemed a used code: %q, %v", label, got, err)
		}
	}
}

// The user reads the code out as 3 groups of 4; the operator may type it back
// with the spaces or hyphens.
func TestRescueCodeIgnoresSpacesAndHyphens(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	g := rescueCodeGateway(t, &now)
	code := issueRescueCode(t, g, "reception-a")
	typed := " " + code[:4] + " " + code[4:8] + "-" + code[8:] + "\t"
	if got, err := g.rescueCodes.redeem(g.byLabel["op1"], typed); err != nil || got != "reception-a" {
		t.Fatalf("redeem(%q) = %q, %v; want reception-a", typed, got, err)
	}
}

// 5 minutes on the gateway clock bound how long a code can be guessed or
// passed on; the guessing calculation assumes no code outlives them.
func TestRescueCodeExpiresAfterFiveMinutes(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	now := start
	g := rescueCodeGateway(t, &now)
	early := issueRescueCode(t, g, "reception-a")
	late := issueRescueCode(t, g, "reception-b")
	now = start.Add(5*time.Minute - time.Nanosecond)
	if got, err := g.rescueCodes.redeem(g.byLabel["op1"], early); err != nil || got != "reception-a" {
		t.Fatalf("redeem just before 5 minutes = %q, %v; want reception-a", got, err)
	}
	now = start.Add(5 * time.Minute)
	if got, err := g.rescueCodes.redeem(g.byLabel["op1"], late); !errors.Is(err, errRescueCodeInvalid) || got != "" {
		t.Fatalf("redeem at 5 minutes = %q, %v; want invalid", got, err)
	}
}

// Turning the reception off must stop the code at once; it must not touch
// another reception's code.
func TestRescueCodeEndsWithItsReception(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	g := rescueCodeGateway(t, &now)
	a := issueRescueCode(t, g, "reception-a")
	b := issueRescueCode(t, g, "reception-b")
	g.rescueCodes.end("reception-a")
	if got, err := g.rescueCodes.redeem(g.byLabel["op1"], a); !errors.Is(err, errRescueCodeInvalid) || got != "" {
		t.Fatalf("redeem after the reception ended = %q, %v; want invalid", got, err)
	}
	if got, err := g.rescueCodes.redeem(g.byLabel["op1"], b); err != nil || got != "reception-b" {
		t.Fatalf("another reception's code = %q, %v; want reception-b", got, err)
	}
}

// Only rescue_operator may try codes, and a principal without it is refused
// before it takes a failure: it must neither guess nor lock itself out.
func TestRescueCodeRefusesPrincipalWithoutRescueRole(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	g := rescueCodeGateway(t, &now)
	code := issueRescueCode(t, g, "reception-a")
	for range specFailures + 1 {
		if got, err := g.rescueCodes.redeem(g.byLabel["owner"], code); !errors.Is(err, ErrRefused) || got != "" {
			t.Fatalf("resident_owner redeem = %q, %v; want ErrRefused", got, err)
		}
	}
	if _, err := g.rescueCodes.redeem(nil, code); !errors.Is(err, ErrRefused) {
		t.Fatalf("nil principal redeem = %v; want ErrRefused", err)
	}
	g.rescueCodes.mu.Lock()
	failures := len(g.rescueCodes.failures["owner"])
	g.rescueCodes.mu.Unlock()
	if failures != 0 {
		t.Fatalf("a refused principal left %d failures", failures)
	}
	if got, err := g.rescueCodes.redeem(g.byLabel["op1"], code); err != nil || got != "reception-a" {
		t.Fatalf("the code was spent by a refused principal: %q, %v", got, err)
	}
}

// 5 failures stop that principal for 10 minutes after the 5th, even when they
// were spread out, and stop no one else. While stopped it does not reach the
// codes at all, so a right guess is not spent either.
func TestRescueFailuresStopPrincipalForTenMinutes(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	now := start
	g := rescueCodeGateway(t, &now)
	op1, op2 := g.byLabel["op1"], g.byLabel["op2"]
	for i := range specFailures {
		now = start.Add(time.Duration(i) * time.Minute)
		if _, err := g.rescueCodes.redeem(op1, wrongRescueCode); !errors.Is(err, errRescueCodeInvalid) {
			t.Fatalf("failure %d = %v; want invalid", i+1, err)
		}
	}
	fifth := now
	code := issueRescueCode(t, g, "reception-a")
	if got, err := g.rescueCodes.redeem(op1, code); !errors.Is(err, errRescueLocked) || got != "" {
		t.Fatalf("op1 right after the 5th failure = %q, %v; want locked", got, err)
	}
	if got, err := g.rescueCodes.redeem(op2, code); err != nil || got != "reception-a" {
		t.Fatalf("op2 = %q, %v; want reception-a (op1's locked try must not spend it, nor stop op2)", got, err)
	}
	// 10 minutes after the first failure, but not after the 5th.
	for _, at := range []time.Time{start.Add(10 * time.Minute), fifth.Add(10*time.Minute - time.Nanosecond)} {
		now = at
		if _, err := g.rescueCodes.redeem(op1, wrongRescueCode); !errors.Is(err, errRescueLocked) {
			t.Fatalf("op1 at +%v = %v; want locked", at.Sub(start), err)
		}
	}
	now = fifth.Add(10 * time.Minute)
	if _, err := g.rescueCodes.redeem(op1, wrongRescueCode); !errors.Is(err, errRescueCodeInvalid) {
		t.Fatalf("op1 10 minutes after the 5th failure = %v; want a try again", err)
	}
}

// A success gives back only its own try. Erasing the earlier failures would
// let a principal that holds one real code keep guessing between its uses.
func TestRescueSuccessKeepsEarlierFailures(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	g := rescueCodeGateway(t, &now)
	op1 := g.byLabel["op1"]
	for i := range specFailures - 1 {
		if _, err := g.rescueCodes.redeem(op1, wrongRescueCode); !errors.Is(err, errRescueCodeInvalid) {
			t.Fatalf("failure %d = %v; want invalid", i+1, err)
		}
	}
	code := issueRescueCode(t, g, "reception-a")
	if got, err := g.rescueCodes.redeem(op1, code); err != nil || got != "reception-a" {
		t.Fatalf("success after %d failures = %q, %v", specFailures-1, got, err)
	}
	if _, err := g.rescueCodes.redeem(op1, wrongRescueCode); !errors.Is(err, errRescueCodeInvalid) {
		t.Fatalf("the success was counted as a failure: %v", err)
	}
	if _, err := g.rescueCodes.redeem(op1, wrongRescueCode); !errors.Is(err, errRescueLocked) {
		t.Fatalf("the success erased the earlier failures: %v", err)
	}
}

// A principal may hold 4 requests at once (MaxSessionsPerPrincipal). Taking a
// failure only after the match would let all 4 pass the check together; the
// window must still see at most 5 tries.
func TestRescueFailuresHoldUnderConcurrentAttempts(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	now := start
	g := rescueCodeGateway(t, &now)
	op1 := g.byLabel["op1"]
	for round := range 50 {
		now = start.Add(time.Duration(round) * time.Hour)
		var tried, started sync.WaitGroup
		var mu sync.Mutex
		results := map[error]int{}
		ready := make(chan struct{})
		for range specConcurrent {
			tried.Add(1)
			started.Add(1)
			go func() {
				defer tried.Done()
				started.Done()
				<-ready
				for range 4 {
					_, err := g.rescueCodes.redeem(op1, wrongRescueCode)
					mu.Lock()
					results[err]++
					mu.Unlock()
				}
			}()
		}
		started.Wait()
		close(ready)
		tried.Wait()
		if results[errRescueCodeInvalid] != specFailures || results[errRescueLocked] != 4*specConcurrent-specFailures {
			t.Fatalf("round %d: %v; want %d tries and the rest locked", round, results, specFailures)
		}
	}
}

// N, the valid codes at once, is a premise of the guessing calculation.
// Spent and expired codes must not count, or the cap would fill for good.
func TestRescueCodesStopAtHundredValid(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	now := start
	g := rescueCodeGateway(t, &now)
	var first string
	for i := range specValidCodes {
		code := issueRescueCode(t, g, fmt.Sprintf("reception-%d", i))
		if i == 0 {
			first = code
		}
	}
	if code, err := g.rescueCodes.issue("reception-over"); !errors.Is(err, errRescueCodesFull) || code != "" {
		t.Fatalf("issue over %d = %q, %v; want full", specValidCodes, code, err)
	}
	if _, err := g.rescueCodes.redeem(g.byLabel["op1"], first); err != nil {
		t.Fatal(err)
	}
	issueRescueCode(t, g, "reception-after-redeem")
	if _, err := g.rescueCodes.issue("reception-over"); !errors.Is(err, errRescueCodesFull) {
		t.Fatalf("issue over %d after one redeem = %v; want full", specValidCodes, err)
	}
	now = start.Add(5 * time.Minute)
	issueRescueCode(t, g, "reception-after-expiry")
}

var twelveDigits = regexp.MustCompile(`^[0-9]{12}$`)

// The code is 12 plain digits with no check digit, and the gateway keeps only
// a keyed digest: a dump of its state must not hand out live codes.
func TestRescueCodeIsTwelveDigitsAndNotKeptInPlain(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	g := rescueCodeGateway(t, &now)
	s := g.rescueCodes
	code := issueRescueCode(t, g, "reception-a")
	if !twelveDigits.MatchString(code) {
		t.Fatalf("code %q is not 12 digits", code)
	}
	guess := "123412341234"
	if _, err := s.redeem(g.byLabel["op1"], guess); !errors.Is(err, errRescueCodeInvalid) {
		t.Fatal(err)
	}
	s.mu.Lock()
	state := fmt.Sprintf("%#v %v %#v %v", s.codes, s.codes, s.failures, s.failures)
	_, kept := s.codes[s.digest(code)]
	s.mu.Unlock()
	for _, plain := range []string{code, guess} {
		if bytes.Contains([]byte(state), []byte(plain)) {
			t.Fatalf("the gateway state holds %q in plain", plain)
		}
	}
	if !kept {
		t.Fatal("the code is not kept under its digest, so the check above proves nothing")
	}
	// A key fixed in the source, or no key, would make every digest the same
	// across restarts and let anyone who reads one test it offline.
	if other := rescueCodeGateway(t, &now).rescueCodes; other.digest(code) == s.digest(code) {
		t.Fatal("two gateways derived the same digest; the key is not per start")
	}
}

// Values at or over 10^12 are drawn again, never folded back by a modulo, and
// a draw that equals a live code is drawn again instead of taking it over.
func TestRescueCodeDrawsUniformlyAndAvoidsLiveCodes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	g := rescueCodeGateway(t, &now)
	s := g.rescueCodes
	s.random = bytes.NewReader([]byte{
		0xff, 0xff, 0xff, 0xff, 0xff, // 2^40-1: out of range
		0, 0, 0, 0, 7, // reception-a
		0, 0, 0, 0, 7, // same as reception-a's live code
		0, 0, 0, 0, 9, // reception-b
	})
	if a := issueRescueCode(t, g, "reception-a"); a != "000000000007" {
		t.Fatalf("first code = %q; want 000000000007 (rejection, not modulo)", a)
	}
	if b := issueRescueCode(t, g, "reception-b"); b != "000000000009" {
		t.Fatalf("second code = %q; want 000000000009", b)
	}
	if got, err := s.redeem(g.byLabel["op1"], "000000000007"); err != nil || got != "reception-a" {
		t.Fatalf("reception-a's code = %q, %v; another reception took it over", got, err)
	}
}
