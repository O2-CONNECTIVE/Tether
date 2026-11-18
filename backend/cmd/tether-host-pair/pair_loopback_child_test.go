//go:build unix && pair_loopback

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPairLoopbackChild(t *testing.T) {
	if os.Getenv("TETHER_PAIR_LOOPBACK_CHILD") == "" {
		t.Fatal("TestPairLoopbackChild is a subprocess fixture; run it through TestPairLoopbackE2E")
	}
	home := os.Getenv("TETHER_PAIR_LOOPBACK_HOME")
	policyPath := os.Getenv("TETHER_PAIR_LOOPBACK_POLICY")
	gateway := os.Getenv("TETHER_PAIR_LOOPBACK_GATEWAY")
	issuer := os.Getenv("TETHER_PAIR_LOOPBACK_ISSUER")
	clientID := os.Getenv("TETHER_PAIR_LOOPBACK_CLIENT_ID")
	caPath := os.Getenv("TETHER_PAIR_LOOPBACK_CA")
	for name, value := range map[string]string{"home": home, "policy": policyPath, "gateway": gateway, "issuer": issuer, "client id": clientID, "CA": caPath} {
		if value == "" {
			t.Fatalf("missing loopback %s", name)
		}
	}
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal("read loopback CA", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("parse loopback CA")
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
	defer client.CloseIdleConnections()
	d := deps{
		client:       client,
		location:     func(context.Context) (string, string, error) { return home, policyPath, nil },
		callbackWait: 5 * time.Second,
		pairTimeout:  5 * time.Second,
	}
	d.openBrowser = func(raw string) error {
		authorization, err := url.Parse(raw)
		if err != nil {
			return err
		}
		callback, err := url.Parse(authorization.Query().Get("redirect_uri"))
		if err != nil {
			return err
		}
		query := callback.Query()
		query.Set("state", authorization.Query().Get("state"))
		query.Set("code", "loopback-auth-code")
		callback.RawQuery = query.Encode()
		response, err := client.Get(callback.String())
		if err == nil {
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("loopback callback status %d", response.StatusCode)
			}
		}
		return err
	}
	args := []string{"--gateway", gateway, "--issuer", issuer, "--client-id", clientID}
	var stdout, stderr strings.Builder
	if code := execute(context.Background(), args, &stdout, &stderr, d); code != 0 {
		t.Fatalf("pair command exit=%d: %s", code, stderr.String())
	}
}
