package main

import (
	"context"
	"crypto/ed25519"
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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
	"golang.org/x/crypto/ssh"
)

type deps struct {
	client                    *http.Client
	location                  func(context.Context) (home, policyPath string, err error)
	readProtected             func(context.Context, string) ([]byte, error)
	openBrowser               func(string) error
	callbackWait, pairTimeout time.Duration
}
type callbackResult struct {
	code string
	err  error
}

const (
	diagPairRunning    = "別の pair が実行中です"
	diagUnsupportedOS  = "この OS では pair を使えません"
	diagLoginCancelled = "ログインを中止しました"
	diagPairRejected   = "gateway が登録を拒否しました。前のペアは変わりません"
	diagPairUnknown    = "gateway 側で登録が済んだか分かりません。済んでいれば前のペアの鍵は使えなくなっています。もう一度 pair を実行してください"
	diagWriteFailed    = "gateway には登録しましたが、鍵か policy を書けませんでした。もう一度 pair を実行してください"
	diagPairComplete   = "ペア登録が完了しました。常駐プロセスを止めて起動し直してください"
	diagLocationUnsafe = "保存先の保護を確かめられませんでした。何も変えていません"
	diagSyncWarning    = "保存先の同期を確かめられませんでした。登録と書き出しは済んでいます"
)

func parseArgs(args []string) (string, string, string, bool) {
	values := map[string]string{}
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) || args[i] != "--gateway" && args[i] != "--issuer" && args[i] != "--client-id" {
			return "", "", "", false
		}
		k := args[i]
		if _, ok := values[k]; ok {
			return "", "", "", false
		}
		values[k] = args[i+1]
		i++
	}
	g, i, c := values["--gateway"], values["--issuer"], values["--client-id"]
	if len(values) != 3 || !validURL(g, true) || !validGatewayForm(g) || !validURL(i, false) || len(c) < 1 || len(c) > 256 {
		return "", "", "", false
	}
	for _, r := range c {
		if r < 0x21 || r > 0x7e {
			return "", "", "", false
		}
	}
	return g, i, c, true
}
func validGatewayForm(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host == strings.ToLower(u.Host) && (u.Path == "" || u.Path == "/") && raw == "https://"+u.Host+u.Path
}
func validURL(raw string, gateway bool) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return false
	}
	return !gateway || u.Path == "" || u.Path == "/"
}
func execute(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int {
	gateway, issuer, clientID, ok := parseArgs(args)
	if !ok {
		fmt.Fprintln(stderr, "引数を確認してください")
		return 2
	}
	if d.client == nil || d.location == nil {
		fmt.Fprintln(stderr, diagUnsupportedOS)
		return 1
	}
	locationCtx, cancelLocation := context.WithTimeout(ctx, 10*time.Second)
	home, policyPath, err := d.location(locationCtx)
	cancelLocation()
	if err != nil || !filepath.IsAbs(home) || !filepath.IsAbs(policyPath) || filepath.Clean(policyPath) != policyPath {
		fmt.Fprintln(stderr, diagUnsupportedOS)
		return 1
	}
	dir := filepath.Dir(policyPath)
	if err = os.MkdirAll(dir, 0700); err != nil {
		fmt.Fprintln(stderr, diagLocationUnsafe)
		return 1
	}
	if err = lockPair(dir); err != nil {
		if errors.Is(err, errPairBusy) {
			fmt.Fprintln(stderr, diagPairRunning)
		} else if errors.Is(err, errPairUnsupported) {
			fmt.Fprintln(stderr, diagUnsupportedOS)
		} else {
			fmt.Fprintln(stderr, diagLocationUnsafe)
		}
		return 1
	}
	defer unlockPair()
	if d.readProtected == nil {
		d.readProtected = agentkeys.ReadTrustConfig
	}
	lockPath := filepath.Join(dir, "pair.lock")
	protectCtx, cancelProtect := context.WithTimeout(ctx, 10*time.Second)
	_, err = d.readProtected(protectCtx, lockPath)
	cancelProtect()
	if err != nil {
		fmt.Fprintln(stderr, diagLocationUnsafe)
		return 1
	}
	var oldPolicy []byte
	if _, e := os.Lstat(policyPath); e == nil {
		protectCtx, cancelProtect = context.WithTimeout(ctx, 10*time.Second)
		oldPolicy, err = d.readProtected(protectCtx, policyPath)
		cancelProtect()
		if err != nil {
			fmt.Fprintln(stderr, diagLocationUnsafe)
			return 1
		}
	} else if !os.IsNotExist(e) {
		fmt.Fprintln(stderr, diagLocationUnsafe)
		return 1
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintln(stderr, diagLoginCancelled)
		return 1
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		fmt.Fprintln(stderr, diagLoginCancelled)
		return 1
	}
	wire := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal())
	discovery, err := discover(ctx, d.client, issuer)
	if err != nil {
		fmt.Fprintln(stderr, diagLoginCancelled)
		return 1
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(stderr, diagLoginCancelled)
		return 1
	}
	defer listener.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)
	state, err := randomURL(32)
	if err != nil {
		fmt.Fprintln(stderr, diagLoginCancelled)
		return 1
	}
	verifier, err := randomURL(32)
	if err != nil {
		fmt.Fprintln(stderr, diagLoginCancelled)
		return 1
	}
	ch := sha256.Sum256([]byte(verifier))
	handler, result := newCallback(state)
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(listener)
	defer srv.Close()
	auth, _ := url.Parse(discovery.AuthorizationEndpoint)
	q := auth.Query()
	q.Del("scope")
	for k, v := range map[string]string{"response_type": "code", "client_id": clientID, "redirect_uri": redirect, "code_challenge": base64.RawURLEncoding.EncodeToString(ch[:]), "code_challenge_method": "S256", "state": state} {
		q.Set(k, v)
	}
	auth.RawQuery = q.Encode()
	fmt.Fprintln(stderr, auth.String())
	if d.openBrowser != nil {
		_ = d.openBrowser(auth.String())
	}
	wait := d.callbackWait
	if wait <= 0 {
		wait = 5 * time.Minute
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	var cb callbackResult
	select {
	case cb = <-result:
	case <-ctx.Done():
		fmt.Fprintln(stderr, diagLoginCancelled)
		return 1
	case <-timer.C:
		fmt.Fprintln(stderr, diagLoginCancelled)
		return 1
	}
	if cb.err != nil {
		fmt.Fprintln(stderr, diagLoginCancelled)
		return 1
	}
	token, err := exchange(ctx, d.client, discovery.TokenEndpoint, cb.code, redirect, clientID, verifier)
	if err != nil {
		fmt.Fprintln(stderr, diagLoginCancelled)
		return 1
	}
	parsedGateway, _ := url.Parse(gateway)
	pairURL := "https://" + parsedGateway.Host + agentgateway.PairPath
	tokenHash := sha256.Sum256([]byte(token))
	sig, err := signer.Sign(rand.Reader, agentgateway.PairTranscript(pairURL, signer.PublicKey().Marshal(), tokenHash))
	if err != nil {
		fmt.Fprintln(stderr, diagPairUnknown)
		return 1
	}
	proof := base64.RawURLEncoding.EncodeToString(ssh.Marshal(sig))
	body, _ := json.Marshal(map[string]string{"host_public_key": wire, "proof": proof})
	pt := d.pairTimeout
	if pt <= 0 {
		pt = 60 * time.Second
	}
	pctx, cancel := context.WithTimeout(ctx, pt)
	defer cancel()
	req, _ := http.NewRequestWithContext(pctx, http.MethodPost, pairURL, strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		fmt.Fprintln(stderr, diagPairUnknown)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		fmt.Fprintln(stderr, diagPairRejected)
		return 1
	}
	if resp.StatusCode != 200 {
		fmt.Fprintln(stderr, diagPairUnknown)
		return 1
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024+1))
	if err != nil || len(raw) > 16*1024 {
		fmt.Fprintln(stderr, diagPairUnknown)
		return 1
	}
	info, err := parsePairResult(resp.Header.Get("Content-Type"), raw)
	if err != nil {
		fmt.Fprintln(stderr, diagPairUnknown)
		return 1
	}
	syncWarning, installErr := install(dir, policyPath, home, priv, signer, info, oldPolicy)
	if installErr != nil {
		fmt.Fprintln(stderr, diagWriteFailed)
		return 1
	}
	if syncWarning {
		fmt.Fprintln(stderr, diagSyncWarning)
	}
	fmt.Fprintln(stdout, diagPairComplete)
	return 0
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
		q := r.URL.Query()
		if q.Get("state") != state || q.Has("error") {
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
		io.WriteString(w, "ログインが完了しました。このページを閉じてください")
		ch <- callbackResult{code: code}
	}), ch
}
func main() {
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	os.Exit(execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr, deps{client: client, location: residentmac.EnrolledLocation, openBrowser: openBrowser, callbackWait: 5 * time.Minute, pairTimeout: 60 * time.Second}))
}
