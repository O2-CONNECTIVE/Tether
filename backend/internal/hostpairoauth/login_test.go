package hostpairoauth

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

const syntheticDiscovery = `{"issuer":"https://id.example","authorization_endpoint":"https://id.example/authorize","token_endpoint":"https://id.example/token","code_challenge_methods_supported":["S256"]}`

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
		OpenBrowser: func(context.Context, string) error {
			t.Fatal("browser opened with a non-loopback callback")
			return nil
		},
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
		OpenBrowser: func(context.Context, string) error {
			t.Fatal("browser opened after discovery redirect")
			return nil
		},
	})
	if err == nil || token != "" || calls != 1 || client.CheckRedirect != nil {
		t.Fatal("discovery redirect was accepted or the caller's client was changed")
	}
}

func TestAccessTokenReportsBrowserStartFailureAndClosesCallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://id.example/.well-known/openid-configuration" {
			t.Fatal("unexpected request after browser startup failed")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(syntheticDiscovery))}, nil
	})}
	var callbackHost string
	token, err := AccessToken(ctx, Config{
		Client: client, Issuer: "https://id.example", ClientID: "test-client", CallbackWait: 40 * time.Millisecond,
		OpenBrowser: func(_ context.Context, raw string) error {
			authURL, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			callbackURL, err := url.Parse(authURL.Query().Get("redirect_uri"))
			if err != nil || callbackURL.Host == "" {
				t.Fatal("browser received no callback address")
			}
			callbackHost = callbackURL.Host
			return errors.New("synthetic platform error with private details")
		},
	})
	if token != "" || err != ErrBrowserOpen {
		t.Errorf("expected only browser startup failure, got %v", err)
	}
	if callbackHost == "" {
		t.Fatal("browser handoff was not attempted")
	}
	conn, dialErr := net.DialTimeout("tcp4", callbackHost, time.Second)
	if dialErr == nil {
		conn.Close()
		t.Fatal("callback remained reachable after browser startup failed")
	}
}

func TestAccessTokenDoesNotFollowTokenRedirect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var tokenRequests int
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.String() {
		case "https://id.example/.well-known/openid-configuration":
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(syntheticDiscovery)),
			}, nil
		case "https://id.example/token":
			tokenRequests++
			if r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("code") != "synthetic-code" || r.Form.Get("code_verifier") == "" {
				t.Fatal("expected the authorization code and PKCE verifier in a token POST")
			}
			return &http.Response{
				StatusCode: http.StatusTemporaryRedirect,
				Header:     http.Header{"Location": {"https://other.example/token"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		default:
			t.Fatalf("unexpected OAuth request to %s", r.URL)
			return nil, nil
		}
	})}
	token, err := AccessToken(ctx, Config{
		Client: client, Issuer: "https://id.example", ClientID: "test-client",
		OpenBrowser: func(ctx context.Context, raw string) error {
			authURL, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			query := authURL.Query()
			params := url.Values{"code": {"synthetic-code"}, "state": {query.Get("state")}}
			callback, err := http.NewRequestWithContext(ctx, http.MethodGet, query.Get("redirect_uri")+"?"+params.Encode(), nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := http.DefaultClient.Do(callback)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("callback returned %d", response.StatusCode)
			}
			return nil
		},
	})
	if err == nil || token != "" || tokenRequests != 1 {
		t.Fatalf("expected a rejected redirect after one token POST, got %d POSTs and error %v", tokenRequests, err)
	}
}
