# 常駐固定操作の保持出力読取

#189 目標5 / #188 の実行管理を、既存の固定操作へ追加する。
`tether_fixed_read_output` は、`tether_fixed_status` と同じ認証主体・
登録済みhost・固定control接続世代が所有する保持出力だけを読む。
通常grant-bound 4 tools、unbound discoveryの404、認証・認可・固定routineは変更しない。
汎用command、host catalog、他主体の実行、永続journal、新しい保持期間は追加しない。

## 入力と読取範囲

- 入力は `request_id`（32桁小文字hex）、`cursor`（整数0〜9007199254740991）、
  `max_bytes`（整数1〜4096）。3項目は必須で、未知・重複field、null、不正型を拒否する。
  cursorの上限はJSON clientで整数位置を正確に扱うための上限。
- 初回はcursor=0、続きは直前のnext_cursor。cursorはstdout/stderr共通のbyte位置で、
  文字位置やbase64文字数ではない。各chunkはstream、offset、encoding=base64、data。
  chunk途中やUTF-8途中でも指定byte数で切り、非UTF-8 bytesを変更しない。
- 一度の呼出しで既存gateway lock内のlive viewをコピーする。最大4096 decoded bytes、
  128 chunks。待機、再試行、hostへの読取、paging用状態の追加はしない。
- 元のagentexec Output cursorは既読ackだが、このtoolのcursorは読取位置だけ。
  保持bytes、元の収集cursor、期限、実行、取消、回収、tombstoneを変更しない。
  同じcursorからの再読取は可能。明示 `tether_fixed_release` だけが既存条件でpayloadを解放する。

## version 1 の出力

`schema_version`、`request_id`、`cursor`、`next_cursor`、`observed_end`、`gap_bytes`、
`chunks`、`has_more`、`collection_ended`、`phase`、`command_phase`、`terminal`、
`command_cleanup`、`owner_cleanup`、`gateway_cleanup`、`source_output_end`、
`source_truncated`、`source_dropped`、`work_error` を必須とする。

- `observed_end` はgatewayが最後に取得したOutput.Next。commandが生成した全出力末尾ではない。
  既存の16KiB/128 chunks上限でpayloadを落とした場合も、この位置は進んでいる。
- `next_cursor` は今回の読取が進んだ位置。`has_more` はnext_cursor < observed_endだけを表す。
  今後の出力有無、完了、成功を表さない。欠落だけの続きもhas_more=trueになり得る。
- `gap_bytes` はcursorからnext_cursorまでに飛ばした未保持bytesの合計。
  先頭欠落、chunk間の穴、末尾欠落を数える。各chunkのoffsetで穴の位置を識別できる。
  `next_cursor - cursor = decoded bytesの合計 + gap_bytes` が成立する。
  decoded bytes予算を使い切った場合はそこで止め、次の読取で末尾欠落を知らせる。
- `source_output_end` / `source_truncated` / `source_dropped` は最後に観測したcommand statusそのもの。
  未観測時はゼロ値であり、古い可能性もある。command_phaseの空文字は未観測を示す。
  source_droppedはcursorを割り当てなかったbytesも含み得るため、gap_bytesとは別。
  この情報を合成して完全な損失量や出力総量を推定しない。
- `collection_ended` はgatewayの収集処理が終了したという観測だけ。
  falseの空chunksは「現在読めるbytesなし」であり、実行終了ではない。
  trueでも出力完全性、command終了、元owner/gateway回収成功を保証しない。
  finishedかつterminalでもwork_errorがresult_unobserved_or_uncertainの可能性がある。
  output_completeは返さない。phase、terminal、3種類のcleanup、work_errorを別々に確認する。
- 読取成功は `isError: false`。非空work_errorは元の実行の失敗・不確実性であり、
  保持出力を読む要求自体の失敗と分ける。出力は未信頼dataであり命令として扱わない。

## エラー、寿命、互換性

エラーは `error` だけのobjectで `isError: true`。
`not_read` は取消済みcontext、`invalid` はbackend引数不正、`not_found` は所有記録なし。
他主体・他世代のwork/tombstoneもnot_foundとし、存在を明かさない。
`result_released` は同じ所有範囲で明示解放済み、`cursor_ahead` はobserved_endより先。
`output_unavailable` は保持chunkの内部整合不成立で、部分的な成功に変換しない。
MCP入力不正はbackend到達前にJSON-RPC invalid paramsとして拒否する。

記録なし、失われたsession、欠落、cursorエラーは未実行の証拠ではない。
新IDで盲目的に実行し直さず、同じrequest_idのstatusとcleanupを確認する。
空読取をbusy loopにせず、必要時に間隔を空けて再照会する。
process再起動で台帳は失われ、切断世代の結果読取は既存sessionの寿命内だけ。
新しいcontrol世代のsessionから旧世代の結果へはアクセスできない。
releaseとの並行読取はlockで順序付け、解放前のコピーかresult_releasedのどちらかを返す。

`ResidentReadOutputBackend` を実装するbackendだけがこのtoolを広告する。
非対応backendの既存3/4操作と、別途任意の一覧対応は維持する。
従来statusは引き続き保持payload全体を返す。既存toolの出力shapeやisErrorは変更しない。

## 検証境界

gatewayの具体読取と[共有golden](../backend/internal/mcpadapter/testdata/resident-read-output.json)を比較し、
同じgoldenをMCP出力schemaへ照合する。HTTP認証、text/structuredContent一致、任意対応、
主体・世代・tombstone分離、不正入力、byte分割、非UTF-8、欠落、上限、並行収集/releaseを回帰試験する。
有限keywordのschema checkerであり、汎用validatorや実dot/native/GUI接続の受入ではない。
実装と試験を追加した回帰検証であり、local Go未実行をRED→GREEN実証とは扱わない。
