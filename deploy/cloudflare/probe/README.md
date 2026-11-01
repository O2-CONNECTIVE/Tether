# Tether: commandless route probe

[配置準備へ戻る](../README.md) / [Linux container候補](../linux-container.md) / 実演準備・操作承認票（過去の参照資料・本文非同梱）

`workers.dev` Worker → 固定VPC Service → 既存Tunnel → 分離Linux container という経路だけを、command・鍵・Mac接続なしで確かめるためのecho probeです。
現在の実証範囲・入口閉鎖状態は[検証状態](../../../docs/tether-pilot-status.md)を正本とします。この文書の手順は新しい公開・認証設定の承認ではありません。
このprobeの成功は、実Macのcommand実行、native端末のWSS認証、製品接続gateの解除を意味しません。

## 構成と呼出し

| 部品 | 場所 | 役割 |
| --- | --- | --- |
| Go probe | [`backend/cmd/tether-echo-probe`](../../../backend/cmd/tether-echo-probe/) | Access assertionを検証し、許可subjectだけにechoを返す |
| Worker | [`worker.mjs`](worker.mjs) | 既定で全拒否。gate開放時も固定2経路だけを1つのVPC Service bindingへ転送する。認証はしない |
| Worker試験 | [`worker.test.mjs`](worker.test.mjs) | `node --test deploy/cloudflare/probe/`（fake binding/WebSocket/timerのみ） |
| 設定sample | [`wrangler.sample.jsonc`](wrangler.sample.jsonc) / [`probe-config.sample.json`](probe-config.sample.json) | 合成・未設定・配置不能。そのままでは両方とも拒否される |

Go probeの呼出しは `tether-echo-probe -config <path>`（echo mode）と `tether-echo-probe --deny-all-probe`（到達確認mode）の2つだけです。

到達確認modeは、ちょうど1個の引数 `--deny-all-probe` だけで選ばれます。文書・trust・鍵・verifierを読まず、環境変数や待受引数も受けず、固定の `tcp4 0.0.0.0:8787` で待受し、全method・全path・upgradeへ同じ固定403を返します。本文・header値・pathを読まず、echoもlogもしません。listen前から数えて最大10分で終了します（停止処理は既存の有限shutdownを再利用）。認証modeへ切り替わる経路はありません。nonrootの専用分離containerでhostの `127.0.0.1` だけへ公開する前提の非command probeであり、製品認証の迂回ではありません。

echo modeの手順：

1. `agentkeys.ReadTrustConfig` で保護済みの公開trust文書を期限付きで読みます（実鍵と同じowner/ACL検査）。
2. 既存の `agentgateway.ParseConfig` で厳密に解析し、`auth_mode` が `cloudflare_access`、`resource` のpathが `/tether-probe/v1/echo`、`allowed_origins` なし、principal 1〜4件の場合だけ受理します。OAuth bearer文書や実gatewayのMCP文書（`/mcp`）は拒否します。
3. 待受addressは既存の `loopback`（既定）/`container_private` 検証と `ListenNetwork()` に従います。
4. 文書が不足・不正なら待受前に終了コード2で止まります。

署名鍵の読込み、agent channel、grant、process起動、file書込み、command APIはありません。JWT検証は既存の `agentgateway.AccessVerifier` だけを使い、新しい実装は追加していません。

| 経路 | method | 条件 | 応答 |
| --- | --- | --- | --- |
| `/tether-probe/v1/echo` | POST | `Content-Type: application/octet-stream`、本文1024 byte以下 | `{"probe":"tether-echo-probe/v1","command_access":false,"note":...,"echo_bytes":n,"echo_base64":...}` |
| `/tether-probe/v1/ws` | GET upgrade | `Sec-WebSocket-Protocol: tether-probe.v1` の1件だけ | text/binaryを保ったecho。1 message 1024 byte、1接続32 message、upgradeから30秒で終了（延長なし） |

共通：完全一致のpathのみ（prefix・末尾slash・percent-encoded path・queryは拒否）、`Origin` 付きは403、`Cf-Access-Jwt-Assertion` はちょうど1件で、既存verifierの署名・issuer・AUD・期限・user token検査を通り、検証済み `sub` が許可listにあるときだけ受理します。emailやdomainは認可に使いません。同時実行はHTTP 8件・WebSocket 4件で、待たずに503を返します。token・claim・header・本文はlogに出しません。
upgrade前に `http.ResponseController` で接続のread/write deadlineを固定lifetimeの終了時刻へ設定し、設定できなければ500で拒否します（HTTP serverの5秒timeoutがhijack後に残ってWebSocketを途中で切ることを防ぐ）。

Workerはgate開放時も `Origin` 付き要求を転送前に403で拒否します（Go側の拒否を黙って外さない）。client本文の読取りは5秒、upstreamは10秒の期限付きで、期限切れではreaderを実際にcancelします。bindingがabortを無視して期限後に応答した場合、HTTP応答の本文はcancelし、遅れたWebSocketはacceptしてすぐcloseし、relayしません。

## 試験が確認する範囲

Go（`package main`、fake reader/listenerと `net.Pipe` だけ。socket・file・実鍵・processは使わない。RSA鍵は試験内でmemory生成）：

- `TestProbeConfigAcceptsOnlyAccessProbeDocument`：Access以外・MCP resource・origin・5件以上・email・空値・listener規則違反の拒否
- `TestHTTPEchoAuthentication`：合成assertionの受理、未認証・未設定鍵・改ざん・許可外subject（署名は有効）・email置換・期限切れ・未来nbf・他issuer/AUD・service token・重複・cookie/bearer/identity headerだけ・過大の拒否
- `TestHTTPEchoRequestShape` / `TestHTTPEchoConcurrencyBound`
- `TestWebSocketEchoPreservesTypeAndSubprotocol` / `TestWebSocketOutlivesServerTimeouts`（serverのread/write timeoutを100msへ短縮し、400ms後のechoが成功する）/ `TestWebSocketRefusesBeforeUpgrade` / `TestWebSocketMessageTooBigCloses` / `TestWebSocketFrameBudgetCloses` / `TestWebSocketIdleLifetimeEnds` / `TestWebSocketLifetimeIsNotExtended` / `TestWebSocketConcurrencyBound` / `TestCloseEndsWebSockets`
- `TestExecuteRefusesBeforeListen` / `TestExecuteListenFailure` / `TestExecuteServesProbeAndShutsDown`
- `TestDenyAllArgumentIsExact` / `TestDenyAllRefusesEverythingWithoutReading` / `TestDenyAllModeListensFixedAndEnds`（到達確認mode。lifetimeは試験だけ短縮でき、延長は拒否）

Node（fakeのみ。global `fetch` は呼ばれたら失敗する罠に置換）：gate未設定・不一致・binding欠落の全拒否、閉じたgateでupgrade・任意pathも本文を読まずtimer/socket/転送0、`Origin` 付き要求の転送前拒否、固定path/methodだけの転送、assertion欠落/重複の早期拒否と「存在だけでは認証しない」こと、固定URL・転送header・`redirect: "manual"`、本文/応答の上限、redirect拒否、upstream timeout、client本文の停滞timeoutとreader cancel、期限後に届いたHTTP応答のcancelとWebSocketのclose（deferred fake binding）、応答本文の停滞cancel、binding例外・stream失敗でtimer/readerが残らないこと、WebSocketのtext/binary/subprotocol保持と1回だけのclose、lifetime timer、過大message・frame上限・error、close codeの正規化、sampleが閉じていること、Blob既定に対して両端binaryTypeをaccept前に明示する回帰。

2026-10-31 の最終 probe 検証：Go normal/race 各22 top-level＋66 subtest、vet、Linux static build が成功。Worker Node 19/19 が成功し、binaryType の Blob 既定回帰は修正前失敗→修正後成功を確認しました。独立 review は限定修正に安全性欠陥なしと判定しました。

別途、明示承認を受けた1名の実 Access 認証で、Workers→VPC Service→既存 Tunnel→非root専用Linux container の HTTP 短文 echo と WSS text/binary echo が成功しました。試験後 Worker gate は再閉鎖しています。初回は root 直下 config 配置を loader が拒否したため専用親directoryへ修正し、binary は Workers の既定 Blob と不一致だったため両端 binaryType を明示しました。保護・認証条件は弱めていません。実 identity・AUD・設定・token はこの公開資料に含めません。

この成功は commandless transport の到達・認証だけを示します。dot MCP 接続、Mac command、4名全員の登録、Managed OAuth、失効・command cleanup の実演が成功したことを意味しません。

## 公開・認証のgate

Worker gate（`TETHER_PROBE_GATE`）を `worker.mjs` の `GATE_VALUE` へ変えるのは、次がすべて揃い、承認された後だけです。

1. Go probeが保護文書（実issuer・AUD・現行公開鍵・subject 1件）で、container内の承認済みaddressだけで待受している。
2. **hostname/path単位のAccess self-hosted application** が、Workerの公開hostnameの `/tether-probe/v1/echo` と `/tether-probe/v1/ws` を許可subjectだけに限定している。Worker設定画面のWorker-level Accessは使いません（WebSocketが403になる既知の制約）。
3. VPC Serviceが既存Tunnel経由でprobeのcontainer portだけを指し、Workerのbindingはその1件だけ。

Workerはassertionを検証しません。header付き要求を転送するだけで、受理はGo側の署名検証とsubject許可に依存します。Accessの適用漏れがあっても偽造assertionはGo側で拒否されますが、有効なassertionの盗用は期限内に限りprobeへ届きます（probeはechoしか返しません）。

## 実Mac接続の認証との違い

このprobeはAccessで保護された経路が往復できるかだけを見ます。実Mac側の登録は別経路で、`agenttransport` のWSSでhostの登録鍵によるchallenge署名（host proof）、`agentchannel` の相互認証、受信者承認済みgrantを要求します。Access保護されたprobe WebSocketはその代替にならず、probeの成功をhost proof・登録・command実行の成功へ読み替えません。

## 利用上の制約

Access公開鍵は保護文書で明示し、rotation時に現行・次の鍵を更新します。probeには公開鍵の自動取得はありません。
probeはagentgateway経由でagentexec/agentchannel/mcpadapterを推移的にlinkしますが、command APIを呼びません。
新しい環境でのAccess適用、HTTP/WSSのassertion配送、VPC bindingとconnector経路はその環境で確認が必要です。既存の限定成功を他環境へ転用しません。

補足: deny-all モードは解析済みの要求（OPTIONS * を含む）を固定403で拒否します。不正HTTPは net/http 自身が400等で拒否し、10分の期限後には最大約10秒の bounded shutdown が加わります。

The authenticated commandless probe has a fixed 10-minute lifetime beginning before the protected configuration read, followed by bounded owned HTTP/WebSocket shutdown. It cannot be extended by configuration or CLI arguments. This is a temporary route probe, not the command gateway. Scheduling/suspend delays and shutdown time must be measured separately; this does not claim a strict wall-clock kill guarantee.

一時試験 container は必ず `--restart=no`。期限後の自動再起動・自動再登録は禁止です。停止済み資産は証跡として保持でき、削除は別の明示承認時だけ行います。再起動は新しい試験の承認を必要とします。認証 mode の有限寿命、無要求時の終了、設定読取中の期限到達と listen 拒否を fake 試験で確認します。

WebSocket relay は両端の `binaryType = "arraybuffer"` を accept 前に明示します。Workers は compatibility date 2026-03-17 以降 Blob が既定のためです。サイズ上限・frame 上限・認証は変えません。公式: https://developers.cloudflare.com/changelog/post/2026-04-21-websocket-standard-binary-type/ 。fake の Blob 既定からの回帰試験を追加しています。
