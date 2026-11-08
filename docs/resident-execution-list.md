# 常駐固定操作の所有実行一覧

#189 の目標5 / #188 の実行管理を、既存の固定操作に小さく追加する。
`rescue_fixed_list` は `rescue_fixed_status` と同じ認証主体・登録済みhost・
control接続世代に属する保持中の実行を列挙する。新しい実行許可を作らない。
汎用の `tether_list_executions`、multi-host routing、grant不要のhost catalogは未採用のまま。
既存grant-bound 4 toolsとunbound discoveryの404契約は変更しない。

## 利用契約

- 入力は `cursor`（初回は空文字、以後は直前の `next_cursor`）と `limit`（整数1〜32）。
  cursorは空文字または32桁小文字hex。未知field、null、重複field、過大limitを拒否する。
- 出力は `schema_version: 1`、`executions`、`next_cursor`。空のnext_cursorは続きなし。
- 各summaryは `request_id`、`phase`、`terminal`、`command_cleanup`、
  `owner_cleanup`、`gateway_cleanup`、`error`、`result_released`。
  作業の停止・終了と各所有者の回収をまとめない。unknownを成功にしない。
- 読取成功はMCP `isError: false`。一覧内の作業errorはその作業の結果で、一覧要求の失敗ではない。
  キャンセル済みcontextは `not_read`、不正なbackend引数は `invalid`。
  MCP引数不正は既存のJSON-RPC invalid paramsとしてbackend到達前に拒否する。
- `request_id`で既存status/cancel/releaseへ進む。command出力・host label・鍵・endpoint・
  他主体・他host・他世代の記録は列挙しない。任意OS process一覧でもない。
- 結果payload解放済みのtombstoneも表示する。解放はpayloadの明示ackであり、
  一覧はpayloadを解放・取得・ackしない。保持上限・保存期間は変更しない。

## ページングと寿命

request_idの辞書順によるkeysetページング。サーバー側snapshotや新しい保持資源は作らない。
1要求中は既存gateway lock内で同じ観測を作り、最大32 summaryだけ返す。
結果→tombstone移行でもrequest_idは変わらない。cursorは権限ではなく並びの位置にすぎない。
同じ値を別sessionへ送っても、そのsession自身の主体・host・世代だけを参照する。

ページ間に新たな小さいIDが追加されると、古いcursorの続きには現れない。
新しい作業も確認する場合は空cursorから再取得する。並行変化を固定snapshotと表現しない。
一覧が空・IDが見つからないことは未実行の証拠ではなく、新IDでの盲目的再実行を正当化しない。
process再起動でメモリー上の記録は失われる。永続journalや再起動後の照会保証は追加しない。

既存resident sessionが切断世代の結果を読める範囲はstatusと同じ。
新しい接続世代から旧世代の記録を参照せず、認証の有効期限やcontrol寿命を延長しない。
MCP session自身の期限・HTTP毎の認証は従来どおり。

## 互換性・検証境界

`ResidentListBackend` を提供するbackendだけでtoolを広告し、未対応backendは既存3/4操作のまま。
通常のgateway resident backendが提供する。新しいhost登録、操作context、認可scopeを追加しない。

source試験は主体/世代分離、32件境界、ページング、tombstone、snapshotの非共有、
payload非公開、不正入力、HTTP認証、structuredContentとtextの一致、input/output schemaを検証する。
既存resident/standard/discoveryの回帰も維持する。実dot・実native・GUI・遠隔接続の受入ではない。
