package agentgateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// This runs ParseConfig -> GatewayConfig -> the candidate's real verifier and
// RefreshKeys, with only its HTTP transport and clock replaced in-package.
// The CLI ownership fixtures never inject private transport into production.
func TestConfiguredAccessRotationPolicyAndFreshness(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"listen": "127.0.0.1:8787", "auth_mode": "cloudflare_access", "resource": "https://gateway.example.invalid/mcp",
		"access_issuer": rotationIssuer, "access_aud": "synthetic-aud", "access_key_source": "cloudflare_certs", "leeway_seconds": 0,
		"principals": []any{map[string]any{"label": "owner", "subject": "synthetic-subject"}},
	})
	c, err := ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2000000000, 0)
	c.Access.Clock = func() time.Time { return now }
	cfg, err := c.GatewayConfig()
	if err != nil || cfg.Verifier != nil || cfg.Access == nil || cfg.Access.rotation == nil || len(c.Access.Keys) != 0 {
		t.Fatalf("wrong composition: %v", err)
	}
	v := cfg.Access
	policy := v.rotation.policy
	if policy.Timeout != 5*time.Second || policy.MinInterval != 60*time.Second || policy.MaxAge != 300*time.Second {
		t.Fatalf("unreviewed policy: %+v", policy)
	}
	if v.rotation.endpoint != rotationIssuer+"/cdn-cgi/access/certs" {
		t.Fatal("unpinned endpoint")
	}
	body, calls := rotationCerts(t, "old"), 0
	v.rotation.client.Transport = rotationTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	old, next := rotationToken(t, "old", now, nil), rotationToken(t, "next", now, nil)
	assert := func(token string, allowed bool) {
		t.Helper()
		_, err := v.Verify(token)
		if (err == nil) != allowed {
			t.Fatalf("verify allowed=%t err=%v", allowed, err)
		}
	}
	assert(old, false)
	if calls != 0 {
		t.Fatal("initial verification fetched")
	}
	if err := v.RefreshKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	assert(old, true)
	assert(next, false)
	if calls != 1 {
		t.Fatal("unknown kid fetched")
	}
	now = now.Add(time.Minute)
	body = rotationCerts(t, "old", "next")
	if err := v.RefreshKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	assert(old, true)
	assert(next, true)
	now = now.Add(time.Minute)
	body = rotationCerts(t, "next")
	if err := v.RefreshKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	assert(old, false)
	assert(next, true)
	lastSuccess := now
	now = lastSuccess.Add(299 * time.Second)
	body = `{"keys":[]}`
	if err := v.RefreshKeys(context.Background()); !errors.Is(err, ErrAccessKeyRefresh) {
		t.Fatal("failed fetch accepted")
	}
	assert(next, true)
	now = lastSuccess.Add(300 * time.Second)
	assert(next, false)
	now = lastSuccess.Add(time.Minute)
	assert(next, false) // no rollback resurrection
	if calls != 4 {
		t.Fatal("expiry/verification fetched")
	}
	now = lastSuccess.Add(360 * time.Second)
	body = rotationCerts(t, "next")
	if err := v.RefreshKeys(context.Background()); err != nil {
		t.Fatal("scheduled recovery refused", err)
	}
	assert(next, true)
}

func TestFileConfigRejectsRotatingSourceOnOAuthOrStaticKeys(t *testing.T) {
	for _, c := range []FileConfig{
		{Mode: ModeCloudflareAccess, AccessKeySource: "unknown"},
		{Mode: ModeCloudflareAccess, AccessKeySource: "cloudflare_certs", Access: AccessVerifierConfig{Issuer: rotationIssuer, AUD: "synthetic-aud", Keys: []byte(`{"keys":[]}`)}},
		{Mode: ModeOAuthBearer, AccessKeySource: "cloudflare_certs"},
	} {
		if _, err := c.GatewayConfig(); !errors.Is(err, ErrConfig) {
			t.Fatal("ambiguous direct config accepted")
		}
	}
}
