package agentgateway

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

// AccessKeyRefreshPolicy has no defaults. Adopting values and scheduling calls
// is a separate trusted composition decision. The explicit file configuration
// uses ConfiguredAccessKeyRefreshPolicy; direct callers must choose a policy.
// MaxAge is the TOTAL age since the last successful request START, not an
// extra stale allowance after MinInterval. Failed refreshes never extend it.
type AccessKeyRefreshPolicy struct {
	MinInterval time.Duration // minimum separation of attempts, including failures
	MaxAge      time.Duration // positive finite lifetime of a fetched key set
	Timeout     time.Duration // positive total request/read timeout, < MinInterval
}

// ErrAccessKeyRefresh is deliberately generic: provider bodies, headers, URLs
// and transport diagnostics are never returned as authentication diagnostics.
var ErrAccessKeyRefresh = errors.New("agentgateway: Access keys were not refreshed")

const maxAccessCertBytes = 64 << 10

// accessKeyRotation owns one immutable snapshot and at most one request. It
// starts empty and never seeds its lifetime from the process startup time.
type accessKeyRotation struct {
	mu        sync.Mutex
	keys      map[string]verificationKey
	fetchedAt time.Time
	nextTry   time.Time
	inFlight  bool
	policy    AccessKeyRefreshPolicy
	endpoint  string
	client    *http.Client
}

// ConfiguredAccessKeyRefreshPolicy is the explicit cloudflare_certs file-mode
// policy, not a default for all verifiers. The owner waits MinInterval AFTER
// each attempt completes. A failed attempt never extends the 300-second age.
// These are deliberate source policy values, not provider overlap guarantees.
func ConfiguredAccessKeyRefreshPolicy() AccessKeyRefreshPolicy {
	return AccessKeyRefreshPolicy{Timeout: 5 * time.Second, MinInterval: 60 * time.Second, MaxAge: 300 * time.Second}
}

// NewRotatingAccessVerifier is an explicit, opt-in counterpart to the static
// NewAccessVerifier. Keys must be empty: only a successful fresh HTTPS response
// can populate this verifier. Construction does not perform I/O or launch a
// goroutine. Verify fails closed until RefreshKeys succeeds and after MaxAge.
// Issuer is pinned to the operator's exact Cloudflare team HTTPS origin;
// neither token claims nor JOSE headers can choose a fetch destination.
func NewRotatingAccessVerifier(cfg AccessVerifierConfig, policy AccessKeyRefreshPolicy) (*AccessVerifier, error) {
	if !cloudflareTeamIssuer(cfg.Issuer) || !accessAUD(cfg.AUD) || len(cfg.Keys) != 0 ||
		cfg.Leeway < 0 || cfg.Leeway > MaxLeeway || policy.Timeout <= 0 ||
		policy.MinInterval <= policy.Timeout || policy.MaxAge <= policy.MinInterval {
		return nil, ErrConfig
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	// A dedicated transport does not accept ambient proxies, a caller-supplied
	// CA pool, or a caller's cookies/credentials. System trust still applies. No configurable transport is exported.
	transport := &http.Transport{
		DialContext:            (&net.Dialer{Timeout: policy.Timeout}).DialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    policy.Timeout,
		ResponseHeaderTimeout:  policy.Timeout,
		MaxResponseHeaderBytes: 16 << 10,
		MaxConnsPerHost:        1,
		DisableKeepAlives:      true,
		DisableCompression:     true,
	}
	r := &accessKeyRotation{policy: policy, endpoint: cfg.Issuer + "/cdn-cgi/access/certs"}
	r.client = &http.Client{Transport: transport, Timeout: policy.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &AccessVerifier{issuer: cfg.Issuer, aud: cfg.AUD, leeway: cfg.Leeway, clock: clock, rotation: r}, nil
}

// RefreshKeys performs at most one bounded GET. Concurrent, cancelled or
// rate-limited calls fail without queued work; Verify never calls this method,
// including for an unknown kid. The owner must schedule refresh and join any
// in-flight call on shutdown. No background scheduler is started here.
func (v *AccessVerifier) RefreshKeys(ctx context.Context) error {
	if v == nil || v.rotation == nil || ctx == nil || ctx.Err() != nil {
		return ErrAccessKeyRefresh
	}
	r := v.rotation
	now := v.clock()
	r.mu.Lock()
	if r.inFlight || now.Before(r.nextTry) {
		r.mu.Unlock()
		return ErrAccessKeyRefresh
	}
	r.inFlight, r.nextTry = true, now.Add(r.policy.MinInterval)
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, r.policy.Timeout)
	defer cancel()
	keys, err := r.fetch(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inFlight = false
	if err != nil || ctx.Err() != nil || !accessKeysFresh(now, v.clock(), r.policy.MaxAge) {
		return ErrAccessKeyRefresh
	}
	// Replace, never union. The provider publishes its own overlap keys and
	// removes retired keys. Failure preserves the old set's ORIGINAL expiry.
	r.keys, r.fetchedAt = keys, now
	return nil
}

func (r *accessKeyRotation) fetch(ctx context.Context) (map[string]verificationKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint, nil)
	if err != nil {
		return nil, ErrAccessKeyRefresh
	}
	req.Header.Set("Accept", "application/json")
	res, err := r.client.Do(req)
	if err != nil {
		return nil, ErrAccessKeyRefresh
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.ContentLength > maxAccessCertBytes ||
		res.Header.Get("Content-Encoding") != "" {
		return nil, ErrAccessKeyRefresh
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxAccessCertBytes+1))
	if err != nil || len(raw) > maxAccessCertBytes || agentwire.CheckJSON(raw, false) != nil {
		return nil, ErrAccessKeyRefresh
	}
	// Cloudflare's envelope also has public_cert and public_certs (PEM).
	// Validate strict JSON throughout, but use ONLY its JWK keys for trust.
	members, err := agentwire.Fields(raw, "keys", "public_cert", "public_certs")
	if err != nil || firstByte(members["keys"]) != '[' {
		return nil, ErrAccessKeyRefresh
	}
	jwks, err := json.Marshal(map[string]json.RawMessage{"keys": members["keys"]})
	if err != nil {
		return nil, ErrAccessKeyRefresh
	}
	keys, err := parseJWKS(jwks, true)
	if err != nil {
		return nil, ErrAccessKeyRefresh
	}
	return keys, nil
}

func (r *accessKeyRotation) verify(v *AccessVerifier, assertion string) (Identity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.keys) == 0 || !accessKeysFresh(r.fetchedAt, v.clock(), r.policy.MaxAge) {
		r.keys = nil // expiry or a time before fetch start invalidates this set
		return Identity{}, ErrToken
	}
	id, err := v.verifyWithKeys(assertion, r.keys)
	if !accessKeysFresh(r.fetchedAt, v.clock(), r.policy.MaxAge) {
		r.keys = nil
		return Identity{}, ErrToken
	}
	return id, err
}

func accessKeysFresh(start, now time.Time, maxAge time.Duration) bool {
	// Sub uses Go's monotonic component when available. Round strips that
	// component for a wall-clock check. Either time before fetch start fails.
	elapsed, wall := now.Sub(start), now.Round(0).Sub(start.Round(0))
	return elapsed >= 0 && elapsed < maxAge && wall >= 0 && wall < maxAge
}

func cloudflareTeamIssuer(issuer string) bool {
	if !accessIssuer(issuer) {
		return false
	}
	team := strings.TrimSuffix(strings.TrimPrefix(issuer, "https://"), ".cloudflareaccess.com")
	if team == "" || len(team) > 63 || strings.HasPrefix(team, "-") || strings.HasSuffix(team, "-") ||
		issuer != "https://"+team+".cloudflareaccess.com" {
		return false
	}
	for _, c := range team {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
