package hostpairoauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestAccessTokenRejectsNonLoopbackCallbackBeforeDiscovery(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("discovery ran with a non-loopback callback")
		return nil, nil
	})}
	token, err := AccessToken(context.Background(), Config{
		Client: client, Issuer: "https://id.example", ClientID: "test-client",
		CallbackAddress: "0.0.0.0:1234",
		OpenBrowser:     func(string) { t.Fatal("browser opened with a non-loopback callback") },
	})
	if err == nil || token != "" {
		t.Fatal("non-loopback callback was accepted")
	}
}

func TestAccessTokenDoesNotFollowDiscoveryRedirect(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://id.example/.well-known/openid-configuration" {
			t.Fatalf("unexpected OAuth request %s", r.URL)
		}
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Header:     http.Header{"Location": {"https://other.example/discovery"}},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})}
	token, err := AccessToken(context.Background(), Config{
		Client: client, Issuer: "https://id.example", ClientID: "test-client",
		OpenBrowser: func(string) { t.Fatal("browser opened after discovery redirect") },
	})
	if err == nil || token != "" || calls != 1 || client.CheckRedirect != nil {
		t.Fatal("discovery redirect was accepted or the caller's client was changed")
	}
}
