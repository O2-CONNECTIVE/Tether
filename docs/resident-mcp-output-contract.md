# Resident固定操作のMCP出力契約

[#188](https://github.com/O2-CONNECTIVE/Tether/issues/188) / [#32](https://github.com/O2-CONNECTIVE/Tether/issues/32) に向けた、既存実装の契約明示です。新しいhost catalog、context、認可、常駐寿命の設計採用ではありません。

## 既存経路との区別

[resident adapter](../backend/internal/mcpadapter/resident_http.go) は `tether_fixed_start` / `tether_fixed_status` / `tether_fixed_cancel` を公開し、明示的なrelease対応backendだけ `tether_fixed_release` を追加します。これらの入力は32桁lowercase hexのrequest_idだけです。固定routine以外のcommandや任意hostは受け取りません。`ResidentRunBackend` 対応時は一覧の末尾に `tether_run_command` を追加します。引数は `request_id`、1〜65536 UTF-8 bytesの `command`、作業root内の相対 `cwd`、任意の整数 `timeout_ms`（既定60000、15000〜240000）です。任意hostは受け取りません。型・必須項目・長さ・`timeout_ms` の範囲の不正はinvalid paramsとなりbackendを呼びません。`cwd` の形式不正（絶対パス、`..`、正規化されていないパス、制御文字）とNULを含む `command` はbackendが `invalid_run_scope` で拒否し、作業を作りません。`request_scope_mismatch` は、同じrequest_idに異なる `command`・`cwd`・`timeout_ms` を指定した場合です。結果を解放した後も同じ照合をします。`timeout_ms` は作業全体の期限で、要求を受けた時点から数えます。

一覧対応backendではさらに `tether_fixed_list` を公開します。入力cursor/limitとsummary出力の契約は[所有実行一覧](resident-execution-list.md)を参照してください。以下のwork結果schemaとは別のversion 1一覧schemaです。

保持出力読取に対応するbackendは `tether_fixed_read_output` も公開します。byte cursorと最大4096 decoded bytesによる非ack読取で、[保持出力読取](resident-read-output.md)のversion 1 schemaを使います。既存work結果schemaは変更しません。

### 待機の期限の照会（#412）

待機の状態に対応するbackendは `tether_standby_status` も公開します。操作する側の AI が、待機の期限の前に作業をまとめられるようにするためです（[#399](https://github.com/O2-CONNECTIVE/Tether/issues/399) の決定）。

- 入力は無しです。argumentsが無いか `{}` だけを受け、それ以外はinvalid paramsとしてbackendを呼びません。延長の口はなく、延長はMacの前にいる人の操作（foregroundまたはhost GUI）だけです。
- 出力はversion 1の `standby`（`on` / `stopping`）、`standby_until`（UTCのRFC 3339、ミリ秒。Macが終わりを出さない間はnull）、`remaining_ms` です。`remaining_ms` はgatewayの時計で `standby_until` までを数え、負なら0、`standby` が `on` でなければ0です。
- エラーは `not_read`（要求の取消）、`not_reported`（controlは生きているが、まだ報告していない）、`control_ended`（`reinitialize_required: true`。sessionの世代のcontrolが無い）です。状態照会はcontrolの状態を1回だけ読み、その結果で応答を決めます。
- 値の出どころは、Macがその常駐のcontrolの上で送る `resident_standby` です。wire fieldは `generation`、`revision`、`standby`、`until_unix_ms` の4値に `type` と `signature` を加えます。Macのホスト鍵で、binding（endpoint・host・principal・世代・両nonce・両鍵）に `host-standby` の役割とこの順の4値を加えて署名します。gatewayは今のcontrolの、前に採ったものよりrevisionの大きい値だけを採り、署名の合わない値・形の違う値・古い値は捨てます。捨てても、報告は権限を持たないのでcontrolは切りません。値はcontrolと一緒に消えます。
- `stopping` はcontrolが残っている間だけ届きます。作業が無い場合、期限でcontrolはすぐ閉じるため、AIには `remaining_ms` の0と、その後の `control_ended` として見えます。
- 期限の意味は [#406](https://github.com/O2-CONNECTIVE/Tether/issues/406) のままです。経過時間と壁時計の早い方で切れ、一度切れたら戻りません。Macは自分の観測の値を写して送るだけで、gatewayは値を作りません。
- 返すのは、sessionの主体（`resident_owner`）・その登録済みhost・sessionの世代のcontrolの値だけです。

制約: 残り時間はgatewayの時計で数えるので、Macとgatewayの時計のずれがそのまま乗ります。新しいMacが古いgatewayに繋ぐと、古いgatewayは知らない型としてcontrolを閉じます。gatewayを先に更新してください。

[gateway](../backend/internal/agentgateway/http.go) が認証済みprincipal・明示enrollment・現在のcontrol generationへ束縛した別経路です。旧4本と恒久的unbound discoveryを変更せず、sessionへ後からgrantを付けません。generation切断後の結果保持・再initialize、許可発行、失効の意味も変えません。

## 公開名の統一

常駐の7固定操作は `tether_fixed_start` / `tether_fixed_status` / `tether_fixed_cancel` / `tether_fixed_release` / `tether_fixed_list` / `tether_fixed_read_output` / `tether_fixed_get_execution` です。対応backendではこれに `tether_run_command` が加わります。同じrequest_id・同じ引数は既存作業を観測し、異なる引数は `request_scope_mismatch` で拒否します。結果は `tether_fixed_start` と同じschemaと上限を使います。
各backendの対応能力による公開範囲は従来と同じです。旧 `rescue_fixed_*` 名のaliasは残さず、旧名での呼出しはdispatch前にprotocol errorで拒否します。
clientは保存したtool名を更新してください。実接続では、対象principalのenrollmentと認証済みstandbyのreadyを確認してから新規initializeし、同じMCP sessionで常駐のtool一覧を確認します。
standby未接続かつ有効な単発grant経路もない場合は既存unbound discoveryとなり、単発派遣の旧4本を表示します。そのsessionは後から常駐へ昇格せず、ready後に一覧を再取得するだけでは切り替わりません。
名前変更による再初期化やgateway再起動を、過去の作業の再実行許可にはしません。

この変更は公開名だけです。入力、結果schema、request_idによる同じ作業の照会、主体・host・世代の束縛、期限、unknown、回収、明示releaseとtombstoneは維持します。
単発派遣の `rescue_start` / `rescue_status` / `rescue_output` / `rescue_cancel` とWindows機能は変更しません。serverInfoのname、実行ファイル名、protocol版も変更しません。
単一outcomeとサーバー採番の具体化、汎用の操作辞書、実クライアント接続の受入は別作業です。

## 実際のstructuredContent

各toolのoutputSchemaは、[既存のresult serializer](../backend/internal/agentgateway/resident.go) と [明示release/tombstone](../backend/internal/agentgateway/resident_capacity.go) の形状を記述します。

- **work結果:** request_id、phase、profile、command_id、command_phase、exit、stop、terminal、command_cleanup、owner_cleanup、gateway_cleanup、chunks、next_cursor、errorを必須とします。未知fieldは許可しません。
- **観測前:** profile / command_id / command_phase / command_cleanupは空文字です。stopは既存ゼロ値の `none`、exit.knownはfalseでcodeはありません。観測前を成功・回収済みへ補完しません。
- **命令と所有回収:** phaseはpending / running / finished / unknown。command_phase、terminal、command_cleanup、owner_cleanup、gateway_cleanupは別です。finishedでも実行前に拒否されてcommand未観測の場合があり、terminal=trueを強制しません。
- **明示release:** 元の所有回収・gateway回収がconfirmedかつfinishedの場合だけpayloadを捨て、result_released=true、空chunksのtombstoneを返します。request_id再送時にはstart/status/cancelでも同じtombstoneを返すため、これらのschemaにも含めます。releaseは実行や停止を意味しません。
- **error-only:** 既存のerror fieldだけの結果、busy_or_refused＋owner_cleanup=unknown、control_ended＋owner_cleanup=unknown＋reinitialize_required=trueを区別します。旧4本のmessageを追加しません。work結果のerrorが非空の場合もあり、isErrorは既存のerror判定を維持します。
- **出力:** 既存resident経路はstdout/stderrともbase64です。128 chunks、元bytes合計16384の既存予算を変更しません。schemaのmaxItems=128、単一dataのmaxLength=21848は形状上の上限で、合計decoded bytesやcanonicalな末尾bitsの検証を代替しません。実際のserializerとruntime予算が引き続き必要です。

固定MCP版は [2025-06-18 Tools](https://modelcontextprotocol.io/specification/2025-06-18/server/tools#output-schema)。structuredContentと同じJSONをtextにも返す既存互換を維持します。schemaは呼出許可・grant・回収証拠ではありません。

## 検証と限界

[匿名golden値](../backend/internal/mcpadapter/testdata/resident-output.json) をgatewayの具体serializer / result methodsと比較し、同じ値を公開MCP schemaへ照合します。mockだけから契約を定義しません。pending、実行中、終了、unknown、実行前拒否後release、tombstone再読取り、各errorを分けます。

既存adapter fixtureも実gatewayの形状へ合わせます。release非対応backendの3本構成、textの一致、isError、未知field、不正型、空の観測値、出力上限、release条件を確認します。有限keywordの試験checkerであり、汎用JSON Schema validatorや実client interoperabilityの証明ではありません。host一覧・host_id・閲覧認可・control認証寿命・multi-host・永続journalは未決のままです。
