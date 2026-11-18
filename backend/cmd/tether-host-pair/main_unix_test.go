//go:build unix

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"golang.org/x/crypto/ssh"
)

func TestExecutePairsWithRealGatewayServePair(t *testing.T) {
	issuerKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x71}, ed25519.SeedSize))
	const kid = "pair-test"
	issuerURL, publicURL := "", ""
	accessToken := ""
	issuerServer := newLoopbackTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuerURL, "authorization_endpoint": issuerURL + "/authorize", "token_endpoint": issuerURL + "/token", "code_challenge_methods_supported": []string{"S256"}})
		case "/token":
			claims, _ := json.Marshal(map[string]any{"iss": issuerURL, "aud": publicURL, "sub": "subject-1", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()})
			header, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": kid, "typ": "at+jwt"})
			input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
			accessToken = input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(issuerKey, []byte(input)))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": accessToken, "token_type": "Bearer"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuerServer.Close()
	issuerURL = issuerServer.URL
	makeVerifier := func(audience string) *agentgateway.Verifier {
		pub := issuerKey.Public().(ed25519.PublicKey)
		jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": kid, "alg": "EdDSA", "use": "sig", "x": base64.RawURLEncoding.EncodeToString(pub)}}})
		v, err := agentgateway.NewVerifier(agentgateway.VerifierConfig{Issuer: issuerURL, Audience: audience, Types: []string{"at+jwt"}, JWKS: jwks})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	mainVerifier := makeVerifier("https://gateway.example.test/mcp")
	_, principalKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	principalSigner, err := ssh.NewSignerFromKey(principalKey)
	if err != nil {
		t.Fatal(err)
	}
	gw, err := agentgateway.New(agentgateway.Config{Verifier: mainVerifier, Resource: "https://gateway.example.test/mcp", Principals: []agentgateway.Principal{{Label: "member", Subject: "subject-1", Signer: principalSigner}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gw.Close(context.Background()) })
	if err := gw.ConfigureHosts([]agentgateway.HostEntry{{Label: "host-test", Principals: []string{"member"}, Paired: true}}); err != nil {
		t.Fatal(err)
	}
	var gatewayServer *httptest.Server
	gatewayServer = newLoopbackTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gw.ServePair(w, r) }))
	t.Cleanup(gatewayServer.Close)
	publicURL = gatewayServer.URL + agentgateway.PairPath
	registration := "wss://gateway.example.test/register"
	if err := gw.ConfigurePairing(agentgateway.PairingConfig{Verifier: makeVerifier(publicURL), Endpoint: publicURL, RegistrationEndpoint: registration}); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(issuerServer.Certificate())
	pool.AddCert(gatewayServer.Certificate())
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	defer client.CloseIdleConnections()
	home := t.TempDir()
	policyPath := filepath.Join(home, "Library", "Application Support", "pair-test", "resident-policy.json")
	args := []string{"--gateway", gatewayServer.URL, "--issuer", issuerServer.URL, "--client-id", "pair-client"}
	d := deps{client: client, location: func(context.Context) (string, string, error) { return home, policyPath, nil }, readProtected: func(_ context.Context, path string) ([]byte, error) { return os.ReadFile(path) }, callbackWait: time.Second, pairTimeout: time.Second}
	d.openBrowser = func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		callback, err := url.Parse(u.Query().Get("redirect_uri"))
		if err != nil {
			return err
		}
		q := callback.Query()
		q.Set("state", u.Query().Get("state"))
		q.Set("code", "auth-code")
		callback.RawQuery = q.Encode()
		resp, err := http.Get(callback.String())
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	var stdout, stderr strings.Builder
	if code := execute(context.Background(), args, &stdout, &stderr, d); code != 0 {
		t.Fatalf("execute status=%d stderr=%q", code, stderr.String())
	}
	raw, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := maclaunch.ValidateFixedResidentPolicy(raw, policyPath); err != nil {
		t.Fatalf("policy validation: %v", err)
	}
	var policy struct {
		RegistrationEndpoint string `json:"registration_endpoint"`
		HostLabel            string `json:"host_label"`
		PrincipalPublicKey   string `json:"principal_public_key"`
		HostKeyPath          string `json:"host_key_path"`
	}
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(policy.HostKeyPath)
	if err != nil {
		t.Fatalf("read host key %q: %v", policy.HostKeyPath, err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		t.Fatal("host key PEM missing")
	}
	signer, err := ssh.ParsePrivateKey(keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if got := gw.HostKey(policy.HostLabel); !bytes.Equal(got, signer.PublicKey().Marshal()) {
		t.Fatalf("gateway HostKey mismatch")
	}
	principalWire := ssh.MarshalAuthorizedKey(principalSigner.PublicKey())
	if policy.RegistrationEndpoint != registration || policy.HostLabel != "host-test" || policy.PrincipalPublicKey != strings.TrimSpace(strings.TrimPrefix(string(principalWire), "ssh-ed25519 ")) {
		t.Fatalf("policy/gateway mismatch: %+v", policy)
	}
	if !bytes.Equal(signer.PublicKey().Marshal(), gw.HostKey("host-test")) || accessToken == "" {
		t.Fatal("gateway did not accept CLI host key or token")
	}
}

func TestExecutePairLockPreventsBrowserAndNetwork(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Application Support", "com.o2connective.rescue-host.shell")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "pair.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	browser := false
	requests := 0
	d := deps{client: &http.Client{Transport: countingTransport{calls: &requests}}, location: func(context.Context) (string, string, error) {
		return home, filepath.Join(dir, "resident-policy.json"), nil
	}, openBrowser: func(string) error { browser = true; return nil }, callbackWait: time.Millisecond, pairTimeout: time.Millisecond}
	var out, stderr strings.Builder
	if code := execute(context.Background(), []string{"--gateway", "https://gateway.test", "--issuer", "https://issuer.test", "--client-id", "x"}, &out, &stderr, d); code != 1 || browser || requests != 0 || !strings.Contains(stderr.String(), diagPairRunning) {
		t.Fatalf("status=%d browser=%v requests=%d stderr=%s", code, browser, requests, stderr.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "pair.lock" {
		t.Fatalf("lock contention changed dir: %v %v", entries, err)
	}
}

func TestExecutePairLockOpenFailureUsesUnsafeLocationDiagnostic(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Application Support", "com.o2connective.rescue-host.shell")
	if err := os.MkdirAll(filepath.Join(dir, "pair.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	browser := false
	d := deps{client: http.DefaultClient, location: func(context.Context) (string, string, error) {
		return home, filepath.Join(dir, "resident-policy.json"), nil
	}, openBrowser: func(string) error { browser = true; return nil }}
	var out, stderr strings.Builder
	args := []string{"--gateway", "https://gw.test", "--issuer", "https://id.test", "--client-id", "x"}
	if code := execute(context.Background(), args, &out, &stderr, d); code != 1 || browser || !strings.Contains(stderr.String(), diagLocationUnsafe) {
		t.Fatalf("status=%d browser=%v stderr=%q", code, browser, stderr.String())
	}
}

func TestExecutePairLockUnexpectedFlockFailureUsesUnsafeLocationDiagnostic(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Application Support", "com.o2connective.rescue-host.shell")
	original := flockPair
	flockPair = func(int, int) error { return os.ErrPermission }
	t.Cleanup(func() { flockPair = original })
	browser := false
	d := deps{client: http.DefaultClient, location: func(context.Context) (string, string, error) {
		return home, filepath.Join(dir, "resident-policy.json"), nil
	}, openBrowser: func(string) error { browser = true; return nil }}
	var out, stderr strings.Builder
	args := []string{"--gateway", "https://gw.test", "--issuer", "https://id.test", "--client-id", "x"}
	if code := execute(context.Background(), args, &out, &stderr, d); code != 1 || browser || !strings.Contains(stderr.String(), diagLocationUnsafe) || strings.Contains(stderr.String(), diagPairRunning) {
		t.Fatalf("status=%d browser=%v stderr=%q", code, browser, stderr.String())
	}
}

type countingTransport struct{ calls *int }

func (r countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	*r.calls++
	return nil, nil
}

func TestExecutePolicyRenameFailureRollsBackNewKeyAndPreservesOldBytes(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Application Support", "com.o2connective.rescue-host.shell")
	policyPath := filepath.Join(dir, "resident-policy.json")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	oldPolicy := []byte("old policy marker bytes")
	if err := os.WriteFile(policyPath, oldPolicy, 0600); err != nil {
		t.Fatal(err)
	}
	oldKey := []byte("old key marker bytes")
	if err := os.WriteFile(filepath.Join(dir, "host-key-old"), oldKey, 0600); err != nil {
		t.Fatal(err)
	}
	originalRename := renameFile
	renameFile = func(old, new string) error {
		if new == policyPath {
			return os.ErrPermission
		}
		return os.Rename(old, new)
	}
	t.Cleanup(func() { renameFile = originalRename })
	f := newFlowFixture(t, "MATCH", 200)
	code, _, stderr, _ := invokeFlowAt(t, f, "", home)
	gotPolicy, policyErr := os.ReadFile(policyPath)
	gotKey, keyErr := os.ReadFile(filepath.Join(dir, "host-key-old"))
	entries, readErr := os.ReadDir(dir)
	if code != 1 || !strings.Contains(stderr, diagWriteFailed) || policyErr != nil || keyErr != nil || readErr != nil || !bytes.Equal(gotPolicy, oldPolicy) || !bytes.Equal(gotKey, oldKey) {
		t.Fatalf("rollback status=%d policy=%q key=%q errors=%v/%v/%v stderr=%s", code, gotPolicy, gotKey, policyErr, keyErr, readErr, stderr)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pair-") || strings.HasPrefix(e.Name(), "host-key-") && e.Name() != "host-key-old" {
			t.Errorf("new artifact remains: %s", e.Name())
		}
	}
}
