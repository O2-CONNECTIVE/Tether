# Tether: native route Worker（source候補）

[配置準備へ戻る](../README.md) / [commandless probe](../probe/README.md) / 実演準備・操作承認票（過去の参照資料・本文非同梱）

既存Goの `rescue-gateway` へ、固定VPC Service binding 1件だけを通して2経路を中継するWorkerです。4名試行用の最小構成です。
**source候補のみです。** 配置・Worker作成・route/hostname設定・VPC Service作成・binding設定・gate開放・Access application作成・dot側のMCP接続登録は、どれも実行・承認されていません。この文書は承認ではありません。
Workerはgrant・token・cookie・session・command権限を発行しません。経路があってもcommand権限は生じません。権限は受信者の明示承認（grant）とGo側の検証だけで決まります。

## 構成

| 部品 | 場所 | 役割 |
| --- | --- | --- |
| Worker | [`worker.mjs`](worker.mjs) | 既定で全拒否。gate開放時も固定2経路だけを `env.TETHER_NATIVE_VPC` へ転送する。認証しない |
| Worker試験 | [`worker.test.mjs`](worker.test.mjs) | `node --test deploy/cloudflare/native/`（fake binding/WebSocket/timerのみ。global `fetch` は呼ばれたら失敗する罠） |
| WebSocket relay | [`../probe/worker.mjs`](../probe/worker.mjs) の `relay` | 独立review済みの実装をimportして再利用（accept前の `binaryType = "arraybuffer"`、1回だけのclose、1本のlifetime timer）。上限値だけnative用を渡す |
| Go MCP | `backend/internal/agentgateway`（`cloudflare_access` mode） | assertionの署名・issuer・AUD・期限を検証し、検証済み `sub` を設定principalへ対応させる |
| Go 登録 | `backend/internal/agenttransport` | 登録済みhost鍵によるchallenge署名と同じgrantを検証してから `agentchannel` を開く |

wrangler設定・compatibility date・hostnameは用意していません（未承認のため）。

## gateとbinding

- gate変数は `TETHER_NATIVE_GATE`、開放値は `worker.mjs` の `GATE_VALUE`（`open-native-tether-gateway`）。probeの `TETHER_PROBE_GATE` とその値では開きません。
- binding名は `TETHER_NATIVE_VPC` の1件だけ。probeの `TETHER_PROBE_VPC` は読みません。gate開放時にbindingが無ければ503。
- 転送先URLは固定の `http://rescue-gateway.internal` + 固定path（VPC Serviceが固定host/portへ送り、Goは完全一致pathで振り分ける）。要求から転送先を選ぶ経路、global `fetch`、redirect追従はありません。

## 経路

| 経路 | method | Workerの早期拒否 | 転送するheader | Go側の責任 |
| --- | --- | --- | --- | --- |
| `/mcp` | POST / DELETE | assertion欠落・重複（`,` 含む）・8 KiB超は403。値の長さ超過は400。本文は宣言/実測とも512 KiB超で413、10秒で408 | `Cf-Access-Jwt-Assertion`、POSTのみ `Content-Type`・`Accept`、両方で `Mcp-Session-Id`・`Mcp-Protocol-Version` | assertionの暗号検証とsubject対応、media type・version・sessionの検査 |
| `/tether-agent/v1/register` | GET upgrade | `Cookie`・`Authorization`・`Proxy-Authorization`・`Cf-Access-Jwt-Assertion` 付きは403。`Sec-WebSocket-Protocol` が `rescue-agent-register.v1` の1件以外は400 | `Upgrade: websocket` と固定subprotocolだけ | challenge（text ≤2 KiB）、host proof、dispatch検査、`Connect` 後のbinary stream（≤32 KiB）とagent channelの相互認証 |

共通：完全一致のpathのみ（末尾slash・大文字・percent-encoded・probe path・`/.well-known/*` は404）、query（空の `?` を含む）は400、その他のmethod・`Upgrade` 付きMCP・GET stream・CORS preflightは405、`Origin` 付きは403（Go側にもallowlistを設定しない前提。CORS応答は出さない）。
`Authorization`（Access edgeのopaque token）・cookie・email等のidentity header・forwarding headerは転送しません。assertionの有無は早期拒否にだけ使い、認証とはみなしません。

MCP応答は、Goが返すstatus（200/202/204/400/403/404/405/406/413/415/429/500/503）だけを中継します。3xx・401・その他は502にし、`Location`・`Set-Cookie`・`WWW-Authenticate` は出しません。中継するheaderは `Content-Type`（`application/json` または `text/plain; charset=utf-8`）、`Mcp-Session-Id`（32桁hex）、`Mcp-Protocol-Version`、`Retry-After`、`Allow: POST, DELETE` だけで、形が違えば502です。OAuth discoveryはAccess edgeの担当で、Workerはmetadataを作りません。

## 上限（`LIMITS`）

| 項目 | 値 | 根拠 |
| --- | --- | --- |
| assertion | 8192 byte | `agentgateway.MaxTokenBytes` |
| MCP本文 / 応答 | 512 KiB / 512 KiB | `mcpadapter.MaxLine`（tool結果は160 KiB） |
| MCP本文読取 | 10秒 | — |
| MCP upstream | 35秒（応答本文の読取りを含む） | 既定call timeout 30秒 + 余裕 |
| 登録upgrade | 10秒。期限後に届いたupgradeはacceptして即close（relayしない） | `DefaultBudgets().Registration` |
| WebSocket message | 32 KiB | `agenttransport.MaxStreamMessage` |
| frame | 片方向4096件 | 有限の試行用上限 |
| WebSocket lifetime | upgradeから絶対5分。延長・再接続・復帰なし | 初期試行 |

期限切れではreaderを実際にcancelし、bindingがabortを無視して後から応答してもHTTP本文はcancel、WebSocketはcloseします。例外・stream失敗は502で、timer・socket・readerを残しません。logは出しません。

## 試験が確認する範囲（fakeのみ）

gate未設定・不一致・probe gate/binding名での全拒否と閉gate時の本文未読・timer/socket 0、固定path/methodとprobe path・metadata path・query・CORS preflightの拒否、`Origin` 拒否、assertion欠落/重複/過大の早期拒否と「存在だけでは認証しない」こと、Bearer・cookie・identity・forwarding headerを転送しないこと、POST/DELETEそれぞれの転送header完全一致と固定URL・`redirect: "manual"`、header長の上限、本文512 KiB境界と宣言長、本文停滞の10秒timeoutとreader cancel、応答のredirect/401/media type/session形式/過大の拒否と安全なheaderの中継、35秒upstream timeoutと期限後応答・停滞応答のcancel、登録の固定header転送とWorkerが何も送らないこと、登録時のcookie/authorization/assertion/subprotocol拒否、101以外・subprotocol不一致の拒否、10秒upgrade timeoutと期限後upgradeのclose、probe relay再利用（accept前のArrayBuffer、text/binary保持）、32 KiB境界・Blob・frame上限、5分timer 1本と再接続しないこと、close/errorの1回close、sourceのimport・fetch呼出し・logging/CORS/cookie不在。

統合担当がNode試験を実行し、native21件（probeとの合計40件）が成功しました。実Cloudflareへの本体route配置・実command試験は未実施です。

## 未了・承認が必要な事項

各項目は個別承認です。

1. Worker作成と公開hostname（synthetic例：`tether-native.example.invalid`）、gate変数・binding設定。preview URLは無効のまま
2. **Access self-hosted application**：同じhostnameの `/mcp` だけを保護し、許可subjectを4名に限定。`/tether-agent/v1/register` は対象外（人のOAuthの外側）。Managed OAuth/discoveryの実設定は未了
3. VPC Service 1件（既存Tunnel、`rescue-gateway` のportのみ）とbinding 1件
4. Go設定：`auth_mode: cloudflare_access`、`resource` は `https://<hostname>/mcp`、登録endpointは `wss://<hostname>/tether-agent/v1/register`、`allowed_origins` なし、principal・host鍵の登録
5. dot（MCP client）側の実接続登録。未了
6. gate開放と、試行後の手動再閉鎖

## 未確認事項と既知の差

- path単位のAccess applicationで `/mcp` だけを保護し、登録pathを対象外にできるかは未確認です。登録pathにAccessが掛かるとMacはAccess identityを持たないため失敗します（安全側）。
- Access edgeがWorkerへ `Authorization` を残すかは未確認です。Workerはどちらでも転送しません。
- Access外の登録pathで、clientが付けた `Cf-Access-Jwt-Assertion` をedgeが除去するかは未確認です。Workerはその場合403で拒否します。
- relayのclose理由文字列はprobe由来（例：lifetime終了は `4000 "probe lifetime ended"`）です。Goはreasonを読みません。probe fileは変更していません。
- 5分のrelay lifetimeが、agent channelの心拍（10秒）とgrant期限の運用に足りるかは実測していません。lifetime到達で経路は切れ、Mac側はtransport喪失でgrantを失効させ、再接続しません。
- 4096 frame/方向が実際のagent channelの書込み粒度で足りるかは実測していません。
- probeと同じく、VPC Service経由のWebSocket upgrade、`signal` の実対応、Access経由で `Cf-Access-Jwt-Assertion` がMCP POSTに届くことは未試験です。実通信の成功は主張しません。

2026-10-31 coordinator検証：Node 21/21 pass（probeと合わせ40件）。独立reviewのraw path正規化指摘に対し、観測できるraw pathと解析済pathの一致を必須化し、dot-segment回帰をred→greenで確認しました。EdgeがWorkerへ渡す前に既に正規化した部分まで観測できるとは主張しません。実Access path境界は別途検証します。

本体CLIのFileConfigはCallTimeoutを設定しないためGo既定30秒で、Worker35秒以内です。将来このcompositionを変更する場合は両方の期限契約を再検証します。pilotのMac grantは最大5分（初回実演は60秒）に限定。匿名registration4slotの枯渇による可用性リスクは残り、WAF設定等を未承認で追加しません。

## Opt-in bounded pilot entry (source only)

`bounded-pilot.mjs` reuses `createNativeWorker(rt)`; it does not change the default entry or authenticate callers. A deployment must explicitly select this new entry. It adds no route, credentials or grant authority.

In addition to the existing exact native gate and VPC binding, both `TETHER_NATIVE_STARTS` and `TETHER_NATIVE_EXPIRES` must be **13-digit decimal strings** in Unix milliseconds, with end > start and a window no larger than 300000 ms. Missing, malformed, unknown gate, early or expired settings reject. The only forwarded paths remain `/mcp` and `/tether-agent/v1/register`, with the original method/header/protocol/authentication boundaries.

A Worker isolate fixes the first admitted window. Changing that window, observing a backwards/invalid clock, or reaching its expiry timer irreversibly closes that isolate's owned pilot work. Reconfiguration is not renewal. At most 32 active request/relay scopes are retained; an early-closed relay may conservatively occupy its scope until the original expiry.

The absolute expiry timer cancels owned HTTP deadlines and closes both owned relay legs. Native body/upstream/upgrade/relay timers are capped by the remaining window. Checks before and after upstream awaits, before publishing the final response, and on each relay message/send prevent stale responses and queued frames from being forwarded after observed expiry. Late upgrade responses are accepted only to close; late HTTP bodies are canceled, not exposed. A caller-selected URL or global fetch is never introduced.

These are application-level bounds, not a real-time OS kill guarantee: event-loop suspension, provider scheduling and network close propagation can delay cleanup. A separate owned watchdog must close the public gate and stop the exact newly started gateway independently of deployment wait. Never reuse a historical container ID or infer Mac grant cleanup from gateway/Worker closure. Mac authority and same-owner cleanup remain separate, and unknown is not success.

Fake validation (no network, keys, real Worker or deployment):

```sh
node --test deploy/cloudflare/native/bounded-pilot.test.mjs deploy/cloudflare/native/worker.test.mjs
```

The tests inject clocks, timers, bindings and sockets, including expiry during body read/fetch/upgrade, late results, clock rollback, already-open relays, invalid windows and scope exhaustion. Real Cloudflare deployment, timing, OAuth and Mac operation remain separate acceptance steps. No actual STARTS/EXPIRES values or deployment commands are supplied here.

The wrapper pins only the time window, not VPC binding identity. Deployment must independently keep the reviewed binding target fixed. Gate changes are not guaranteed to reach existing sockets in other isolates immediately; the absolute window and independent owned stop remain required.

## 常駐制御の明示的な中継候補

`TETHER_RESIDENT_GATE=open-resident-tether-control` を明示した場合だけ、同じ登録pathで `rescue-agent-resident.v1` も受け付けます。既存のnative gateと固定VPC bindingは引き続き必須です。未設定・未知値では従来の `rescue-agent-register.v1` だけを受け付けます。複数protocol、ambient credential、Originは拒否し、応答protocolは要求と完全一致しなければ中継しません。

これはGoの既存常駐認証を通すsource候補です。本人の実行承認、host/principal署名、generation、同じ所有者の回収確認をWorkerが代行するものではありません。既存MCP認証、5分のrelay上限、frame/message上限とbounded pilotの絶対期限は維持します。Worker自身は再接続しません。Macの既存ResidentClientはcode4000による終了後、回収を確認して新しい接続を試みます。常駐制御の再接続は実行grantの更新・復帰を意味せず、制御断による進行中作業への影響はGo側の失効規則に従います。5分を超える単一接続や無中断実行は提供しません。

既存Nodeのfake試験に、既定拒否、明示opt-in、固定宛先、protocol取り違え拒否、禁止header、5分終了を追加しています。実Cloudflare上の常駐再接続、Macの実回収・再認証、公開routeと認証設定は未検証です。配置・gate変更・credential作成・継続的アクセスの追加はこのsource変更に含めません。
