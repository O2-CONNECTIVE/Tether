package hostpairoauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
	if w.Code != http.StatusOK || w.Body.String() != "ログインの応答を受け取りました。このページを閉じ、Tether で登録結果を確認してください。" {
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
