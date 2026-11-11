package agentgateway

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAccessRotationConstructorRequiresExplicitTrustAndPolicy(t *testing.T) {
	cfg := AccessVerifierConfig{Issuer: rotationIssuer, AUD: "synthetic-aud"}
	policy := AccessKeyRefreshPolicy{MinInterval: time.Minute, MaxAge: time.Hour, Timeout: time.Second}
	for _, issuer := range []string{"", "http://synthetic-team.cloudflareaccess.com", "https://example.invalid",
		"https://cloudflareaccess.com", "https://a.b.cloudflareaccess.com", "https://a.cloudflareaccess.com:443",
		"https://a.cloudflareaccess.com/", "https://a.cloudflareaccess.com?x=1", "https://user@a.cloudflareaccess.com",
		"https://a.cloudflareaccess.com.attacker.invalid", "https://-a.cloudflareaccess.com", "https://A.cloudflareaccess.com"} {
		bad := cfg
		bad.Issuer = issuer
		if _, err := NewRotatingAccessVerifier(bad, policy); !errors.Is(err, ErrConfig) {
			t.Errorf("bad issuer accepted: %q", issuer)
		}
	}
	for name, edit := range map[string]func(*AccessVerifierConfig, *AccessKeyRefreshPolicy){
		"static seed":        func(c *AccessVerifierConfig, _ *AccessKeyRefreshPolicy) { c.Keys = []byte(`{"keys":[]}`) },
		"bad aud":            func(c *AccessVerifierConfig, _ *AccessKeyRefreshPolicy) { c.AUD = "https://resource.invalid/mcp" },
		"leeway":             func(c *AccessVerifierConfig, _ *AccessKeyRefreshPolicy) { c.Leeway = MaxLeeway + 1 },
		"zero timeout":       func(_ *AccessVerifierConfig, p *AccessKeyRefreshPolicy) { p.Timeout = 0 },
		"negative timeout":   func(_ *AccessVerifierConfig, p *AccessKeyRefreshPolicy) { p.Timeout = -1 },
		"no spacing":         func(_ *AccessVerifierConfig, p *AccessKeyRefreshPolicy) { p.MinInterval = p.Timeout },
		"no freshness":       func(_ *AccessVerifierConfig, p *AccessKeyRefreshPolicy) { p.MaxAge = p.MinInterval },
		"negative freshness": func(_ *AccessVerifierConfig, p *AccessKeyRefreshPolicy) { p.MaxAge = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			c, p := cfg, policy
			edit(&c, &p)
			if _, err := NewRotatingAccessVerifier(c, p); !errors.Is(err, ErrConfig) {
				t.Fatal("invalid constructor accepted", err)
			}
		})
	}
	v, err := NewRotatingAccessVerifier(cfg, policy)
	if err != nil {
		t.Fatal(err)
	}
	client := v.rotation.client
	tr := client.Transport.(*http.Transport)
	if client.Jar != nil || client.Timeout != policy.Timeout || tr.Proxy != nil || tr.TLSClientConfig.InsecureSkipVerify ||
		tr.TLSClientConfig.RootCAs != nil || len(tr.TLSClientConfig.Certificates) != 0 ||
		tr.TLSClientConfig.MinVersion < tls.VersionTLS12 || !tr.DisableCompression || !tr.DisableKeepAlives ||
		tr.MaxConnsPerHost != 1 || tr.MaxResponseHeaderBytes != 16<<10 {
		t.Fatal("unsafe production HTTP policy")
	}
	if err := client.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatal("redirects enabled")
	}
	if err := (&AccessVerifier{}).RefreshKeys(context.Background()); !errors.Is(err, ErrAccessKeyRefresh) {
		t.Fatal("static verifier allowed refresh")
	}
}

type rotationBody struct {
	io.Reader
	closed bool
}

func (b *rotationBody) Close() error { b.closed = true; return nil }

func TestAccessRotationHTTPFailuresCloseBodiesAndKeepEmptyTrust(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect,
		http.StatusNotModified, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newRotationFixture(t)
			body := &rotationBody{Reader: strings.NewReader(f.body)}
			calls := 0
			f.v.rotation.client.Transport = rotationTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://attacker.invalid/keys"}}, Body: body}, nil
			})
			if f.v.RefreshKeys(context.Background()) == nil || !body.closed || calls != 1 {
				t.Fatal("non-200 accepted, redirect followed, or body leaked")
			}
		})
	}
	for _, mode := range []string{"declared-size", "streamed-size", "compressed", "read-error"} {
		t.Run(mode, func(t *testing.T) {
			f := newRotationFixture(t)
			body := &rotationBody{Reader: strings.NewReader(f.body)}
			res := &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}
			switch mode {
			case "declared-size":
				res.ContentLength = maxAccessCertBytes + 1
			case "streamed-size":
				res.ContentLength = -1
				body.Reader = strings.NewReader(strings.Repeat(" ", maxAccessCertBytes+1))
			case "compressed":
				res.Header.Set("Content-Encoding", "gzip")
			case "read-error":
				body.Reader = rotationErrorReader{}
			}
			f.v.rotation.client.Transport = rotationTransport(func(*http.Request) (*http.Response, error) { return res, nil })
			if f.v.RefreshKeys(context.Background()) == nil || !body.closed {
				t.Fatal("invalid response accepted or leaked")
			}
		})
	}
}

type rotationErrorReader struct{}

func (rotationErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic-private-diagnostic")
}

func TestAccessRotationLifetimeStartsBeforeRequest(t *testing.T) {
	f := newRotationFixture(t)
	start := f.now
	original := f.v.rotation.client.Transport
	f.v.rotation.client.Transport = rotationTransport(func(req *http.Request) (*http.Response, error) {
		f.now = start.Add(9 * time.Minute)
		return original.RoundTrip(req)
	})
	if err := f.v.RefreshKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.now = start.Add(10 * time.Minute)
	if _, err := f.v.Verify(rotationToken(t, "old", f.now, nil)); !errors.Is(err, ErrToken) {
		t.Fatal("slow response extended key lifetime")
	}
}

func TestAccessRotationRejectsClockRollback(t *testing.T) {
	f := newRotationFixture(t)
	start := f.now
	if err := f.v.RefreshKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.now = start.Add(-time.Second)
	if _, err := f.v.Verify(rotationToken(t, "old", start, nil)); !errors.Is(err, ErrToken) {
		t.Fatal("cache survived clock rollback")
	}
	f.now = start
	if _, err := f.v.Verify(rotationToken(t, "old", start, nil)); !errors.Is(err, ErrToken) {
		t.Fatal("rollback-invalidated cache resurrected")
	}
}

func TestAccessRotationRejectsLateOrCancelledRefresh(t *testing.T) {
	for _, mode := range []string{"expired", "rollback", "cancelled", "transport", "read"} {
		t.Run(mode, func(t *testing.T) {
			f := newRotationFixture(t)
			start := f.now
			old := rotationToken(t, "old", start, nil)
			if err := f.v.RefreshKeys(context.Background()); err != nil {
				t.Fatal(err)
			}
			original := f.v.rotation.client.Transport
			f.now = start.Add(time.Minute)
			f.body = rotationCerts(t, "next")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.v.rotation.client.Transport = rotationTransport(func(req *http.Request) (*http.Response, error) {
				switch mode {
				case "expired":
					f.now = start.Add(11 * time.Minute)
				case "rollback":
					f.now = start.Add(-time.Second)
				case "cancelled":
					cancel()
				case "transport":
					return nil, errors.New("synthetic-secret-diagnostic")
				case "read":
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(rotationErrorReader{})}, nil
				}
				return original.RoundTrip(req)
			})
			if err := f.v.RefreshKeys(ctx); err != ErrAccessKeyRefresh {
				t.Fatal("failure accepted or leaked diagnostics", err)
			}
			if !f.v.rotation.fetchedAt.Equal(start) {
				t.Fatal("failure renewed age")
			}
			if _, has := f.v.rotation.keys["next"]; has {
				t.Fatal("failure published new keys")
			}
			if mode != "expired" && mode != "rollback" {
				if _, err := f.v.Verify(old); err != nil {
					t.Fatal("lost fresh prior snapshot", err)
				}
			}
			f.now = start.Add(10 * time.Minute)
			if _, err := f.v.Verify(old); !errors.Is(err, ErrToken) {
				t.Fatal("old lifetime extended")
			}
		})
	}
}

func TestAccessRotationExpiresDuringVerification(t *testing.T) {
	f := newRotationFixture(t)
	start := f.now
	old := rotationToken(t, "old", start, nil)
	if err := f.v.RefreshKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.v.clock = func() time.Time {
		calls++
		if calls >= 3 {
			return start.Add(10 * time.Minute)
		}
		return start.Add(9 * time.Minute)
	}
	if _, err := f.v.Verify(old); !errors.Is(err, ErrToken) {
		t.Fatal("verification crossed cache deadline")
	}
}
