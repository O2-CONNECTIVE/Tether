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
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"golang.org/x/crypto/ssh"
)

func TestNewCallbackAcceptsOnlyOneMatchingLoopbackLogin(t *testing.T) {
	h, result := newCallback("expected-state")
	bad := httptest.NewRecorder()
	wrong := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/callback?state=wrong&code=secret", nil)
	wrong.RemoteAddr = "127.0.0.1:1234"
	h.ServeHTTP(bad, wrong)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("wrong state status=%d", bad.Code)
	}
	select {
	case got := <-result:
		if got.err == nil {
			t.Fatal("wrong state did not cancel")
		}
	default:
		t.Fatal("wrong state was not reported")
	}

	h, result = newCallback("state")
	for _, host := range []string{"192.0.2.1:1234", "[::1]:1234"} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/callback?state=state&code=x", nil)
		r.RemoteAddr = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("remote %s status=%d", host, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/callback?state=state&code=code", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "ログインが完了しました。このページを閉じてください" {
		t.Fatalf("accepted callback: %d %q", w.Code, w.Body.String())
	}
	second := httptest.NewRecorder()
	h.ServeHTTP(second, r)
	if second.Code != http.StatusConflict {
		t.Fatalf("second callback status=%d", second.Code)
	}
	select {
	case got := <-result:
		if got.code != "code" || got.err != nil {
			t.Fatalf("result=%+v", got)
		}
	default:
		t.Fatal("accepted callback not reported")
	}
}

func TestCallbackRejectsPresentEmptyError(t *testing.T) {
	h, result := newCallback("state")
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/callback?state=state&code=code&error=", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty error status=%d", w.Code)
	}
	if got := <-result; got.err == nil {
		t.Fatalf("empty error accepted: %+v", got)
	}
}

func TestCallbackRejectsMalformedQueryBeforeTokenExchange(t *testing.T) {
	h, result := newCallback("state")
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/callback?state=state&code=code&error=%zz", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed query status=%d", w.Code)
	}
	if got := <-result; got.err == nil || got.code != "" {
		t.Fatalf("malformed query accepted: %+v", got)
	}
}

func TestParsePairResultRequiresExactJSONMediaTypeAndMembers(t *testing.T) {
	valid := []byte(`{"registration_endpoint":"wss://gw.test/register","host_label":"host","principal_label":"principal","principal_public_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAvzQ9BrCC5xE1lQfM4NXu7CwJwE74lfM2nCHx9+spk8"}`)
	duplicate := strings.Replace(string(valid), `,"host_label"`, `,"registration_endpoint":"wss://gw.test/register","host_label"`, 1)
	unknown := strings.TrimSuffix(string(valid), "}") + `,"extra":true}`
	for _, tc := range []struct{ ct, body string }{
		{"application/jsonp", string(valid)},
		{"application/json", duplicate},
		{"application/json", unknown},
	} {
		if _, err := parsePairResult(tc.ct, []byte(tc.body)); err == nil {
			t.Errorf("accepted content-type=%q body=%s", tc.ct, tc.body)
		}
	}
}

func TestExecuteRejectsInvalidArgumentsWithoutNetworkOrBrowser(t *testing.T) {
	calls := 0
	d := deps{client: http.DefaultClient, location: func(context.Context) (string, string, error) { calls++; return "", "", nil }, openBrowser: func(string) error { calls++; return nil }, callbackWait: time.Millisecond, pairTimeout: time.Millisecond}
	cases := [][]string{{}, {"--gateway", "https://gw.test", "--issuer", "https://id.test"}, {"--gateway", "https://gw.test", "--gateway", "https://gw.test", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "http://gw.test", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "https://gw.test/path", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "https://gw.test?x=1", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "HTTPS://gw.test", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "https://GW.test", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "https://gw.test", "--issuer", "https://id.test", "--client-id", "x", "extra"}}
	for _, args := range cases {
		var out, errOut strings.Builder
		if got := execute(context.Background(), args, &out, &errOut, d); got != 2 {
			t.Errorf("args %q status=%d stderr=%q", args, got, errOut.String())
		}
	}
	if calls != 0 {
		t.Fatalf("invalid arguments touched injected services %d times", calls)
	}
}

func TestExecuteDirectoryCreationFailureUsesUnsafeLocationDiagnostic(t *testing.T) {
	home := t.TempDir()
	blocker := filepath.Join(home, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	d := deps{client: http.DefaultClient, location: func(context.Context) (string, string, error) {
		return home, filepath.Join(blocker, "resident-policy.json"), nil
	}}
	var out, stderr strings.Builder
	args := []string{"--gateway", "https://gw.test", "--issuer", "https://id.test", "--client-id", "x"}
	if code := execute(context.Background(), args, &out, &stderr, d); code != 1 || !strings.Contains(stderr.String(), diagLocationUnsafe) {
		t.Fatalf("status=%d stderr=%q", code, stderr.String())
	}
}

type flowFixture struct {
	home                  string
	issuer, gateway       *httptest.Server
	client                *http.Client
	gatewayCalls          int
	malformed             bool
	gatewayDelay          time.Duration
	tokenForm             url.Values
	seenHostKey           string
	readProtected         func(context.Context, string) ([]byte, error)
	authorizationEndpoint string
}

func newLoopbackTLSServer(h http.Handler) *httptest.Server {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	s := &httptest.Server{Listener: ln, Config: &http.Server{Handler: h}}
	s.StartTLS()
	return s
}

func newFlowFixture(t *testing.T, discoveryIssuer string, gatewayStatus int) *flowFixture {
	t.Helper()
	f := &flowFixture{}
	principal, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	principalSSH, err := ssh.NewPublicKey(principal)
	if err != nil {
		t.Fatal(err)
	}
	principalWire := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(principalSSH.Marshal())
	f.gateway = newLoopbackTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gatewayCalls++
		if f.gatewayDelay > 0 {
			time.Sleep(f.gatewayDelay)
		}
		var request struct {
			HostPublicKey string `json:"host_public_key"`
			Proof         string `json:"proof"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		f.seenHostKey = request.HostPublicKey
		if r.Header.Get("Authorization") != "Bearer ACCESS_SECRET" {
			t.Errorf("unexpected authorization header")
		}
		parts := strings.Split(request.HostPublicKey, " ")
		if len(parts) != 2 {
			t.Errorf("bad host key wire %q", request.HostPublicKey)
		} else {
			wire, e := base64.StdEncoding.DecodeString(parts[1])
			if e != nil {
				t.Error(e)
			} else {
				pub, e := ssh.ParsePublicKey(wire)
				if e != nil {
					t.Error(e)
				} else {
					proof, e := base64.RawURLEncoding.DecodeString(request.Proof)
					if e != nil {
						t.Error(e)
					} else {
						var sig ssh.Signature
						e = ssh.Unmarshal(proof, &sig)
						sum := sha256.Sum256([]byte("ACCESS_SECRET"))
						if e != nil || pub.Verify(agentgateway.PairTranscript(f.gateway.URL+agentgateway.PairPath, wire, sum), &sig) != nil {
							t.Errorf("pair proof failed: %v", e)
						}
					}
				}
			}
		}
		if r.URL.Path != "/tether-agent/v1/pair" {
			t.Errorf("pair path=%s", r.URL.Path)
		}
		if gatewayStatus == 200 {
			w.Header().Set("Content-Type", "application/json")
			if f.malformed {
				io.WriteString(w, `{"unexpected":true}`)
			} else {
				response, _ := json.Marshal(map[string]string{"registration_endpoint": "wss://gw.test/register", "host_label": "host", "principal_label": "principal", "principal_public_key": principalWire})
				_, _ = w.Write(response)
			}
			return
		}
		w.WriteHeader(gatewayStatus)
	}))
	f.issuer = newLoopbackTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "openid-configuration") {
			w.Header().Set("Content-Type", "application/json")
			authEndpoint := f.authorizationEndpoint
			if authEndpoint == "" {
				authEndpoint = f.issuer.URL + "/authorize"
			}
			json.NewEncoder(w).Encode(map[string]any{"issuer": func() string {
				if discoveryIssuer == "MATCH" {
					return f.issuer.URL
				}
				return discoveryIssuer
			}(), "authorization_endpoint": authEndpoint, "token_endpoint": f.issuer.URL + "/token", "code_challenge_methods_supported": []string{"S256"}})
			return
		}
		if r.URL.Path == "/token" {
			_ = r.ParseForm()
			f.tokenForm = r.PostForm
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"access_token": "ACCESS_SECRET", "refresh_token": "REFRESH_SECRET", "token_type": "Bearer"})
			return
		}
		http.NotFound(w, r)
	}))
	pool := x509.NewCertPool()
	pool.AddCert(f.issuer.Certificate())
	pool.AddCert(f.gateway.Certificate())
	f.client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	t.Cleanup(func() { f.issuer.Close(); f.gateway.Close(); f.client.CloseIdleConnections() })
	return f
}

func invokeFlow(t *testing.T, f *flowFixture, callbackMode string) (int, string, string, string) {
	t.Helper()
	home := t.TempDir()
	return invokeFlowAt(t, f, callbackMode, home)
}
func invokeFlowAt(t *testing.T, f *flowFixture, callbackMode, home string) (int, string, string, string) {
	f.home = home
	dir := home + "/Library/Application Support/com.o2connective.rescue-host.shell"
	policy := dir + "/resident-policy.json"
	var authURL string
	d := deps{client: f.client, location: func(context.Context) (string, string, error) { return home, policy, nil }, callbackWait: 40 * time.Millisecond, pairTimeout: 40 * time.Millisecond, readProtected: f.readProtected, openBrowser: func(raw string) error {
		authURL = raw
		go func() {
			u, _ := url.Parse(raw)
			redirect, _ := url.Parse(u.Query().Get("redirect_uri"))
			q := redirect.Query()
			q.Set("state", u.Query().Get("state"))
			q.Set("code", "AUTH_CODE")
			if callbackMode == "no-callback" {
				return
			}
			if callbackMode == "wrong-state" {
				q.Set("state", "wrong")
			}
			if callbackMode == "malformed-query" {
				redirect.RawQuery = "state=" + url.QueryEscape(u.Query().Get("state")) + "&code=AUTH_CODE&error=%zz"
				_, _ = f.client.Get(redirect.String())
				return
			}
			redirect.RawQuery = q.Encode()
			_, _ = f.client.Get(redirect.String())
			if callbackMode == "twice" {
				_, _ = f.client.Get(redirect.String())
			}
		}()
		return nil
	}}
	if d.readProtected == nil {
		d.readProtected = func(_ context.Context, path string) ([]byte, error) { return os.ReadFile(path) }
	}
	var stdout, stderr strings.Builder
	code := execute(context.Background(), []string{"--gateway", f.gateway.URL, "--issuer", f.issuer.URL, "--client-id", "client"}, &stdout, &stderr, d)
	return code, stdout.String(), stderr.String(), authURL
}

func TestExecuteMalformedCallbackDoesNotExchangeToken(t *testing.T) {
	f := newFlowFixture(t, "MATCH", 200)
	code, _, stderr, _ := invokeFlow(t, f, "malformed-query")
	if code != 1 || f.tokenForm != nil || f.gatewayCalls != 0 || !strings.Contains(stderr, diagLoginCancelled) {
		t.Fatalf("status=%d token=%v gateway=%d stderr=%q", code, f.tokenForm, f.gatewayCalls, stderr)
	}
}

func TestExecuteCompletesPKCEPairAndWritesNoTokens(t *testing.T) {
	f := newFlowFixture(t, "MATCH", 200)
	code, _, stderr, raw := invokeFlow(t, f, "twice")
	if code != 0 {
		t.Fatalf("execute status=%d stderr=%s", code, stderr)
	}
	auth, _ := url.Parse(raw)
	q := auth.Query()
	challenge := q.Get("code_challenge")
	if challenge == "" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("scope") != "" {
		t.Fatalf("authorization query=%v", q)
	}
	if !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") {
		t.Fatalf("redirect_uri=%q", q.Get("redirect_uri"))
	}
	verifier := f.tokenForm.Get("code_verifier")
	sum := sha256.Sum256([]byte(verifier))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
		t.Fatalf("token verifier does not match challenge")
	}
	if f.tokenForm.Get("client_id") != "client" || f.tokenForm.Get("redirect_uri") != q.Get("redirect_uri") || f.tokenForm.Get("code") != "AUTH_CODE" {
		t.Fatalf("token form mismatch: %v", f.tokenForm)
	}
	if f.seenHostKey == "" {
		t.Fatal("gateway did not receive a host public key")
	}
	policyPath := f.home + "/Library/Application Support/com.o2connective.rescue-host.shell/resident-policy.json"
	policyBytes, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = maclaunch.ValidateFixedResidentPolicy(policyBytes, policyPath); err != nil {
		t.Fatalf("written policy rejected: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(policyPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".pair-probe-") {
			t.Fatalf("successful pair left probe: %s", entry.Name())
		}
	}
	var doc struct {
		HostKeyPath string `json:"host_key_path"`
		Fingerprint string `json:"host_key_fingerprint"`
	}
	if err = json.Unmarshal(policyBytes, &doc); err != nil {
		t.Fatal(err)
	}
	keyRaw, err := os.ReadFile(doc.HostKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(keyRaw)
	if block == nil {
		t.Fatal("private key is not PEM")
	}
	signer, err := ssh.ParsePrivateKey(keyRaw)
	if err != nil {
		t.Fatal(err)
	}
	if "ssh-ed25519 "+base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal()) != f.seenHostKey || ssh.FingerprintSHA256(signer.PublicKey()) != doc.Fingerprint {
		t.Fatal("persisted key differs from gateway key or fingerprint")
	}
	keyInfo, err := os.Stat(doc.HostKeyPath)
	if err != nil || keyInfo.Mode().Perm() != 0600 {
		t.Fatalf("private key mode=%v err=%v", keyInfo, err)
	}
	dirInfo, err := os.Stat(filepath.Dir(policyPath))
	if err != nil || dirInfo.Mode().Perm() != 0700 {
		t.Fatalf("enrollment directory mode=%v err=%v", dirInfo, err)
	}
	policyInfo, err := os.Stat(policyPath)
	if err != nil || policyInfo.Mode().Perm() != 0600 {
		t.Fatalf("policy mode=%v err=%v", policyInfo, err)
	}
	if strings.Contains(string(keyRaw), "ACCESS_SECRET") || strings.Contains(string(raw), "ACCESS_SECRET") {
		t.Fatal("access token persisted")
	}
	// The token endpoint's form must bind this exact browser transaction.
	if f.gatewayCalls != 1 {
		t.Fatalf("gateway calls=%d", f.gatewayCalls)
	}
	if strings.Contains(stderr, "ACCESS_SECRET") || strings.Contains(stderr, "REFRESH_SECRET") {
		t.Fatal("credential leaked to stderr")
	}
	err = filepath.WalkDir(f.home, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if bytes.Contains(b, []byte("ACCESS_SECRET")) || bytes.Contains(b, []byte("REFRESH_SECRET")) {
			t.Errorf("token persisted in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestExecuteWrongStateStopsBeforePairAndLeavesLocationAlone(t *testing.T) {
	f := newFlowFixture(t, "MATCH", 200)
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Application Support", "com.o2connective.rescue-host.shell")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	before := []byte("unchanged bytes")
	if err := os.WriteFile(filepath.Join(dir, "resident-policy.json"), before, 0600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr, _ := invokeFlowAt(t, f, "wrong-state", home)
	after, err := os.ReadFile(filepath.Join(dir, "resident-policy.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("wrong-state callback changed policy: %q %v", after, err)
	}
	if code != 1 || !strings.Contains(stderr, diagLoginCancelled) || f.gatewayCalls != 0 {
		t.Fatalf("status=%d calls=%d stderr=%s", code, f.gatewayCalls, stderr)
	}
}
func TestExecuteDiscoveryIssuerMismatchDoesNotOpenBrowser(t *testing.T) {
	f := newFlowFixture(t, "https://other.test", 200)
	code, _, _, auth := invokeFlow(t, f, "")
	if code != 1 || f.gatewayCalls != 0 || auth != "" {
		t.Fatalf("status=%d gateway=%d browser=%q", code, f.gatewayCalls, auth)
	}
}

func TestExecuteProtectedReadFailureStopsBeforeBrowserOrNetwork(t *testing.T) {
	f := newFlowFixture(t, "MATCH", 200)
	f.readProtected = func(context.Context, string) ([]byte, error) { return nil, os.ErrPermission }
	code, _, stderr, auth := invokeFlow(t, f, "")
	if code != 1 || stderr == "" || !strings.Contains(stderr, diagLocationUnsafe) || auth != "" || f.gatewayCalls != 0 {
		t.Fatalf("status=%d auth=%q gateway=%d stderr=%q", code, auth, f.gatewayCalls, stderr)
	}
}

func TestExecuteProbeReadFailureStopsAndRemovesProbe(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "enrollment")
	policy := filepath.Join(dir, "resident-policy.json")
	args := []string{"--gateway", "https://gw.test", "--issuer", "https://id.test", "--client-id", "client"}
	reads := 0
	auth, network := 0, 0
	d := deps{client: http.DefaultClient, location: func(context.Context) (string, string, error) { return home, policy, nil }, readProtected: func(ctx context.Context, path string) ([]byte, error) {
		if strings.HasPrefix(filepath.Base(path), ".pair-probe-") {
			reads++
			return nil, os.ErrPermission
		}
		return os.ReadFile(path)
	}, openBrowser: func(string) error { auth++; return nil }}
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { network++; return nil, os.ErrPermission }}
	d.client = &http.Client{Transport: transport}
	var stdout, stderr strings.Builder
	code := execute(context.Background(), args, &stdout, &stderr, d)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".pair-probe-") {
			t.Fatalf("probe left behind: %s", entry.Name())
		}
	}
	if code != 1 || reads != 1 || auth != 0 || network != 0 || !strings.Contains(stderr.String(), diagLocationUnsafe) {
		t.Fatalf("status=%d probe reads=%d auth=%d network=%d stderr=%q", code, reads, auth, network, stderr.String())
	}
}

func TestAuthorizationEndpointQueryIsPreservedExceptScope(t *testing.T) {
	f := newFlowFixture(t, "MATCH", 200)
	f.authorizationEndpoint = f.issuer.URL + "/authorize?audience=existing&scope=discard"
	_, _, _, raw := invokeFlow(t, f, "")
	u, err := url.Parse(raw)
	if err != nil || u.Query().Get("audience") != "existing" || u.Query().Has("scope") {
		t.Fatalf("authorization URL=%q err=%v", raw, err)
	}
}
func TestExecuteGatewayOutcomesPreserveExistingDirectory(t *testing.T) {
	for _, status := range []int{403, 409, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newFlowFixture(t, "MATCH", status)
			oldHome := t.TempDir()
			oldDir := filepath.Join(oldHome, "Library", "Application Support", "com.o2connective.rescue-host.shell")
			if err := os.MkdirAll(oldDir, 0700); err != nil {
				t.Fatal(err)
			}
			oldPolicy := []byte("old-policy-sentinel\x00bytes")
			oldKey := []byte("old-key-sentinel\x00bytes")
			if err := os.WriteFile(filepath.Join(oldDir, "resident-policy.json"), oldPolicy, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(oldDir, "host-key-old"), oldKey, 0600); err != nil {
				t.Fatal(err)
			}
			code, _, stderr, _ := invokeFlowAt(t, f, "", oldHome)
			for name, want := range map[string][]byte{"resident-policy.json": oldPolicy, "host-key-old": oldKey} {
				got, err := os.ReadFile(filepath.Join(oldDir, name))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("%s changed: %q %v", name, got, err)
				}
			}
			if code != 1 || f.gatewayCalls != 1 || !strings.Contains(stderr, diagPairRejected) && status < 500 || !strings.Contains(stderr, diagPairUnknown) && status >= 500 {
				t.Fatalf("status=%d stderr=%s", code, stderr)
			}
		})
	}
}
func TestExecuteCallbackDeadlineAndMalformedSuccessAreFailures(t *testing.T) {
	f := newFlowFixture(t, "MATCH", 200)
	code, _, stderr, _ := invokeFlow(t, f, "no-callback")
	if code != 1 || f.gatewayCalls != 0 || !strings.Contains(stderr, diagLoginCancelled) {
		t.Fatalf("callback timeout status=%d calls=%d stderr=%s", code, f.gatewayCalls, stderr)
	}
}

func TestExecuteMalformedGatewaySuccessIsUnknownAndPreservesOldFiles(t *testing.T) {
	f := newFlowFixture(t, "MATCH", 200)
	f.malformed = true
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Application Support", "com.o2connective.rescue-host.shell")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	old := []byte("prior-policy-bytes")
	if err := os.WriteFile(filepath.Join(dir, "resident-policy.json"), old, 0600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr, _ := invokeFlowAt(t, f, "", home)
	got, err := os.ReadFile(filepath.Join(dir, "resident-policy.json"))
	if err != nil || !bytes.Equal(got, old) || code != 1 || !strings.Contains(stderr, diagPairUnknown) {
		t.Fatalf("status=%d old=%q err=%v stderr=%s", code, got, err, stderr)
	}
}

func TestExecuteSuccessRemovesOnlyPairOwnedOldKey(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "pair-owned", true: "user-key"}[external], func(t *testing.T) {
			f := newFlowFixture(t, "MATCH", 200)
			home := t.TempDir()
			dir := filepath.Join(home, "Library", "Application Support", "com.o2connective.rescue-host.shell")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			oldKey := filepath.Join(dir, "host-key-old")
			if external {
				oldKey = filepath.Join(home, ".ssh", "id_ed25519")
				if err := os.MkdirAll(filepath.Dir(oldKey), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "resident-policy.json"), []byte(`{"host_key_path":"`+oldKey+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(oldKey, []byte("old-key"), 0600); err != nil {
				t.Fatal(err)
			}
			code, _, _, _ := invokeFlowAt(t, f, "", home)
			_, err := os.Stat(oldKey)
			if code != 0 || external && err != nil || !external && !os.IsNotExist(err) {
				t.Fatalf("status=%d old-key error=%v", code, err)
			}
		})
	}
}

func TestExecuteSuccessDoesNotRemoveNonCanonicalOldKeyPath(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "enrollment")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "host-key-old")
	if err := os.WriteFile(outside, []byte("outside key"), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(home, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(nested, filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	oldKey := dir + "/sub/../host-key-old"
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(private.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	result := pairResult{RegistrationEndpoint: "wss://gw.test/register", HostLabel: "host", PrincipalLabel: "principal", PrincipalPublicKey: "ssh-ed25519 " + base64.StdEncoding.EncodeToString(public.Marshal())}
	if _, err := install(dir, filepath.Join(dir, "resident-policy.json"), home, private, signer, result, []byte(`{"host_key_path":"`+oldKey+`"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside key removed: %v", err)
	}
}

func TestExecuteUsesProtectedPolicyBytesForOldKeySelection(t *testing.T) {
	f := newFlowFixture(t, "MATCH", 200)
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Application Support", "com.o2connective.rescue-host.shell")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "host-key-from-protected-read")
	if err := os.WriteFile(key, []byte("old-key"), 0600); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(dir, "resident-policy.json")
	if err := os.WriteFile(policyPath, []byte(`{"host_key_path":"external"}`), 0600); err != nil {
		t.Fatal(err)
	}
	f.readProtected = func(ctx context.Context, path string) ([]byte, error) {
		if path == policyPath {
			return []byte(`{"host_key_path":"` + key + `"}`), nil
		}
		return os.ReadFile(path)
	}
	code, _, stderr, _ := invokeFlowAt(t, f, "", home)
	_, err := os.Stat(key)
	if code != 0 || !os.IsNotExist(err) {
		t.Fatalf("status=%d key error=%v stderr=%q", code, err, stderr)
	}
}

func TestExecuteDirectorySyncFailureWarnsAfterSuccessfulInstall(t *testing.T) {
	original := syncDir
	syncDir = func(string) error { return os.ErrPermission }
	t.Cleanup(func() { syncDir = original })
	f := newFlowFixture(t, "MATCH", 200)
	code, stdout, stderr, _ := invokeFlow(t, f, "")
	if code != 0 || !strings.Contains(stdout, diagPairComplete) || !strings.Contains(stderr, diagSyncWarning) {
		t.Fatalf("status=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	policyPath := filepath.Join(f.home, "Library", "Application Support", "com.o2connective.rescue-host.shell", "resident-policy.json")
	if _, err := os.Stat(policyPath); err != nil {
		t.Fatalf("policy missing: %v", err)
	}
}

func TestExecutePairTimeoutPreservesOldEnrollment(t *testing.T) {
	f := newFlowFixture(t, "MATCH", 200)
	f.gatewayDelay = 150 * time.Millisecond
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Application Support", "com.o2connective.rescue-host.shell")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	old := []byte("old policy exact bytes")
	if err := os.WriteFile(filepath.Join(dir, "resident-policy.json"), old, 0600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr, _ := invokeFlowAt(t, f, "", home)
	after, err := os.ReadFile(filepath.Join(dir, "resident-policy.json"))
	if err != nil || !bytes.Equal(after, old) || code != 1 || !strings.Contains(stderr, diagPairUnknown) {
		t.Fatalf("status=%d old=%q err=%v stderr=%s", code, after, err, stderr)
	}
}
