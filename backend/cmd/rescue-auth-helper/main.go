// rescue-auth-helperは単回認証helperと専用supplier serviceの2役割を持つ。
// 役割と期待値はimage内の固定値だけで決まり、既定の未設定値では両方を拒否する。
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
)

func main() { os.Exit(execute(context.Background(), os.Args[1:], os.Stderr)) }

func execute(ctx context.Context, args []string, diagnostic io.Writer) int {
	if ctx != nil && ctx.Err() == nil {
		if err := operatorauth.RunFixed(ctx, args...); err == nil {
			return 0
		}
	}
	if diagnostic != nil {
		fmt.Fprintln(diagnostic, "認証helperの固定構成または回収を確認できません")
	}
	return 2
}
