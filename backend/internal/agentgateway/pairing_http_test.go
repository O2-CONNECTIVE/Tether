package agentgateway_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

func pairSetup(t *testing.T, subject string, hosts []agentgateway.HostEntry, save func([]agentgateway.PairRecord) error) (*agentgateway.Gateway, ssh.Signer) {
	return pairSetupWithRecords(t, subject, hosts, nil, save)
}

func TestConfigurePairingRejectsInvalidRegistrationEndpoint(t *testing.T) {
	for _, endpoint := range []string{"wss://gw.example/", "wss://GW.example/register", "wss://" + strings.Repeat("a", 250) + ".test/register"} {
		t.Run(endpoint, func(t *testing.T) {
			host := fixtureSigner(t, 0x51)
			g, err := agentgateway.New(agentgateway.Config{Verifier: verifier(t), Resource: testResource, Principals: []agentgateway.Principal{{Label: label(0), Subject: subjects[0], Roles: []agentgateway.Role{agentgateway.RoleRescueOperator, agentgateway.RoleResidentOwner}, Signer: fixtureSigner(t, 0x52)}}})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close(context.Background())
			if err := g.ConfigureHosts([]agentgateway.HostEntry{{Label: "host-1", Key: host.PublicKey().Marshal(), Principals: []string{label(0)}}}); err != nil {
				t.Fatal(err)
			}
			err = g.ConfigurePairing(agentgateway.PairingConfig{Verifier: pairingVerifier(t), Endpoint: "https://gateway.example.invalid" + agentgateway.PairPath, RegistrationEndpoint: endpoint})
			if !errors.Is(err, agentgateway.ErrConfig) {
				t.Fatalf("endpoint accepted: %q err=%v", endpoint, err)
			}
		})
	}
}

func pairSetupWithRecords(t *testing.T, subject string, hosts []agentgateway.HostEntry, records []agentgateway.PairRecord, save func([]agentgateway.PairRecord) error) (*agentgateway.Gateway, ssh.Signer) {
	t.Helper()
	v, err := agentgateway.NewVerifier(agentgateway.VerifierConfig{Issuer: testIssuer, Audience: pairingAudience, Types: []string{"at+jwt"}, JWKS: jwks(t)})
	if err != nil {
		t.Fatal(err)
	}
	principals := make([]agentgateway.Principal, len(subjects))
	for i := range subjects {
		principals[i] = agentgateway.Principal{Label: label(i), Subject: subjects[i], Roles: []agentgateway.Role{agentgateway.RoleRescueOperator, agentgateway.RoleResidentOwner}, Signer: fixtureSigner(t, byte(0x21+i))}
	}
	principals[0].Subject = subject
	g, err := agentgateway.New(agentgateway.Config{Verifier: verifier(t), Resource: testResource, Principals: principals})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.ConfigureHosts(hosts); err != nil {
		t.Fatal(err)
	}
	if err := g.ConfigurePairing(agentgateway.PairingConfig{Verifier: v, Endpoint: "https://gateway.example.invalid" + agentgateway.PairPath, RegistrationEndpoint: "wss://gateway.example.invalid/register", Records: records, Save: save}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close(context.Background()) })
	return g, fixtureSigner(t, 0x31)
}

func pairTranscriptForTest(endpoint string, key []byte, hash [32]byte) []byte {
	var out bytes.Buffer
	for _, part := range [][]byte{[]byte("rescue-host.agentgateway.pair\x00v1\x00"), []byte(endpoint), key, hash[:]} {
		_ = binary.Write(&out, binary.BigEndian, uint32(len(part)))
		out.Write(part)
	}
	return out.Bytes()
}

func pairRequest(t *testing.T, g *agentgateway.Gateway, signer ssh.Signer, subject string) *httptest.ResponseRecorder {
	return pairRequestWithProof(t, g, signer, signer, subject, "https://gateway.example.invalid"+agentgateway.PairPath, false)
}

func pairRequestWithProof(t *testing.T, g *agentgateway.Gateway, hostSigner, proofSigner ssh.Signer, subject, proofEndpoint string, wrongHash bool) *httptest.ResponseRecorder {
	t.Helper()
	req := makePairRequest(t, hostSigner, proofSigner, subject, proofEndpoint, wrongHash)
	rr := httptest.NewRecorder()
	g.ServePair(rr, req)
	return rr
}

func makePairRequest(t *testing.T, hostSigner, proofSigner ssh.Signer, subject, proofEndpoint string, wrongHash bool) *http.Request {
	t.Helper()
	base := claims(subject)
	base["aud"] = pairingAudience
	bearer := sign(t, edHeader(), base, issuerKey)
	digest := sha256.Sum256([]byte(bearer))
	if wrongHash {
		digest[0] ^= 1
	}
	transcript := pairTranscriptForTest(proofEndpoint, hostSigner.PublicKey().Marshal(), digest)
	proof, err := proofSigner.Sign(rand.Reader, transcript)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey := ssh.MarshalAuthorizedKey(hostSigner.PublicKey())
	body, err := json.Marshal(map[string]string{"host_public_key": string(encodedKey[:len(encodedKey)-1]), "proof": base64.RawURLEncoding.EncodeToString(ssh.Marshal(proof))})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "http://gateway.internal"+agentgateway.PairPath, bytes.NewReader(body))
	req.Host = "internal-alias"
	req.Header.Set("Authorization", "Bearer "+bearer)
	return req
}

func TestServePairRejectsUnknownSubjectsA3(t *testing.T) {
	g, signer := pairSetup(t, subjects[0], []agentgateway.HostEntry{{Label: "host-1", Principals: []string{label(0)}, Paired: true}}, nil)
	rr := pairRequest(t, g, signer, "unconfigured-subject")
	// An authenticated subject without a configured principal must not enroll a host.
	if rr.Code != 403 {
		t.Fatalf("unknown subject status = %d, want 403", rr.Code)
	}
}

func TestServePairRejectsPrincipalWithoutPairingSlotA3(t *testing.T) {
	g, signer := pairSetup(t, subjects[0], []agentgateway.HostEntry{{Label: "host-1", Key: fixtureSigner(t, 0x32).PublicKey().Marshal(), Principals: []string{label(1)}}}, nil)
	rr := pairRequest(t, g, signer, subjects[0])
	// An authenticated configured principal without a paired host slot has no enrollment authority.
	if rr.Code != 403 {
		t.Fatalf("principal without slot status = %d, want 403", rr.Code)
	}
}

func TestServePairBindsProofToConfiguredEndpointA4(t *testing.T) {
	g, signer := pairSetup(t, subjects[0], []agentgateway.HostEntry{{Label: "host-1", Principals: []string{label(0)}, Paired: true}}, nil)
	rr := pairRequest(t, g, signer, subjects[0])
	// Worker forwards internally, so the proof must bind the configured public endpoint.
	if rr.Code != 200 {
		t.Fatalf("configured endpoint proof status = %d, want 200", rr.Code)
	}
}

func TestServePairRejectsWrongProofA4(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint string
		proofKey       byte
		wrongHash      bool
	}{
		{"different endpoint", "https://attacker.example.invalid" + agentgateway.PairPath, 0x31, false},
		{"different token", "https://gateway.example.invalid" + agentgateway.PairPath, 0x31, true},
		{"different key", "https://gateway.example.invalid" + agentgateway.PairPath, 0x33, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, hostSigner := pairSetup(t, subjects[0], []agentgateway.HostEntry{{Label: "host-1", Principals: []string{label(0)}, Paired: true}}, nil)
			proofSigner := hostSigner
			if tc.proofKey != 0x31 {
				proofSigner = fixtureSigner(t, tc.proofKey)
			}
			bad := pairRequestWithProof(t, g, hostSigner, proofSigner, subjects[0], tc.endpoint, tc.wrongHash)
			// A proof tied to another endpoint, token or host key must be refused.
			if bad.Code != 400 {
				t.Fatalf("invalid proof status = %d, want 400", bad.Code)
			}
		})
	}
}

func TestServePairRejectsDuplicateKeysA5(t *testing.T) {
	key := fixtureSigner(t, 0x31).PublicKey().Marshal()
	g, signer := pairSetup(t, subjects[0], []agentgateway.HostEntry{{Label: "host-1", Principals: []string{label(0)}, Paired: true}, {Label: "host-2", Key: key, Principals: []string{label(1)}}}, nil)
	rr := pairRequest(t, g, signer, subjects[0])
	// A host key already assigned to another host must not be claimed twice.
	if rr.Code != 409 {
		t.Fatalf("duplicate key status = %d, want 409", rr.Code)
	}
	_ = g
}

func TestServePairRejectsPrincipalKeyReuseA5(t *testing.T) {
	otherKey := fixtureSigner(t, 0x32).PublicKey().Marshal()
	g, _ := pairSetup(t, subjects[0], []agentgateway.HostEntry{{Label: "host-1", Principals: []string{label(0)}, Paired: true}, {Label: "host-2", Key: otherKey, Principals: []string{label(1)}}}, nil)
	principalSigner := fixtureSigner(t, 0x21)
	rr := pairRequestWithProof(t, g, principalSigner, principalSigner, subjects[0], "https://gateway.example.invalid"+agentgateway.PairPath, false)
	// A host key cannot reuse a principal's enrollment key.
	if rr.Code != 409 {
		t.Fatalf("principal key reuse status = %d, want 409", rr.Code)
	}
}

func TestServePairSaveFailurePreservesOldHostKeyA11p(t *testing.T) {
	old := fixtureSigner(t, 0x32).PublicKey().Marshal()
	g, signer := pairSetupWithRecords(t, subjects[0], []agentgateway.HostEntry{{Label: "host-1", Principals: []string{label(0)}, Paired: true}}, []agentgateway.PairRecord{{HostLabel: "host-1", HostKey: old, Issuer: testIssuer, Subject: subjects[0]}}, func([]agentgateway.PairRecord) error { return errSave })
	rr := pairRequest(t, g, signer, subjects[0])
	// A failed durable write must leave the previously enrolled key active.
	if rr.Code != 503 {
		t.Fatalf("failed save status = %d, want 503", rr.Code)
	}
	if got := g.HostKey("host-1"); string(got) != string(old) {
		t.Fatal("failed save changed the host key")
	}
}

func TestServePairSaveSuccessReplacesHostKeyA11p(t *testing.T) {
	old := fixtureSigner(t, 0x32).PublicKey().Marshal()
	g, signer := pairSetupWithRecords(t, subjects[0], []agentgateway.HostEntry{{Label: "host-1", Principals: []string{label(0)}, Paired: true}}, []agentgateway.PairRecord{{HostLabel: "host-1", HostKey: old, Issuer: testIssuer, Subject: subjects[0]}}, nil)
	rr := pairRequest(t, g, signer, subjects[0])
	// A successful durable write must activate the new host identity.
	if rr.Code != 200 {
		t.Fatalf("successful save status = %d, want 200", rr.Code)
	}
	if string(g.HostKey("host-1")) == string(old) {
		t.Fatal("successful save did not replace the host key")
	}
}

func TestConnectRejectsOldHostKeyAfterPairingReplacementA9(t *testing.T) {
	r := newModeRig(t, agentgateway.Config{Verifier: verifier(t)})
	oldKey := fixtureSigner(t, 0x41).PublicKey().Marshal()
	newKey := fixtureSigner(t, 0x42).PublicKey().Marshal()
	if err := r.gw.ConfigureHosts([]agentgateway.HostEntry{{Label: "host-1", Principals: []string{label(0)}, Paired: true}}); err != nil {
		t.Fatal(err)
	}
	if err := r.gw.ConfigurePairing(agentgateway.PairingConfig{Verifier: pairingVerifier(t), Endpoint: "https://gateway.example.invalid" + agentgateway.PairPath, RegistrationEndpoint: "wss://gateway.example.invalid/register", Records: []agentgateway.PairRecord{{HostLabel: "host-1", HostKey: newKey, Issuer: testIssuer, Subject: subjects[0]}}}); err != nil {
		t.Fatal(err)
	}
	d := agentgateway.Dispatch{Binding: agentchannel.Binding{Grant: supervisor.Binding{Identity: supervisor.Identity{LaunchID: "fresh-launch"}}, Profile: agentexec.ProfileMacCurrentUser}, Deadline: time.Now().Add(time.Minute), HostKey: oldKey}
	client, peer := net.Pipe()
	_ = peer.Close()
	err := r.gw.Connect(label(0), d, client)
	// A dispatch verified under the replaced host key must not open a new route.
	if !errors.Is(err, agentgateway.ErrRefused) {
		t.Fatalf("old-key Connect error = %v, want ErrRefused", err)
	}
}

var errSave = saveError{}

type saveError struct{}

func (saveError) Error() string { return "save failed" }

func pairingMultiGateway(t *testing.T, save func([]agentgateway.PairRecord) error) *agentgateway.Gateway {
	t.Helper()
	v, err := agentgateway.NewVerifier(agentgateway.VerifierConfig{Issuer: testIssuer, Audience: pairingAudience, Types: []string{"at+jwt"}, JWKS: jwks(t)})
	if err != nil {
		t.Fatal(err)
	}
	r := newModeRig(t, agentgateway.Config{Verifier: verifier(t), Resource: testResource})
	if err := r.gw.ConfigureHosts([]agentgateway.HostEntry{{Label: "slot-1", Principals: []string{label(0)}, Paired: true}, {Label: "slot-2", Principals: []string{label(1)}, Paired: true}}); err != nil {
		t.Fatal(err)
	}
	if err := r.gw.ConfigurePairing(agentgateway.PairingConfig{Verifier: v, Endpoint: "https://gateway.example.invalid" + agentgateway.PairPath, RegistrationEndpoint: "wss://gateway.example.invalid/register", Save: save}); err != nil {
		t.Fatal(err)
	}
	return r.gw
}

func TestConcurrentPairingDifferentPrincipalsPersistsBothA12(t *testing.T) {
	var mu sync.Mutex
	var saved []agentgateway.PairRecord
	g := pairingMultiGateway(t, func(records []agentgateway.PairRecord) error {
		mu.Lock()
		saved = append([]agentgateway.PairRecord(nil), records...)
		mu.Unlock()
		return nil
	})
	start := make(chan struct{})
	results := make(chan int, 2)
	requests := make([]*http.Request, 2)
	for i := range requests {
		signer := fixtureSigner(t, byte(0x51+i))
		requests[i] = makePairRequest(t, signer, signer, subjects[i], "https://gateway.example.invalid"+agentgateway.PairPath, false)
	}
	for i := 0; i < 2; i++ {
		go func(req *http.Request) {
			<-start
			rr := httptest.NewRecorder()
			g.ServePair(rr, req)
			results <- rr.Code
		}(requests[i])
	}
	close(start)
	for i := 0; i < 2; i++ {
		if status := <-results; status != 200 {
			t.Errorf("concurrent registration status = %d, want 200", status)
		}
	}
	mu.Lock()
	got := append([]agentgateway.PairRecord(nil), saved...)
	mu.Unlock()
	if len(got) != 2 {
		t.Errorf("saved records = %d, want both principals", len(got))
	}
	labels := map[string]bool{}
	for _, record := range got {
		labels[record.HostLabel] = true
	}
	if !labels["slot-1"] || !labels["slot-2"] {
		t.Errorf("saved records do not contain both slot labels: %v", labels)
	}
	if len(g.HostKey("slot-1")) == 0 || len(g.HostKey("slot-2")) == 0 {
		t.Error("host key table does not contain both registered keys")
	}
}

func TestConcurrentPairingSameKeyHasSingleWinnerA12(t *testing.T) {
	g := pairingMultiGateway(t, nil)
	shared := fixtureSigner(t, 0x58)
	start := make(chan struct{})
	results := make(chan int, 2)
	requests := []*http.Request{
		makePairRequest(t, shared, shared, subjects[0], "https://gateway.example.invalid"+agentgateway.PairPath, false),
		makePairRequest(t, shared, shared, subjects[1], "https://gateway.example.invalid"+agentgateway.PairPath, false),
	}
	for i := 0; i < 2; i++ {
		go func(req *http.Request) {
			<-start
			rr := httptest.NewRecorder()
			g.ServePair(rr, req)
			results <- rr.Code
		}(requests[i])
	}
	close(start)
	counts := map[int]int{}
	for i := 0; i < 2; i++ {
		counts[<-results]++
	}
	// Two pairing slots must not claim the same host key.
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent outcomes = %v, want one 200 and one 409", counts)
	}
}

// pairingVerifier accepts only tokens issued for the pair route.
func pairingVerifier(t *testing.T) *agentgateway.Verifier {
	t.Helper()
	v, err := agentgateway.NewVerifier(agentgateway.VerifierConfig{Issuer: testIssuer, Audience: pairingAudience, Types: []string{"at+jwt"}, JWKS: jwks(t)})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
