# 共通 owner を使う前景 host CLI

`backend/cmd/tether-host` は GUI を使わず、同じ `residentmac.Standby` に
待機制御を渡す薄い入口です。対話 terminal / TTY ではなく、専用の匿名 stdin /
stdout pipe で駆動します。FIFO であること自体は認証や起動許可の証明ではありません。

**通常の `tether-host foreground` は `RESIDENT_SOURCE_UNAVAILABLE` と終了値2で拒否します。**
sealed enrollment、publisher / image trust は未構成です。鍵・policy・CA・同意・
信頼根を指定して解除する flag や環境変数はありません。引数検査と拒否は stdin を
読まず、stdout に接続済みや OFF の表示を作りません。`tether-host --help` だけは利用できます。

実入力から同じ owner までの接続は、[hosted Mac CI](producer-connection-ci.md) の
明示 fixture で検証します。通常製品の認証、署名済み配布、実 dot、GUI の受入ではありません。
検証成功の判断には変更後の固定headとCI結果が必要です。この文書は実行結果ではありません。

## 操作の分担

- host CLI: `status`、`on`、`off`、`close`。待機の観測と切替、元ownerの終了。
- MCP: 固定作業の実行、同じ作業の結果・出力照会、取消、確認済み結果の解放。
- 認可: 既存の認証主体・登録host・control世代、有限の作業許可を維持。ONや接続成功が
  任意shellの許可になったり、CLIが別のgrantを発行したりすることはありません。

今回、CLIの `run` / `result` / `cancel` コマンドは提供しません。
GUI用JSONL入口とCLI入力は `residentpipe` の同じread / mapper / serve / Closeへ入り、
`Standby` の意味や公開APIは変えません。独立した停止loopや別ownerは作りません。

## 入力と観測

以下はfixture / 将来のtrusted compositionのプロトコルです。通常binaryを起動可能にする手順ではありません。
呼出元は両専用pipeを保持し、stdoutを継続して読み、必要な操作だけをstdinへ送ります。
全行はASCIIの固定語・単一space・10進整数・末尾LFです。CRLF、余分な語、空白、
符号、先頭0、小数、未知コマンド、4096 bytes超の行は拒否します。

```text
status
on <request_id> <expected_revision>
off <request_id> <expected_revision>
close
```

1. 起動後の `status` JSONLを読む。`sourceContinuousMs` は同じMacのsleepを含む時計、
   `snapshot.validForMs` は3000ms。古いstatusを現在の接続や受付可否として使わない。
2. `status` 行は同じmapperの新しいstatusを要求するだけで、grant・期限・所有を変えない。
   定期heartbeatもあるためstatusとコマンドの1対1応答を仮定しない。
3. ON/OFFにはそのstatusの `snapshot.revision` と、増加する `request_id` を渡す。
   IDは1〜9007199254740991、revisionは0〜同上限。0や古いrevisionは受付成功ではない。
   IDの重複、古いrevision、状態変更、期限切れは既存mapperのstale/rejected判定へ進む。
4. 同じIDの `receipt` がacceptedでも完了ではない。`status.snapshot.transition` の
   同じID / intent / appliedと、対応するstandby・registration・cleanupを確認する。
   staleを新しいIDで盲目的に再送せず、最新statusと既存作業を確認する。
5. OFFは新規受付を閉じ、所有作業の停止と回収を待つ。`close` はadapterを永久終了する
   要求で、同じownerのCloseを1回だけ呼ぶ。close以降の同chunk / 次chunkは実行しない。
   OFF確認、close受付、closed-confirmed、実pipe EOF、元child Waitは別の事象。

stdoutは既存の `status` / `receipt` / `closed` JSONL契約のままです。
JSONL入口でCLI行を受けたり、CLI入口で任意JSONを受けたりはしません。
`close` に別receiptはなく、元ownerの結果に従うclosed frameで報告します。
SIGINT / SIGTERM / SIGHUPも同じ回収経路へ入り、signal起因の終了は非0です。

不正入力、未完成行のEOF、行組立て1秒超、pipe読書き失敗でも同じCloseへ進みます。
書込みは既存の有限・非blocking・単回方式です。stdoutを読まない呼出元のために
無限blockしたり、途中frameを再送したりしません。
元ownerの回収が不明ならunknownを保持し、成功終了・再起動・別ownerへの切替をしません。
75秒はCloseの待機予算であり、応答しないnative操作を強制終了する保証ではありません。

## 検証境界

通常Go試験はparser、help、通常拒否、JSONLとの分離を検査します。Macのcomponent試験は
匿名pipeと合成のidle ownerでstatus・stale / duplicate・close後不実行・EOFを確認し、
ON・接続・commandは起動しません。従来のmapper freshness / unknown試験も維持します。
statusの専用same-process pipe回帰はheartbeatを配信しないため、コマンド自体の
publishを検査します。同じloopのcontext取消→同じownerのClose / EOFも検査します。
OS signalを実送付する試験は今回追加しておらず、NotifyContextの実signal配送は未実証です。

実producer CIは `json` / `cli` × `normal` / `race` の独立Mac26 ARM64 VMです。
CLI laneでは通常のmain・引数処理をそのままcompileし、未構成の `entry.go` だけを
hash固定したcompiler overlayでfixture bootstrapへ置換します。任意path / CAは
通常入口へ追加せず、合成認証のbootstrapはfixture所有の継承pipeだけで渡します。
CLIの実status / ON / OFF / close、既存MCPの固定command・結果・取消、
元ownerのClose・EOF・Wait0を検査します。JSON laneは従来入口の互換検証を継続します。

このfixtureはGitHub-hosted Mac専用で、利用者のMacへ持ち込んで実行しません。
unknown時は元keeper / producer / pipeを保持し、runner破棄は回収成功に数えません。
GoのMac26経路の成功を、macOS27必須のSwift `OwnedPreparationIO` / sealed launchの
成功へ広げません。通常製品入口、実enrollment、publisher / 署名 / 配置は別工程です。
