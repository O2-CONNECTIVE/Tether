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
ON だけでは command/path/env を client が指定する入口はありません。

`allow <request_id> <expected_revision>` は、署名付きの汎用コマンド
（[run-command-design](run-command-design.md)）を現在の待機の間、最大15分だけ
通します。`deny` は取り消します。期間は pipe で指定できません。許可は待機の
状態遷移ではないので `status` には出ず、結果は receipt だけで返ります。
pipe 側が未知（poisoned）または遷移待ちの間は `deny` も Mac 側の owner へ
届きません。その間に許可を確実に閉じる手段は `close` か `off` です。
ON で `allow` が受理される経路は source 試験では通しておらず、CI の実 producer
の結合試験で確認します。

## 実行したコマンドの記録

汎用コマンドは、起動の直前に policy と同じディレクトリの `run-history.jsonl`
へ1行（時刻、作業ディレクトリ、本文）追記します。追記できないコマンドは
起動しません。固定 operation は記録しません。開発 GUI はこのファイルを毎秒
読み、直近5件を表示します。

- 本文を平文で残します。mode は 0600 で、消すにはファイル（と `.1`）を削除します。
- 1 MiB を超えると `run-history.jsonl.1` へ退避します。退避は1世代だけです。
- 記録は起動の試行です。記録の後で起動に失敗したコマンドも残ります。
- **改ざんの証拠にはなりません。** コマンドは同じユーザー権限で動くので、
  記録を書き換えたり消したりできます。GUI は記録が短くなったことだけを
  表示します。link に差し替えられた記録には追記せず、次のコマンドを拒否します。
- 通常製品の経路も同じ読込みを通りますが、汎用コマンドの許可を開く入口が
  無いので、記録は書かれません。

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
