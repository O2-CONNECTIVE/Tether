# 固定作業を同じ接続で操作する CLI

`tether-work` は、既存の認証済み HTTP MCP サービスに固定作業の操作を渡す前景 client です。
`residentclient` が wire 契約、同じ session の所有、応答の厳密検査を担当し、CLI は
固定入力をその port へ変換します。host の `Standby`、grant 発行、認証、MCP tool schema は変更しません。

**通常の `tether-work foreground` は `WORK_SOURCE_UNAVAILABLE`、終了値2で拒否します。**
実 credential の入力・保管・更新方法と production trust は未決です。token、endpoint、CA、
file、環境変数による解除はなく、通常入口は stdin を読みません。`--help` は使用できます。
HTTP client の構成関数は非公開です。現在の実接続は、review 済み compiler overlay が
匿名 fixture の endpoint / token / CA を専用 FD3 から渡す hosted Mac CI だけです。
FD3 や FIFO は認証証明ではなく、この fixture は本番の credential 入力方式ではありません。

## 操作と境界

専用 stdin / stdout pipe を保持する呼出元が、ready JSONL を読んでから LF 行を送ります。
対話 terminal / TTY ではありません。以下は fixture と将来の trusted composition 用契約です。
通常 binary を接続可能にする手順ではありません。

```text
start <request_id>
result <request_id>
cancel <request_id>
release <request_id>
close
```

- `request_id` は既存 fixed-work の32桁小文字16進 idempotency key。CLI が生成しません。
  JSON-RPC の連番、host ON/OFF の request ID / revision とは別です。
- `start` → `rescue_fixed_start`、`result` → `rescue_fixed_status`、
  `cancel` → `rescue_fixed_cancel`、`release` → `rescue_fixed_release`。
  shell、command、profile、host、scope を選ぶ引数はありません。
- ID の再送可否と保持 ledger は既存サービスが判断します。CLI は自動再送しません。
  一度の initialize / initialized で得た元 SID を全操作で使います。認証主体・host・
  control 世代の束縛を client 側で変更せず、token refresh や自動再接続もしません。
- result JSONL は操作と ID、検査済み value を含みます。受付、phase、terminal、exit の
  known / code、stop、command / owner / gateway cleanup を別々に保ちます。
  `pending` や cancel の受付を終了・成功・回収へ読み替えません。
- 出力 chunk は既存の bounded base64 のままです。表示・保存先が解釈する時も未信頼データです。
  任意の HTTP / MCP error text、server instructions、redirect先や未知 field は表示しません。
- `release` は既存サービスの確認済み回収 gate を通る結果解放だけです。作業取消ではありません。
  解放 tombstone は同じ session で照会できます。
- `close` / EOF / 入力不正 / I/O不確実時は元 SID を一度だけ DELETE します。
  close の後の同chunk / 次chunk入力を処理しません。closed frame の session が confirmed
  でも work_cleanup は常に `not_observed`。作業取消、結果解放、host OFF、native 回収ではありません。
  session DELETE が未確認なら `unknown` と非0終了です。実 EOF と元child Waitも別に観測します。

## 上限と不確実性

入力は末尾LFを含め128 bytes、単一space、固定語とIDだけです。CRLF、余分な空白、
タブ、NUL、部分行EOF、未知語、過大行は拒否します。read は1分、write / close は5秒、
HTTP request は最大35秒です。CI fixture の全体寿命は5分です。
一つの HTTP 応答は512 KiB、JSON深度・重複field・型・correlation・固定schemaを検査します。
textとstructuredContentの内容一致も確認し、exit未知なのにcodeがある等の矛盾を拒否します。

HTTPは明示CAの検証済みHTTPSのみ、proxy環境変数不使用、redirect禁止です。
不正応答や通信失敗の後は以降のtool callを拒否し、元SIDのCloseだけを残します。
不正initialize応答でも検証済み接続から有効な元SIDを受け取れた場合は保持してCloseします。
応答を受け取れない場合、server側受付・session終了・作業回収は不明です。
clientの終了、transport close、期限切れをremote作業停止の証明にはしません。

## 検証範囲

- 通常Go試験: gate拒否、固定行・出力schema、EOF / partial write / cancellation、
  匿名HTTPS fixtureで同一SID・correlation・redirect禁止・poison・元SID Closeを検査します。
- producer CI のJSON lanesは既存HTTP fixtureを維持します。CLI lanesは通常とraceで、
  実 `tether-host` と実 `tether-work` を別processとして起動し、同じMCP sessionから
  自然終了、出力、取消、回収確認、release / tombstone、session close / EOF / 元Waitを確認します。
  その後でhost OFF / close / 元Waitを確認します。どちらかが不明なら元ownerを保持し、
  そのrunnerで再実行・再取得・cleanupの代替をしません。
- overlayは2つの通常CLIのcomposition leafだけを置換します。work parser / client / schema
  とhost lifecycleは製品sourceのままです。入口source pinが変われば再reviewを要求します。

source追加と試験定義は実行成功ではありません。固定headのCI結果を別に確認してください。
ローカルcloudにはGo compilerがなく、実行済みred / greenやnative成功を主張しません。
新しい実credentialの扱い、本番認証・publisher / enrollment trust、署名配布、GUI、実dot接続は
この変更の受入に含みません。host準備の制約は[前景host CLI](foreground-host-cli.md)、
実producerの所有境界は[CI手順](producer-connection-ci.md)を参照してください。
