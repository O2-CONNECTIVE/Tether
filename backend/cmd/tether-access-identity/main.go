// tether-access-identity inspects one Cloudflare Access application assertion
// for one-time identity enrollment. It builds the existing
// agentgateway.AccessVerifier from the public flags --issuer, --aud and
// --jwks-json (a literal public JWK Set, not a path), and only then reads
// exactly one compact assertion from stdin. After Verify succeeds it prints a
// fixed JSON allowlist (verified, issuer, subject, expected_aud, email,
// expires_at). It grants nothing: it is not a command authorization path, the
// printed email is display-only, and no subject is registered anywhere.
//
// It reads no environment, file or network, starts no process, issues no
// token and writes nothing but stdout and stderr. Diagnostics are fixed
// messages; no token, header, claim, key or error text is printed. See
// docs/tether-access-identity.md.
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

const (
	readBudget = 30 * time.Second // the whole stdin read, from before the first Read
	leeway     = 30 * time.Second // clock skew tolerance, within agentgateway.MaxLeeway
	// maxInput is one maximal assertion and one terminal newline.
	maxInput = agentgateway.MaxTokenBytes + 1
)

const (
	msgUsage   = "使い方: tether-access-identity --issuer <https://team-domain> --aud <AUD tag> --jwks-json <公開JWK Set JSON>（assertionはstdinから1件だけ）"
	msgConfig  = "issuer、AUD、JWK Setのいずれかが不正です。stdinを読みません"
	msgInput   = "stdinから単一のAccess assertionを読めませんでした。識別情報を出力しません"
	msgTimeout = "stdinの読取りが期限を超えました。入力を閉じ、識別情報を出力しません"
	msgVerify  = "Access assertionを検証できませんでした。識別情報を出力しません"
	msgOutput  = "結果を出力できませんでした"
)

var (
	errOversize = errors.New("input exceeds the assertion budget")
	errTimeout  = errors.New("input read deadline")
	b64         = base64.RawURLEncoding.Strict()
)

// deps are the native operations. Production always uses nativeDeps; tests
// substitute an in-memory input, a fixed clock and a controlled deadline.
type deps struct {
	input io.ReadCloser
	clock func() time.Time
	after func(time.Duration) <-chan time.Time
}

func nativeDeps() deps {
	return deps{input: os.Stdin, clock: time.Now, after: time.After}
}

func main() {
	os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr, nativeDeps()))
}

// inspection is the whole output. Nothing else from the assertion is printed.
type inspection struct {
	Verified    bool   `json:"verified"`
	Issuer      string `json:"issuer"`
	Subject     string `json:"subject"`
	ExpectedAUD string `json:"expected_aud"`
	Email       string `json:"email"`
	ExpiresAt   string `json:"expires_at"`
}

// execute refuses with exit 2 before any stdin read when the flags or the
// trust inputs are not exact; every later refusal exits 1 with nothing on out.
func execute(args []string, out, diagnostic io.Writer, d deps) int {
	issuer, aud, jwks, ok := parseArgs(args)
	if !ok || out == nil || d.input == nil || d.clock == nil || d.after == nil {
		report(diagnostic, msgUsage)
		return 2
	}
	v, err := agentgateway.NewAccessVerifier(agentgateway.AccessVerifierConfig{Issuer: issuer, AUD: aud, Keys: jwks, Leeway: leeway, Clock: d.clock})
	if err != nil {
		report(diagnostic, msgConfig)
		return 2
	}
	raw, err := readInput(d.input, d.after)
	if err != nil {
		if errors.Is(err, errTimeout) {
			report(diagnostic, msgTimeout)
		} else {
			report(diagnostic, msgInput)
		}
		return 1
	}
	compact, ok := compactToken(raw)
	if !ok {
		wipe(raw)
		report(diagnostic, msgInput)
		return 1
	}
	// The string is an immutable copy that cannot be cleared; only raw is.
	token := string(compact)
	wipe(raw)
	id, err := v.Verify(token)
	if err != nil {
		report(diagnostic, msgVerify)
		return 1
	}
	email, ok := verifiedEmail(token)
	if !ok {
		report(diagnostic, msgVerify)
		return 1
	}
	// Verify required aud, exactly as configured, in the assertion's "aud".
	b, err := json.Marshal(inspection{Verified: true, Issuer: id.Issuer, Subject: id.Subject, ExpectedAUD: aud, Email: email,
		ExpiresAt: id.Expiry.UTC().Format(time.RFC3339)})
	if err != nil {
		report(diagnostic, msgOutput)
		return 1
	}
	if _, err := out.Write(append(b, '\n')); err != nil {
		report(diagnostic, msgOutput)
		return 1
	}
	return 0
}

// parseArgs accepts exactly the three public flags, each once, as
// "--name value" pairs in any order. There is no positional argument, "="
// form, single-dash form or flag that carries the assertion.
func parseArgs(args []string) (issuer, aud string, jwks []byte, ok bool) {
	if len(args) != 6 {
		return "", "", nil, false
	}
	seen := map[string]string{}
	for i := 0; i < len(args); i += 2 {
		switch name := args[i]; name {
		case "--issuer", "--aud", "--jwks-json":
			if _, dup := seen[name]; dup {
				return "", "", nil, false
			}
			seen[name] = args[i+1]
		default:
			return "", "", nil, false
		}
	}
	return seen["--issuer"], seen["--aud"], []byte(seen["--jwks-json"]), true
}

// readInput reads in to EOF within readBudget into a buffer one byte larger
// than maxInput, so that a full buffer means oversized input. in is closed
// once the read ends or the deadline passes. A blocking stdin may not return
// from Read when closed, so the reader goroutine is not joined after the
// deadline: it clears its abandoned buffer at its next read result, or ends
// with the process. The caller owns and clears the returned buffer.
func readInput(in io.ReadCloser, after func(time.Duration) <-chan time.Time) ([]byte, error) {
	deadline := after(readBudget)
	buf := make([]byte, maxInput+1)
	type result struct {
		n   int
		err error
	}
	done, abandoned := make(chan result), make(chan struct{})
	go func() {
		n, err := fill(in, buf)
		select {
		case done <- result{n, err}:
		case <-abandoned:
			wipe(buf)
		}
	}()
	select {
	case r := <-done:
		_ = in.Close()
		if r.err != nil {
			wipe(buf)
			return nil, r.err
		}
		return buf[:r.n], nil
	case <-deadline:
		close(abandoned)
		_ = in.Close()
		return nil, errTimeout
	}
}

// fill reads until EOF; a full buffer or any other read error fails.
func fill(in io.Reader, buf []byte) (int, error) {
	n := 0
	for {
		if n == len(buf) {
			return n, errOversize
		}
		m, err := in.Read(buf[n:])
		n += m
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
	}
}

// compactToken removes at most one terminal "\n" and requires the rest to be
// 1..MaxTokenBytes of base64url text with exactly two dots. Whitespace, a CR,
// a second line or a second token is refused here; the JWS itself is checked
// only by AccessVerifier.Verify.
func compactToken(raw []byte) ([]byte, bool) {
	if n := len(raw); n > 0 && raw[n-1] == '\n' {
		raw = raw[:n-1]
	}
	if len(raw) == 0 || len(raw) > agentgateway.MaxTokenBytes {
		return nil, false
	}
	dots := 0
	for _, c := range raw {
		switch {
		case c == '.':
			dots++
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return nil, false
		}
	}
	return raw, dots == 2
}

// verifiedEmail reads "email" from the payload of a token that Verify has
// already accepted, so it is the exact signed payload. Verify required the
// member as a string with an '@' after its first byte; it is display-only and
// never identifies or authorizes anyone.
func verifiedEmail(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	payload, err := b64.DecodeString(parts[1])
	defer wipe(payload)
	if err != nil {
		return "", false
	}
	c, err := agentwire.Fields(payload)
	if err != nil {
		return "", false
	}
	raw := c["email"]
	var email string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &email) != nil || strings.IndexByte(email, '@') < 1 {
		return "", false
	}
	return email, true
}

func report(w io.Writer, message string) {
	if w != nil {
		fmt.Fprintln(w, message)
	}
}

// wipe clears a mutable buffer; best effort, it does not reach copies made by
// the runtime or the immutable token string.
func wipe(b []byte) {
	clear(b[:cap(b)])
}
