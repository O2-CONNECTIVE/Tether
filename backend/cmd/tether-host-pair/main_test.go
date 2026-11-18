package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewCallbackAcceptsOnlyOneMatchingLoopbackLogin(t *testing.T) {
	h, result := newCallback("expected-state")
	bad := httptest.NewRecorder()
	wrong := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/callback?state=wrong&code=secret", nil)
	wrong.RemoteAddr = "127.0.0.1:1234"
	h.ServeHTTP(bad, wrong)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("wrong state status=%d", bad.Code)
	}
	select {
	case got := <-result:
		if got.err == nil {
			t.Fatal("wrong state did not cancel")
		}
	default:
		t.Fatal("wrong state was not reported")
	}

	h, result = newCallback("state")
	for _, host := range []string{"192.0.2.1:1234", "[::1]:1234"} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/callback?state=state&code=x", nil)
		r.RemoteAddr = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("remote %s status=%d", host, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/callback?state=state&code=code", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "ログインが完了しました。このページを閉じてください" {
		t.Fatalf("accepted callback: %d %q", w.Code, w.Body.String())
	}
	second := httptest.NewRecorder()
	h.ServeHTTP(second, r)
	if second.Code != http.StatusConflict {
		t.Fatalf("second callback status=%d", second.Code)
	}
	select {
	case got := <-result:
		if got.code != "code" || got.err != nil {
			t.Fatalf("result=%+v", got)
		}
	default:
		t.Fatal("accepted callback not reported")
	}
}

func TestCallbackRejectsPresentEmptyError(t *testing.T) {
	h, result := newCallback("state")
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/callback?state=state&code=code&error=", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty error status=%d", w.Code)
	}
	if got := <-result; got.err == nil {
		t.Fatalf("empty error accepted: %+v", got)
	}
}

func TestCallbackRejectsMalformedQueryBeforeTokenExchange(t *testing.T) {
	h, result := newCallback("state")
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/callback?state=state&code=code&error=%zz", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed query status=%d", w.Code)
	}
	if got := <-result; got.err == nil || got.code != "" {
		t.Fatalf("malformed query accepted: %+v", got)
	}
}

func TestParsePairResultRequiresExactJSONMediaTypeAndMembers(t *testing.T) {
	valid := []byte(`{"registration_endpoint":"wss://gw.test/register","host_label":"host","principal_label":"principal","principal_public_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAvzQ9BrCC5xE1lQfM4NXu7CwJwE74lfM2nCHx9+spk8"}`)
	duplicate := strings.Replace(string(valid), `,"host_label"`, `,"registration_endpoint":"wss://gw.test/register","host_label"`, 1)
	unknown := strings.TrimSuffix(string(valid), "}") + `,"extra":true}`
	for _, tc := range []struct{ ct, body string }{
		{"application/jsonp", string(valid)},
		{"application/json", duplicate},
		{"application/json", unknown},
	} {
		if _, err := parsePairResult(tc.ct, []byte(tc.body)); err == nil {
			t.Errorf("accepted content-type=%q body=%s", tc.ct, tc.body)
		}
	}
}

func TestExecuteRejectsInvalidArgumentsWithoutNetworkOrBrowser(t *testing.T) {
	calls := 0
	d := deps{client: http.DefaultClient, location: func(context.Context) (string, string, error) { calls++; return "", "", nil }, openBrowser: func(string) error { calls++; return nil }, callbackWait: time.Millisecond, pairTimeout: time.Millisecond}
	cases := [][]string{{}, {"--gateway", "https://gw.test", "--issuer", "https://id.test"}, {"--gateway", "https://gw.test", "--gateway", "https://gw.test", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "http://gw.test", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "https://gw.test/path", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "https://gw.test?x=1", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "HTTPS://gw.test", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "https://GW.test", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "https://gw.test", "--issuer", "https://id.test", "--client-id", "x", "extra"}}
	for _, args := range cases {
		var out, errOut strings.Builder
		if got := execute(context.Background(), args, &out, &errOut, d); got != 2 {
			t.Errorf("args %q status=%d stderr=%q", args, got, errOut.String())
		}
	}
	if calls != 0 {
		t.Fatalf("invalid arguments touched injected services %d times", calls)
	}
}
