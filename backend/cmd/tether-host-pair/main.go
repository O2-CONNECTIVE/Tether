package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"time"
)

type deps struct {
	client       *http.Client
	location     func(context.Context) (home, policyPath string, err error)
	openBrowser  func(url string) error
	callbackWait time.Duration
	pairTimeout  time.Duration
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
)

func execute(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int { return 1 }
func newCallback(state string) (http.Handler, <-chan callbackResult) {
	ch := make(chan callbackResult)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not implemented", http.StatusInternalServerError)
	}), ch
}
func main() {
	os.Exit(execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr, deps{client: http.DefaultClient, location: func(context.Context) (string, string, error) { return "", "", nil }, openBrowser: func(string) error { return nil }, callbackWait: 5 * time.Minute, pairTimeout: 60 * time.Second}))
}
