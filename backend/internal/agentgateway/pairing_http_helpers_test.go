package agentgateway_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"golang.org/x/crypto/ssh"
)

func pairTranscriptForTest(endpoint string, key []byte, hash [32]byte) []byte {
	var out bytes.Buffer
	for _, part := range [][]byte{[]byte("rescue-host.agentgateway.pair\x00v1\x00"), []byte(endpoint), key, hash[:]} {
		_ = binary.Write(&out, binary.BigEndian, uint32(len(part)))
		_, _ = out.Write(part)
	}
	return out.Bytes()
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

func pairRequest(t *testing.T, g *agentgateway.Gateway, signer ssh.Signer, subject string) *httptest.ResponseRecorder {
	return pairRequestWithProof(t, g, signer, signer, subject, "https://gateway.example.invalid"+agentgateway.PairPath, false)
}

func pairRequestWithProof(t *testing.T, g *agentgateway.Gateway, hostSigner, proofSigner ssh.Signer, subject, proofEndpoint string, wrongHash bool) *httptest.ResponseRecorder {
	t.Helper()
	req := makePairRequest(t, hostSigner, proofSigner, subject, proofEndpoint, wrongHash)
	recorder := httptest.NewRecorder()
	g.ServePair(recorder, req)
	return recorder
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
