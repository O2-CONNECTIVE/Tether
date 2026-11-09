# 固定作業のメタデータ照会

[操作辞書案 #188](https://github.com/O2-CONNECTIVE/Tether/issues/188) / [段階目標 #189](https://github.com/O2-CONNECTIVE/Tether/issues/189) / [保持出力の読取](resident-read-output.md)

`rescue_fixed_get_execution` は、既存の常駐固定作業について出力本文を取得せずに状態を確認する追加の読取操作です。
汎用の `tether_get_execution` や新しい操作contextを実装したものではありません。
同じ認証主体・登録host・固定されたcontrol世代の保持作業と、明示release後の既存tombstoneだけを返します。

内部のtypedな読取portと認可・adapterの分担は[共通application API](shared-application-api.md)を参照してください。
この切出しで公開JSONや利用者の権限は変えません。

## 入出力と互換性

入力は必須の `request_id` 一つだけで、32桁の小文字16進数です。
未知field、重複field、null、型違い、不正IDはdispatch前に拒否します。
`host_id`、principal、generation、command、承認boolで参照先や権限を選べません。
公開 `inputSchema` と `outputSchema` は閉じたobjectです。

成功応答には次の15 fieldがあります。

| field | 意味 |
| --- | --- |
| `schema_version` | この追加応答の版。現在は1 |
| `request_id` | 保持作業の元の相関ID |
| `phase` | gateway作業の `pending / running / finished / unknown` |
| `profile`, `command_id`, `command_phase` | 最後に観測したcommandの状態。未観測なら空文字 |
| `exit` | `known: false` だけ、または `known: true` と符号付き32bit整数の `code` |
| `stop`, `terminal`, `command_cleanup` | commandの停止理由・終端・回収を別々に表す |
| `owner_cleanup`, `gateway_cleanup` | 元host ownerとgatewayの回収観測。`pending / confirmed / unknown` |
| `collection_ended` | gatewayの結果収集処理が終了状態へ到達したか |
| `result_released` | 結果payloadを明示release済みか |
| `work_error` | 既存作業の失敗・不確定理由。失敗がなければ空文字 |

この照会の成功は作業の成功ではありません。
`work_error` が空でなくても、メタデータを読めた応答の `isError` はfalseです。
照会失敗は `error` だけを持つobjectで、`not_read / invalid / not_found` のいずれか、`isError` はtrueです。
引数・未対応toolの拒否は従来どおりJSON-RPCのprotocol errorです。
`structuredContent` とtext mirrorは同じ値です。

既存の `rescue_fixed_status` は全保持出力と従来の `error` を返し、作業失敗を `isError: true` にする互換動作を維持します。
この追加で旧statusのschemaや意味を変更しません。
optionalな `ResidentGetExecutionBackend` 実装時だけtoolを公開し、旧3本・release付き4本のbackend、通常4tool、静的discoveryの境界は維持します。

## 観測と回収を混同しない

- commandのterminalや既知exit codeは、host owner・gatewayの回収完了、結果収集の完了を証明しません。
- `collection_ended: true` は収集処理の終了観測であり、全出力を取得できたこと、成功終了、物理停止や回収成功の証明ではありません。`phase: unknown` やcleanup unknownとも併存します。
- live作業では既存の `done` を返します。元collectorのgoroutine join完了は別の条件であり、releaseでは従来どおりjoinも要求します。
- tombstoneでは既存release gateが元collectorのjoin、done、finished、元host owner/gatewayのconfirmed、currentからの離脱を要求するため、`collection_ended: true` と `result_released: true` を返せます。この根拠以外の観測値は補いません。
- admissionがcommand取得前に拒否された結果も、回収確認後にreleaseできます。このtombstoneでcommand ID・phaseが空、exit未知、terminal falseのままなのは正しい状態です。
- commandの観測は古い可能性があります。空field、未知exit、`not_found` は未実行の証拠ではありません。同じ作業を新IDで盲目的に再実行しません。

`chunks`、cursor、`source_*`、出力完全性、stdin対応などを追加しません。
出力bytesは対応する `rescue_fixed_read_output` から読み、必要な結果を保存してから明示releaseします。
この照会は出力を複製・ack・破棄せず、保持量やtombstoneを変えません。
返すobjectとexit objectは独立しており、呼出側の書換えは保持結果へ戻りません。

## 読取境界と検証範囲

gatewayの既存mutex下で同一世代の保持記録だけを読みます。
contextがnil/終了済みなら `not_read`、lock取得後にもcontext終了を再確認します。
別世代・別主体/登録hostの記録は存在を開示せず `not_found` にします。
現在のcontrol世代を照会・更新せず、切断後も元sessionに束縛された保持結果だけを参照します。
host dispatch、新しいgrant、permission更新、control接続、retention延長は行いません。
process再起動で既存のメモリ台帳は失われ、永続journalや再起動後のexactly-onceは保証しません。

source回帰はlive/終了不確定/terminalと収集の分離、元collectorの取得前拒否、明示release前後、外国の世代/主体、取消context、非破壊・detached応答、並行収集/releaseを対象とします。
認証済みHTTPの読取・未認証/別主体の拒否と、旧statusの `isError`・出力互換性も検査します。
gateway実serializerとMCP schemaが同じ匿名goldenを読み、未知field、exit条件、release済みcleanup条件、入力上限を照合します。
試験追加は回帰検証であり、実行済みのred→greenの主張ではありません。

実clientによるtool選択、実dot・native・GUIの接続、実hostの動作、配布の証明は別です。
host catalog、本人承認、許可root、stdin/input、Windows runtimeの未決事項はこの変更で確定しません。
具体的な検証結果はPRの固定head・CI記録を参照し、sourceやCIの成功を製品接続gateの解除へ読み替えません。
