package agentgateway_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

const pairingAudience = "https://gateway.example.invalid/tether-agent/v1/pair"

func TestPairingVerifierRejectsInvalidTokensA1(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	file := agentgateway.FileConfig{Mode: agentgateway.ModeOAuthBearer, Verifier: agentgateway.VerifierConfig{
		Issuer: testIssuer, JWKS: jwks(t), Clock: func() time.Time { return now },
	}}
	v, err := file.PairingVerifier(pairingAudience, []string{"at+jwt"})
	if err != nil {
		t.Fatal(err)
	}
	baseClaims := func() map[string]any {
		return map[string]any{"iss": testIssuer, "aud": pairingAudience, "sub": subjects[0], "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()}
	}
	cases := []struct {
		name   string
		h, c   map[string]any
		key    []byte
		mutate bool
	}{
		{"MCP audience", edHeader(), func() map[string]any { c := baseClaims(); c["aud"] = testResource; return c }(), issuerKey, false},
		{"different issuer", edHeader(), func() map[string]any { c := baseClaims(); c["iss"] = "https://other.example.invalid/"; return c }(), issuerKey, false},
		{"expired", edHeader(), func() map[string]any {
			c := baseClaims()
			c["iat"] = now.Add(-2 * time.Minute).Unix()
			c["exp"] = now.Add(-time.Minute).Unix()
			return c
		}(), issuerKey, false},
		{"601 second lifetime", edHeader(), func() map[string]any { c := baseClaims(); c["exp"] = now.Add(601 * time.Second).Unix(); return c }(), issuerKey, false},
		{"unknown kid", map[string]any{"alg": "EdDSA", "kid": "missing", "typ": "at+jwt"}, baseClaims(), issuerKey, false},
		{"modified signature", edHeader(), baseClaims(), issuerKey, true},
		{"extra audience", edHeader(), func() map[string]any {
			c := baseClaims()
			c["aud"] = []string{pairingAudience, "https://other.example.invalid"}
			return c
		}(), issuerKey, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := sign(t, tc.h, tc.c, tc.key)
			if tc.mutate {
				parts := strings.Split(token, ".")
				sig, err := base64.RawURLEncoding.DecodeString(parts[2])
				if err != nil {
					t.Fatal(err)
				}
				sig[0] ^= 0x80
				parts[2] = base64.RawURLEncoding.EncodeToString(sig)
				token = strings.Join(parts, ".")
			}
			// Pairing credentials must be rejected before they can authorize enrollment.
			if _, err := v.Verify(token); err == nil {
				t.Fatal("invalid pairing token was accepted")
			}
		})
	}
}

func TestVerifierRefusesPairingAudienceA2(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	v, err := agentgateway.NewVerifier(agentgateway.VerifierConfig{Issuer: testIssuer, Audience: testResource, Types: []string{"at+jwt"}, JWKS: jwks(t), Clock: func() time.Time { return now }, RefusedAudience: pairingAudience})
	if err != nil {
		t.Fatal(err)
	}
	for _, aud := range []any{pairingAudience, []string{testResource, pairingAudience}} {
		c := map[string]any{"iss": testIssuer, "aud": aud, "sub": subjects[0], "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()}
		// A pairing token must never establish an MCP session.
		if _, err := v.Verify(sign(t, edHeader(), c, issuerKey)); err == nil {
			t.Fatalf("pairing audience %v was accepted by MCP verifier", aud)
		}
	}
}
