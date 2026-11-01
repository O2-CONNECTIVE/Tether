# Tether: Mac agentの外向きWebSocket登録transport

> **現在地の正本（2026-11-01）:** 本文には初期source実装時点の「未実装」「未接続」「実機未実行」が残っています。API・安全契約を維持しつつ、現在のCLI/transport接続と限定実証は[文書索引](README.md)・[検証状態](tether-pilot-status.md)を優先してください。nativeの固定4case、Claude HTTP/OAuthのdiscovery、GUI合成試験は別の証拠で、実AI command・全利用者・新build全体の成功へ広げません。

[遠隔MCP gateway](agent-gateway.md) / [agent専用channel](agent-channel.md) / [Mac runtime](mac-agent-runtime.md) / [安全要件](security.md)

[遠隔MCP gateway](agent-gateway.md#実装済みの配線と残る条件)で未実装だった「登録adapter（transport）」のsourceです（`backend/internal/agenttransport`）。
受け手のMac（一時的な現在利用者のcommand target）から共有gatewayへ外向きにWSSで接続し、登録を証明した後、同じstreamを`agentgateway.Gateway.Connect`と`maclaunch.Approved.Serve`へ渡します。
2026-10-31時点では、sourceとmemory上の検証です。最終修正版のtransport試験はnormal/race各44件＋84サブテスト、関連16 packageの統合試験は各324件＋596サブテストが成功（fail/skip 0）、vet成功です。GoCIのproduction 39 packageはDarwin/Windowsのamd64/arm64でbuild成功。最終独立レビューに重大なsource指摘はありません。
CLIの設定parser・listener compositionは実装済みですが、実待受・接続・配置はしていません。tunnel route、DNS、TLS、provider、実鍵と登録は未設定です。実機成功や本番利用可能を示しません。

## Mac の DNS producer と FD 継承拒否

Mac の外向き WSS client だけが専用 `net.Dialer` と `net.Resolver{PreferGo: true}` を所有します。
`net.DefaultResolver`、`GODEBUG`、OS の DNS 設定は変更せず、system/cgo resolver への独自 fallback は追加しません。
通常の TLS 検証、固定 endpoint と host key、dial/handshake の期限、同じ接続 owner は従来どおりです。

2026-10-31 の無害な単独診断で、Darwin の既定 DNS lookup 後に自プロセス内の socket 1 本が
`FD_CLOEXEC` 未設定となり、Mac runtime の既存拒否に該当しました。
同じ公開 hostname を fresh process の専用 Go resolver で引く比較では解決に成功し、未設定 FD は 0 本でした。
実鍵・grant・コマンドはこの診断では使用していません。この比較は本体のコマンド実行成功を意味しません。
不明な FD の close、flags の修復、一括 sweep、番号による除外は行わず、runtime の拒否条件を維持します。

この選択は macOS 固有の split DNS、mDNS、企業 VPN の scoped resolver と同じ挙動を保証しません。
そのような配置で解決できなければ接続失敗として閉じ、system resolver に戻したり安全検査を緩めたりしません。
採用 endpoint の名前解決と認証接続は配置ごとに確認が必要です。
[Go Resolver の公開仕様](https://pkg.go.dev/net#Resolver) と
[Go の resolver 選択実装](https://go.dev/src/net/conf.go) に基づく transport 局所の選択です。

## 呼出し経路

```text
Mac: maclaunch.Pending.ApproveLocal（/dev/tty、唯一のgrant発行元）→ *maclaunch.Approved
  → agenttransport.NewClient(ClientConfig{Endpoint, HostLabel, PrincipalLabel, HostKey, Budgets}, approved)
  → Client.Run(ctx)
      WSS（通常のTLS検証。proxy・cookie・token・redirectなし） → 既存のnamed tunnel（未接続）
gateway: loopbackの待受（sourceのみ。未起動） → agenttransport.Handler.ServeHTTP
      事前検査 → upgrade → challenge → 登録証明の検証 → accepted
      → Registrar.Connect(principal label, Dispatch, stream)   // *agentgateway.Gateway
          → agentchannel.Open（SSH host鍵証明。同じstream）
Mac:  → approved.Serve(ctx, 同じstream) → agentlink.Host → 同じgrantのSession
```

- `*agentgateway.Gateway`は`Registrar`を、`*maclaunch.Approved`は`Owner`をそのまま満たします。既存APIの変更は不要です。
- endpoint URLは双方の構成値で、command API、MCP、gatewayの応答からは受け取りません。host/portの引数、任意のtunnel先、既存sshdへのfallbackはありません。

## API

| API | 呼出し元 | 所有 |
| --- | --- | --- |
| `NewHandler(HandlerConfig)` | gatewayの信頼済みcomposition | 設定を検査するだけで、待受も接続もしません |
| `HandlerConfig` | 同上 | `Endpoint`（公開wss URL。証明の資源識別子）、`Hosts`（1〜4件の`EnrolledHost{Label, PublicKey, Principals}`）、`Registrar`、`Budgets`、`Clock` |
| `(*Handler).ServeHTTP` | compositionが`Endpoint`のpathへmountする | upgradeしたtransportを所有し、登録成功時だけ`Registrar.Connect`へ移します |
| `(*Handler).Close(ctx)` | 同上 | 新規を拒否し、登録中と受け渡したstreamをすべて終わらせ、ctxの範囲で処理中requestの終了とcloseの確認を待ちます。未確認は保持し、次のCloseで同じ試行を待ちます。待つためのgoroutineは作りません（net/httpのhijack内で止まったrequestは未確認として報告します）。Registrarは閉じません |
| `NewClient(ClientConfig, Owner)` | Macの信頼済みcomposition | 検査だけでI/Oはしません。失敗時はownerを受け取らず、呼出し元が閉じます |
| `(*Client).Run(ctx)` | 同上 | 1回だけ。外向き接続、登録、`Owner.Serve`。戻るときはtransportのcloseとownerのClose（失効）を済ませています。nilは返りません（`Serve`がnilを返しても`ErrClosed`） |
| `(*Client).Close(ctx)` | 同上 | 実行中なら終わらせて待ち、未確認のowner・transportだけを再試行します。確認済みのownerは再びCloseしません。Run前ならownerを閉じ、Runを拒否させます |

- `ClientConfig.HostKey`は`maclaunch.Config.HostKey`と同じsignerです。公開鍵がdispatchの`HostKey`と一致しなければ`ErrConfig`です。
- `HostLabel`と`PrincipalLabel`は、gatewayへ登録済みの固定対応を選ぶだけです。設定にない組合せは拒否され、範囲を広げられません。
- `Run`のerrorが`ErrCleanupUnknown`（`agentchannel.ErrCleanupUnknown`と同じ値）を含むときは、Clientを保持し、回数と時間を区切って`Close`を再試行します。区切りを過ぎても未確認なら、無制限に繰り返さず、未確認のまま受け手（local operator）へ報告します。cleanである扱いにはしません。別のowner・別の接続を閉じることはありません。
- 消費した承認から自動で再接続する経路はありません。次の接続には、受け手の新しい承認が必要です。

## 登録の手順と証明

WebSocketのsubprotocolは`rescue-agent-register.v1`だけです。登録frameはtext message（厳格JSON。重複・未知・欠落member、null、非正規の数値・base64・hexを拒否）、登録後はbinary messageだけです。

1. gateway → Mac：`{"type":"challenge","nonce":"<32 byte, base64url>"}`。nonceは接続ごとに`crypto/rand`で作り、その接続のhandlerだけが保持します。
2. Mac → gateway：`{"type":"register", host, principal, generation, launch_id, epoch, policy_digest, key_set_digest, profile, deadline_unix_nano, host_key, signature}`。
3. gatewayの検証（1つでも外れれば応答せずにtransportを閉じます）：
   - `host`が設定済みlabelで、`principal`がそのhostに対応づけた設定済みlabelであること
   - `host_key`が、そのhostに設定した公開鍵とbyte単位で一致すること（requestの鍵は信頼しません。未知の鍵・自己登録は拒否）
   - profileがMacの現在利用者、policyが`MacCurrentUserPolicyDigest`、generation・Identity・鍵集合digestが有効、期限が「現在+1秒」より後で24時間以内
   - signatureが、設定済みの公開鍵で次のtranscriptを検証できるraw `ssh-ed25519`であること
4. gateway → Mac：`{"type":"accepted"}`。nonceは消去し、2件目の登録messageは読みません。以後はbinary streamです。
5. gatewayが`Connect`を呼び、`agentchannel.Open`が同じstream上でSSHのhost鍵証明とbindingのcommitmentを独立に検証します。Macは同じstreamを`Serve`へ渡します。

transcript（署名対象）は、各fieldを4 byteの長さ付きで連結したbytesです。

`"rescue-host.agenttransport.registration\x00v1\x00"`、gatewayの資源識別子（構成した`Endpoint`）、host label、principal label、Generation、LaunchID、Epoch、PolicyDigest、KeySetDigest、profile、期限（UnixNano）、host公開鍵、nonce

- 再送（replay）：nonceは接続ごとに新しく、別の接続では検証できません。同じdispatchを新しいnonceで正しく署名し直しても、gatewayはこのprocessで使ったLaunchIDを拒否します（`Connect`の既存規則）。
- 別streamへの付け替え：署名は特定接続のnonceに束縛されます。gatewayはTLSを終端しないため（named tunnelで終端）、TLS channel bindingは使いません。束縛はnonceと、その後の同じstream上のSSH証明によります。
- host・memberの入替え：labelはtranscriptに含まれ、設定の対応表で照合します。
- 新しい信頼元は作りません。token・証明書・CAの発行、OAuth tokenの転送、password、自己申告header、未知host鍵の受理はありません。gatewayのOAuth認可（[agent-gateway](agent-gateway.md#認証と認可分離)）とは独立で、registrationはMCPのsessionを作りません。grantの発行元はMacの`maclaunch`だけです。

## 事前検査とHTTP

upgrade前に次を拒否します（hijackしません）。

| 条件 | 応答 |
| --- | --- |
| GET以外 | 405 |
| path不一致 | 404 |
| query | 400 |
| `Origin`（browserからの登録）、`Cookie`、`Authorization`、`Proxy-Authorization` | 403 |
| `Sec-WebSocket-Protocol`が正確に`rescue-agent-register.v1`の1件でない | 400 |
| 登録中が4件、または所有transportが上限 | 503 `Retry-After: 1` |

- 圧縮は無効です（permessage-deflateを交渉しません）。
- 受理（admission）した時点で、そのrequestの`closer`、transportのcontext、Registration・Approvalの予算を`Accept`より前に作ります。hijackされた接続はその`closer`へすぐ渡ります。予算切れや`Handler.Close`の後にhijackが戻った場合は、その接続をその場で閉じてupgradeを失敗させます。
- hijackした接続には、`net/http`のserver期限が残っている場合があるため、hijack時に解除します。以後は登録・channelの予算が待ちを区切ります。
- 限界：hijackが戻る前（net/httpが101をflushしている間）の接続は、このpackageからは閉じられません。この区間はhttp.Serverの`WriteTimeout`などで区切る必要があります（gatewayの構成側の責任）。
- `Registrar.Connect`がpanicしても、同じstreamを閉じ、受理数・予約数を一度だけ戻します。panicは握りつぶさず、そのまま呼出し元（net/http）へ伝わります。

Mac側の接続：

- `wss`の正規URLだけ（`ws`・`https`・userinfo・query・fragment・大文字host・pathなしは`ErrConfig`）。
- 標準の`http.Transport`で、`Proxy: nil`（環境変数のproxyとその資格情報を読まない）、TLS 1.2以上と通常の証明書検証、HTTP/2なし、keep-aliveなし、圧縮なし、header 16KiB。cookie jarなし、redirectは追わない（3xxは失敗）、追加headerなし。
- 接続はこのClientの`closer`がdial前から所有し、upgrade中の1本だけを受け取ります。2本目、close開始後、upgrade終了後に完了したdialは、その場で閉じて拒否し、close結果を記録します。
- net/httpは、round tripを放棄した後もdialを続けることがあります。そのため、native transportの各dialをownerの`begin`/`end`で囲みます（dialerのtimeout 10秒で有限）。dialの途中、または遅れて届いた接続のclose中は、ownerを未確認として報告します。遅れた接続のcloseが失敗した場合は、未確認のまま保持します。これは遅れて完了するfakeのdialで試験しています。実native dialでは未検証です。
- `Dial`は成功したが観測した接続がない（transportが接続を報告しなかった）場合は、登録せずに拒否します。`CloseNow`が戻っただけでは閉鎖を確認済みとせず、未確認として保持します。

## 上限と予算

| 項目 | 値 |
| --- | --- |
| 登録済みhost | 1〜4（labelとraw Ed25519鍵はそれぞれ重複不可） |
| hostごとのprincipal label | 1〜4 |
| 同時の未認証登録 | 4（`MaxPending`） |
| Handlerが所有するtransport（登録中・受渡し済み・close未確認の合計） | 8（`MaxTracked`） |
| 登録message（全fragment合計） | 2KiB |
| stream message | 32KiB（ちょうど32KiBは有効）。受信側は渡す前にmessage全体を32KiB+1 byteのbufferで検査し、超過は1 byteも渡しません。送信側は1回のWriteを32KiBごとのmessageに分けます。同時のWriteは直列化し、chunkを混ぜません。0・部分書込みは`io.ErrShortWrite`、負や過大な書込み数はerrorで終え、進まないまま繰り返しません |
| `Budgets.Registration`（gatewayは受理〜証明、Macはupgrade後〜証明） | 0超〜30秒（既定10秒） |
| `Budgets.Approval` | Registration超〜60秒（既定30秒）。gatewayは受理から`Connect`が戻るまで、Macは接続開始から`accepted`まで |
| `Budgets.Cleanup`（transport・ownerのclose 1回の待ち） | 0超〜1分（既定5秒） |
| dispatchの残り期間 | 1秒超〜24時間 |

予算を過ぎると、そのtransportのcontextを取り消して閉じます。停滞（frameの途中で止まる、登録を送らない、challengeを送らない、acceptedを返さない）はこの予算で終わります。

## 終了と所有

- transportごとに1つの`closer`が、upgrade前から所有します。1回だけのclose試行は、contextの取消 → 所有する生の接続（gatewayはhijackした接続、MacはdialしたTCP接続）の`Close` → `CloseNow`（close handshakeなし。libraryの合流だけ）の順です。生の接続を先に閉じるので、libraryが自分で始めたclose handshake（相手が応答しない場合）に生の接続のcloseが待たされません。
- 生の接続のcloseは、誰が呼んでも最初の1回の結果を記録し、再試行しません（再利用された接続を閉じないため）。
- 確認済み＝生の接続の最初の`Close`が成功し、その後`CloseNow`が戻ったことです。生の接続を観測していないWebSocketは、`CloseNow`が戻っても確認済みにしません。予算内に戻らない・失敗した場合は`ErrCleanupUnknown`として保持し、後の`Close`は同じ試行を待ち直します。完了済みの試行は、待つ側のctxが既に終わっていても実際の結果を返します。
- 最初の`Close`が失敗した生の接続は、永久に未確認のままです。gatewayでは`MaxTracked`の枠を1つ消費し続けます。解消にはprocessの再起動が必要ですが、再起動は旧processの接続が閉じたことの証明ではありません。旧processと接続の回収を確認してから、cleanとして扱ってください。
- streamの`Close`はWebSocketのclose handshakeを使わず、上の`closer`を待ちます。agentchannelは自分の予算でこれを待ち、未確認なら同じconnを再試行します。
- 確認済みには、進行中のdialも遅れた接続のcloseも残っていないことを含みます。
- Mac：失敗・期限・失効・切断のどれでも、`Run`は`Owner.Close`（失効と回収待ち）とtransportのcloseを済ませてから戻ります。`Approved.Serve`が戻った時点で失効済みです。再接続・新IDでの再試行・再開はありません。
- gateway：`Connect`の拒否・失敗ではconnが閉じられ、Handlerも同じ`closer`を確認します。受け渡し後はgatewayのchannelがconnを所有します。`Handler.Close`は受け渡したstreamも終わらせます（gatewayのrouteが終わり、Macは失効します）。

## coder/websocketとの境界（sourceは未読）

依存module（`github.com/coder/websocket` v1.8.15）のsourceは、権限の制限で読んでいません。挙動はmemory上の試験（`review_test.go`）で確かめます。

### 観測した不一致（統合担当の実行。normalとraceで同じ結果）

`websocket.NetConn`を使っていた版では、次の2件の試験が失敗しました。

- `NetConn`作成の後に`SetReadLimit(32KiB)`を設定し直しても、32KiB+1 byteのmessageが両方向とも丸ごと届いた（`oversized message delivered whole (32769 bytes)`）。
- 待機中の読取りの期限切れが`failed to get reader: context canceled`で返り、`os.ErrDeadlineExceeded`に一致しなかった。agentchannelはこれを停滞ではなくtransportの終了と判定してしまいます。

同じ実行で、実`net/http.Server`の試験と、生の接続を先に閉じる試験は通りました。

### 採った境界（`msgConn`）

`NetConn`をやめ、libraryの公開API（`Conn.Reader`、`Conn.Write`、`SetReadLimit`、`CloseNow`）だけを使う小さな`net.Conn` adapterにしました。独自のWebSocket・SSH protocolは作っていません。

- 受信：1つのmessageを、SSH streamへ渡す前に全部読みます。bufferは32KiB+1 byteの1つだけです。binaryで32KiB以下のmessageだけを渡します。それ以外（text、32KiB超、読取り数の異常、進まない読取り）は1 byteも渡さずに失敗させ、その後も失敗を返し続け、ownerのcloseを始めます。空のmessageは何も渡さず、次のmessageを読みます。libraryの`SetReadLimit(32KiB+1)`は補助で、上限はadapterが判定します。登録messageも`ws.Read`の後に長さを正確に検査します。
- 送信：1回の`Write`が1つのbinary messageです。streamが32KiBごとに分け、同時の`Write`を直列化します（0・部分・不正な書込み数の検査は維持）。
- 読み手は1つ、書き手は1つです（それぞれmutex）。
- 期限：方向ごとに持ちます。timerは呼出しの待機中だけ動きます。
  - 期限が過ぎた状態での呼出しは、I/Oをせず、streamも終わらせずに`os.ErrDeadlineExceeded`で失敗します。期限を設定し直せば次は通ります。
  - 待機中に期限が来ると、その方向の専用contextを取り消し、`os.ErrDeadlineExceeded`に一致し`Timeout()`がtrueのerrorを返します。adapterがその方向を終端状態に固定するため、以後の呼出しも同じerrorです。library内部で接続全体が終わることには依存せず、上位channelがerrorを受けて所有接続を閉じます。
  - ownerのcloseによる取消や相手の喪失は、期限切れとしては報告しません。
- contextは、transportの生存contextの子だけです。ownerのcloseで必ず終わります。

試験で直接確かめていない前提：`CloseNow`が内部goroutineの合流を待つこと。contextを取り消したときに、待機中の`Reader`が戻ること（取消後に戻る挙動は、上の観測で見えています）。実TLS（`tls.Conn`のclose_notify）と実TCPでのclose。native `http.Transport`の実際のdial時間。

### 試験との対応

| 契約 | 試験 |
| --- | --- |
| ちょうど32KiBは両方向とも実際のreaderを通り、32KiB+1は1 byteも渡さずにstreamを終わらせる | `TestExactStreamMessageBothDirectionsAndOneMoreRefused` |
| 期限切れ後の呼出しは失敗し、設定し直せば通る。待機中の期限切れは`os.ErrDeadlineExceeded`と`Timeout()` | `TestStreamDeadlinesMatchTheChannelStallLogic`（旧`TestNetConnDeadlinesMatchTheChannelStallLogic`） |
| ownerの取消と相手の喪失は期限切れではない | `TestStreamCancellationAndLossAreNotTimeouts` |
| binary modeのtext messageでも、生の接続を先に1回だけ閉じ、すぐに確認できる | `TestWrongTypeHandshakeDoesNotHoldTheRawClose` |
| 実際の`net/http.Server`（偽の`net.Listener`がmemory connを渡す。portは開かない）での101、hijack、server期限の解除、request contextの終了後もstreamが続くこと、`Handler.Close`の後のHTTP drain | `TestRealHTTPServerUpgradeOverAMemoryListener` |

adapterへ変えた後の最終版で上表をnormal/raceとも再実行し成功しました。上の失敗は旧版のred証跡です。

## CLIの具体的な呼出し側

`cmd/rescue-gateway`は保護configを読み、固定host鍵とprincipal対応を検査してからHandlerをmountします。停止は受付停止、Handler.Close、Gateway.Close、有限HTTP drainの順です。

`cmd/rescue-mac-agent`は保護configと同じhost signerからPrepareし、/dev/ttyのApproveLocalを経てNewClientとRunを呼びます。Run終了理由にかかわらず同じClient.Closeを有限budgetで確認し、未確認は非zero終了として残します。constructor失敗時も取得済みownerだけを有限回収します。詳細は[CLI](agent-cli.md)を参照してください。

## 検証済みの範囲と未実行の境界

- 試験は`backend/internal/agenttransport`の`_test.go`です。Listen・Dial・`httptest.NewServer`・network・process・実鍵・account・ACLは使いません。
  - HTTP/1.1のrequest・responseとWebSocketのframeは、buffer付きmemory conn上の実bytesです。server側はmemory上でrequestを読んで`ServeHTTP`を呼び、hijackを模すResponseWriter、Mac側は`http.RoundTripper`の代替です。Mac側は実際の`websocket.Dial`、gateway側は実際の`websocket.Accept`を通ります。攻撃側の試験は手書きのmask付きframeを送ります。
  - 合成の試験は、実`Handler` → 実`agentgateway.Gateway.Connect`（実SSH agent channel） → Mac側の実`agentlink.Host`と実`agentexec.Session`（起動しないfake runtime）を通します。Macの承認は`maclaunch.Approved`と同じ構成・順序を再現したfixture（`linkOwner`）で代用しており、/dev/ttyでの承認ではありません。`mcp_vertical_test.go`は合成OAuth JWTを使う実HTTP MCP requestから同じ経路を通し、正常command、request ID冪等性と異payload拒否、cancel、transport loss、要求のない期限失効を検証します。実provider・/dev/tty・native commandの試験ではありません。
- 対象：
  - 正当なhostとprincipalが承認と同じbindingで`Connect`に届き、Mac側が同じstreamで提供すること
  - 未知host label、自己登録鍵、別hostの鍵、host入替え、未対応principal、署名後のprincipal・LaunchID・期限の改変、別の資源識別子、期限切れ・24時間超・Windows policy、0のnonce、未知・重複member、binary・過大・fragment合計超過の登録、close frame
  - 別streamのnonceの証明、捕捉した登録の再送、切断後の同じdispatchの再登録（gatewayがLaunchIDを拒否）
  - upgrade前の拒否（method、path、query、Origin、Cookie、Authorization、Proxy-Authorization、subprotocol）、未認証登録の上限と503、登録・frame途中の停滞、登録中の`Handler.Close`、受渡し済みstreamの`Handler.Close`、Connectの拒否、Connectの停滞とApproval予算
  - streamのbinary限定、32KiB分割、adapterによるmessage全体の上限検査、text messageでの終了
  - Mac：半開の切断・失効・gateway終了での両端の終了と非再開、設定の不一致、challengeなし・binary・過大・acceptedなし・不正accepted・subprotocolなし・redirect・通常応答、ownerの未確認cleanupの同じownerでの再試行、閉じないtransportの未確認保持と同じ接続の確認、Run前のClose、登録中の取消、設定の拒否、native transportの設定値の検査（実行はしない）
  - レビュー修正（`review_test.go`）：
    - library起点のclose handshake中も生の接続を先に閉じること
    - 完了済みcloseと終了済みctxの組合せを繰り返しても実際の結果を返すこと（closer、`Handler.Close`、`Client.Close`）
    - 0・部分・負・過大な書込み数と、並行Writeでchunkが混ざらないこと
    - `Serve`がnilのとき`ErrClosed`になること
    - 観測していないtransportの拒否と未確認保持
    - registrarのpanic（試験内で明示的にrecover）：panicが伝わり、streamが閉じ、受理数が正しいこと
    - 予算を過ぎて戻ったhijackと、止まったupgrade中の`Handler.Close`
    - 受渡し済みと未確認の合計が`MaxTracked`に達すると503になること、同じ未確認closeの再試行
    - 上の表のlibrary境界（adapterの上限・期限・取消）
    - dial途中と遅れた接続のclose中は確認済みにしないこと、遅れたcloseの失敗の保持、round tripより長く続くdial（fake）
- 時間に依存する試験があります（数十ミリ秒〜数秒）。遅い環境での揺れは統合担当の実行で確認が必要です。
- 結果はsourceとfake・memory上のものだけです。これらの合格は、実tunnel、実TLS、実OS、実Macでの起動、実鍵の登録、OAuth provider、dotsからの遠隔操作、配置の成功を示しません。

## 公開前の前提（exposure）

- registration endpointは認証前のupgradeを受けます。登録中4件・所有8件の上限は資源を区切りますが、公開endpointへの接続で枠を埋めるDoS（slot枯渇）は残ります。公開する前に、既存のtunnel・入口側で到達元を限るなどの対策が必要です。このpackageは新しい資格情報、edge gate、製品設定を追加しません。
- 最初のcloseに失敗した接続が枠を消費し続ける点も、同じ公開前提の一部です（上の「終了と所有」）。

## 未承認・未実施の実操作

named tunnelへのroute追加、DNS・証明書、gatewayの待受と配置、Mac向けpackageの作成と配布、実host鍵・member鍵の登録、providerとdotsのMCP登録、実機での接続試験は、それぞれ別途の承認が必要です。
