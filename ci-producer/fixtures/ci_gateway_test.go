//go:build darwin && cgo && ci_actual_producer

package residentpipe

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"golang.org/x/crypto/ssh"
)

const ciHost = "ci-host"
const ciPrincipal = "ci-member"
const ciSubject = "ci-synthetic-user"
const ciIssuer = "https://issuer.example.invalid/"

// Every acquired owner remains reachable even after a fatal assertion. There
// is no automatic cleanup retry, process signal, or disposal after unknown.
var ciGateways struct {
	sync.Mutex
	held []*ciGateway
}

type ciGateway struct {
	t                    *testing.T
	diagnostic           *ciDiagnostic
	endpoint             string
	caDER                []byte
	policyPath           string
	handler              *agenttransport.Handler
	gateway              *agentgateway.Gateway
	listener             net.Listener
	server               *http.Server
	served               chan error
	transport            *http.Transport
	client               *http.Client
	resource, token, sid string
	otherToken           string
	sequence             int
	closeOnce            sync.Once
	closeErr             error
}

func newCIGateway(t *testing.T, root string, diag *ciDiagnostic) *ciGateway {
	t.Helper()
	g := &ciGateway{t: t, diagnostic: diag}
	ciGateways.Lock()
	ciGateways.held = append(ciGateways.held, g)
	ciGateways.Unlock()
	checked := func(err error) {
		t.Helper()
		if err != nil {
			diag.fail(t, "gateway-setup")
		}
	}
	info, err := os.Lstat(root)
	checked(err)
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !info.IsDir() || info.Mode().Perm() != 0700 {
		diag.fail(t, "gateway-root")
	}
	pub, caKey, err := ed25519.GenerateKey(rand.Reader)
	checked(err)
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "CI ephemeral producer CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	g.caDER, err = x509.CreateCertificate(rand.Reader, ca, ca, pub, caKey)
	checked(err)
	ca, err = x509.ParseCertificate(g.caDER)
	checked(err)
	serverPub, serverKey, err := ed25519.GenerateKey(rand.Reader)
	checked(err)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "CI loopback"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, serverPub, caKey)
	checked(err)
	clear(caKey)
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	checked(err)
	defer clear(hostKey)
	host, err := ssh.NewSignerFromKey(hostKey)
	checked(err)
	_, principalKey, err := ed25519.GenerateKey(rand.Reader)
	checked(err)
	principal, err := ssh.NewSignerFromKey(principalKey)
	checked(err)
	_, otherKey, err := ed25519.GenerateKey(rand.Reader)
	checked(err)
	otherPrincipal, err := ssh.NewSignerFromKey(otherKey)
	checked(err)
	jwtPub, jwtKey, err := ed25519.GenerateKey(rand.Reader)
	checked(err)
	defer clear(jwtKey)
	g.listener, err = net.Listen("tcp4", "127.0.0.1:0")
	checked(err)
	address := g.listener.Addr().(*net.TCPAddr)
	if !address.IP.Equal(net.ParseIP("127.0.0.1")) || address.Port < 1024 {
		diag.fail(t, "gateway-listener")
	}
	g.endpoint = "wss://" + address.String() + "/agent/register"
	g.resource = "https://" + address.String() + "/mcp"
	jwks, err := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "kid": "ci", "alg": "EdDSA", "x": base64.RawURLEncoding.EncodeToString(jwtPub)}}})
	checked(err)
	verifier, err := agentgateway.NewVerifier(agentgateway.VerifierConfig{Issuer: ciIssuer, Audience: g.resource, Scope: "ci", Types: []string{"at+jwt"}, JWKS: jwks})
	checked(err)
	g.gateway, err = agentgateway.New(agentgateway.Config{Verifier: verifier, Principals: []agentgateway.Principal{{Label: ciPrincipal, Subject: ciSubject, Signer: principal}, {Label: "ci-other-member", Subject: "ci-other-synthetic-user", Signer: otherPrincipal}}, Budgets: agentgateway.ChannelBudgets()})
	checked(err)
	g.handler, err = agenttransport.NewHandler(agenttransport.HandlerConfig{Endpoint: g.endpoint, Registrar: g.gateway, Budgets: agenttransport.DefaultBudgets(), Hosts: []agenttransport.EnrolledHost{{Label: ciHost, PublicKey: host.PublicKey(), Principals: []string{ciPrincipal}}}, ResidentPrincipals: []agenttransport.ResidentPrincipal{{Label: ciPrincipal, Signer: principal}}})
	checked(err)
	checked(g.gateway.ConfigureResident(g.endpoint, g.handler, []agentgateway.ResidentEnrollment{{HostLabel: ciHost, PrincipalLabel: ciPrincipal, HostKey: host.PublicKey().Marshal()}}))
	block, err := ssh.MarshalPrivateKey(hostKey, "ephemeral synthetic CI fixture")
	checked(err)
	keyBytes := pem.EncodeToMemory(block)
	defer clear(keyBytes)
	keyPath := filepath.Join(root, "synthetic-host-key")
	g.policyPath = filepath.Join(root, "synthetic-policy.json")
	checked(ciWriteExclusive(keyPath, keyBytes))
	policy, err := json.Marshal(map[string]any{"protocol": "rescue-mac-resident-policy/1", "policy_digest": agentexec.MacCurrentUserPolicyDigest(), "registration_endpoint": g.endpoint, "host_label": ciHost, "principal_label": ciPrincipal, "principal_public_key": base64.StdEncoding.EncodeToString(principal.PublicKey().Marshal()), "host_key_path": keyPath, "host_key_fingerprint": ssh.FingerprintSHA256(host.PublicKey()), "work_dir": root, "home": root, "cleanup_seconds": 10, "operations": []any{map[string]any{"id": maclaunch.ResidentFixedRequestID, "command": maclaunch.ResidentFixedCommand, "timeout_ms": 15000}}})
	checked(err)
	checked(ciWriteExclusive(g.policyPath, policy))
	mux := http.NewServeMux()
	mux.Handle("/agent/register", g.handler)
	mux.Handle("/mcp", g.gateway)
	g.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	g.served = make(chan error, 1)
	secured := tls.NewListener(g.listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der, g.caDER}, PrivateKey: serverKey}}})
	go func() { g.served <- g.server.Serve(secured) }()
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	g.transport = &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 20 * time.Second, DisableKeepAlives: true, DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
		if target != address.String() {
			return nil, errors.New("non-fixture destination refused")
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp4", target)
	}}
	g.client = &http.Client{Transport: g.transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}
	g.token = ciJWT(t, jwtKey, g.resource)
	g.otherToken = ciJWTSubject(t, jwtKey, g.resource, "ci-other-synthetic-user")
	return g
}

func ciWriteExclusive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	return errors.Join(writeErr, f.Sync(), f.Close())
}

// Call once, only after the original producer proves OFF, closed, EOF and Wait.
// A failed stage stops collection and remains sticky; disposal is caller-owned.
func (g *ciGateway) close() error {
	g.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if g.handler != nil {
			if err := g.handler.Close(ctx); err != nil {
				g.diagnostic.note("handler-close")
				g.closeErr = err
				return
			}
		}
		if g.gateway != nil {
			if err := g.gateway.Close(ctx); err != nil {
				g.diagnostic.note("gateway-close")
				g.closeErr = err
				return
			}
		}
		if g.transport != nil {
			g.transport.CloseIdleConnections()
		}
		if g.server != nil {
			if err := g.server.Shutdown(ctx); err != nil {
				g.diagnostic.note("http-shutdown")
				g.closeErr = err
				return
			}
			select {
			case err := <-g.served:
				if !errors.Is(err, http.ErrServerClosed) {
					g.diagnostic.note("http-serve")
					g.closeErr = err
				}
			case <-ctx.Done():
				g.diagnostic.note("http-serve-deadline")
				g.closeErr = ctx.Err()
			}
		}
	})
	return g.closeErr
}
