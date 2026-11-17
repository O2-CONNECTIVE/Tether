package agentgateway

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

const (
	oauthJWKSRefreshTimeout = 5 * time.Second
	oauthJWKSMinInterval    = time.Minute
	maxJWKSBytes            = 64 << 10
)

type jwksRefresh struct {
	url    string
	client *http.Client
	mu     sync.Mutex
	next   time.Time
}

func newJWKSRefresh(endpoint string) *jwksRefresh {
	timeout := oauthJWKSRefreshTimeout
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: timeout}).DialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		MaxResponseHeaderBytes: 16 << 10,
		MaxConnsPerHost:        1,
		DisableKeepAlives:      true,
		DisableCompression:     true,
	}
	return &jwksRefresh{
		url: endpoint,
		client: &http.Client{Transport: transport, Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func (v *Verifier) keySnapshot() map[string]verificationKey {
	v.keysMu.RLock()
	defer v.keysMu.RUnlock()
	return v.keys
}

func (v *Verifier) refreshUnknownKey(kid string) {
	r := v.refresh
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, known := v.keySnapshot()[kid]; known {
		return
	}
	now := v.clock()
	if now.Before(r.next) {
		return
	}
	r.next = now.Add(oauthJWKSMinInterval)
	keys, err := r.fetch()
	if err != nil {
		return
	}
	v.keysMu.Lock()
	v.keys = keys
	v.keysMu.Unlock()
}

func (r *jwksRefresh) fetch() (map[string]verificationKey, error) {
	ctx, cancel := context.WithTimeout(context.Background(), oauthJWKSRefreshTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return nil, ErrConfig
	}
	req.Header.Set("Accept", "application/json")
	res, err := r.client.Do(req)
	if err != nil {
		return nil, ErrConfig
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.ContentLength > maxJWKSBytes || res.Header.Get("Content-Encoding") != "" {
		return nil, ErrConfig
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxJWKSBytes+1))
	if err != nil || len(raw) > maxJWKSBytes || agentwire.CheckJSON(raw, false) != nil {
		return nil, ErrConfig
	}
	return parseJWKS(raw, false)
}

func jwsKeyID(token string) (string, bool) {
	if len(token) == 0 || len(token) > MaxTokenBytes {
		return "", false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" {
		return "", false
	}
	raw, err := b64.DecodeString(parts[0])
	if err != nil || agentwire.CheckJSON(raw, false) != nil {
		return "", false
	}
	h, err := agentwire.Fields(raw, "alg", "kid", "typ", "x5t", "x5t#S256")
	if err != nil {
		return "", false
	}
	alg, ok1 := str(h["alg"])
	kid, ok2 := str(h["kid"])
	if !ok1 || !ok2 || !printable(kid) || (alg != "RS256" && alg != "EdDSA" && alg != "ES256") {
		return "", false
	}
	return kid, true
}
