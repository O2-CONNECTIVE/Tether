//go:build darwin && cgo && pair_loopback

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

const pairLoopbackSubject = "synthetic-subject-1"

type pairLoopbackPolicy struct {
	RegistrationEndpoint string `json:"registration_endpoint"`
	HostLabel            string `json:"host_label"`
	PrincipalLabel       string `json:"principal_label"`
	HostKeyPath          string `json:"host_key_path"`
	HostKeyFingerprint   string `json:"host_key_fingerprint"`
}

type pairLoopbackCapture struct{}

func (pairLoopbackCapture) NewClient(maclaunch.ResidentClientConfig, *maclaunch.Approved) (maclaunch.ResidentClient, error) {
	return nil, fmt.Errorf("resident client construction is not part of this loopback")
}

func TestPairLoopbackE2E(t *testing.T) {
	child := os.Getenv("TETHER_PAIR_CHILD_BINARY")
	if child == "" {
		t.Fatal("TETHER_PAIR_CHILD_BINARY must point to the pair child test binary")
	}
	if _, err := os.Stat(child); err != nil {
		t.Fatal("pair child binary unavailable", err)
	}
	expectedRedirect := os.Getenv("TETHER_PAIR_LOOPBACK_EXPECT_REDIRECT")
	root, err := os.MkdirTemp(".", ".pair-loopback-")
	if err != nil {
		t.Fatal("create protected loopback directory", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal("resolve protected loopback directory", err)
	}
	caPEM, cert, roots := pairLoopbackCertificates(t)
	caPath := filepath.Join(root, "loopback-ca.pem")
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatal("write CA", err)
	}
	issuerListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen issuer", err)
	}
	issuerURL := "https://" + issuerListener.Addr().String() + "/"
	issuerKey := seedKey(0x51)
	resource := ""
	token := func(audience string) string {
		header := base64.RawURLEncoding.EncodeToString(encode(t, map[string]any{"alg": "EdDSA", "kid": "synthetic-ed", "typ": "at+jwt"}))
		claims := base64.RawURLEncoding.EncodeToString(encode(t, map[string]any{"iss": issuerURL, "aud": audience, "sub": pairLoopbackSubject, "scope": "rescue.agent", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()}))
		input := header + "." + claims
		return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(issuerKey, []byte(input)))
	}
	issuerMux := http.NewServeMux()
	issuerMux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuerURL, "authorization_endpoint": strings.TrimSuffix(issuerURL, "/") + "/authorize", "token_endpoint": strings.TrimSuffix(issuerURL, "/") + "/token", "code_challenge_methods_supported": []string{"S256"}})
	})
	issuerMux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		audience := "pair-api"
		if r.Form.Get("code") == "mcp-loopback-code" {
			audience = resource
		} else if expectedRedirect != "" && r.Form.Get("redirect_uri") != expectedRedirect {
			http.Error(w, "invalid redirect URI", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token(audience), "token_type": "Bearer"})
	})
	issuerServer := &http.Server{Handler: issuerMux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		_ = issuerServer.Serve(tls.NewListener(issuerListener, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}))
	}()
	t.Cleanup(func() { _ = issuerServer.Close() })

	gatewayListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen gateway", err)
	}
	t.Cleanup(func() { _ = gatewayListener.Close() })
	baseURL := "https://" + gatewayListener.Addr().String()
	resource = baseURL + "/mcp"
	registrationEndpoint := "wss://" + gatewayListener.Addr().String() + "/agent-register"
	issuerClient := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	defer issuerClient.CloseIdleConnections()

	principalDir := filepath.Join(root, "gateway-keys")
	if err := os.Mkdir(principalDir, 0o700); err != nil {
		t.Fatal("create principal key directory", err)
	}
	for _, item := range []struct {
		label string
		seed  byte
	}{{"member-1", 0x11}, {"member-2", 0x12}} {
		block, err := ssh.MarshalPrivateKey(seedKey(item.seed), "loopback "+item.label)
		if err != nil {
			t.Fatal("marshal gateway principal key", err)
		}
		path := filepath.Join(principalDir, item.label)
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal("write gateway principal key", err)
		}
	}

	config := pairingDoc(t)
	gw := config["gateway"].(map[string]any)
	gw["listen"], gw["issuer"], gw["resource"] = gatewayListener.Addr().String(), issuerURL, resource
	keys := config["principal_keys"].([]any)
	for i, item := range []struct {
		label string
		seed  byte
	}{{"member-1", 0x11}, {"member-2", 0x12}} {
		keys[i].(map[string]any)["path"] = filepath.Join(principalDir, item.label)
	}
	config["registration_endpoint"] = registrationEndpoint
	pairing := config["pairing"].(map[string]any)
	pairing["registry_path"] = filepath.Join(root, "pairing-registry.json")
	configPath := filepath.Join(root, "gateway.json")
	if err := os.WriteFile(configPath, encode(t, config), 0o600); err != nil {
		t.Fatal("write gateway config", err)
	}
	preflight, preflightCancel := context.WithTimeout(context.Background(), 5*time.Second)
	preflightRaw, err := agentkeys.ReadTrustConfig(preflight, configPath)
	clear(preflightRaw)
	preflightCancel()
	if err != nil {
		t.Fatalf("gateway config is not readable by the native trust reader: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var diagnostic strings.Builder
	d := nativeDeps()
	d.listen = func(network, addr string) (net.Listener, error) {
		if network != "tcp" || addr != gatewayListener.Addr().String() {
			return nil, fmt.Errorf("unexpected gateway listen request")
		}
		return tls.NewListener(gatewayListener, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}), nil
	}
	started := make(chan int, 1)
	go func() { started <- execute(ctx, []string{"-config", configPath}, &diagnostic, d) }()
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	defer client.CloseIdleConnections()
	waitGatewayReady(t, client, baseURL+"/mcp", &diagnostic)
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-started:
			if code != 0 {
				t.Errorf("gateway exit=%d: %s", code, diagnostic.String())
			}
		case <-time.After(10 * time.Second):
			t.Error("gateway did not stop")
		}
	})

	home := filepath.Join(root, "pair-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal("create pair home", err)
	}
	policyPath := filepath.Join(home, "Library", "Application Support", "O2-CONNECTIVE", "Tether", "resident-policy.json")
	childEnv := []string{"TETHER_PAIR_LOOPBACK_CHILD=1", "TETHER_PAIR_LOOPBACK_HOME=" + home, "TETHER_PAIR_LOOPBACK_POLICY=" + policyPath, "TETHER_PAIR_LOOPBACK_GATEWAY=" + baseURL, "TETHER_PAIR_LOOPBACK_ISSUER=" + issuerURL, "TETHER_PAIR_LOOPBACK_CLIENT_ID=pair-client", "TETHER_PAIR_LOOPBACK_CA=" + caPath}
	if expectedRedirect != "" {
		childEnv = append(childEnv, "TETHER_PAIR_LOOPBACK_EXPECT_REDIRECT="+expectedRedirect)
	}
	runPairChild(t, child, childEnv)
	first := loadPairPolicy(t, policyPath)
	loadStandbyPolicy(t, policyPath)
	firstSigner := loadPairHostKey(t, first)
	ws, transcript := dialResidentTLS(t, client, first, firstSigner, publicOf(t, 0x11))
	defer ws.CloseNow()
	mcpToken := issueLoopbackToken(t, issuerClient, issuerURL, "mcp-loopback-code")
	sid := residentSessionTLS(t, client, baseURL, mcpToken)
	requestID := strings.Repeat("a", 32)
	if code, body := residentToolTLS(t, client, baseURL, mcpToken, sid, "tether_fixed_start", map[string]any{"request_id": requestID}); code != http.StatusOK || !strings.Contains(body, `"phase":"pending"`) {
		t.Fatalf("fixed work dispatch failed: %d %s", code, body)
	}
	intent := residentReadMessage(t, ws)
	refusal := []string{intent["generation"], intent["sequence"], intent["request_id"], "work_unconfigured"}
	sig, err := firstSigner.Sign(nil, transcript("host-refused", refusal...))
	if err != nil {
		t.Fatal("sign A6 refusal", err)
	}
	residentWriteMessage(t, ws, map[string]string{"type": "resident_refused", "generation": refusal[0], "sequence": refusal[1], "request_id": refusal[2], "code": refusal[3], "signature": base64.RawURLEncoding.EncodeToString(ssh.Marshal(sig))})
	waitFinishedTLS(t, client, baseURL, mcpToken, sid, requestID)

	runPairChild(t, child, childEnv)
	second := loadPairPolicy(t, policyPath)
	loadStandbyPolicy(t, policyPath)
	secondSigner := loadPairHostKey(t, second)
	if second.HostKeyFingerprint == first.HostKeyFingerprint || string(secondSigner.PublicKey().Marshal()) == string(firstSigner.PublicKey().Marshal()) {
		t.Fatal("replacement did not install a new host key")
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _, closeErr := ws.Read(closeCtx)
	closeCtxErr := closeCtx.Err()
	closeCancel()
	if closeErr == nil || closeCtxErr != nil {
		t.Fatal("old resident websocket remained open after replacement")
	}
	if _, _, err := dialResidentTLSAttempt(t, client, first, firstSigner, publicOf(t, 0x11)); err == nil {
		t.Fatal("old host key reconnected after replacement")
	}
	newWS, _ := dialResidentTLS(t, client, second, secondSigner, publicOf(t, 0x11))
	defer newWS.CloseNow()
	registryPath := pairing["registry_path"].(string)
	registryRaw, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal("read persisted pairing registry", err)
	}
	var stored storedPairRegistry
	if err := json.Unmarshal(registryRaw, &stored); err != nil || len(stored.Hosts) != 1 {
		t.Fatalf("decode persisted registry: %v; entries=%d", err, len(stored.Hosts))
	}
	registeredKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(stored.Hosts[0].PublicKey))
	if err != nil || ssh.FingerprintSHA256(registeredKey) != second.HostKeyFingerprint {
		t.Fatalf("registry did not persist replacement key: %v", err)
	}
}

func pairLoopbackCertificates(t *testing.T) ([]byte, tls.Certificate, *x509.CertPool) {
	t.Helper()
	_, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal("generate CA key", err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "pair-loopback CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caKey.Public(), caKey)
	if err != nil {
		t.Fatal("create CA", err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal("parse CA", err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("generate loopback TLS key", err)
	}
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, leafKey.Public(), caKey)
	if err != nil {
		t.Fatal("create loopback certificate", err)
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal("marshal TLS key", err)
	}
	cert, err := tls.X509KeyPair(append(leafPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal("load TLS key pair", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("load CA roots")
	}
	return caPEM, cert, roots
}

func waitGatewayReady(t *testing.T, client *http.Client, endpoint string, diagnostic *strings.Builder) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	last := "no response"
	for time.Now().Before(deadline) {
		response, err := client.Get(endpoint)
		if err == nil {
			response.Body.Close()
			last = fmt.Sprintf("HTTP %d", response.StatusCode)
			if response.StatusCode < http.StatusInternalServerError {
				return
			}
		} else {
			last = err.Error()
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("gateway did not become ready: %s; execute diagnostic=%s", last, diagnostic.String())
}

func runPairChild(t *testing.T, binary string, env []string) {
	t.Helper()
	cmd := exec.Command(binary, "-test.run=^TestPairLoopbackChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pair child failed: %v\n%s", err, output)
	}
}

func issueLoopbackToken(t *testing.T, client *http.Client, issuer, code string) string {
	t.Helper()
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://127.0.0.1/callback"}, "client_id": {"pair-client"}, "code_verifier": {"loopback"}}
	response, err := client.PostForm(strings.TrimSuffix(issuer, "/")+"/token", form)
	if err != nil {
		t.Fatal("request loopback IdP token", err)
	}
	defer response.Body.Close()
	var result struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&result) != nil || result.AccessToken == "" || result.TokenType != "Bearer" {
		t.Fatalf("loopback IdP token response rejected: %d", response.StatusCode)
	}
	return result.AccessToken
}

func loadPairPolicy(t *testing.T, path string) pairLoopbackPolicy {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read generated policy", err)
	}
	var policy pairLoopbackPolicy
	if err := json.Unmarshal(raw, &policy); err != nil || policy.RegistrationEndpoint == "" || policy.HostLabel == "" || policy.PrincipalLabel == "" {
		t.Fatalf("decode generated binding: %v", err)
	}
	return policy
}

func loadStandbyPolicy(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	factory, err := maclaunch.LoadStandbyResident(ctx, path, pairLoopbackCapture{}, agentgateway.ChannelBudgets())
	if err != nil || factory == nil || !factory.FixedWirePolicy() {
		t.Fatalf("standby loader rejected paired policy: %v", err)
	}
}

func loadPairHostKey(t *testing.T, policy pairLoopbackPolicy) ssh.Signer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	signer, err := agentkeys.LoadCurrentUser(ctx, agentkeys.Config{Path: policy.HostKeyPath, Fingerprint: policy.HostKeyFingerprint})
	if err != nil {
		t.Fatal("load generated host signer", err)
	}
	return signer
}

func dialResidentTLS(t *testing.T, client *http.Client, policy pairLoopbackPolicy, hostKey ssh.Signer, gatewayKey ssh.PublicKey) (*websocket.Conn, func(string, ...string) []byte) {
	t.Helper()
	ws, transcript, err := dialResidentTLSAttempt(t, client, policy, hostKey, gatewayKey)
	if err != nil {
		t.Fatal("resident TLS handshake refused", err)
	}
	return ws, transcript
}

func dialResidentTLSAttempt(t *testing.T, client *http.Client, policy pairLoopbackPolicy, hostKey ssh.Signer, gatewayKey ssh.PublicKey) (*websocket.Conn, func(string, ...string) []byte, error) {
	t.Helper()
	parsed, err := url.Parse(policy.RegistrationEndpoint)
	if err != nil {
		return nil, nil, err
	}
	wsURL := "wss://" + parsed.Host + parsed.Path
	tr := &http.Transport{TLSClientConfig: client.Transport.(*http.Transport).TLSClientConfig.Clone()}
	return residentPeerAttemptWithKeys(t, wsURL, policy.RegistrationEndpoint, policy.HostLabel, policy.PrincipalLabel, hostKey, gatewayKey, tr)
}

func residentSessionTLS(t *testing.T, client *http.Client, baseURL, bearer string) string {
	t.Helper()
	code, headers, _ := residentRPCURL(t, client, baseURL, bearer, "", "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "pair-loopback", "version": "1"}})
	sid := headers.Get(agentgateway.HeaderSession)
	if code != http.StatusOK || sid == "" {
		t.Fatalf("MCP initialize failed: %d", code)
	}
	if code, _, _ := residentRPCURL(t, client, baseURL, bearer, sid, "notifications/initialized", nil); code != 202 {
		t.Fatalf("MCP initialized failed: %d", code)
	}
	return sid
}

func residentToolTLS(t *testing.T, client *http.Client, baseURL, bearer, sid, name string, args map[string]any) (int, string) {
	t.Helper()
	code, _, body := residentRPCURL(t, client, baseURL, bearer, sid, "tools/call", map[string]any{"name": name, "arguments": args})
	return code, body
}

func residentRPCURL(t *testing.T, client *http.Client, baseURL, bearer, sid, method string, params any) (int, http.Header, string) {
	t.Helper()
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if method != "notifications/initialized" {
		message["id"] = 1
	}
	if params != nil {
		message["params"] = params
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/mcp", strings.NewReader(string(encode(t, message))))
	if err != nil {
		t.Fatal("build MCP request", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if sid != "" {
		req.Header.Set(agentgateway.HeaderSession, sid)
		req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("MCP request", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal("read MCP response", err)
	}
	return response.StatusCode, response.Header, string(body)
}

func waitFinishedTLS(t *testing.T, client *http.Client, baseURL, bearer, sid, requestID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		code, body := residentToolTLS(t, client, baseURL, bearer, sid, "tether_fixed_status", map[string]any{"request_id": requestID})
		if code == http.StatusOK && strings.Contains(body, `"phase":"finished"`) && strings.Contains(body, `"owner_cleanup":"confirmed"`) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("signed host recovery did not settle fixed work")
}
