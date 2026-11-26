# 限定開発用の常駐 CLI 候補

この source 候補は `tether_development` build tag を明示した専用の
常駐 CLI とペア登録 CLI です。通常の GUI、sealed producer、署名 gate、
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

## 開発用ホストの OAuth ペア登録

`backend` で次のコマンドを実行すると、開発用のペア登録 CLI をビルドできます。

```sh
go build -tags tether_development \
  -o tether-dev-host-pair ./cmd/tether-host-pair
```

GUI登録に使うgateway、issuer、public client IDは `pair_config.json` の固定値です。repository内の同ファイルは3値とも空のため、このコマンドで直接buildしたhelperの引数なし起動は `unavailable` を返して何も行いません。Mac候補・署名release builderはstage内のJSONだけを設定値で置き換えてbuildします。開発タグでは認証基盤に `http://127.0.0.1:51847/callback` を正確に登録します。このportが使用中ならOAuth browserを開く前に失敗します。

引数なしで起動すると [ホストのペア登録](host-pairing.md) の二段階GUI登録を行います。新しい鍵はKeychainへ保存し、native dialogとgatewayの確認画面を経てからKeychain形式のpolicyへ切り替えます。ペア登録後は開発用常駐を起動し直してください。既存の登録枠を置き換える場合、native dialogとbrowser画面の両方に置換対象を表示します。

既存CLIを使う場合は従来どおり `--gateway`、`--issuer`、`--client-id` の3引数を渡します。このCLI経路はPEM形式の鍵とpolicyを出力します。GUI用の固定JSONはCLIの引数値では上書きできません。

常駐 policy は従来の `host_key_path` による PEM 参照と、
`host_key_store: "keychain"` による Keychain 参照を相互排他で受け付けます。
後者は `host_key_path` を持たず、`host_key_fingerprint` を Keychain account として使います。
形式を混在させた policy や Keychain 読込み失敗時に PEM へ切り替える動作はありません。
引数なしのGUI helperがKeychain形式のpolicyを書き、3引数CLIは従来のPEM形式を書きます。
署名済み製品の Keychain entitlement 手順と未照合範囲は
[Mac署名release手順](mac-signed-release-build.md#keychain-access-group-の署名手順未照合)を参照してください。

認証基盤には `http://127.0.0.1:51847/callback` を正確に登録します。
このポートが使用中なら、ログイン画面を開く前に失敗します。
ChatGPT の MCP クライアントとは別の public client と audience を使用し、
認証基盤でその audience を発行できるクライアントを開発用の1件に限定します。
期限付きの開発用 gateway にその組み合わせを設定し、
[開発環境の Worker のペア登録口](../deploy/cloudflare/native/README.md#常駐制御の明示的な中継候補)を有効にします。
このビルドとローカル試験で OAuth クライアントの登録、gateway の配備、実接続は行いません。

## 常駐の操作

`foreground` は既存の専用 stdin/stdout pipe 契約を使います。TTY や通常ファイル
は対象外です。`status`、fresh revision に対する `on` / `extend` / `off`、`close`
を扱い、ON するまでは接続しません。実 host/principal 認証、固定 operation、generation、
replay 拒否、元 owner の回収は既存実装を使います。operation は既存の
`TETHER_MCP_GUI_BEGIN` 出力、5秒待機、`TETHER_MCP_GUI_END` 出力だけで、
ON だけでは command/path/env を client が指定する入口はありません。

`on <request_id> <expected_revision> <hours>` は、待機を選んだ時間だけ ON にします
（[#399](https://github.com/O2-CONNECTIVE/Tether/issues/399)）。`<hours>` は
`2`・`4`・`8`・`12`・`24` のどれかで、それ以外の値や綴り（`02`、`3`、時間なし）は
protocol error です。`extend <request_id> <expected_revision> <hours>` は、待機中の
期限をその時点から選んだ時間に置き換えます。製品 JSON の `standbyHours` は
`2`・`4`・`8`・`12`・`24`のどれかです。`on` は省略でき、省略時は既定の2時間です。
製品 JSON の `extend` は `standbyHours` が必須で、
`off` には付けられません。`allow-run` / `deny-run` は製品 JSON では受けません。期限は経過時間と
壁時計の早い方で切れ、スリープ中も進みます。期限が来ると、待機 OFF と同じく受付を
閉じ、所有作業を止めて回収します。延長は待機の状態遷移ではないので `transition` には
出ません。`status.snapshot` は、待機が終わる時刻を今の壁時計で
表した `standbyUntil`（Unix ミリ秒、受付が閉じていれば `null`。壁時計を戻すと、経過時間で
切れる早い方の時刻に変わります）、期限の10分前からの `standbyEndingSoon`、期限で
止まったことを示す `standbyExpired`（次の ON で `false`。回収が unknown になっても残ります）を
持ちます。foreground と製品 JSON の `status.snapshot` はこれらの期限の鍵に加え、
Go が観測したスリープ防止の状態 `sleepPrevention`（`inactive` / `active` / `unknown`）を含みます。
取得・解除中や解除の結果が未確認の場合は `unknown` です。
`previousCleanup` は前回の常駐 owner の回収が確認できたかを示し、`unknown` は Mac の再起動まで残ります。アプリの再起動や今回の正常終了では解消しません。

所有作業の受付中または Owner が作業を保持している間、macOS のシステム idle sleep を防ぎます。
回収確認後に assertion を解放します。待機 ON だけでは取得せず、`AllowSleepDuringWork: true`
なら取得しません。待機 OFF・期限切れ・Close は先に受付と source を閉じ、既存の回収予算後に
Unknown の結果にかかわらず解放を予約します。待機 ON 中は回収が Unknown でも Owner が作業を
保持している間は assertion を保ちます。
解放 API の失敗は回収結果に影響せず診断へ記録し、desired state が「保持しない」の間、1秒から30秒の指数バックオフで再試行します。
取得は最大5秒、作業期限の5秒前の早い方で待ち、残りが5秒以下なら取得しません。
取得 API の操作名と戻り値は標準エラーに記録されますが、署名済み GUI 起動では子標準エラーが破棄されるため製品から参照できません。
その作業は `work_unconfigured` で拒否し、process を開始せず、回収確認済みとして次の作業を受け付けます。
待機 ON や `allow` だけでは防止を開始しません。
画面の idle sleep と蓋を閉じたときのスリープは防ぎません。Go の `StandbyConfig` は
ゼロ値で防止 ON、`AllowSleepDuringWork: true` で OFF です。設定の保存先と GUI の
入力はまだ接続していません。powerd 再起動などで OS が assertion を失った場合の検出・復旧は扱わず、プロセス終了時は macOS が assertion を解放します。

`allow <request_id> <expected_revision>` は、署名付きの汎用コマンド
（[run-command-design](run-command-design.md)）を現在の待機が OFF になるまで
通します。許可自身の期限はなく、待機が期限で OFF になれば一緒に終わります。
`deny` は取り消します。許可は待機の状態遷移ではないので `status` には出ず、
結果は receipt だけで返ります。
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

起動から15分で終了を要求する寿命は、待機の期限に置き換えました
（[#399](https://github.com/O2-CONNECTIVE/Tether/issues/399)）。process は自分の
寿命では終了せず、待機の期限は待機を OFF にするだけです。signal・close・入力断は
元 owner の Close を要求します。終了要求の時刻と実際の cleanup 完了は異なり、
unknown は既存実装どおり retained になります。
ローカルの期限を公開アクセスの厳密な絶対期限の代わりにはしません。実試験には
別途固定した server-side bounded pilot の期限・同じ対象の停止確認が必要です。
LaunchAgent、ログイン起動、自動更新・再起動はありません。

試験運転の gateway の15分（`pilotMaxLifetime`、[agent CLI](agent-cli.md)）は据え置きます。

source 検証の入口（実 enrollment/接続は開始しない）:

```sh
cd backend
go test -tags tether_development ./cmd/tether-host-pair ./cmd/tether-dev-resident ./internal/residentmac ./internal/residentpipe
go vet -tags tether_development ./cmd/tether-host-pair ./cmd/tether-dev-resident ./internal/residentmac ./internal/residentpipe
```

Darwin/cgo の実起動、ChatGPT の登録・同意、認証済み standby、5分を超える再接続、
固定 command の実結果、取消/OFF と全 owner 回収は別受入です。unit fixture や
通常 build の成功では完了扱いしません。build tag は利用者の同意を代替しません。
