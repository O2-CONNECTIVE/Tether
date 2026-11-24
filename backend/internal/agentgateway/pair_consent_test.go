package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	testIssuer      = "https://issuer.example.invalid/"
	testResource    = "https://gateway.example.invalid/mcp"
	testScope       = "rescue.agent"
	pairingAudience = "https://gateway.example.invalid/tether-agent/v1/pair"
)

var (
	subjects  = []string{"synthetic-subject-1", "synthetic-subject-2", "synthetic-subject-3", "synthetic-subject-4"}
	issuerKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize))
	bothRoles = []Role{RoleRescueOperator, RoleResidentOwner}
)

func fixtureSigner(t *testing.T, seed byte) ssh.Signer {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func jwks(t *testing.T) []byte {
	t.Helper()
	public := issuerKey.Public().(ed25519.PublicKey)
	raw, err := json.Marshal(map[string]any{"keys": []any{map[string]any{
		"kty": "OKP", "crv": "Ed25519", "kid": "test", "alg": "EdDSA",
		"x": base64.RawURLEncoding.EncodeToString(public),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func edHeader() map[string]any { return map[string]any{"alg": "EdDSA", "kid": "test", "typ": "at+jwt"} }

func sign(t *testing.T, header, claims map[string]any, key ed25519.PrivateKey) string {
	t.Helper()
	encodedHeader, _ := json.Marshal(header)
	encodedClaims, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(encodedHeader) + "." + base64.RawURLEncoding.EncodeToString(encodedClaims)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
}

func claims(subject string) map[string]any {
	now := time.Now()
	return map[string]any{"iss": testIssuer, "aud": testResource, "sub": subject, "scope": testScope, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()}
}

// consentIDP is a local synthetic issuer. Its authorization endpoint returns
// a code immediately so these tests exercise the real browser redirect and
// cookie path without contacting an external identity provider.
type consentIDP struct {
	mu         sync.Mutex
	server     *httptest.Server
	authorize  url.Values
	tokenForms []url.Values
	codeTokens map[string]string
	nextCode   string
}

func newConsentIDP(t *testing.T, subject string) *consentIDP {
	t.Helper()
	i := &consentIDP{codeTokens: map[string]string{"same": consentToken(t, subject), "other": consentToken(t, subjects[1])}, nextCode: "same"}
	i.server = newPairTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/authorize":
			i.mu.Lock()
			i.authorize = r.URL.Query()
			code := i.nextCode
			i.nextCode = "same"
			i.mu.Unlock()
			callback, err := url.Parse(r.URL.Query().Get("redirect_uri"))
			if err != nil || callback.Scheme != "https" || callback.Host == "" {
				http.Error(w, "bad redirect", http.StatusBadRequest)
				return
			}
			q := callback.Query()
			q.Set("code", code)
			q.Set("state", r.URL.Query().Get("state"))
			callback.RawQuery = q.Encode()
			http.Redirect(w, r, callback.String(), http.StatusFound)
		case "/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			i.mu.Lock()
			i.tokenForms = append(i.tokenForms, r.Form)
			token := i.codeTokens[r.Form.Get("code")]
			i.mu.Unlock()
			if token == "" {
				http.Error(w, "unknown code", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token, "token_type": "Bearer"})
		default:
			http.NotFound(w, r)
		}
	}))
	return i
}

func newPairTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

type pairConsentFixture struct {
	t           *testing.T
	idp         *consentIDP
	server      *httptest.Server
	client      *http.Client
	transport   *http.Transport
	g           *Gateway
	base        string
	saveCount   atomic.Int32
	disconnects atomic.Int32
	persisted   []PairRecord
	persistMu   sync.Mutex
	saveResult  error
	subject     string
	clockNow    time.Time
	keySequence atomic.Uint32
}

func newPairConsentFixture(t *testing.T, subject string) *pairConsentFixture {
	t.Helper()
	f := &pairConsentFixture{t: t, subject: subject, clockNow: time.Now().UTC()}
	f.idp = newConsentIDP(t, subject)
	f.server = newPairTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.serve(w, r) }))
	f.base = f.server.URL
	return f
}

func (f *pairConsentFixture) initialize(t *testing.T, records []PairRecord, saveResult error) {
	t.Helper()
	// The test servers use self-signed certificates. Trust only these two
	// loopback certificates, preserving normal TLS verification.
	roots := x509.NewCertPool()
	roots.AddCert(f.server.Certificate())
	roots.AddCert(f.idp.server.Certificate())
	f.transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	f.client = &http.Client{Transport: f.transport, Jar: jar, Timeout: 5 * time.Second}
	f.saveResult = saveResult
	f.persisted = clonePairRecords(records)
	f.newGateway(t, records)
}

func (f *pairConsentFixture) newGateway(t *testing.T, records []PairRecord) {
	t.Helper()
	mcpVerifier, err := NewVerifier(VerifierConfig{Issuer: testIssuer, Audience: testResource, Scope: testScope, Types: []string{"at+jwt"}, JWKS: jwks(t)})
	if err != nil {
		t.Fatal(err)
	}
	pairVerifier, err := NewVerifier(VerifierConfig{Issuer: testIssuer, Audience: pairingAudience, Types: []string{"at+jwt"}, JWKS: jwks(t)})
	if err != nil {
		t.Fatal(err)
	}
	principalSigner := fixtureSigner(t, 0x62)
	principal := Principal{Label: "member-1", Subject: f.subject, Roles: bothRoles, Signer: principalSigner}
	if f.subject == subjects[0] {
		principal.Subject = subjects[0]
	}
	g, err := New(Config{Verifier: mcpVerifier, Resource: testResource, Principals: []Principal{principal}, Clock: func() time.Time { return f.clockNow }})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.ConfigureHosts([]HostEntry{{Label: "host-1", Principals: []string{"member-1"}, Paired: true}}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: f.transport, Timeout: 5 * time.Second}
	if err := g.ConfigurePairing(PairingConfig{
		Verifier: pairVerifier, Endpoint: f.base + PairPath,
		RegistrationEndpoint: "wss://gateway.example.invalid/register", Records: records,
		Consent: &PairConsentConfig{ClientID: "pair-client", AuthorizationEndpoint: f.idp.server.URL + "/authorize", TokenEndpoint: f.idp.server.URL + "/token", HTTPClient: client},
		Save: func(next []PairRecord) error {
			f.saveCount.Add(1)
			if f.saveResult == nil || errors.Is(f.saveResult, ErrRegistryDurabilityUnknown) {
				f.persistMu.Lock()
				f.persisted = clonePairRecords(next)
				f.persistMu.Unlock()
			}
			return f.saveResult
		},
		Disconnect: func(string) { f.disconnects.Add(1) },
	}); err != nil {
		t.Fatal(err)
	}
	f.g = g
	t.Cleanup(func() {
		_ = g.Close(context.Background())
		f.transport.CloseIdleConnections()
	})
}

func (f *pairConsentFixture) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case PairPath:
		f.g.ServePair(w, r)
	case PairOperationsPath:
		f.g.ServePairOperations(w, r)
	case PairConsentPath:
		f.g.ServePairConsent(w, r)
	case PairConsentCallbackPath:
		f.g.ServePairConsentCallback(w, r)
	case PairConfirmPath:
		f.g.ServePairConfirm(w, r)
	default:
		http.NotFound(w, r)
	}
}

func newPairConsentFixtureReady(t *testing.T, subject string, records []PairRecord, saveResult error) *pairConsentFixture {
	t.Helper()
	f := newPairConsentFixture(t, subject)
	f.initialize(t, records, saveResult)
	return f
}

func consentToken(t *testing.T, subject string) string {
	t.Helper()
	claims := map[string]any{"iss": testIssuer, "aud": pairingAudience, "sub": subject, "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix()}
	return sign(t, edHeader(), claims, issuerKey)
}

func (f *pairConsentFixture) newHost(t *testing.T) ssh.Signer {
	t.Helper()
	seed := byte(0x80 + f.keySequence.Add(1))
	return fixtureSigner(t, seed)
}

func (f *pairConsentFixture) postOperation(t *testing.T, signer ssh.Signer, token string, proofSigner ssh.Signer) (*http.Response, map[string]any) {
	t.Helper()
	if proofSigner == nil {
		proofSigner = signer
	}
	proof, err := proofSigner.Sign(nil, PairOperationTranscript(f.base+PairOperationsPath, signer.PublicKey().Marshal(), sha256.Sum256([]byte(token))))
	if err != nil {
		t.Fatal(err)
	}
	key := strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(signer.PublicKey())), "\n")
	body, _ := json.Marshal(map[string]string{"host_public_key": key, "proof": base64.RawURLEncoding.EncodeToString(ssh.Marshal(proof))})
	req, _ := http.NewRequest(http.MethodPost, f.base+PairOperationsPath, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return resp, result
}

func (f *pairConsentFixture) newOperation(t *testing.T) (string, ssh.Signer) {
	t.Helper()
	signer := f.newHost(t)
	resp, body := f.postOperation(t, signer, consentToken(t, f.subject), signer)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operation status = %d, body=%v", resp.StatusCode, body)
	}
	id, _ := body["operation"].(string)
	if id == "" {
		t.Fatalf("operation missing: %v", body)
	}
	return id, signer
}

func (f *pairConsentFixture) consentPage(t *testing.T, id string) string {
	t.Helper()
	resp, err := f.client.Get(f.base + PairConsentPath + "?operation=" + url.QueryEscape(id))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("consent page status = %d, body=%s", resp.StatusCode, body)
	}
	return string(body)
}

func (f *pairConsentFixture) formToken(t *testing.T, id string) string {
	t.Helper()
	page := f.consentPage(t, id)
	re := regexp.MustCompile(`name="form_token" value="([^"]+)"`)
	match := re.FindStringSubmatch(page)
	if len(match) != 2 {
		t.Fatalf("form token missing from page: %s", page)
	}
	return match[1]
}

func (f *pairConsentFixture) decide(t *testing.T, id, token, decision string) *http.Response {
	t.Helper()
	form := url.Values{"operation": {id}, "form_token": {token}, "decision": {decision}}
	req, _ := http.NewRequest(http.MethodPost, f.base+PairConsentPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (f *pairConsentFixture) confirm(t *testing.T, id string, signer, proofSigner ssh.Signer) *http.Response {
	t.Helper()
	if proofSigner == nil {
		proofSigner = signer
	}
	return f.confirmWithProofID(t, id, id, signer, proofSigner)
}

func (f *pairConsentFixture) confirmWithProofID(t *testing.T, requestID, proofID string, signer, proofSigner ssh.Signer) *http.Response {
	t.Helper()
	proof, err := proofSigner.Sign(nil, PairConfirmTranscript(f.base+PairConfirmPath, proofID, signer.PublicKey().Marshal()))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"operation": requestID, "proof": base64.RawURLEncoding.EncodeToString(ssh.Marshal(proof))})
	req, _ := http.NewRequest(http.MethodPost, f.base+PairConfirmPath, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func responseError(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return body["error"]
}

func recordForConsentTest(t *testing.T, signer ssh.Signer) PairRecord {
	t.Helper()
	return PairRecord{HostLabel: "host-1", HostKey: signer.PublicKey().Marshal(), Issuer: testIssuer, Subject: subjects[0], RegisteredAt: time.Now().UTC().Truncate(time.Second)}
}

func TestPairOperationsPreviewDoesNotWriteRegistry(t *testing.T) {
	// Preview must show exactly what the account will approve without creating a registry change.
	t.Run("new slot", func(t *testing.T) {
		f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
		host := f.newHost(t)
		resp, body := f.postOperation(t, host, consentToken(t, f.subject), host)
		defer resp.Body.Close()
		account, _ := body["account"].(map[string]any)
		if resp.StatusCode != http.StatusOK || body["host_label"] != "host-1" || body["new_key_fingerprint"] != ssh.FingerprintSHA256(host.PublicKey()) || body["consent_url"] != f.base+PairConsentPath+"?operation="+url.QueryEscape(body["operation"].(string)) || body["replacing"] != nil || account["issuer"] != testIssuer || account["subject"] != subjects[0] || account["principal_label"] != "member-1" || f.saveCount.Load() != 0 {
			t.Fatalf("preview status=%d body=%v saves=%d", resp.StatusCode, body, f.saveCount.Load())
		}
		if len(body["operation"].(string)) == 0 || len(body["expires_at"].(string)) == 0 {
			t.Fatalf("preview missing operation or expiry: %v", body)
		}
	})
	t.Run("existing slot", func(t *testing.T) {
		old := recordForConsentTest(t, fixtureSigner(t, 0x79))
		f := newPairConsentFixtureReady(t, subjects[0], []PairRecord{old}, nil)
		resp, body := f.postOperation(t, f.newHost(t), consentToken(t, f.subject), nil)
		defer resp.Body.Close()
		replacing, _ := body["replacing"].(map[string]any)
		if resp.StatusCode != http.StatusOK || replacing["fingerprint"] != ssh.FingerprintSHA256(mustPairPublicKey(t, old.HostKey)) || replacing["registered_at"] != old.RegisteredAt.Format(time.RFC3339) || f.saveCount.Load() != 0 {
			t.Fatalf("existing preview status=%d replacing=%v saves=%d", resp.StatusCode, replacing, f.saveCount.Load())
		}
	})
}

func mustPairPublicKey(t *testing.T, raw []byte) ssh.PublicKey {
	t.Helper()
	key, err := ssh.ParsePublicKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestPairOperationsBusySlotDoesNotCreateOperation(t *testing.T) {
	// A busy slot cannot produce a consentable operation that is certain to fail later.
	f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
	f.g.mu.Lock()
	f.g.byLabel["member-1"].connecting = true
	f.g.mu.Unlock()
	resp, body := f.postOperation(t, f.newHost(t), consentToken(t, f.subject), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || body["error"] != "replace_blocked" || body["operation"] != nil || f.saveCount.Load() != 0 {
		t.Fatalf("busy preview status=%d body=%v saves=%d", resp.StatusCode, body, f.saveCount.Load())
	}
}

func TestPairOperationsRejectsBadProofAndInvalidOrMCPToken(t *testing.T) {
	// Both proof ownership and the dedicated pairing audience prevent a bearer-only enrollment.
	f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
	host, other := f.newHost(t), f.newHost(t)
	bad, _ := f.postOperation(t, host, consentToken(t, f.subject), other)
	if bad.StatusCode != http.StatusBadRequest {
		bad.Body.Close()
		t.Fatalf("bad proof status = %d, want 400", bad.StatusCode)
	}
	bad.Body.Close()
	invalid, _ := f.postOperation(t, host, "not-a-token", host)
	if invalid.StatusCode != http.StatusUnauthorized {
		invalid.Body.Close()
		t.Fatalf("invalid token status = %d, want 401", invalid.StatusCode)
	}
	invalid.Body.Close()
	mcp := sign(t, edHeader(), claims(subjects[0]), issuerKey)
	wrongAudience, _ := f.postOperation(t, host, mcp, host)
	defer wrongAudience.Body.Close()
	if wrongAudience.StatusCode != http.StatusUnauthorized {
		t.Fatalf("MCP token status = %d, want 401", wrongAudience.StatusCode)
	}
}

func TestPairConsentGETSetsCookieAndStartsPKCES256AtPublicOrigin(t *testing.T) {
	// OAuth state, PKCE, and the redirect URI must all be bound to the configured public gateway origin.
	f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
	id, _ := f.newOperation(t)
	client := *f.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Get(f.base + PairConsentPath + "?operation=" + url.QueryEscape(id))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "__Host-tether-pair-consent" {
			cookie = c
		}
	}
	location, _ := url.Parse(resp.Header.Get("Location"))
	query := location.Query()
	f.idp.mu.Lock()
	seen := f.idp.authorize
	f.idp.mu.Unlock()
	if resp.StatusCode != http.StatusFound || cookie == nil || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" || location.Scheme != "https" || location.Host != strings.TrimPrefix(f.idp.server.URL, "https://") || query.Get("client_id") != "pair-client" || query.Get("response_type") != "code" || query.Get("redirect_uri") != f.base+PairConsentCallbackPath || query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" || query.Get("state") == "" || len(seen) != 0 {
		t.Fatalf("OAuth start status=%d cookie=%+v location=%s seen=%v", resp.StatusCode, cookie, location, seen)
	}
	continued, err := f.client.Get(location.String())
	if err != nil {
		t.Fatal(err)
	}
	continued.Body.Close()
	f.idp.mu.Lock()
	seen, tokenForms := f.idp.authorize, append([]url.Values(nil), f.idp.tokenForms...)
	f.idp.mu.Unlock()
	if continued.StatusCode != http.StatusOK || len(tokenForms) != 1 || tokenForms[0].Get("redirect_uri") != f.base+PairConsentCallbackPath || tokenForms[0].Get("client_id") != "pair-client" || tokenForms[0].Get("code_verifier") == "" {
		t.Fatalf("callback status=%d authorization=%v token forms=%v", continued.StatusCode, seen, tokenForms)
	}
	challenge := sha256.Sum256([]byte(tokenForms[0].Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(challenge[:]) != seen.Get("code_challenge") {
		t.Fatalf("PKCE verifier does not match challenge: authorize=%v token=%v", seen, tokenForms[0])
	}
}

func TestPairConsentCallbackRejectsAccountAndCookieMismatch(t *testing.T) {
	// A different IdP account or a browser without the originating cookie must not receive a decision token.
	t.Run("different account", func(t *testing.T) {
		f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
		id, _ := f.newOperation(t)
		f.idp.mu.Lock()
		f.idp.nextCode = "other"
		f.idp.mu.Unlock()
		resp, err := f.client.Get(f.base + PairConsentPath + "?operation=" + url.QueryEscape(id))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		page, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusForbidden || strings.Contains(string(page), "form_token") {
			t.Fatalf("account mismatch status=%d page=%s", resp.StatusCode, page)
		}
	})
	t.Run("missing and mismatched cookie", func(t *testing.T) {
		f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
		id, _ := f.newOperation(t)
		client := *f.client
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		start, err := client.Get(f.base + PairConsentPath + "?operation=" + url.QueryEscape(id))
		if err != nil {
			t.Fatal(err)
		}
		loc, _ := url.Parse(start.Header.Get("Location"))
		start.Body.Close()
		callback, _ := url.Parse(loc.Query().Get("redirect_uri"))
		callbackQuery := callback.Query()
		callbackQuery.Set("code", "same")
		callbackQuery.Set("state", loc.Query().Get("state"))
		callback.RawQuery = callbackQuery.Encode()
		noJar := &http.Client{Transport: f.transport, Timeout: 5 * time.Second}
		for _, cookie := range []string{"", "__Host-tether-pair-consent=wrong"} {
			req, _ := http.NewRequest(http.MethodGet, callback.String(), nil)
			if cookie != "" {
				req.Header.Set("Cookie", cookie)
			}
			resp, err := noJar.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			page, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden || strings.Contains(string(page), "form_token") {
				t.Fatalf("cookie %q status=%d page=%s", cookie, resp.StatusCode, page)
			}
		}
	})
}

func TestPairConsentPageEscapesValuesAndSetsBrowserSecurityHeaders(t *testing.T) {
	// All IdP and slot values are escaped, and the page cannot be framed or cached by another site.
	malicious := "<img src=x onerror=alert(1)>"
	f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
	f.g.mu.Lock()
	f.g.byLabel["member-1"].label = malicious
	f.g.mu.Unlock()
	id, _ := f.newOperation(t)
	resp, err := f.client.Get(f.base + PairConsentPath + "?operation=" + url.QueryEscape(id))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	page, _ := io.ReadAll(resp.Body)
	text := string(page)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Frame-Options") != "DENY" || resp.Header.Get("Content-Security-Policy") != "default-src 'none'; form-action 'self'; frame-ancestors 'none'" || strings.Contains(text, malicious) || !strings.Contains(text, "&lt;img src=x onerror=alert(1)&gt;") || !strings.Contains(text, "登録を確定") || !strings.Contains(text, "取り消す") {
		t.Fatalf("confirmation page status=%d headers=%v body=%s", resp.StatusCode, resp.Header, text)
	}
}

func TestPairConsentPageShowsThePreflightReplacementSnapshot(t *testing.T) {
	// The user must see the same replacement whose revision the operation is bound to.
	f := newPairConsentFixture(t, subjects[0])
	oldSigner := f.newHost(t)
	oldRecord := recordForConsentTest(t, oldSigner)
	f.initialize(t, []PairRecord{oldRecord}, nil)
	id, _ := f.newOperation(t)

	changedSigner := f.newHost(t)
	changedRecord := recordForConsentTest(t, changedSigner)
	f.g.pairMu.Lock()
	f.g.mu.Lock()
	slot := f.g.hosts[oldRecord.HostLabel]
	slot.key = append([]byte(nil), changedRecord.HostKey...)
	slot.record = &changedRecord
	f.g.mu.Unlock()
	f.g.pairMu.Unlock()

	page := f.consentPage(t, id)
	if !strings.Contains(page, ssh.FingerprintSHA256(oldSigner.PublicKey())) || strings.Contains(page, ssh.FingerprintSHA256(changedSigner.PublicKey())) {
		t.Fatalf("page did not show preflight replacement snapshot: %s", page)
	}
}

func TestPairConsentStateRequestsHaveABoundedMap(t *testing.T) {
	// Repeated consent links must not grow server-side state without a fixed limit.
	f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
	id, _ := f.newOperation(t)
	noFollow := &http.Client{
		Transport: f.transport, Jar: f.client.Jar, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for i := 0; i < 64; i++ {
		resp, err := noFollow.Get(f.base + PairConsentPath + "?operation=" + url.QueryEscape(id))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("state request %d status = %d, want 302", i+1, resp.StatusCode)
		}
	}
	resp, err := noFollow.Get(f.base + PairConsentPath + "?operation=" + url.QueryEscape(id))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("state cap status = %d, want 503", resp.StatusCode)
	}
}

func TestPairConsentCancelThenConfirmIsForbiddenWithoutSaving(t *testing.T) {
	// Cancel is the first terminal browser decision and cannot be changed by a later signed request.
	f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
	id, signer := f.newOperation(t)
	form := f.formToken(t, id)
	resp := f.decide(t, id, form, "cancel")
	resp.Body.Close()
	confirm := f.confirm(t, id, signer, nil)
	defer confirm.Body.Close()
	if confirm.StatusCode != http.StatusForbidden || responseError(t, confirm) != "consent_cancelled" || f.saveCount.Load() != 0 {
		t.Fatalf("confirm after cancel status=%d saves=%d", confirm.StatusCode, f.saveCount.Load())
	}
}

func TestPairConsentFirstDecisionWinsAcrossTabs(t *testing.T) {
	// A decision from either open tab invalidates every form token for that operation.
	t.Run("cancel first", func(t *testing.T) {
		f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
		id, signer := f.newOperation(t)
		cancelToken := f.formToken(t, id)
		confirmToken := f.formToken(t, id)
		first := f.decide(t, id, cancelToken, "cancel")
		first.Body.Close()
		second := f.decide(t, id, confirmToken, "confirm")
		second.Body.Close()
		result := f.confirm(t, id, signer, nil)
		defer result.Body.Close()
		if result.StatusCode != http.StatusForbidden || responseError(t, result) != "consent_cancelled" || f.saveCount.Load() != 0 {
			t.Fatalf("second tab changed cancel: status=%d saves=%d", result.StatusCode, f.saveCount.Load())
		}
	})
	t.Run("confirm first", func(t *testing.T) {
		f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
		id, signer := f.newOperation(t)
		confirmToken := f.formToken(t, id)
		cancelToken := f.formToken(t, id)
		first := f.decide(t, id, confirmToken, "confirm")
		first.Body.Close()
		second := f.decide(t, id, cancelToken, "cancel")
		second.Body.Close()
		result := f.confirm(t, id, signer, nil)
		defer result.Body.Close()
		if result.StatusCode != http.StatusOK || f.saveCount.Load() != 1 {
			t.Fatalf("second tab changed confirm: status=%d saves=%d", result.StatusCode, f.saveCount.Load())
		}
	})
}

func TestPairConfirmRevisionChangeIsTerminal(t *testing.T) {
	// Consent applies to the exact slot version shown to the account, so a later replacement must block commit.
	f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
	id, signer := f.newOperation(t)
	f.decide(t, id, f.formToken(t, id), "confirm").Body.Close()
	changedKey := f.newHost(t)
	changedRecord := PairRecord{HostLabel: "host-1", HostKey: changedKey.PublicKey().Marshal(), Issuer: testIssuer, Subject: f.subject, RegisteredAt: f.clockNow.Add(time.Second)}
	f.g.mu.Lock()
	slot := f.g.hosts["host-1"]
	slot.key = append([]byte(nil), changedRecord.HostKey...)
	slot.record = &changedRecord
	f.g.pairing.Records = []PairRecord{changedRecord}
	f.g.mu.Unlock()
	saves := f.saveCount.Load()
	first := f.confirm(t, id, signer, nil)
	defer first.Body.Close()
	if first.StatusCode != http.StatusConflict || responseError(t, first) != "revision_changed" || f.saveCount.Load() != saves {
		t.Fatalf("stale confirm status=%d saves=%d want=%d", first.StatusCode, f.saveCount.Load(), saves)
	}
}

func TestPairConfirmRequiresConsentThenPersistsOperationOnce(t *testing.T) {
	// The registry operation marker is the durable proof that this host key consumed one browser consent.
	f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
	id, signer := f.newOperation(t)
	pending := f.confirm(t, id, signer, nil)
	if pending.StatusCode != http.StatusAccepted {
		pending.Body.Close()
		t.Fatalf("pending confirm status=%d, want 202", pending.StatusCode)
	}
	pending.Body.Close()
	f.decide(t, id, f.formToken(t, id), "confirm").Body.Close()
	confirmed := f.confirm(t, id, signer, nil)
	defer confirmed.Body.Close()
	var response map[string]string
	_ = json.NewDecoder(confirmed.Body).Decode(&response)
	f.g.mu.Lock()
	stored := clonePairRecords(f.g.pairing.Records)
	f.g.mu.Unlock()
	if confirmed.StatusCode != http.StatusOK || len(response) != 4 || response["host_label"] != "host-1" || f.saveCount.Load() != 1 || len(stored) != 1 || stored[0].Operation != id || string(f.g.HostKey("host-1")) != string(signer.PublicKey().Marshal()) {
		t.Fatalf("confirmed status=%d response=%v records=%+v saves=%d", confirmed.StatusCode, response, stored, f.saveCount.Load())
	}
}

func TestPairConfirmConcurrentRequestsSaveExactlyOnce(t *testing.T) {
	// Pair replacement holds the slot through persistence and disconnect so concurrent retries share one result.
	f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
	id, signer := f.newOperation(t)
	f.decide(t, id, f.formToken(t, id), "confirm").Body.Close()
	const n = 12
	var wg sync.WaitGroup
	statuses := make(chan int, n)
	bodies := make(chan string, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := f.confirm(t, id, signer, nil)
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			statuses <- resp.StatusCode
			bodies <- string(body)
		}()
	}
	wg.Wait()
	close(statuses)
	close(bodies)
	var firstBody string
	for status := range statuses {
		if status != http.StatusOK {
			t.Errorf("concurrent confirm status=%d, want 200", status)
		}
	}
	for body := range bodies {
		if firstBody == "" {
			firstBody = body
		} else if body != firstBody {
			t.Errorf("concurrent confirm response differs: %s vs %s", body, firstBody)
		}
	}
	if f.saveCount.Load() != 1 {
		t.Fatalf("Save calls = %d, want 1", f.saveCount.Load())
	}
}

func TestPairConfirmRetryAndRegistryLookupReturnSuccess(t *testing.T) {
	// A lost response can be retried after the operation cache expires because the registry keeps its operation ID.
	f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
	id, signer := f.newOperation(t)
	f.decide(t, id, f.formToken(t, id), "confirm").Body.Close()
	first := f.confirm(t, id, signer, nil)
	first.Body.Close()
	saves := f.saveCount.Load()
	retry := f.confirm(t, id, signer, nil)
	if retry.StatusCode != http.StatusOK {
		retry.Body.Close()
		t.Fatalf("retry status=%d, want 200", retry.StatusCode)
	}
	retry.Body.Close()
	f.persistMu.Lock()
	records := clonePairRecords(f.persisted)
	f.persistMu.Unlock()
	f.clockNow = time.Now().UTC().Add(11 * time.Minute)
	f.newGateway(t, records)
	afterExpiry := f.confirm(t, id, signer, nil)
	defer afterExpiry.Body.Close()
	if afterExpiry.StatusCode != http.StatusOK || f.saveCount.Load() != saves {
		t.Fatalf("registry retry status=%d saves=%d want=%d", afterExpiry.StatusCode, f.saveCount.Load(), saves)
	}
}

func TestPairConfirmProofBindsKeyAndOperation(t *testing.T) {
	// Both the new host key and operation ID are signed into the one-time confirmation proof.
	t.Run("different key", func(t *testing.T) {
		f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
		id, signer := f.newOperation(t)
		resp := f.confirm(t, id, signer, f.newHost(t))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("mismatched key proof status=%d, want 400", resp.StatusCode)
		}
	})
	t.Run("different operation", func(t *testing.T) {
		f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
		signer := f.newHost(t)
		oldResp, oldBody := f.postOperation(t, signer, consentToken(t, f.subject), signer)
		oldResp.Body.Close()
		oldID := oldBody["operation"].(string)
		// The same key may be previewed again, but the new operation ID changes the transcript.
		newResp, body := f.postOperation(t, signer, consentToken(t, f.subject), signer)
		newResp.Body.Close()
		newID := body["operation"].(string)
		resp := f.confirmWithProofID(t, newID, oldID, signer, signer)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("mismatched operation proof status=%d, want 400", resp.StatusCode)
		}
	})
}

func TestPairConfirmRejectsSupersededAndExpiredOperations(t *testing.T) {
	// A fresh preview for the same slot invalidates the older consent; expiration also removes an uncommitted operation.
	f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
	oldID, oldKey := f.newOperation(t)
	newID, newKey := f.newOperation(t)
	if oldID == newID {
		t.Fatal("operation IDs repeated")
	}
	superseded := f.confirm(t, oldID, oldKey, nil)
	if superseded.StatusCode != http.StatusNotFound {
		superseded.Body.Close()
		t.Fatalf("superseded operation status=%d, want 404", superseded.StatusCode)
	}
	superseded.Body.Close()
	// Keep the gateway's clock injectable in the fixture so expiry is deterministic.
	f.clockNow = f.clockNow.Add(11 * time.Minute)
	expired := f.confirm(t, newID, newKey, nil)
	defer expired.Body.Close()
	if expired.StatusCode != http.StatusNotFound {
		t.Fatalf("expired operation status=%d, want 404", expired.StatusCode)
	}
}

func TestPairConfirmBusyResultIsTerminal(t *testing.T) {
	// A slot that becomes busy after consent must stay blocked on retry even after it clears.
	f := newPairConsentFixtureReady(t, subjects[0], nil, nil)
	id, signer := f.newOperation(t)
	f.decide(t, id, f.formToken(t, id), "confirm").Body.Close()
	f.g.mu.Lock()
	f.g.byLabel["member-1"].connecting = true
	f.g.mu.Unlock()
	first := f.confirm(t, id, signer, nil)
	if first.StatusCode != http.StatusConflict || responseError(t, first) != "replace_blocked" {
		first.Body.Close()
		t.Fatalf("busy confirm status=%d", first.StatusCode)
	}
	f.g.mu.Lock()
	f.g.byLabel["member-1"].connecting = false
	f.g.mu.Unlock()
	second := f.confirm(t, id, signer, nil)
	defer second.Body.Close()
	if second.StatusCode != http.StatusConflict || responseError(t, second) != "replace_blocked" || f.saveCount.Load() != 0 {
		t.Fatalf("busy retry status=%d saves=%d", second.StatusCode, f.saveCount.Load())
	}
}

func TestPairConfirmSaveFailurePreservesOldRecordAndIsTerminal(t *testing.T) {
	// A pre-rename storage failure leaves the old key active and the same failure is returned for every retry.
	errSave := errors.New("save failed")
	old := recordForConsentTest(t, fixtureSigner(t, 0x71))
	f := newPairConsentFixtureReady(t, subjects[0], []PairRecord{old}, errSave)
	id, signer := f.newOperation(t)
	f.decide(t, id, f.formToken(t, id), "confirm").Body.Close()
	first := f.confirm(t, id, signer, nil)
	if first.StatusCode != http.StatusServiceUnavailable {
		first.Body.Close()
		t.Fatalf("failed save status=%d, want 503", first.StatusCode)
	}
	first.Body.Close()
	second := f.confirm(t, id, signer, nil)
	defer second.Body.Close()
	secondError := responseError(t, second)
	if second.StatusCode != http.StatusServiceUnavailable || secondError != "save_failed" || f.saveCount.Load() != 1 || string(f.g.HostKey("host-1")) != string(old.HostKey) {
		t.Fatalf("save retry status=%d error=%q saves=%d", second.StatusCode, secondError, f.saveCount.Load())
	}
}

func TestPairConfirmUncertainDirectorySyncStaysUnknown(t *testing.T) {
	// A renamed record without a durable parent directory is never reported as a successful registration.
	f := newPairConsentFixtureReady(t, subjects[0], nil, ErrRegistryDurabilityUnknown)
	id, signer := f.newOperation(t)
	f.decide(t, id, f.formToken(t, id), "confirm").Body.Close()
	first := f.confirm(t, id, signer, nil)
	if first.StatusCode != http.StatusInternalServerError || responseError(t, first) != "registration_unknown" {
		first.Body.Close()
		t.Fatalf("uncertain save status=%d", first.StatusCode)
	}
	first.Body.Close()
	second := f.confirm(t, id, signer, nil)
	if second.StatusCode != http.StatusInternalServerError || responseError(t, second) != "registration_unknown" || f.saveCount.Load() != 1 {
		second.Body.Close()
		t.Fatalf("uncertain retry status=%d saves=%d", second.StatusCode, f.saveCount.Load())
	}
	second.Body.Close()
	f.persistMu.Lock()
	records := clonePairRecords(f.persisted)
	f.persistMu.Unlock()
	f.newGateway(t, records)
	reopened := f.confirm(t, id, signer, nil)
	defer reopened.Body.Close()
	if reopened.StatusCode != http.StatusOK || f.saveCount.Load() != 1 {
		t.Fatalf("reopened record status=%d saves=%d", reopened.StatusCode, f.saveCount.Load())
	}
}

func TestPairConfirmDisconnectsOldHostAfterDurableCommit(t *testing.T) {
	// Existing transports are cut only after the new registry record is durably saved and reflected.
	old := recordForConsentTest(t, fixtureSigner(t, 0x72))
	f := newPairConsentFixtureReady(t, subjects[0], []PairRecord{old}, nil)
	id, signer := f.newOperation(t)
	f.decide(t, id, f.formToken(t, id), "confirm").Body.Close()
	resp := f.confirm(t, id, signer, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || f.disconnects.Load() != 1 {
		t.Fatalf("confirm status=%d disconnects=%d", resp.StatusCode, f.disconnects.Load())
	}
}
