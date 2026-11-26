//go:build unix

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/hostkeychain"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

const guiPairSubject = "gui-pair-subject"

type guiTestKeychain struct {
	mu           sync.Mutex
	items        map[string][]byte
	known        map[string]bool
	useReal      bool
	fingerprints []string
}

func newGUITestKeychain(t *testing.T) *guiTestKeychain {
	t.Helper()
	k := &guiTestKeychain{items: map[string][]byte{}, known: map[string]bool{}, useReal: guiPairUsesRealKeychain()}
	if k.useReal {
		previous := hostkeychain.UseDataProtectionKeychain
		hostkeychain.UseDataProtectionKeychain = false
		t.Cleanup(func() {
			for _, fingerprint := range k.fingerprints {
				if err := hostkeychain.Delete(fingerprint); err != nil {
					t.Errorf("remove test Keychain item: %v", err)
				}
			}
			for _, fingerprint := range k.fingerprints {
				if _, err := hostkeychain.Load(fingerprint); !errors.Is(err, hostkeychain.ErrNotFound) {
					t.Errorf("test Keychain item remains: %v", err)
				}
			}
			hostkeychain.UseDataProtectionKeychain = previous
		})
	}
	return k
}

func (k *guiTestKeychain) store(raw []byte, fingerprint string) error {
	if k.useReal {
		if err := hostkeychain.Store(raw, fingerprint); err != nil {
			return err
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.items[fingerprint] = append([]byte(nil), raw...)
	k.known[fingerprint] = true
	k.fingerprints = append(k.fingerprints, fingerprint)
	return nil
}

func (k *guiTestKeychain) load(fingerprint string) (ssh.Signer, error) {
	if k.useReal {
		return hostkeychain.Load(fingerprint)
	}
	k.mu.Lock()
	raw, ok := k.items[fingerprint]
	k.mu.Unlock()
	if !ok {
		return nil, hostkeychain.ErrNotFound
	}
	return ssh.NewSignerFromKey(ed25519.PrivateKey(raw))
}

func (k *guiTestKeychain) delete(fingerprint string) error {
	if k.useReal {
		if err := hostkeychain.Delete(fingerprint); err != nil {
			return err
		}
	}
	k.mu.Lock()
	delete(k.items, fingerprint)
	k.known[fingerprint] = false
	k.mu.Unlock()
	return nil
}

func (k *guiTestKeychain) has(fingerprint string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.known[fingerprint]
}

func (k *guiTestKeychain) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for _, present := range k.known {
		if present {
			n++
		}
	}
	return n
}

type guiPairFixture struct {
	t                   *testing.T
	issuer, gateway     *httptest.Server
	client, browser     *http.Client
	issuerKey           ed25519.PrivateKey
	principal           ssh.Signer
	activeHost          ssh.Signer
	gw                  *agentgateway.Gateway
	gateways            []*agentgateway.Gateway
	keychain            *guiTestKeychain
	home, dir, policy   string
	accessToken         string
	operation           string
	browserDecision     string
	oauthCancel         bool
	consentBrowserError bool
	changeRevision      bool
	saveUnknownNext     bool
	saveMu              sync.Mutex
	persisted           []agentgateway.PairRecord
	operationsCalls     atomic.Int32
	confirmCalls        atomic.Int32
	saveCalls           atomic.Int32
	consentLaunches     atomic.Int32
	authLaunches        atomic.Int32
	nativeCalls         atomic.Int32
	nativeDecision      string
	nativeScript        string
	nativeArgs          []string
	lastToken           string
	depsOverride        func(*deps)
}

func newGUIPairFixture(t *testing.T, records []agentgateway.PairRecord) *guiPairFixture {
	t.Helper()
	f := &guiPairFixture{t: t, issuerKey: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x43}, ed25519.SeedSize)), browserDecision: "confirm", nativeDecision: "continue", keychain: newGUITestKeychain(t)}
	f.issuer = newLoopbackTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": f.issuer.URL, "authorization_endpoint": f.issuer.URL + "/authorize", "token_endpoint": f.issuer.URL + "/token", "code_challenge_methods_supported": []string{"S256"}})
		case "/authorize":
			callback, err := url.Parse(r.URL.Query().Get("redirect_uri"))
			if err != nil {
				http.Error(w, "bad redirect", http.StatusBadRequest)
				return
			}
			q := callback.Query()
			q.Set("state", r.URL.Query().Get("state"))
			if f.oauthCancel {
				q.Set("error", "access_denied")
			} else {
				q.Set("code", "gui-auth-code")
			}
			callback.RawQuery = q.Encode()
			http.Redirect(w, r, callback.String(), http.StatusFound)
		case "/token":
			claims, _ := json.Marshal(map[string]any{"iss": f.issuer.URL, "aud": f.gateway.URL + agentgateway.PairPath, "sub": guiPairSubject, "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix()})
			header, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": "gui-test", "typ": "at+jwt"})
			input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
			f.accessToken = input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.issuerKey, []byte(input)))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": f.accessToken, "token_type": "Bearer"})
		default:
			http.NotFound(w, r)
		}
	}))
	f.gateway = newLoopbackTLSServer(http.HandlerFunc(f.serve))
	pool := x509.NewCertPool()
	pool.AddCert(f.issuer.Certificate())
	pool.AddCert(f.gateway.Certificate())
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	f.client = &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	f.browser = &http.Client{Transport: transport, Timeout: 5 * time.Second, Jar: jar}
	f.home = t.TempDir()
	f.dir = filepath.Join(f.home, "Library", "Application Support", "com.o2connective.rescue-host.shell")
	f.policy = filepath.Join(f.dir, "resident-policy.json")
	for i := range records {
		if records[i].Issuer == "https://issuer.invalid" {
			records[i].Issuer = f.issuer.URL
		}
	}
	if err := f.configureGateway(records); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.issuer.Close()
		f.gateway.Close()
		f.client.CloseIdleConnections()
	})
	return f
}

func (f *guiPairFixture) configureGateway(records []agentgateway.PairRecord) error {
	if f.gw != nil {
		_ = f.gw.Close(context.Background())
	}
	mainVerifier, err := f.newVerifier(f.gateway.URL + "/mcp")
	if err != nil {
		return err
	}
	principalKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x22}, ed25519.SeedSize))
	var errSigner error
	f.principal, errSigner = ssh.NewSignerFromKey(principalKey)
	if errSigner != nil {
		return errSigner
	}
	activeHostKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x2a}, ed25519.SeedSize))
	f.activeHost, errSigner = ssh.NewSignerFromKey(activeHostKey)
	if errSigner != nil {
		return errSigner
	}
	gw, err := agentgateway.New(agentgateway.Config{Verifier: mainVerifier, Resource: f.gateway.URL + "/mcp", Budgets: agentgateway.ChannelBudgets(), Principals: []agentgateway.Principal{{Label: "gui-member", Subject: guiPairSubject, Roles: []agentgateway.Role{agentgateway.RoleRescueOperator, agentgateway.RoleResidentOwner}, Signer: f.principal}}})
	if err != nil {
		return err
	}
	if err := gw.ConfigureHosts([]agentgateway.HostEntry{
		{Label: "gui-host", Principals: []string{"gui-member"}, Paired: true},
		{Label: "gui-rescue", Key: f.activeHost.PublicKey().Marshal(), Principals: []string{"gui-member"}},
	}); err != nil {
		return err
	}
	pairVerifier, err := f.newVerifier(f.gateway.URL + agentgateway.PairPath)
	if err != nil {
		return err
	}
	if err := gw.ConfigurePairing(agentgateway.PairingConfig{
		Verifier: pairVerifier, Endpoint: f.gateway.URL + agentgateway.PairPath,
		RegistrationEndpoint: "wss://gateway.example.test/register", Records: cloneGUIRecords(records),
		Consent: &agentgateway.PairConsentConfig{ClientID: "browser-client", AuthorizationEndpoint: f.issuer.URL + "/authorize", TokenEndpoint: f.issuer.URL + "/token", HTTPClient: f.client},
		Save: func(next []agentgateway.PairRecord) error {
			f.saveCalls.Add(1)
			f.saveMu.Lock()
			f.persisted = cloneGUIRecords(next)
			failUnknown := f.saveUnknownNext
			f.saveUnknownNext = false
			f.saveMu.Unlock()
			if failUnknown {
				return agentgateway.ErrRegistryDurabilityUnknown
			}
			return nil
		},
	}); err != nil {
		return err
	}
	f.gw = gw
	f.gateways = append(f.gateways, gw)
	f.saveMu.Lock()
	f.persisted = cloneGUIRecords(records)
	f.saveMu.Unlock()
	return nil
}

func (f *guiPairFixture) newVerifier(audience string) (*agentgateway.Verifier, error) {
	public := f.issuerKey.Public().(ed25519.PublicKey)
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "gui-test", "alg": "EdDSA", "use": "sig", "x": base64.RawURLEncoding.EncodeToString(public)}}})
	return agentgateway.NewVerifier(agentgateway.VerifierConfig{Issuer: f.issuer.URL, Audience: audience, Types: []string{"at+jwt"}, JWKS: jwks})
}

func (f *guiPairFixture) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case agentgateway.PairPath:
		f.gw.ServePair(w, r)
	case agentgateway.PairOperationsPath:
		f.operationsCalls.Add(1)
		f.gw.ServePairOperations(w, r)
	case agentgateway.PairConfirmPath:
		f.confirmCalls.Add(1)
		f.gw.ServePairConfirm(w, r)
	case agentgateway.PairConsentPath:
		f.gw.ServePairConsent(w, r)
	case agentgateway.PairConsentCallbackPath:
		f.gw.ServePairConsentCallback(w, r)
	default:
		http.NotFound(w, r)
	}
}

func cloneGUIRecords(records []agentgateway.PairRecord) []agentgateway.PairRecord {
	copyOf := make([]agentgateway.PairRecord, len(records))
	for i := range records {
		copyOf[i] = records[i]
		copyOf[i].HostKey = append([]byte(nil), records[i].HostKey...)
	}
	return copyOf
}

func (f *guiPairFixture) openBrowser(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Host == mustURLHost(f.t, f.issuer.URL) {
		f.authLaunches.Add(1)
		resp, err := f.browser.Get(raw)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if u.Host == mustURLHost(f.t, f.gateway.URL) && u.Path == agentgateway.PairConsentPath {
		f.consentLaunches.Add(1)
		if f.consentBrowserError {
			return errors.New("synthetic browser start failure")
		}
		resp, err := f.browser.Get(raw)
		if err != nil {
			return err
		}
		page, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil || resp.StatusCode != http.StatusOK {
			return errors.New("synthetic consent page did not load")
		}
		if f.changeRevision {
			if err := f.replaceViaLegacy(); err != nil {
				return err
			}
		}
		operation := regexp.MustCompile(`name="operation" value="([^"]+)"`).FindSubmatch(page)
		formToken := regexp.MustCompile(`name="form_token" value="([^"]+)"`).FindSubmatch(page)
		if len(operation) != 2 || len(formToken) != 2 {
			return errors.New("synthetic consent form missing fields")
		}
		f.operation = string(operation[1])
		decision := f.browserDecision
		if decision == "" {
			decision = "confirm"
		}
		form := url.Values{"operation": {f.operation}, "form_token": {string(formToken[1])}, "decision": {decision}}
		post, err := http.NewRequest(http.MethodPost, f.gateway.URL+agentgateway.PairConsentPath, strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		result, err := f.browser.Do(post)
		if err == nil {
			result.Body.Close()
		}
		return err
	}
	return errors.New("unexpected browser destination")
}

func mustURLHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func (f *guiPairFixture) replaceViaLegacy() error {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return err
	}
	token := f.signedToken(f.gateway.URL + agentgateway.PairPath)
	sum := sha256.Sum256([]byte(token))
	signature, err := signer.Sign(rand.Reader, agentgateway.PairTranscript(f.gateway.URL+agentgateway.PairPath, signer.PublicKey().Marshal(), sum))
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"host_public_key": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), "proof": base64.RawURLEncoding.EncodeToString(ssh.Marshal(signature))})
	req, _ := http.NewRequest(http.MethodPost, f.gateway.URL+agentgateway.PairPath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("synthetic legacy replacement failed")
	}
	return nil
}

func (f *guiPairFixture) signedToken(audience string) string {
	claims, _ := json.Marshal(map[string]any{"iss": f.issuer.URL, "aud": audience, "sub": guiPairSubject, "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix()})
	header, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": "gui-test", "typ": "at+jwt"})
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(f.issuerKey, []byte(input)))
}

func (f *guiPairFixture) run(t *testing.T) (int, string, string) {
	t.Helper()
	oldGateway, oldIssuer, oldClient := guiGateway, guiIssuer, guiClientID
	guiGateway, guiIssuer, guiClientID = f.gateway.URL, f.issuer.URL, "gui-public-client"
	t.Cleanup(func() { guiGateway, guiIssuer, guiClientID = oldGateway, oldIssuer, oldClient })
	d := deps{client: f.client, callbackWait: 3 * time.Second, pairTimeout: 3 * time.Second}
	configurePairing(&d)
	d.location = func(context.Context) (string, string, error) { return f.home, f.policy, nil }
	d.readProtected = func(_ context.Context, path string) ([]byte, error) { return os.ReadFile(path) }
	d.openBrowser = f.openBrowser
	d.storeHostKey = f.keychain.store
	d.loadHostKey = f.keychain.load
	d.deleteHostKey = f.keychain.delete
	d.runAppleScript = func(_ context.Context, script string, args ...string) (string, error) {
		f.nativeCalls.Add(1)
		f.nativeScript = script
		f.nativeArgs = append([]string(nil), args...)
		return f.nativeDecision, nil
	}
	if f.depsOverride != nil {
		f.depsOverride(&d)
	}
	var stdout, stderr strings.Builder
	code := execute(context.Background(), nil, &stdout, &stderr, d)
	return code, stdout.String(), stderr.String()
}

func (f *guiPairFixture) markPrincipalConnecting(t *testing.T) func() {
	t.Helper()
	digest, err := runbackend.KeySetDigest([]ssh.PublicKey{f.principal.PublicKey()})
	if err != nil {
		t.Fatal(err)
	}
	dispatch := agentgateway.Dispatch{
		Binding: agentchannel.Binding{
			Grant: supervisor.Binding{
				Generation: 1,
				Identity:   supervisor.Identity{LaunchID: "gui-busy-test", Epoch: "1", PolicyDigest: agentexec.MacCurrentUserPolicyDigest()},
			},
			ConnectionGeneration: 1,
			KeySetDigest:         digest,
			Profile:              agentexec.ProfileMacCurrentUser,
		},
		Deadline: time.Now().Add(time.Minute),
		HostKey:  f.activeHost.PublicKey().Marshal(),
	}
	client, peer := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- f.gw.Connect("gui-member", dispatch, client) }()
	_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := peer.Read(make([]byte, 256)); err != nil {
		peer.Close()
		select {
		case connectErr := <-done:
			t.Fatalf("gateway did not enter active connection setup: read=%v connect=%v", err, connectErr)
		default:
			t.Fatalf("gateway did not enter active connection setup: %v", err)
		}
	}
	return func() {
		_ = peer.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("gateway connection did not stop after peer close")
		}
	}
}

func (f *guiPairFixture) restartGateway(t *testing.T) {
	t.Helper()
	f.saveMu.Lock()
	records := cloneGUIRecords(f.persisted)
	f.saveMu.Unlock()
	if err := f.configureGateway(records); err != nil {
		t.Fatal(err)
	}
}

func guiResult(t *testing.T, raw string) string {
	t.Helper()
	line := strings.TrimSuffix(raw, "\n")
	if line == "" || strings.Contains(line, "\n") {
		t.Fatalf("GUI stdout is not one JSON line: %q", raw)
	}
	var got struct {
		ProtocolVersion int    `json:"protocolVersion"`
		Result          string `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil || got.ProtocolVersion != 1 || got.Result == "" {
		t.Fatalf("GUI stdout JSON = %q, err=%v", raw, err)
	}
	return got.Result
}

func guiOldRecord(t *testing.T, signer ssh.Signer) agentgateway.PairRecord {
	t.Helper()
	return agentgateway.PairRecord{HostLabel: "gui-host", HostKey: signer.PublicKey().Marshal(), Issuer: "https://issuer.invalid", Subject: guiPairSubject, RegisteredAt: time.Now().UTC().Truncate(time.Second)}
}

func TestGUIEmptyFixedValuesReturnsUnavailableWithoutWrites(t *testing.T) {
	// Why: a build without trusted fixed endpoints must not touch Keychain, the filesystem, or network.
	oldGateway, oldIssuer, oldClient := guiGateway, guiIssuer, guiClientID
	guiGateway, guiIssuer, guiClientID = "", "", ""
	t.Cleanup(func() { guiGateway, guiIssuer, guiClientID = oldGateway, oldIssuer, oldClient })
	var stdout, stderr strings.Builder
	code := execute(context.Background(), nil, &stdout, &stderr, deps{})
	if code != 0 || guiResult(t, stdout.String()) != "unavailable" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestGUIInvalidFixedValuesReturnUnavailableBeforeWrites(t *testing.T) {
	// Why: malformed build-time endpoints and client IDs must fail before location or network work.
	cases := []struct{ gateway, issuer, clientID string }{
		{"https://UPPER.example", "https://issuer.example", "client"},
		{"https://gateway.example/path", "https://issuer.example", "client"},
		{"https://gateway.example", "http://issuer.example", "client"},
		{"https://gateway.example", "https://issuer.example", "bad\nclient"},
	}
	oldGateway, oldIssuer, oldClient := guiGateway, guiIssuer, guiClientID
	defer func() { guiGateway, guiIssuer, guiClientID = oldGateway, oldIssuer, oldClient }()
	for _, tc := range cases {
		guiGateway, guiIssuer, guiClientID = tc.gateway, tc.issuer, tc.clientID
		var stdout, stderr strings.Builder
		code := execute(context.Background(), nil, &stdout, &stderr, deps{})
		if code != 0 || guiResult(t, stdout.String()) != "unavailable" || stderr.Len() != 0 {
			t.Fatalf("fixed values were not rejected before work: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	}
}

func TestGUISuccessWritesKeychainPolicyAndDeletesOldPEM(t *testing.T) {
	// Why: success must atomically switch policy to the new Keychain key and retire the previous PEM.
	oldSigner := mustSignerFromSeed(t, 0x54)
	f := newGUIPairFixture(t, []agentgateway.PairRecord{guiOldRecord(t, oldSigner)})
	if err := os.MkdirAll(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	oldPEMPath := filepath.Join(f.dir, "host-key-old")
	if err := os.WriteFile(oldPEMPath, []byte("old key"), 0600); err != nil {
		t.Fatal(err)
	}
	oldPolicy, _ := json.Marshal(map[string]string{"host_key_path": oldPEMPath, "host_key_fingerprint": ssh.FingerprintSHA256(oldSigner.PublicKey())})
	if err := os.WriteFile(f.policy, oldPolicy, 0600); err != nil {
		t.Fatal(err)
	}
	originalRename := renameFile
	renameFile = func(old, new string) error {
		if new == f.policy {
			if _, err := os.Stat(oldPEMPath); err != nil {
				t.Fatalf("old PEM was removed before policy replacement: %v", err)
			}
		}
		return os.Rename(old, new)
	}
	t.Cleanup(func() { renameFile = originalRename })
	code, stdout, stderr := f.run(t)
	if code != 0 || guiResult(t, stdout) != "succeeded" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	raw, err := os.ReadFile(f.policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := maclaunch.ValidateFixedResidentPolicy(raw, f.policy); err != nil {
		t.Fatalf("Keychain policy validation: %v", err)
	}
	var policy map[string]any
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	fingerprint, _ := policy["host_key_fingerprint"].(string)
	principalKey := strings.TrimSpace(strings.Split(string(ssh.MarshalAuthorizedKey(f.principal.PublicKey())), " ")[1])
	if policy["host_key_store"] != "keychain" || policy["host_key_path"] != nil || !f.keychain.has(fingerprint) ||
		policy["registration_endpoint"] != "wss://gateway.example.test/register" || policy["host_label"] != "gui-host" ||
		policy["principal_label"] != "gui-member" || policy["principal_public_key"] != principalKey {
		t.Fatalf("policy/keychain mismatch: policy=%v expected principal key=%q", policy, principalKey)
	}
	if _, err := os.Lstat(oldPEMPath); !os.IsNotExist(err) {
		t.Fatalf("old PEM remains: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(f.dir, "pairing-pending.json")); !os.IsNotExist(err) {
		t.Fatalf("pending record remains: %v", err)
	}
	f.saveMu.Lock()
	defer f.saveMu.Unlock()
	if f.saveCalls.Load() != 1 || len(f.persisted) != 1 || f.persisted[0].Operation == "" {
		t.Fatalf("gateway registration was not persisted once: saves=%d records=%+v", f.saveCalls.Load(), f.persisted)
	}
}

func TestGUISuccessDeletesOldKeychainItem(t *testing.T) {
	// Why: a successful policy switch must retire the prior Keychain account as well as legacy PEM keys.
	oldPrivate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x57}, ed25519.SeedSize))
	defer clear(oldPrivate)
	oldSigner, err := ssh.NewSignerFromKey(oldPrivate)
	if err != nil {
		t.Fatal(err)
	}
	oldFingerprint := ssh.FingerprintSHA256(oldSigner.PublicKey())
	f := newGUIPairFixture(t, []agentgateway.PairRecord{guiOldRecord(t, oldSigner)})
	if err := f.keychain.store(oldPrivate, oldFingerprint); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	oldPolicy, _ := json.Marshal(map[string]string{"host_key_store": "keychain", "host_key_fingerprint": oldFingerprint})
	if err := os.WriteFile(f.policy, oldPolicy, 0600); err != nil {
		t.Fatal(err)
	}
	originalRename := renameFile
	renameFile = func(old, new string) error {
		if new == f.policy && !f.keychain.has(oldFingerprint) {
			t.Fatal("old Keychain item was deleted before policy replacement")
		}
		return os.Rename(old, new)
	}
	t.Cleanup(func() { renameFile = originalRename })
	code, stdout, _ := f.run(t)
	if code != 0 || guiResult(t, stdout) != "succeeded" || f.keychain.has(oldFingerprint) || f.keychain.count() != 1 {
		t.Fatalf("old Keychain item was not retired: code=%d stdout=%q old-present=%v remaining=%d", code, stdout, f.keychain.has(oldFingerprint), f.keychain.count())
	}
}

func TestGUINativeCancelDoesNotWriteGatewayOrKeepKey(t *testing.T) {
	// Why: declining the native review occurs before consent and must create no gateway record.
	f := newGUIPairFixture(t, nil)
	f.nativeDecision = "cancel"
	code, stdout, _ := f.run(t)
	if code != 0 || guiResult(t, stdout) != "cancelled" || f.saveCalls.Load() != 0 || f.keychain.count() != 0 {
		t.Fatalf("code=%d stdout=%q saves=%d keys=%d", code, stdout, f.saveCalls.Load(), f.keychain.count())
	}
}

func TestGUIOAuthCancelDoesNotStartPairing(t *testing.T) {
	// Why: an IdP denial is cancellation, while discovery and transport errors remain unavailable.
	f := newGUIPairFixture(t, nil)
	f.oauthCancel = true
	code, stdout, _ := f.run(t)
	if code != 0 || guiResult(t, stdout) != "cancelled" || f.authLaunches.Load() != 1 || f.operationsCalls.Load() != 0 || f.keychain.count() != 0 {
		t.Fatalf("code=%d stdout=%q auth=%d operations=%d keys=%d", code, stdout, f.authLaunches.Load(), f.operationsCalls.Load(), f.keychain.count())
	}
}

func TestGUIBrowserCancelDoesNotWriteGatewayOrKeepKey(t *testing.T) {
	// Why: a browser-side cancel is terminal and must clear the pending operation and generated key.
	f := newGUIPairFixture(t, nil)
	f.browserDecision = "cancel"
	code, stdout, _ := f.run(t)
	if code != 0 || guiResult(t, stdout) != "cancelled" || f.saveCalls.Load() != 0 || f.keychain.count() != 0 {
		t.Fatalf("code=%d stdout=%q saves=%d keys=%d", code, stdout, f.saveCalls.Load(), f.keychain.count())
	}
	if _, err := os.Lstat(filepath.Join(f.dir, "pairing-pending.json")); !os.IsNotExist(err) {
		t.Fatalf("pending record remains after browser cancel: %v", err)
	}
}

func TestGUIConsentBrowserLaunchFailureCleansPendingKey(t *testing.T) {
	// Why: a failed browser launch must be distinguishable from cancellation and must not leave an unusable pending operation.
	f := newGUIPairFixture(t, nil)
	f.consentBrowserError = true
	code, stdout, _ := f.run(t)
	if code != 0 || guiResult(t, stdout) != "browser_failed" || f.saveCalls.Load() != 0 || f.keychain.count() != 0 {
		t.Fatalf("code=%d stdout=%q saves=%d keys=%d", code, stdout, f.saveCalls.Load(), f.keychain.count())
	}
	if _, err := os.Lstat(filepath.Join(f.dir, guiPairingPendingName)); !os.IsNotExist(err) {
		t.Fatalf("pending record remains after browser launch failure: %v", err)
	}
}

func TestGUIRevisionChangeRequiresRegistrationAgain(t *testing.T) {
	// Why: consent applies to the exact slot revision shown to the user, so a changed slot must not be committed.
	oldSigner := mustSignerFromSeed(t, 0x55)
	f := newGUIPairFixture(t, []agentgateway.PairRecord{guiOldRecord(t, oldSigner)})
	f.changeRevision = true
	code, stdout, _ := f.run(t)
	if code != 0 || guiResult(t, stdout) != "re_registration_required" || f.saveCalls.Load() != 1 || f.keychain.count() != 0 {
		t.Fatalf("code=%d stdout=%q saves=%d keys=%d", code, stdout, f.saveCalls.Load(), f.keychain.count())
	}
	if _, err := os.Lstat(filepath.Join(f.dir, "pairing-pending.json")); !os.IsNotExist(err) {
		t.Fatalf("pending record remains after revision change: %v", err)
	}
}

func TestGUIBusySlotReturnsBlockedBeforeConsent(t *testing.T) {
	// Why: gateway preflight must block replacement while the existing host is applying a change.
	oldSigner := mustSignerFromSeed(t, 0x56)
	f := newGUIPairFixture(t, []agentgateway.PairRecord{guiOldRecord(t, oldSigner)})
	stopConnection := f.markPrincipalConnecting(t)
	code, stdout, _ := f.run(t)
	stopConnection()
	if code != 0 || guiResult(t, stdout) != "blocked" || f.operationsCalls.Load() != 1 || f.consentLaunches.Load() != 0 || f.keychain.count() != 0 {
		t.Fatalf("code=%d stdout=%q operations=%d consent=%d keys=%d", code, stdout, f.operationsCalls.Load(), f.consentLaunches.Load(), f.keychain.count())
	}
}

func TestGUIUnknownConfirmReconcilesSameOperationAfterGatewayRestart(t *testing.T) {
	// Why: a 500 after a possibly durable registry rename must retain the same operation and key for reconciliation.
	f := newGUIPairFixture(t, nil)
	f.saveUnknownNext = true
	code, stdout, _ := f.run(t)
	if code != 0 || guiResult(t, stdout) != "unknown" || f.keychain.count() != 1 {
		t.Fatalf("first run code=%d stdout=%q keys=%d", code, stdout, f.keychain.count())
	}
	pendingPath := filepath.Join(f.dir, guiPairingPendingName)
	info, err := os.Stat(pendingPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("pending mode is not 0600: info=%v err=%v", info, err)
	}
	pendingBytes, err := os.ReadFile(pendingPath)
	fields, fieldErr := agentwire.Fields(pendingBytes, "gateway", "operation", "host_key_fingerprint")
	if err != nil || fieldErr != nil || len(fields) != 3 || strings.Contains(string(pendingBytes), f.accessToken) {
		t.Fatalf("pending record is not strict or contains a token: err=%v fields=%v", err, fields)
	}
	firstOperation, firstPreflights := f.operation, f.operationsCalls.Load()
	f.restartGateway(t)
	code, stdout, _ = f.run(t)
	if code != 0 || guiResult(t, stdout) != "succeeded" || f.operation != firstOperation || f.operationsCalls.Load() != firstPreflights || f.consentLaunches.Load() != 1 || f.nativeCalls.Load() != 1 {
		t.Fatalf("reconcile code=%d stdout=%q operation=%q want=%q preflights=%d consent=%d native=%d", code, stdout, f.operation, firstOperation, f.operationsCalls.Load(), f.consentLaunches.Load(), f.nativeCalls.Load())
	}
}

func TestGUIPendingRecordWithUnknownFieldsStartsFreshPair(t *testing.T) {
	// Why: an unreadable pending operation must be discarded so recovery can start a new authorized registration.
	f := newGUIPairFixture(t, nil)
	oldFingerprint := seedGUIPendingKey(t, f, 0x70)
	pending := guiPairPending{Gateway: f.gateway.URL, Operation: validGUIPendingOperation(), HostKeyFingerprint: oldFingerprint}
	raw, err := json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"unexpected":"value"}`)...)
	writeGUIPendingRecord(t, f, raw)
	code, stdout, stderr := f.run(t)
	assertGUIFreshRegistration(t, f, code, stdout, stderr, oldFingerprint)
}

func TestGUILocalPolicyFailureKeepsPendingKeyAndReturnsLocalApplyFailed(t *testing.T) {
	// Why: gateway success cannot be reported until the durable local policy points at the new Keychain key.
	f := newGUIPairFixture(t, nil)
	originalRename := renameFile
	renameFile = func(old, new string) error {
		if new == f.policy {
			return os.ErrPermission
		}
		return os.Rename(old, new)
	}
	t.Cleanup(func() { renameFile = originalRename })
	code, stdout, _ := f.run(t)
	if code != 0 || guiResult(t, stdout) != "local_apply_failed" || f.keychain.count() != 1 {
		t.Fatalf("code=%d stdout=%q keys=%d", code, stdout, f.keychain.count())
	}
	if _, err := os.Stat(filepath.Join(f.dir, "pairing-pending.json")); err != nil {
		t.Fatalf("pending record was not retained: %v", err)
	}
}

func TestGUIPolicyDirectorySyncFailureRestoresPreviousPolicyForRetry(t *testing.T) {
	// Why: an uncertain local policy rename must retain the prior metadata needed for a safe retry.
	f := newGUIPairFixture(t, nil)
	if err := os.MkdirAll(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	oldPolicy := []byte(`{"marker":"previous"}`)
	if err := os.WriteFile(f.policy, oldPolicy, 0600); err != nil {
		t.Fatal(err)
	}
	originalSync := syncDir
	var syncCalls atomic.Int32
	syncDir = func(dir string) error {
		if syncCalls.Add(1) == 2 {
			return os.ErrPermission
		}
		return originalSync(dir)
	}
	t.Cleanup(func() { syncDir = originalSync })
	code, stdout, _ := f.run(t)
	if code != 0 || guiResult(t, stdout) != "local_apply_failed" || f.keychain.count() != 1 {
		t.Fatalf("first apply code=%d stdout=%q keys=%d", code, stdout, f.keychain.count())
	}
	gotPolicy, err := os.ReadFile(f.policy)
	if err != nil || !bytes.Equal(gotPolicy, oldPolicy) {
		t.Fatalf("previous policy was not restored: policy=%q err=%v", gotPolicy, err)
	}
	syncDir = originalSync
	code, stdout, _ = f.run(t)
	if code != 0 || guiResult(t, stdout) != "succeeded" || f.operationsCalls.Load() != 1 || f.authLaunches.Load() != 1 || f.nativeCalls.Load() != 1 {
		t.Fatalf("retry code=%d stdout=%q operations=%d auth=%d native=%d", code, stdout, f.operationsCalls.Load(), f.authLaunches.Load(), f.nativeCalls.Load())
	}
}

func TestGUIPendingStartupOnlyConfirmsAndDoesNotStartAnotherPair(t *testing.T) {
	// Why: a stored pending operation must be the only action on restart; new OAuth, preflight, and dialogs could replace it.
	f := newGUIPairFixture(t, nil)
	originalRename := renameFile
	renameFile = func(old, new string) error {
		if new == f.policy {
			return os.ErrPermission
		}
		return os.Rename(old, new)
	}
	code, stdout, _ := f.run(t)
	if code != 0 || guiResult(t, stdout) != "local_apply_failed" {
		renameFile = originalRename
		t.Fatalf("seed pending code=%d stdout=%q", code, stdout)
	}
	renameFile = originalRename
	preflights, auth, consent, native, confirms := f.operationsCalls.Load(), f.authLaunches.Load(), f.consentLaunches.Load(), f.nativeCalls.Load(), f.confirmCalls.Load()
	code, stdout, _ = f.run(t)
	if code != 0 || guiResult(t, stdout) != "succeeded" || f.operationsCalls.Load() != preflights || f.authLaunches.Load() != auth || f.consentLaunches.Load() != consent || f.nativeCalls.Load() != native || f.confirmCalls.Load() != confirms+1 {
		t.Fatalf("resume code=%d stdout=%q auth=%d/%d preflights=%d/%d consent=%d/%d native=%d/%d confirm=%d/%d", code, stdout, f.authLaunches.Load(), auth, f.operationsCalls.Load(), preflights, f.consentLaunches.Load(), consent, f.nativeCalls.Load(), native, f.confirmCalls.Load(), confirms+1)
	}
}

func TestGUINativePromptUsesFixedScriptAndArgvValues(t *testing.T) {
	// Why: gateway-derived account and key data must never become executable AppleScript source.
	f := newGUIPairFixture(t, nil)
	code, stdout, _ := f.run(t)
	if code != 0 || guiResult(t, stdout) != "succeeded" || f.nativeCalls.Load() != 1 || f.nativeScript == "" || len(f.nativeArgs) != 4 {
		t.Fatalf("native invocation missing: code=%d stdout=%q calls=%d script=%q args=%q", code, stdout, f.nativeCalls.Load(), f.nativeScript, f.nativeArgs)
	}
	for _, value := range f.nativeArgs {
		if strings.Contains(f.nativeScript, value) {
			t.Fatalf("dynamic value embedded in AppleScript: value=%q script=%q", value, f.nativeScript)
		}
	}
	if f.nativeArgs[0] != guiPairSubject || f.nativeArgs[1] != "gui-host" || f.nativeArgs[2] != "新規" || !strings.HasPrefix(f.nativeArgs[3], "SHA256:") ||
		!strings.Contains(f.nativeScript, "on run argv") || !strings.Contains(f.nativeScript, "\"ブラウザで確認する\"") || !strings.Contains(f.nativeScript, "\"取り消す\"") {
		t.Fatalf("AppleScript argv mismatch: script=%q args=%q", f.nativeScript, f.nativeArgs)
	}
}

func TestGUIStdoutIsOneRedactedJSONLine(t *testing.T) {
	// Why: Tauri may receive only the protocol result, never bearer tokens, key material, or operation identifiers.
	f := newGUIPairFixture(t, nil)
	code, stdout, stderr := f.run(t)
	want := "{\"protocolVersion\":1,\"result\":\"succeeded\"}\n"
	if code != 0 || stdout != want || strings.Count(stdout, "\n") != 1 || strings.Contains(stdout, f.accessToken) || strings.Contains(stdout, f.operation) || strings.Contains(stdout, "SHA256:") || strings.Contains(stdout, "ssh-ed25519") || strings.Contains(stderr, f.accessToken) || strings.Contains(stderr, f.operation) || strings.Contains(stderr, "SHA256:") {
		t.Fatalf("GUI output leaked data or was malformed: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func mustSignerFromSeed(t *testing.T, seed byte) ssh.Signer {
	t.Helper()
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
