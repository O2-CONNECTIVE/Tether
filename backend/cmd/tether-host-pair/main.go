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
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/hostkeychain"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/hostpairoauth"
	"golang.org/x/crypto/ssh"
)

type deps struct {
	client                    *http.Client
	guiClient                 *http.Client
	location                  func(context.Context) (home, policyPath string, err error)
	readProtected             func(context.Context, string) ([]byte, error)
	openBrowser               func(string) error
	storeHostKey              func([]byte, string) error
	loadHostKey               func(string) (ssh.Signer, error)
	deleteHostKey             func(string) error
	runAppleScript            func(context.Context, string, ...string) (string, error)
	callbackAddress           string
	callbackWait, pairTimeout time.Duration
	confirmInterval           time.Duration
}

var guiGateway, guiIssuer, guiClientID string

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
	if len(values) != 3 || !validGatewayForm(g) || !hostpairoauth.ValidClient(i, c) {
		return "", "", "", false
	}
	return g, i, c, true
}
func validGatewayForm(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.Host == strings.ToLower(u.Host) && (u.Path == "" || u.Path == "/") && raw == "https://"+u.Host+u.Path
}
func execute(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int {
	if len(args) == 0 {
		return executeGUI(ctx, stdout, d)
	}
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
	probePath, err := writeTemp(dir, ".pair-probe-", []byte("pair probe\n"))
	if err != nil {
		fmt.Fprintln(stderr, diagLocationUnsafe)
		return 1
	}
	defer os.Remove(probePath)
	protectCtx, cancelProtect = context.WithTimeout(ctx, 10*time.Second)
	_, probeErr := d.readProtected(protectCtx, probePath)
	cancelProtect()
	removeErr := os.Remove(probePath)
	if probeErr != nil || removeErr != nil {
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
	token, err := hostpairoauth.AccessToken(ctx, hostpairoauth.Config{
		Client: d.client, Issuer: issuer, ClientID: clientID,
		CallbackAddress: d.callbackAddress, CallbackWait: d.callbackWait,
		OpenBrowser: func(_ context.Context, authURL string) error {
			fmt.Fprintln(stderr, authURL)
			if d.openBrowser != nil {
				_ = d.openBrowser(authURL)
			}
			// The CLI can continue with the printed URL if automatic opening fails.
			return nil
		},
	})
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
func productionDeps() deps {
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	d := deps{
		client: client, guiClient: guiHTTPClient(), readProtected: agentkeys.ReadTrustConfig,
		openBrowser: openBrowser, storeHostKey: hostkeychain.Store, loadHostKey: hostkeychain.Load,
		deleteHostKey: hostkeychain.Delete, runAppleScript: runAppleScript,
		callbackWait: 5 * time.Minute, pairTimeout: 60 * time.Second,
	}
	configurePairing(&d)
	return d
}

func guiSignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func runMain(args []string, stdout, stderr io.Writer, d deps) int {
	if len(args) != 0 {
		return execute(context.Background(), args, stdout, stderr, d)
	}
	ctx, stop := guiSignalContext()
	defer stop()
	return execute(ctx, args, stdout, stderr, d)
}

func main() {
	os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr, productionDeps()))
}
