package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

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
	if e != nil || len(b) > 64*1024 || resp.StatusCode != 200 || json.Unmarshal(b, &d) != nil || d.Issuer != issuer || !validURL(d.AuthorizationEndpoint, false) || !validURL(d.TokenEndpoint, false) {
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
