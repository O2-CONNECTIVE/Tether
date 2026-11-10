# 限定開発用の常駐 CLI 候補

この source 候補は `tether_development` build tag を明示した専用
`cmd/tether-dev-resident` だけです。通常の GUI、sealed producer、署名 gate、
通常 `tether-host` は変更しません。Developer ID の成功を偽装せず、利用者が
個別に承認したローカル開発物を直接起動する別の信頼モデルです。
製品と同等の配布・publisher・native launcher 保証はありません。

OS account home の下の固定
`Library/Application Support/com.o2connective.tether.development/resident-policy.json`
だけを既存 strict loader で読みます。HOME、引数、remote の path は採用せず、
製品 enrollment への fallback はありません。別 enrollment の作成、実キーの
利用・登録、外部公開と永続アクセスは実行時の個別承認が必要です。
この変更はそれらを実施しません。実試験は別の dev host/principal identity と
credential を使用し、製品 enrollment/credential をコピー・共有しません。固定した
別ファイル名だけでは認証 identity の隔離は強制されません。

`foreground` は既存の専用 stdin/stdout pipe 契約を使います。TTY や通常ファイル
は対象外です。`status`、fresh revision に対する `on` / `off`、`close` を扱い、
ON するまでは接続しません。実 host/principal 認証、固定 operation、generation、
replay 拒否、元 owner の回収は既存実装を使います。operation は既存の
`TETHER_MCP_GUI_BEGIN` 出力、5秒待機、`TETHER_MCP_GUI_END` 出力だけで、
command/path/env を client が指定する入口を追加しません。

15分の終了要求期限は invocation の最初に固定し、ON/OFF・再接続で延長しません。
期限・signal・close・入力断は元 owner の Close を要求します。終了要求の時刻と
実際の cleanup 完了は異なり、unknown は既存実装どおり retained になります。
ローカル timer を公開アクセスの厳密な絶対期限の代わりにはしません。実試験には
別途固定した server-side bounded pilot の期限・同じ対象の停止確認が必要です。
LaunchAgent、ログイン起動、自動更新・再起動はありません。

source 検証の入口（実 enrollment/接続は開始しない）:

```sh
cd backend
go test -tags tether_development ./cmd/tether-dev-resident ./internal/residentmac ./internal/residentpipe
go vet -tags tether_development ./cmd/tether-dev-resident ./internal/residentmac ./internal/residentpipe
```

Darwin/cgo の実起動、ChatGPT の登録・同意、認証済み standby、5分を超える再接続、
固定 command の実結果、取消/OFF と全 owner 回収は別受入です。unit fixture や
通常 build の成功では完了扱いしません。build tag は利用者の同意を代替しません。
