package hostpairoauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config describes the trusted native caller's OAuth client and browser entry.
// The default callback is an ephemeral IPv4 loopback port.
type Config struct {
	Client          *http.Client
	Issuer          string
	ClientID        string
	CallbackAddress string
	CallbackWait    time.Duration
	// OpenBrowser starts the external browser and returns without waiting for consent.
	// The caller observes ctx cancellation while starting it.
	OpenBrowser func(ctx context.Context, authorizationURL string)
}

// ValidClient checks the issuer and public client ID before local or network work.
func ValidClient(issuer, clientID string) bool {
	return validEndpoint(issuer, false) && len(clientID) > 0 && len(clientID) <= 256 && asciiVisible(clientID)
}

// AccessToken completes browser authorization and returns a transient bearer
// token to the trusted caller, which owns subsequent registration decisions.
func AccessToken(ctx context.Context, cfg Config) (string, error) {
	if cfg.Client == nil || cfg.OpenBrowser == nil || !ValidClient(cfg.Issuer, cfg.ClientID) {
		return "", errors.New("invalid host pairing OAuth configuration")
	}
	address := cfg.CallbackAddress
	if address == "" {
		address = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return "", errors.New("invalid host pairing OAuth callback address")
	}
	client := *cfg.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	discovery, err := discover(ctx, &client, cfg.Issuer)
	if err != nil {
		return "", err
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return "", err
	}
	defer listener.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)
	state, err := randomURL(32)
	if err != nil {
		return "", err
	}
	verifier, err := randomURL(32)
	if err != nil {
		return "", err
	}
	challenge := sha256.Sum256([]byte(verifier))
	handler, result := newCallback(state)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	defer server.Close()

	authorize, _ := url.Parse(discovery.AuthorizationEndpoint)
	query := authorize.Query()
	query.Del("scope")
	query.Set("response_type", "code")
	query.Set("client_id", cfg.ClientID)
	query.Set("redirect_uri", redirect)
	query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	query.Set("code_challenge_method", "S256")
	query.Set("state", state)
	authorize.RawQuery = query.Encode()
	cfg.OpenBrowser(ctx, authorize.String())

	wait := cfg.CallbackWait
	if wait <= 0 {
		wait = 5 * time.Minute
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case response := <-result:
		if response.err != nil {
			return "", response.err
		}
		return exchange(ctx, &client, discovery.TokenEndpoint, response.code, redirect, cfg.ClientID, verifier)
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return "", errors.New("host pairing OAuth callback timed out")
	}
}

type oidcDiscovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
}
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

func discover(ctx context.Context, c *http.Client, issuer string) (oidcDiscovery, error) {
	var d oidcDiscovery
	u := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
	r, e := http.NewRequestWithContext(ctx, "GET", u, nil)
	if e != nil {
		return d, e
	}
	resp, e := c.Do(r)
	if e != nil {
		return d, e
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if e != nil || len(b) > 64*1024 || resp.StatusCode != 200 || json.Unmarshal(b, &d) != nil || d.Issuer != issuer || !validEndpoint(d.AuthorizationEndpoint, true) || !validEndpoint(d.TokenEndpoint, false) {
		return d, fmt.Errorf("discovery invalid")
	}
	if d.CodeChallengeMethods != nil {
		found := false
		for _, m := range d.CodeChallengeMethods {
			if m == "S256" {
				found = true
			}
		}
		if !found {
			return d, fmt.Errorf("pkce unavailable")
		}
	}
	return d, nil
}
func validEndpoint(raw string, allowQuery bool) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Opaque == "" &&
		!strings.Contains(raw, "#") && (allowQuery || u.RawQuery == "" && !u.ForceQuery)
}

func randomURL(n int) (string, error) {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func exchange(ctx context.Context, c *http.Client, endpoint, code, redirect, clientID, verifier string) (string, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "client_id": {clientID}, "code_verifier": {verifier}}
	r, e := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if e != nil {
		return "", e
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, e := c.Do(r)
	if e != nil {
		return "", e
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if e != nil || len(b) > 64*1024 || resp.StatusCode != 200 {
		return "", fmt.Errorf("token response invalid")
	}
	var t tokenResponse
	if json.Unmarshal(b, &t) != nil || len(t.AccessToken) < 1 || len(t.AccessToken) > 8000 || !asciiVisible(t.AccessToken) || !strings.EqualFold(t.TokenType, "Bearer") {
		return "", fmt.Errorf("token response invalid")
	}
	return t.AccessToken, nil
}
func asciiVisible(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
