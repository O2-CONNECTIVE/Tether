package agentgateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"golang.org/x/crypto/ssh"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Characterization of the existing registration boundary, not new permission
// model integration. Synthetic keys only; no listener, host or process starts.
func legacyEnrollmentFixture(t *testing.T) (*Gateway, []ResidentEnrollment) {
	t.Helper()
	template, s, host, _ := residentEvidenceRig(t)
	defer template.Close(context.Background())
	g, err := New(Config{Verifier: template.verifier, Budgets: template.budgets,
		Principals: []Principal{{Label: "member1", Subject: "subject", Signer: s.principal.signer},
			{Label: "member2", Subject: "subject2", Signer: s.principal.signer},
			{Label: "unsigned", Subject: "subject3"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := g.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return g, []ResidentEnrollment{{HostLabel: "host1", PrincipalLabel: "member1", HostKey: host.PublicKey().Marshal()},
		{HostLabel: "host2", PrincipalLabel: "member2", HostKey: host.PublicKey().Marshal()}}
}

const legacyEndpoint = "wss://example.test/register"

func legacyControl() *residentControlFixture {
	return &residentControlFixture{begin: make(chan struct{})}
}

func TestResidentEnrollmentCompatibilityRejectsWithoutPartialInstall(t *testing.T) {
	rsaKey, err := ssh.NewPublicKey(&rsa.PublicKey{N: big.NewInt(65537), E: 3})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func([]ResidentEnrollment) []ResidentEnrollment{
		"unsupported-key-type":               func(e []ResidentEnrollment) []ResidentEnrollment { e[1].HostKey = rsaKey.Marshal(); return e },
		"empty":                              func(_ []ResidentEnrollment) []ResidentEnrollment { return nil },
		"unknown-principal":                  func(e []ResidentEnrollment) []ResidentEnrollment { e[1].PrincipalLabel = "unknown"; return e },
		"unsigned-principal":                 func(e []ResidentEnrollment) []ResidentEnrollment { e[1].PrincipalLabel = "unsigned"; return e },
		"duplicate-principal-different-host": func(e []ResidentEnrollment) []ResidentEnrollment { e[1].PrincipalLabel = e[0].PrincipalLabel; return e },
		"duplicate-identical":                func(e []ResidentEnrollment) []ResidentEnrollment { return []ResidentEnrollment{e[0], e[0]} },
		"invalid-host":                       func(e []ResidentEnrollment) []ResidentEnrollment { e[1].HostLabel = "host/other"; return e },
		"bad-key":                            func(e []ResidentEnrollment) []ResidentEnrollment { e[1].HostKey = []byte("not-a-key"); return e },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			g, entries := legacyEnrollmentFixture(t)
			good := append([]ResidentEnrollment(nil), entries...)
			if err := g.ConfigureResident(legacyEndpoint, legacyControl(), mutate(entries)); !errors.Is(err, ErrConfig) {
				t.Fatalf("got %v", err)
			}
			if g.resident != nil || len(g.sessions) != 0 {
				t.Fatal("failed configuration installed state")
			}
			if err := g.ConfigureResident(legacyEndpoint, legacyControl(), good); err != nil {
				t.Fatal("refusal consumed configuration slot", err)
			}
		})
	}
}

func TestResidentEnrollmentCompatibilityOneShotAndDetachedValues(t *testing.T) {
	g, entries := legacyEnrollmentFixture(t)
	wantKey := append([]byte(nil), entries[0].HostKey...)
	if err := g.ConfigureResident(legacyEndpoint, legacyControl(), entries); err != nil {
		t.Fatal(err)
	}
	original := g.resident
	entry := original.entries[g.byLabel["member1"]]
	entries[0].HostLabel = "replacement"
	entries[0].HostKey[0] ^= 0xff
	if entry.enrollment.HostLabel != "host1" || !bytes.Equal(entry.enrollment.HostKey, wantKey) {
		t.Fatal("input aliases installed enrollment")
	}
	_, replacement := legacyEnrollmentFixture(t)
	replacement[0].HostLabel = "replacement"
	if err := g.ConfigureResident(legacyEndpoint, legacyControl(), replacement); !errors.Is(err, ErrRefused) {
		t.Fatal(err)
	}
	if g.resident != original || original.entries[g.byLabel["member1"]] != entry || entry.enrollment.HostLabel != "host1" {
		t.Fatal("reconfiguration changed original enrollment")
	}
}

func TestResidentEnrollmentCompatibilitySameHostIsSeparatePrincipalSlots(t *testing.T) {
	g, entries := legacyEnrollmentFixture(t)
	entries[1].HostLabel = entries[0].HostLabel
	if err := g.ConfigureResident(legacyEndpoint, legacyControl(), entries); err != nil {
		t.Fatal(err)
	}
	a, b := g.resident.entries[g.byLabel["member1"]], g.resident.entries[g.byLabel["member2"]]
	if len(g.resident.entries) != 2 || a == b || !reflect.DeepEqual(a.enrollment.HostKey, b.enrollment.HostKey) {
		t.Fatal("unexpected slot mapping")
	}
	a.works["synthetic"] = &residentWork{}
	a.enrollment.HostKey[0] ^= 0xff
	if len(b.works) != 0 || bytes.Equal(a.enrollment.HostKey, b.enrollment.HostKey) {
		t.Fatal("principal slots share mutable state")
	}
	// This is configuration acceptance only, not a shared-host authorization,
	// connection, concurrent execution or possession proof.
}

func TestResidentEnrollmentCompatibilityCannotAttachAfterHTTPInitialize(t *testing.T) {
	g, entries := legacyEnrollmentFixture(t)
	header, _ := json.Marshal(map[string]any{"alg": "EdDSA", "kid": "test", "typ": "at+jwt"})
	claims, _ := json.Marshal(map[string]any{"iss": "https://issuer.example.invalid/", "aud": "https://example.test/mcp", "sub": "subject", "scope": "scope", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()})
	enc := base64.RawURLEncoding
	input := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	token := input + "." + enc.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, 32)), []byte(input)))
	req := httptest.NewRequest(http.MethodPost, "https://example.test/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	g.ServeHTTP(res, req)
	sid := res.Header().Get(HeaderSession)
	if res.Code != http.StatusOK || sid == "" {
		t.Fatalf("initialize failed: %d", res.Code)
	}
	original := g.sessions[sid]
	if original == nil || original.resident != nil || original.route != nil {
		t.Fatal("expected discovery-only session")
	}
	if err := g.ConfigureResident(legacyEndpoint, legacyControl(), entries); !errors.Is(err, ErrRefused) {
		t.Fatal(err)
	}
	if g.resident != nil || g.sessions[sid] != original || original.resident != nil || original.route != nil {
		t.Fatal("late configuration attached a session")
	}
}
