//go:build darwin && cgo && ci_actual_producer

package agenttransport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func ciTrustCA(t *testing.T, isCA bool) ([]byte, *x509.Certificate, ed25519.PrivateKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ephemeral CI trust regression"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: isCA, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature}
	if isCA {
		template.KeyUsage |= x509.KeyUsageCertSign
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return der, cert, key
}

func ciBoundTransport(t *testing.T, kind string, trust *CIProducerTrust, owner *closer) *http.Transport {
	t.Helper()
	var round http.RoundTripper
	switch kind {
	case "worker":
		c := &Client{endpoint: trust.endpoint}
		if err := trust.BindClient(c); err != nil {
			t.Fatal(err)
		}
		round = c.transport(owner)
	case "control":
		c := &ResidentClient{cfg: ResidentClientConfig{Endpoint: trust.endpoint}}
		if err := trust.BindResident(c); err != nil {
			t.Fatal(err)
		}
		round = c.transport(owner)
	default:
		t.Fatal("unknown fixture client")
	}
	return round.(*http.Transport)
}

// No dial, listener, authentication or native runtime occurs in these tests.
func TestCIProducerTrustValidation(t *testing.T) {
	der, _, key := ciTrustCA(t, true)
	defer clear(key)
	nonCA, _, otherKey := ciTrustCA(t, false)
	defer clear(otherKey)
	endpoint := "wss://127.0.0.1:23456/register"
	for _, bad := range []string{"", "://bad", "ws://127.0.0.1:23456/register", "wss://localhost:23456/register", "wss://127.0.0.2:23456/register", "wss://[::1]:23456/register", "wss://127.0.0.1/register", "wss://127.0.0.1:0/register", "wss://127.0.0.1:65536/register", "wss://127.0.0.1:023456/register", "wss://user@127.0.0.1:23456/register", "wss://127.0.0.1:23456/register?x=y", "wss://127.0.0.1:23456/register#fragment", "wss://127.0.0.1:23456/"} {
		if _, err := NewCIProducerTrust(bad, der); !errors.Is(err, ErrConfig) {
			t.Fatalf("endpoint accepted: %q", bad)
		}
	}
	for _, bad := range [][]byte{nil, []byte("invalid"), nonCA} {
		if _, err := NewCIProducerTrust(endpoint, bad); !errors.Is(err, ErrConfig) {
			t.Fatal("invalid certificate accepted")
		}
	}
	trust, err := NewCIProducerTrust(endpoint, der)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"worker", "control"} {
		t.Run(kind, func(t *testing.T) {
			check := func(tr *CIProducerTrust, started bool, target string, nilClient bool) error {
				if kind == "worker" {
					var c *Client
					if !nilClient {
						c = &Client{endpoint: target, started: started}
					}
					return tr.BindClient(c)
				}
				var c *ResidentClient
				if !nilClient {
					c = &ResidentClient{cfg: ResidentClientConfig{Endpoint: target}, started: started}
				}
				return tr.BindResident(c)
			}
			for _, tc := range []struct {
				name      string
				trust     *CIProducerTrust
				started   bool
				target    string
				nilClient bool
			}{{"nil trust", nil, false, endpoint, false}, {"empty trust", &CIProducerTrust{}, false, endpoint, false}, {"nil client", trust, false, endpoint, true}, {"used", trust, true, endpoint, false}, {"mismatch", trust, false, "wss://127.0.0.1:23457/register", false}} {
				if err := check(tc.trust, tc.started, tc.target, tc.nilClient); !errors.Is(err, ErrConfig) {
					t.Fatal(tc.name, "accepted", err)
				}
			}
			if err := check(trust, false, endpoint, false); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCIProducerTrustInstanceClones(t *testing.T) {
	der, _, key := ciTrustCA(t, true)
	defer clear(key)
	trust, err := NewCIProducerTrust("wss://127.0.0.1:23456/register", der)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"worker", "control"} {
		t.Run(kind, func(t *testing.T) {
			_, cancel1 := context.WithCancel(context.Background())
			defer cancel1()
			_, cancel2 := context.WithCancel(context.Background())
			defer cancel2()
			a := ciBoundTransport(t, kind, trust, newCloser(cancel1))
			b := ciBoundTransport(t, kind, trust, newCloser(cancel2))
			if a == b || a.TLSClientConfig == b.TLSClientConfig || a.TLSClientConfig.RootCAs == b.TLSClientConfig.RootCAs || a.TLSClientConfig.RootCAs == trust.roots {
				t.Fatal("shared mutable TLS config")
			}
			for _, tr := range []*http.Transport{a, b} {
				if tr.DialContext == nil || tr.Proxy != nil || tr.DialTLSContext != nil || tr.TLSClientConfig.InsecureSkipVerify || tr.TLSClientConfig.VerifyPeerCertificate != nil || tr.TLSClientConfig.VerifyConnection != nil || tr.TLSClientConfig.ServerName != "" || tr.TLSClientConfig.MinVersion != tls.VersionTLS12 || !tr.DisableKeepAlives || !tr.DisableCompression || tr.ForceAttemptHTTP2 {
					t.Fatal("normal native transport validation changed")
				}
			}
			a.TLSClientConfig.ServerName = "mutated.example.invalid"
			a.TLSClientConfig.RootCAs = x509.NewCertPool()
			if b.TLSClientConfig.ServerName != "" || len(b.TLSClientConfig.RootCAs.Subjects()) != 1 || len(trust.roots.Subjects()) != 1 {
				t.Fatal("instance mutation escaped")
			}
		})
	}
}

type ciTLSNegativeOwner struct {
	server    *http.Server
	listener  net.Listener
	served    chan error
	transport *http.Transport
	owner     *closer
	response  *http.Response
}

var ciTLSNegativeHeld struct {
	sync.Mutex
	owners []*ciTLSNegativeOwner
}

// Explicit socket test: never run locally during preparation. Both client
// factories must perform real native owned TCP/TLS and reject, before HTTP,
// either a foreign CA or the wrong IP SAN. No authenticated Session is minted.
func TestCIProducerTrustTLSNegative(t *testing.T) {
	if os.Getenv("CI_ACTUAL_PRODUCER") != "1" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Getenv("RUNNER_OS") != "macOS" || os.Getenv("RUNNER_ARCH") != "ARM64" {
		t.Fatal("requires explicit disposable CI lane")
	}
	invocation := os.Getenv("CI_PRODUCER_INVOCATION")
	nonce, nonceErr := hex.DecodeString(invocation)
	if nonceErr != nil || len(nonce) != 16 || hex.EncodeToString(nonce) != invocation {
		t.Fatal("fresh invocation identity required")
	}
	report := os.Getenv("CI_PRODUCER_RESULT")
	if !filepath.IsAbs(report) {
		t.Fatal("external outcome path required")
	}
	if _, err := os.Lstat(report); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("outcome already exists")
	}
	t.Cleanup(func() {
		outcome, cleanup := "passed", "confirmed"
		if t.Failed() {
			outcome, cleanup = "failed-unknown", "unconfirmed"
		}
		raw, _ := json.Marshal(map[string]any{"outcome": outcome, "cleanup": cleanup, "test": t.Name(), "invocation": invocation})
		f, err := os.OpenFile(report, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_, err = f.Write(append(raw, '\n'))
			err = errors.Join(err, f.Sync(), f.Close())
		}
		if err != nil {
			t.Error("outcome persistence failed")
		}
		if t.Failed() {
			for {
				time.Sleep(time.Hour)
				runtime.KeepAlive(&ciTLSNegativeHeld)
			}
		}
	})
	for _, kind := range []string{"worker", "control"} {
		for _, failure := range []string{"wrong-ca", "wrong-hostname"} {
			if !t.Run(kind+"/"+failure, func(t *testing.T) {
				rootDER, ca, caKey := ciTrustCA(t, true)
				defer clear(caKey)
				trustDER := rootDER
				if failure == "wrong-ca" {
					var key ed25519.PrivateKey
					trustDER, _, key = ciTrustCA(t, true)
					defer clear(key)
				}
				pub, key, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				ip := "127.0.0.1"
				if failure == "wrong-hostname" {
					ip = "127.0.0.2"
				}
				leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP(ip)}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
				der, err := x509.CreateCertificate(rand.Reader, leaf, ca, pub, caKey)
				if err != nil {
					t.Fatal(err)
				}
				held := &ciTLSNegativeOwner{}
				ciTLSNegativeHeld.Lock()
				ciTLSNegativeHeld.owners = append(ciTLSNegativeHeld.owners, held)
				ciTLSNegativeHeld.Unlock()
				held.listener, err = net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				address := held.listener.Addr().String()
				trust, err := NewCIProducerTrust("wss://"+address+"/register", trustDER)
				if err != nil {
					t.Fatal(err)
				}
				var calls atomic.Int32
				held.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }), ReadHeaderTimeout: 5 * time.Second}
				held.served = make(chan error, 1)
				listener := tls.NewListener(held.listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der, rootDER}, PrivateKey: key}}})
				go func() { held.served <- held.server.Serve(listener) }()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				held.owner = newCloser(cancel)
				held.transport = ciBoundTransport(t, kind, trust, held.owner)
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+address+"/register", nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := held.transport.RoundTrip(req)
				held.response = response
				if response != nil {
					t.Fatal("unexpected TLS response; original resources retained")
				}
				if failure == "wrong-ca" {
					var expected x509.UnknownAuthorityError
					if !errors.As(err, &expected) {
						t.Fatal("wrong CA was not rejected", err)
					}
				} else {
					var expected x509.HostnameError
					if !errors.As(err, &expected) {
						t.Fatal("wrong hostname was not rejected", err)
					}
				}
				if calls.Load() != 0 {
					t.Fatal("unverified request reached HTTP")
				}
				if !held.owner.settle() {
					t.Fatal("native dial was not observed by actual owner")
				}
				cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
				defer stop()
				if err := held.owner.wait(cleanup); err != nil {
					t.Fatal("original client cleanup unknown; retained", err)
				}
				if !held.owner.confirmed() {
					t.Fatal("original client cleanup not confirmed")
				}
				held.transport.CloseIdleConnections()
				if err := held.server.Shutdown(cleanup); err != nil {
					t.Fatal("original server shutdown unknown; retained", err)
				}
				select {
				case err := <-held.served:
					if !errors.Is(err, http.ErrServerClosed) {
						t.Fatal("original Serve result", err)
					}
				case <-cleanup.Done():
					t.Fatal("original Serve join unknown; retained")
				}
				clear(key) // only after original server and Serve joins
			}) {
				return
			}
		}
	}
}

func TestCIProducerTrustOwnsCertificateBytes(t *testing.T) {
	der, _, key := ciTrustCA(t, true)
	defer clear(key)
	original, err := x509.ParseCertificate(append([]byte(nil), der...))
	if err != nil {
		t.Fatal(err)
	}
	trust, err := NewCIProducerTrust("wss://127.0.0.1:23456/register", der)
	if err != nil {
		t.Fatal(err)
	}
	clear(der)
	subjects := trust.roots.Subjects()
	if len(subjects) != 1 || !bytes.Equal(subjects[0], original.RawSubject) {
		t.Fatal("caller mutation corrupted fixture trust")
	}
	if _, err = original.Verify(x509.VerifyOptions{Roots: trust.roots}); err != nil {
		t.Fatal("owned root unavailable after caller clear", err)
	}
}
