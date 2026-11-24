package hostpairoauth

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
)

type callbackResult struct {
	code string
	err  error
}

func newCallback(state string) (http.Handler, <-chan callbackResult) {
	ch := make(chan callbackResult, 1)
	var mu sync.Mutex
	done := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, e := net.SplitHostPort(r.RemoteAddr)
		if e != nil || ip != "127.0.0.1" {
			http.Error(w, "", http.StatusForbidden)
			return
		}
		if r.Method != "GET" || r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if done {
			http.Error(w, "", http.StatusConflict)
			return
		}
		done = true
		q, queryErr := url.ParseQuery(r.URL.RawQuery)
		if queryErr != nil || q.Get("state") != state || q.Has("error") {
			http.Error(w, "", http.StatusBadRequest)
			ch <- callbackResult{err: errors.New("cancelled")}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "", http.StatusBadRequest)
			ch <- callbackResult{err: errors.New("cancelled")}
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ログインの応答を受け取りました。このページを閉じ、Tether で登録結果を確認してください。")
		ch <- callbackResult{code: code}
	}), ch
}
