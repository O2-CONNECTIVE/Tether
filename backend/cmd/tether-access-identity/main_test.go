package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math/big"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// Every key, assertion, issuer, AUD tag, subject and address is a synthetic
// in-memory placeholder; no Access team, certs endpoint, account or person is
// named or contacted, and no file, socket or process is used. The verifier's
// own claim rules are covered by agentgateway's tests; these cases check that
// this entry reaches it and never prints anything it did not accept.

const (
	testIssuer    = "https://synthetic-team.example.invalid"
	testKid       = "synthetic-access"
	sentinelSub   = "synthetic-subject-sentinel"
	sentinelEmail = "member-sentinel@example.invalid"
	sentinelClaim = "synthetic-unverified-claim-sentinel"
)

var (
	testAUD  = strings.Repeat("5a", 32) // the shape of an Access AUD tag
	testNow  = time.Unix(1_900_000_000, 0)
	enc      = base64.RawURLEncoding
	keysOnce sync.Once
	signer   *rsa.PrivateKey // configured in publicJWKS
	stranger *rsa.PrivateKey // never configured
)

func keys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	keysOnce.Do(func() {
		var err1, err2 error
		signer, err1 = rsa.GenerateKey(rand.Reader, 2048)
		stranger, err2 = rsa.GenerateKey(rand.Reader, 2048)
		if err := errors.Join(err1, err2); err != nil {
			t.Fatal(err)
		}
	})
	return signer, stranger
}

// publicJWKS holds only the public members of the signer key.
func publicJWKS(t *testing.T) string {
	t.Helper()
	k, _ := keys(t)
	b, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": testKid, "use": "sig", "alg": "RS256",
		"n": enc.EncodeToString(k.N.Bytes()), "e": enc.EncodeToString(big.NewInt(int64(k.E)).Bytes())}}})
	return string(b)
}

func validArgs(t *testing.T) []string {
	t.Helper()
	return []string{"--issuer", testIssuer, "--aud", testAUD, "--jwks-json", publicJWKS(t)}
}

func header() map[string]any { return map[string]any{"alg": "RS256", "kid": testKid, "typ": "JWT"} }

// claims carry sentinel members that must never be printed.
func claims() map[string]any {
	return map[string]any{"iss": testIssuer, "aud": []string{testAUD}, "sub": sentinelSub, "email": sentinelEmail, "type": "app",
		"iat": testNow.Add(-time.Minute).Unix(), "nbf": testNow.Add(-time.Minute).Unix(), "exp": testNow.Add(10 * time.Minute).Unix(),
		"identity_nonce": sentinelClaim, "country": sentinelClaim}
}

func sign(t *testing.T, h, body map[string]any, key *rsa.PrivateKey) string {
	t.Helper()
	hb, _ := json.Marshal(h)
	cb, _ := json.Marshal(body)
	input := enc.EncodeToString(hb) + "." + enc.EncodeToString(cb)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + enc.EncodeToString(sig)
}

func assertion(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	c := claims()
	if edit != nil {
		edit(c)
	}
	k, _ := keys(t)
	return sign(t, header(), c, k)
}

// fakeInput serves src, then EOF, the tail error, or (block) waits for Close.
type fakeInput struct {
	src       io.Reader
	tail      error
	block     bool
	mu        sync.Mutex
	reads     int
	consumed  int
	waiting   chan struct{}
	closed    chan struct{}
	waitOnce  sync.Once
	closeOnce sync.Once
}

func newInput(src io.Reader) *fakeInput {
	return &fakeInput{src: src, waiting: make(chan struct{}), closed: make(chan struct{})}
}

func (f *fakeInput) Read(p []byte) (int, error) {
	n, err := f.src.Read(p)
	f.mu.Lock()
	f.reads++
	f.consumed += n
	f.mu.Unlock()
	if errors.Is(err, io.EOF) && f.tail != nil {
		return n, f.tail
	}
	if errors.Is(err, io.EOF) && f.block {
		f.waitOnce.Do(func() { close(f.waiting) })
		<-f.closed
		return n, errors.New("synthetic closed input " + sentinelClaim)
	}
	return n, err
}

func (f *fakeInput) Close() error {
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeInput) isClosed() bool {
	select {
	case <-f.closed:
		return true
	default:
		return false
	}
}

func (f *fakeInput) counts() (reads, consumed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads, f.consumed
}

type outcome struct {
	code           int
	stdout, stderr string
	budget         time.Duration // the requested read deadline; 0 if none
}

func run(t *testing.T, args []string, in *fakeInput, deadline <-chan time.Time) outcome {
	t.Helper()
	var stdout, stderr bytes.Buffer
	var budget time.Duration
	d := deps{input: in, clock: func() time.Time { return testNow }, after: func(b time.Duration) <-chan time.Time {
		budget = b
		return deadline
	}}
	code := execute(args, &stdout, &stderr, d)
	return outcome{code, stdout.String(), stderr.String(), budget}
}

// refused requires the exit code, nothing on stdout and exactly one fixed
// diagnostic, so no token, claim, key or error text can appear.
func refused(t *testing.T, o outcome, code int, message string) {
	t.Helper()
	if o.code != code || o.stdout != "" || o.stderr != message+"\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q", o.code, o.stdout, o.stderr)
	}
}

func TestVerifiedAssertionPrintsOnlyTheAllowlist(t *testing.T) {
	token := assertion(t, nil)
	k, _ := keys(t)
	want := map[string]any{"verified": true, "issuer": testIssuer, "subject": sentinelSub, "expected_aud": testAUD,
		"email": sentinelEmail, "expires_at": testNow.Add(10 * time.Minute).UTC().Format(time.RFC3339)}
	for _, tc := range []struct {
		name string
		src  io.Reader
	}{
		{"bare", strings.NewReader(token)},
		{"missing optional typ", strings.NewReader(sign(t, map[string]any{"alg": "RS256", "kid": testKid}, claims(), k))},
		{"terminal newline", strings.NewReader(token + "\n")},
		{"one byte per read", iotest.OneByteReader(strings.NewReader(token + "\n"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := newInput(tc.src)
			o := run(t, validArgs(t), in, nil)
			if o.code != 0 || o.stderr != "" || o.budget != 30*time.Second || !in.isClosed() {
				t.Fatalf("exit %d, stderr %q, budget %v, closed %v", o.code, o.stderr, o.budget, in.isClosed())
			}
			var got map[string]any
			if strings.Count(o.stdout, "\n") != 1 || !strings.HasSuffix(o.stdout, "\n") || json.Unmarshal([]byte(o.stdout), &got) != nil || !maps.Equal(got, want) {
				t.Fatalf("stdout = %q", o.stdout)
			}
			for _, leak := range append(strings.Split(token, "."), sentinelClaim, "eyJ", enc.EncodeToString(k.N.Bytes())) {
				if strings.Contains(o.stdout, leak) {
					t.Fatalf("stdout discloses %q", leak)
				}
			}
		})
	}
}

func TestUnverifiedAssertionsNeverSucceed(t *testing.T) {
	signerKey, strangerKey := keys(t)
	parts := strings.Split(assertion(t, nil), ".")
	forgedClaims := claims()
	forgedClaims["sub"] = "synthetic-forged-subject"
	forged, _ := json.Marshal(forgedClaims)
	unknownKid := header()
	unknownKid["kid"] = "synthetic-unknown"
	for _, tc := range []struct {
		name, token string
	}{
		{"other signing key", sign(t, header(), claims(), strangerKey)},
		{"tampered payload", parts[0] + "." + enc.EncodeToString(forged) + "." + parts[2]},
		{"unknown kid", sign(t, unknownKid, claims(), signerKey)},
		{"other issuer", assertion(t, func(c map[string]any) { c["iss"] = "https://other-team.example.invalid" })},
		{"other AUD", assertion(t, func(c map[string]any) { c["aud"] = []string{strings.Repeat("5b", 32)} })},
		{"AUD as a string", assertion(t, func(c map[string]any) { c["aud"] = testAUD })},
		{"expired", assertion(t, func(c map[string]any) {
			c["iat"], c["nbf"], c["exp"] = testNow.Add(-20*time.Minute).Unix(), testNow.Add(-20*time.Minute).Unix(), testNow.Add(-2*time.Minute).Unix()
		})},
		{"not yet valid", assertion(t, func(c map[string]any) { c["nbf"] = testNow.Add(2 * time.Minute).Unix() })},
		{"issued in the future", assertion(t, func(c map[string]any) { c["iat"] = testNow.Add(2 * time.Minute).Unix() })},
		{"service token", assertion(t, func(c map[string]any) {
			c["sub"], c["common_name"] = "", "synthetic-client.access"
			delete(c, "email")
		})},
		{"service identity with user sub", assertion(t, func(c map[string]any) { c["common_name"] = "synthetic-client.access" })},
		{"empty subject", assertion(t, func(c map[string]any) { c["sub"] = "" })},
		{"organization token", assertion(t, func(c map[string]any) { c["type"] = "org" })},
		{"missing email", assertion(t, func(c map[string]any) { delete(c, "email") })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := newInput(strings.NewReader(tc.token + "\n"))
			refused(t, run(t, validArgs(t), in, nil), 1, msgVerify)
			if !in.isClosed() {
				t.Fatal("input not closed")
			}
		})
	}
}

// Exactly one compact token with at most one terminal "\n" reaches the
// verifier, and no more than the input budget is read.
func TestInputFramingAndBound(t *testing.T) {
	token := assertion(t, nil)
	maxShape := strings.Repeat("a", agentgateway.MaxTokenBytes-4) + ".a.a"
	for _, tc := range []struct {
		name, input, message string
	}{
		{"empty", "", msgInput},
		{"newline only", "\n", msgInput},
		{"two newlines", token + "\n\n", msgInput},
		{"CRLF", token + "\r\n", msgInput},
		{"leading space", " " + token, msgInput},
		{"trailing space", token + " ", msgInput},
		{"two lines", token + "\n" + token + "\n", msgInput},
		{"two tokens joined", token + token, msgInput},
		{"two tokens with a comma", token + "," + token, msgInput},
		{"extra data after the newline", token + "\n" + sentinelClaim, msgInput},
		{"one segment", "synthetic", msgInput},
		{"padded segment", token + "=", msgInput},
		{"maximal shape reaches the verifier", maxShape + "\n", msgVerify},
		{"one byte over the token budget", "a" + maxShape, msgInput},
		{"far over the input budget", strings.Repeat("a", 4*agentgateway.MaxTokenBytes), msgInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := newInput(strings.NewReader(tc.input))
			refused(t, run(t, validArgs(t), in, nil), 1, tc.message)
			if _, consumed := in.counts(); !in.isClosed() || consumed > maxInput+1 {
				t.Fatalf("closed %v, consumed %d", in.isClosed(), consumed)
			}
		})
	}
}

func TestReadFailureAndDeadlineCloseInput(t *testing.T) {
	token := assertion(t, nil)
	t.Run("read error after a valid assertion", func(t *testing.T) {
		in := newInput(strings.NewReader(token + "\n"))
		in.tail = errors.New("synthetic read failure " + token)
		refused(t, run(t, validArgs(t), in, nil), 1, msgInput)
		if !in.isClosed() {
			t.Fatal("input not closed")
		}
	})
	for _, tc := range []struct{ name, input string }{
		{"no input", ""},
		{"valid assertion without EOF", token + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := newInput(strings.NewReader(tc.input))
			in.block = true
			deadline := make(chan time.Time, 1)
			go func() {
				<-in.waiting
				deadline <- testNow
			}()
			o := run(t, validArgs(t), in, deadline)
			refused(t, o, 1, msgTimeout)
			if !in.isClosed() || o.budget != 30*time.Second {
				t.Fatalf("closed %v, budget %v", in.isClosed(), o.budget)
			}
		})
	}
}

// Flags and trust inputs are refused before the deadline starts or stdin is
// read; an assertion offered as an argument is never echoed.
func TestArgumentsAndTrustInputsAreCheckedBeforeStdin(t *testing.T) {
	token := assertion(t, nil)
	jwks := publicJWKS(t)
	with := func(i int, v string) []string {
		a := validArgs(t)
		a[i] = v
		return a
	}
	private := strings.Replace(jwks, `"kty"`, `"d":"c3ludGhldGlj","kty"`, 1)
	for _, tc := range []struct {
		name    string
		args    []string
		message string
	}{
		{"no arguments", nil, msgUsage},
		{"assertion as a positional argument", []string{token}, msgUsage},
		{"flags and a positional assertion", append(validArgs(t), token), msgUsage},
		{"assertion flag", []string{"--issuer", testIssuer, "--aud", testAUD, "--token", token}, msgUsage},
		{"equals form", []string{"--issuer=" + testIssuer, "--aud", testAUD, "--jwks-json", jwks, "--aud"}, msgUsage},
		{"single dash", with(0, "-issuer"), msgUsage},
		{"duplicate flag", with(2, "--issuer"), msgUsage},
		{"missing value", validArgs(t)[:5], msgUsage},
		{"JWK Set path", with(5, "/synthetic/protected/access-certs.json"), msgConfig},
		{"JWK Set with a private member", with(5, private), msgConfig},
		{"empty JWK Set", with(5, `{"keys":[]}`), msgConfig},
		{"issuer with slash", with(1, testIssuer+"/"), msgConfig},
		{"http issuer", with(1, "http://synthetic-team.example.invalid"), msgConfig},
		{"empty AUD", with(3, ""), msgConfig},
		{"AUD is a URL", with(3, "https://gateway.example.invalid/mcp"), msgConfig},
		{"assertion as AUD", with(3, token), msgConfig},
		{"assertion as issuer", with(1, token), msgConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := newInput(strings.NewReader(token + "\n"))
			o := run(t, tc.args, in, nil)
			refused(t, o, 2, tc.message)
			if reads, _ := in.counts(); reads != 0 || in.isClosed() || o.budget != 0 {
				t.Fatalf("reads %d, closed %v, budget %v", reads, in.isClosed(), o.budget)
			}
		})
	}

	in := newInput(strings.NewReader(token + "\n"))
	clock := func() time.Time { return testNow }
	after := func(time.Duration) <-chan time.Time { return nil }
	for _, d := range []deps{{clock: clock, after: after}, {input: in, after: after}, {input: in, clock: clock}} {
		var stderr bytes.Buffer
		if code := execute(validArgs(t), io.Discard, &stderr, d); code != 2 || stderr.String() != msgUsage+"\n" {
			t.Fatalf("missing dependency: exit %d, stderr %q", code, stderr.String())
		}
	}
	var stderr bytes.Buffer
	if code := execute(validArgs(t), nil, &stderr, deps{input: in, clock: clock, after: after}); code != 2 || stderr.String() != msgUsage+"\n" {
		t.Fatalf("missing stdout: exit %d, stderr %q", code, stderr.String())
	}
	if reads, _ := in.counts(); reads != 0 {
		t.Fatalf("reads %d", reads)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("synthetic write failure") }

func TestOutputFailureIsReported(t *testing.T) {
	in := newInput(strings.NewReader(assertion(t, nil)))
	var stderr bytes.Buffer
	d := deps{input: in, clock: func() time.Time { return testNow }, after: func(time.Duration) <-chan time.Time { return nil }}
	if code := execute(validArgs(t), failingWriter{}, &stderr, d); code != 1 || stderr.String() != msgOutput+"\n" {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestNativeDepsUseStdinAndFixedBudget(t *testing.T) {
	d := nativeDeps()
	if d.input != os.Stdin || d.clock == nil || d.after == nil || readBudget != 30*time.Second || leeway > agentgateway.MaxLeeway ||
		maxInput != agentgateway.MaxTokenBytes+1 {
		t.Fatal("native dependencies or budgets changed")
	}
}
