# Resident固定操作のMCP出力契約

[#188](https://github.com/O2-CONNECTIVE/Tether/issues/188) / [#32](https://github.com/O2-CONNECTIVE/Tether/issues/32) に向けた、既存実装の契約明示です。新しいhost catalog、context、認可、常駐寿命の設計採用ではありません。

## 既存経路との区別

[resident adapter](../backend/internal/mcpadapter/resident_http.go) は `rescue_fixed_start` / `rescue_fixed_status` / `rescue_fixed_cancel` を公開し、明示的なrelease対応backendだけ `rescue_fixed_release` を追加します。これらの入力は32桁lowercase hexのrequest_idだけです。固定routine以外のcommandや任意hostは受け取りません。

一覧対応backendではさらに `rescue_fixed_list` を公開します。入力cursor/limitとsummary出力の契約は[所有実行一覧](resident-execution-list.md)を参照してください。以下のwork結果schemaとは別のversion 1一覧schemaです。

保持出力読取に対応するbackendは `rescue_fixed_read_output` も公開します。byte cursorと最大4096 decoded bytesによる非ack読取で、[保持出力読取](resident-read-output.md)のversion 1 schemaを使います。既存work結果schemaは変更しません。

[gateway](../backend/internal/agentgateway/http.go) が認証済みprincipal・明示enrollment・現在のcontrol generationへ束縛した別経路です。旧4本と恒久的unbound discoveryを変更せず、sessionへ後からgrantを付けません。generation切断後の結果保持・再initialize、許可発行、失効の意味も変えません。

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
