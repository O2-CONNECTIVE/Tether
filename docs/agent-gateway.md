# 遠隔MCP gateway（Streamable HTTP）

> **現在地の正本（2026-11-01）:** 本文には初期source実装時点の「未実装」「未接続」「実機未実行」が残っています。API・安全契約を維持しつつ、現在のCLI/transport接続と限定実証は[文書索引](README.md)・[検証状態](tether-pilot-status.md)を優先してください。nativeの固定4case、Claude HTTP/OAuthのdiscovery、GUI合成試験は別の証拠で、実AI command・全利用者・新build全体の成功へ広げません。

[MCP adapterとagent wire](agent-mcp-adapter.md) / [agent専用channel](agent-channel.md) / [agent command契約](agent-command-contract.md) / [安全要件](security.md)

dotsが、受け手が自分で起動して同意した一時的なMacの現在利用者へ、4つのtool（start/output/status/cancel）を送るための共有gatewayのsourceです（`backend/internal/agentgateway`、`backend/cmd/rescue-gateway`、`backend/internal/mcpadapter/http_bridge.go`）。
source/memory試験と限定native実演・HTTP discoveryは別の検証境界です。現在の固定実績と未達は[検証状態](tether-pilot-status.md)を参照してください。
このsourceは実環境のtunnel、provider登録、account、鍵、routeを自動作成しません。限定実績と閉鎖済み入口、配布の残件は検証状態の正本に従います。
人向けSSH互換は目的外です。

## 呼出し経路

```text
dots（遠隔MCP client）
  → [Access方式のみ] Cloudflare Access edge（Managed OAuth。discovery・401 challenge・opaque tokenの検証はedge側）
  → 既存のnamed tunnel（[未接続] TLS終端。gatewayは関与しない）
  → 既定loopbackまたは明示container_privateの待受（source only。cmd/rescue-gatewayが保護設定/鍵loaderとWSS登録handlerを構成）
  → agentgateway.Gateway.ServeHTTP
      Origin照合 → 設定した1方式の認証（Bearer: Verifier / Access: AccessVerifier） → [Bearerのみ]scope → 設定済みsubject（認可） → Mcp-Session-Idの照合
  → mcpadapter.HTTPSession.Handle（stdioと同じlifecycle・4 tool・厳格parse・結果上限・error写像）
  → そのprincipalの現在のroute（*agentwire.Client）
  → agentchannel.Open済みの*Channel（principalの登録済みsigner、dispatchのpin済みhost鍵とBinding）
  → agenttransport WSS stream adapter（source/memory検証） → Macのagentlink.Host → 同じgrantのSession → macagent
```

- routeを作れるのは`Gateway.Connect(label, Dispatch, net.Conn)`だけです。Connectは自分で`agentchannel.Open`を呼びます。BindingはMac側がhandshakeのcommitmentで照合した値に限られ、requestやrelayのbytesからは選べません。
- gatewayはgrantを発行・延長しません。Mac側の同意（`maclaunch`）が唯一の発行元です。
- tool引数でhost、signer、Binding、policy、profile、権限を指定すると、`-32602`で拒否します（stdioと同じ）。

## 認証と認可（分離）

gatewayはtokenを検証するだけで、authorization server、CA、token発行元にはなりません。
認証方式は設定文書の必須member `auth_mode`で1つだけ選びます。既定値はなく、方式間のfallback・併用はありません。

| `auth_mode` | 読むheader | 読まないもの |
| --- | --- | --- |
| `oauth_bearer_jwt` | `Authorization: Bearer`（既存providerのJWT access token） | `Cf-Access-Jwt-Assertion` |
| `cloudflare_access` | `Cf-Access-Jwt-Assertion`（Access edgeが付けるapplication token） | `Authorization`（clientのopaque edge token）、`CF_Authorization` cookie、`Cf-Access-Authenticated-User-Email`などの識別header |

### OAuth bearer方式（`oauth_bearer_jwt`）

| 段階 | 内容 | 失敗時 |
| --- | --- | --- |
| 認証（`Verifier`） | compact JWS。alg `RS256`（2048〜8192 bit）または`EdDSA`（Ed25519）。`kid`で設定済みJWKSから選んだ鍵の`alg`と一致すること。headerは`alg`/`kid`/`typ`/`x5t`/`x5t#S256`だけを許可（`jwk`・`jku`・`x5u`・`crit`などは拒否）。`typ`は設定値（例`at+jwt`）。`iss`は完全一致、`aud`（文字列か配列）に資源URIを含む、`exp`・`iat`は必須の整数、`nbf`は任意、許容誤差は最大60秒、寿命（exp−iat）は24時間以下 | 401 `WWW-Authenticate: Bearer resource_metadata="…", error="invalid_token"`（tokenがないときは`error`なし） |
| scope | `scope`（空白区切り）または`scp`（文字列か配列）に設定scopeを含む。両方あるtokenは曖昧なため拒否 | 403 `error="insufficient_scope", scope="…"` |
| 認可 | 検証済み`sub`が、設定した最大4人のprincipalのどれかと完全一致 | 403（challengeなし） |

- auth-noneやbooleanで通すverifierはありません。自己申告のuser header（`X-Forwarded-User`など）は読みません。tokenはqueryで受けず（400）、Macやrelayへ渡しません。tokenやheaderをlogに出しません。
- JWKSは設定文書に静的に置きます。取得（fetch）やdiscoveryはしません。private member（`d`など）や対称鍵を含むJWKSは拒否します。
- `/.well-known/oauth-protected-resource`と`/.well-known/oauth-protected-resource<資源path>`で、RFC 9728のmetadata（`resource`、`authorization_servers`、`scopes_supported`、`bearer_methods_supported`）を認証なしで返します（この方式だけ）。

### Cloudflare Access assertion方式（`cloudflare_access`）

Managed OAuthではclientはCloudflareからopaque access tokenを受け取り、Access edgeがそれを検証してoriginへ署名付き`Cf-Access-Jwt-Assertion`を付けます。gatewayはこのassertionだけを検証し、OAuth access tokenとしては扱いません。

| 段階 | 内容 | 失敗時 |
| --- | --- | --- |
| 認証（`AccessVerifier`） | header`Cf-Access-Jwt-Assertion`がちょうど1つ。compact JWS、alg `RS256`だけ。headerは`alg`/`kid`/`typ`だけ（`jku`・`x5u`・`jwk`・`x5t`・`crit`などは拒否）、`typ`は省略可能で、存在する場合は`JWT`（空・null・非文字列は拒否）。`kid`は設定した`access_keys`にあること（未知kidは拒否、取得しない）。payloadは重複member拒否、`type`が`app`、`iss`が設定したteam-domain URLと完全一致、`aud`は配列で設定したAUD tagを含む（文字列`aud`や資源URLは拒否）、`sub`は空でない文字列、`email`は`@`を含む文字列、`common_name`（service token）があれば拒否、`exp`・`iat`・`nbf`は必須の整数、許容誤差は最大60秒、寿命は24時間以下 | 403、`WWW-Authenticate`なし |
| 認可 | 検証済み`sub`が、設定した最大4人のprincipalのどれかと完全一致。`email`は存在確認だけで、認可にも識別にも使いません | 403（challengeなし） |

- scopeは読みません（Accessのapplication tokenはOAuth scopeを約束しません）。Access側のpolicyで許可した利用者のうち、設定した`sub`だけが通ります。
- discoveryとOAuth challengeはedgeのものです。originは`/.well-known/oauth-protected-resource*`を返さず（404）、Access issuerをOAuth discovery issuerとして公開しません。assertionのない・無効なrequestがoriginに届くのはedgeを経由していない（または設定不整合）ためで、originから再認証を促せないので401 challengeではなく403を返します。
- `sub`はCloudflare accountとemailに固有で、利用者の削除・再追加で変わります。そのときは設定を更新して再起動します。

## principal・route・sessionの束縛

- principalは1〜4人で、label（運用者の表示名）、subject、signerを持ちます。labelとsubjectの重複は拒否します。
- 1 principalにつき、live routeは最大1本です。routeは1つのgrant（LaunchID、epoch、generation、鍵集合digest、Mac profile）と、1本のchannel・wire clientに対応します。この対応は変わりません。
- Connectは、未知のlabel、signerのないprincipal、route取得中・保持中のprincipal、Mac以外のprofile、このprocessで既に使ったLaunchIDを拒否し、渡されたconnを閉じます。open に進んだ Connect は失敗してもその LaunchID を消費します。open 前の拒否は LaunchID を記録せず、渡された stream を閉じます。その場合の Mac 承認失効は既存 host の接続喪失処理が担い、gateway による消費とは区別します。
- `initialize`が成功したときだけ、128 bit乱数の`Mcp-Session-Id`を発行します。sessionはそのprincipalへ固定されます。その時点で有効なrouteがあれば同routeへ束縛し、なければ恒久的に未束縛のdiscovery専用sessionを作ります。後からgrantへattachしません。
- 後続の各requestでは、毎回tokenを検証したうえで、次の条件をすべて確認します。いずれかが外れると404です。
  - sessionのprincipalが同じであること。他principalのsession IDは未知のものと同じに扱います
  - routeへ束縛されたsessionでは、そのrouteがprincipalの現在のrouteで、終了していないこと（discovery専用sessionにはrouteがない）
  - 最終使用から30分以内であること
- routeは、channelの終了（心拍喪失、停滞、hard deadline、相手のclose）、wire clientの終了、gatewayのCloseで終わります。終わったrouteへ束縛したsessionは無効です。routeがなければ新しい`initialize`は認証済みdiscovery専用sessionを作れますが、commandは開始できません。次の作業には受け手の新しい承認・Connectと、新しいinitializeによるroute束縛が必要です。
- 状態はすべてmemory上にあり、再起動で消えます。再起動後に旧session IDやrouteを再開する手段はありません。
- 既知の制約：Macの`agentchannel`は、認証前に終わった接続でもgrantを失効させます。そのため、Connectを拒否してconnを閉じると、そのapprovalは使えなくなります（fail closed）。

## Streamable HTTP（2025-06-18）での振る舞い

| request | 応答 |
| --- | --- |
| POST（JSON-RPC request 1件） | 200 `application/json`で応答1件。SSEは返しません |
| POST（notification、またはclientのresponse） | 202、本文なし |
| POST（parse error、batch、不正なid） | 400、JSON-RPC error |
| POST（session headerのない`initialize`以外） | 400。sessionは作りません |
| `initialize`時にrouteがない / session数が上限 | 認証済みdiscovery専用session / 429 |
| discovery専用sessionの妥当なtools/call | backendへ到達せず404で同sessionを破棄。不正な要求は400等で拒否し得る。新規initializeが必要。grantへ後付け束縛せず、commandを配送しない |
| `notifications/cancelled`で取り消された元のPOST | 200、元 id の JSON-RPC error -32800。HTTP の単一 JSON 応答を保ち、遠隔配送の不確定性を明記します。遠隔 command は止まりません。notification 自体は 202 です |
| GET（SSE stream）、PUTなど | 405、`Allow: POST, DELETE` |
| DELETE | その MCP session だけを終えて 204。進行中 POST は JSON-RPC 応答を捏造せず HTTP 404 となります。command と grant には影響しません |
| `Mcp-Protocol-Version` | `initialize`以後は必須で、`2025-06-18`だけを受け付けます。省略時に旧版を仮定しません（400） |
| Content-Type / Accept | `application/json`（charsetはutf-8だけ） / `application/json`を受理できること（それ以外は415 / 406） |
| Origin | 存在する場合は、設定値と完全一致すること（それ以外は403） |

- HTTP接続の切断は取消として扱いません。呼出しは`CallTimeout`まで続き、結果は捨てます。遠隔commandを止めるのは`rescue_cancel`だけで、cancelの受理は回収の証明ではありません（回収状態は`rescue_status`の`cleanup`で確認します）。
- `request_id`の冪等性と`start_uncertain`は、stdioと同じ`mcpadapter`の実装を使います。新しいIDでの自動再送はしません。
- shell、pty、forwarding、loopbackへのfallbackはありません。

## 上限

| 項目 | 値 |
| --- | --- |
| principal / principalごとのsession | 4 / 4 |
| 同時HTTP request（全体） | 32（超過は503、`Retry-After: 1`） |
| principalごとの同時tool呼出し | 4（そのprincipalの全sessionで共有。超過は`-32000`） |
| 本文 / tool結果 | 512KiB（超過は413） / 符号化後160KiB |
| 1 tool呼出し | 既定30秒（超過は`uncertain`/`start_uncertain`） |
| session idle | 30分 |
| token / JWKS鍵数 / 設定文書 | 8KiB / 16 / 64KiB |
| 1 processで登録できるLaunchID | 1024（超過時は再起動まで拒否） |
| HTTP server（source only） | header 5秒、読取り30秒、書込み75秒、idle 60秒、header 16KiB |
| channelの予算（`ChannelBudgets`） | handshake 15秒、心拍10秒/10秒、読取り停滞30秒、書込み停滞15秒、回収5秒 |

## 設定（placeholderだけの例）

秘密情報は含みません。signer、client secret、tokenを置くmemberはなく、未知のmemberは拒否します。既定の待受は数値のloopbackアドレスだけです。保護設定の `deployment_profile: "container_private"` を明示した場合のみ、IPv4の未指定またはRFC1918アドレス・非特権portを `tcp4` で使用します。[コンテナ配置条件](../deploy/cloudflare/linux-container.md)を必須とし、認証を省略する設定ではありません。
`auth_mode`が必須で、方式ごとにmember集合が決まります。もう一方の方式のmember（例：Access方式での`issuer`・`scope`・`jwks`）は未知memberとして拒否します。`auth_mode`のない旧形式の文書も拒否します（実配置済みの設定はありません）。

OAuth bearer方式：

```json
{
  "listen": "127.0.0.1:<port>",
  "auth_mode": "oauth_bearer_jwt",
  "issuer": "https://<existing-provider-issuer>/",
  "resource": "https://<gateway-public-host>/mcp",
  "scope": "<scope-registered-for-this-resource>",
  "token_types": ["at+jwt"],
  "leeway_seconds": 30,
  "jwks": { "keys": [ { "kty": "RSA", "kid": "<provider-kid>", "alg": "RS256", "use": "sig", "n": "<base64url>", "e": "AQAB" } ] },
  "principals": [ { "label": "member-1", "subject": "<provider-subject-of-member-1>" } ],
  "allowed_origins": []
}
```

Cloudflare Access assertion方式：

```json
{
  "listen": "127.0.0.1:<port>",
  "auth_mode": "cloudflare_access",
  "resource": "https://<gateway-public-host>/mcp",
  "access_issuer": "https://<team-domain>",
  "access_aud": "<access-application-aud-tag>",
  "access_keys": { "keys": [ { "kty": "RSA", "kid": "<team-kid>", "alg": "RS256", "use": "sig", "n": "<base64url>", "e": "AQAB" } ] },
  "leeway_seconds": 30,
  "principals": [ { "label": "member-1", "subject": "<access-user-sub-of-member-1>" } ],
  "allowed_origins": []
}
```

| member | 規則 |
| --- | --- |
| `resource` | 公開MCP資源URL（https、pathあり）。routingと登録endpointのhost照合だけに使い、assertionの`aud`とは比較しません |
| `access_issuer` | team-domainのURL。小文字host、https、port・path・末尾`/`・query・fragmentなし。`iss`と完全一致で比較 |
| `access_aud` | Access applicationのAUD tag。英数字・`-`・`_`の1〜256文字（URLは書けません）。資源URLとは別の値 |
| `access_keys` | teamのcerts文書（`https://<team-domain>/cdn-cgi/access/certs`）の`keys` memberをそのまま`{"keys":[…]}`として転記したもの。RSA（2048〜8192 bit）だけ、`alg`は省略可だが書くなら`RS256`、`use`は書くなら`sig`、1〜16鍵、kid重複・private member・`key_ops`などは拒否。`public_cert`/`public_certs`は含めません（top-levelの未知memberとして拒否） |
| `principals[].subject` | Accessの`sub`（利用者ごと）。emailは書けません |

CLI envelopeは[agent CLI](agent-cli.md)に記載します。保護文書と登録済み鍵を検証し、最後に既定loopbackまたは明示container_privateで待受するsourceです。実起動は未承認です。診断は固定文だけで、subject、path、token、headerを出力しません。

## 信頼の前提

- gatewayは、4人それぞれのagent署名鍵（raw Ed25519）を保持します。Macの承認画面は鍵のfingerprintを表示するため、受け手はどのmemberの鍵を承認したかを確認できます。gatewayの運用者とhostは、承認済みのMacに対してそのmemberとして操作できます。この信頼は、共有gatewayの設計に含まれます。
- dispatch（Binding、期限、host公開鍵）は資格情報ではありません。ただし、偽のhost鍵が届くと、偽のhostへ接続してしまいます。dispatchを真正に届けることは、登録adapterの責任です。
- Access方式の境界：署名検証は「Accessが発行したこのapplication向けのassertionである」ことだけを示します。送信元networkは証明せず、盗まれた有効なassertionの期限内replayも防げません（tokenのbindingやnonce照合はありません）。そのため、originはnamed Tunnelの内側に置き、既定loopback、または専用bridgeとhost側127.0.0.1公開だけの明示container_privateを使用します。公開IPや別の公開hostnameへ直接出さず、host networkは禁止します。CI hostの管理者・到達可能なhost process・bridge peerは有効assertionを直接replayし得るため、到達範囲も信頼境界に含めます。network分離を認証の代替にしません。assertionの寿命はAccess applicationのsession時間で決まるため、24時間以下に設定します（超えると拒否）。

Access applicationとAUDは、このMCP hostname/path専用にします。他origin、Worker route、preview routeとの共用は不可です。同一AUDの別originへ渡ったassertionも署名検証上は受理可能なためです。

署名鍵の先行公開は未確認です。更新前に新鍵を取得できなければ、保護設定更新と再起動までfail-closedで停止し、新しいMac承認が必要です。無停止rotationを保証しません。

## 実装済みの配線と残る条件

- agenttransport.Handler/Clientはhost鍵のnonce proofと同じWSS streamをchannelへ渡し、両CLIへ接続しています。maclaunchの今回の同意をgatewayが代行する経路はありません。
- signerはagentkeysの保護path/fingerprint検査から供給します。新しい端末の鍵発行・配置・enrollmentはsource取得で自動的に成立しません。
- issuer、audience、scope、署名鍵、token type、subjectとclient登録は実環境ごとの入力です。4名向けpolicyは4名の登録完了を意味しません。実identityを公開sampleへ埋めません。
- JWKSは自動取得・更新しません。未知kidは拒否し、保護された経路で独立確認した新旧鍵（最大16鍵）を設定して再起動します。再起動では既存routeと承認を失い、本人の新しい同意が必要です。無停止rotationは保証しません。`azp`/`client_id`の照合はありません。
- 限定実演の入口は閉鎖・停止済みです。Claude HTTP/OAuthの4tool discoveryはcommand実行ではなく、dot workspaceのplugin作成403も別の未達です。現在の実績は[正本](tether-pilot-status.md)に集約します。

## source/memory試験が確認する範囲

- 試験は`backend/internal/agentgateway`の`_test.go`と`backend/cmd/rescue-gateway`の`main_test.go`・`config_test.go`・`access_test.go`です。`httptest.NewRecorder`と`ServeHTTP`の直接呼出し、buffer付きmemory conn上の実SSH agent channel、実`agentlink.Host`と実`agentexec.Session`、起動しないfake Mac runtime、固定seedの合成Ed25519鍵と試験内で生成した合成RSA鍵を使います。`httptest.NewServer`、Listen、Dial、shell、OS変更、実認証、network、実providerは使いません。
- 対象は次のとおりです。
  - 4 toolの正常経路（stdin、同じrequest_idの再送と不一致、出力、終了、cancelと回収の区別）
  - tokenの拒否：署名、改ざん、kid、`alg: none`、alg混同、header鍵、crit、typ、issuer、audience、期限、nbf、iat、寿命、exp欠落・小数、sub欠落、scope欠落・曖昧、未設定subject、過大、Authorization形式・重複
  - Access assertion（`access_test.go`）：別鍵の署名、改ざん、未知kid、`alg: none`/HS256/EdDSA、OAuth tokenの流用、`jku`/`x5u`/`jwk`/`x5t`、header・claimの重複member、typ、issuer（末尾`/`・別team・OAuth issuer）、文字列・空・資源URL・別applicationの`aud`、`type`、service token（`common_name`、空`sub`）、`sub`欠落・空・数値、email欠落・形式、有効emailでも未設定subjectの場合と、検証結果がemailでなくsubを保持すること、期限・nbf・iat・寿命・欠落・小数・文字列、過大、未設定subject、header重複・1行に2つ、Bearerだけ・cookieだけ・識別headerだけ（fallbackなし）、opaque edge tokenとの併存、challenge・metadataを出さないこと、OAuth方式がassertionを読まないこと、Access方式でのprincipal・session・grantの束縛とroute喪失時の失効、設定の拒否（方式の欠落・不明・混在、必須値の欠落、issuer・AUD・鍵の形式、`Config`の方式重複・資源URL）
  - CLI（`cmd/rescue-gateway/access_test.go`）：Access文書からの構成、合成assertionでの認証通過とrouteなしdiscovery、欠落・Bearerだけ・未設定subject・未設定鍵・識別header・重複が403でchallengeなし、metadataが404
  - RS256の受理、principal間とsessionの分離、自己申告headerの無視、routeのないprincipal
  - backendの喪失とhard deadlineでのsession終了と非再開、再起動後の非再開、登録の拒否（二重route、LaunchIDの再利用、未知label、Windows profile、signerなし）
  - 開始の不確定とJSON-RPC IDの衝突、出力の停滞と`notifications/cancelled`、DELETEとsession上限
  - 不正なHTTP・JSON、SSE（GET）の拒否、Origin、query、protocol version、metadata、設定の拒否、CLIの待受前拒否
- 未確認の項目：全体の同時HTTP上限（32）、principalごとのtool同時上限の飽和、session idleの30分、LaunchID 1024件の上限、HTTP serverの実timeout。
- これらの合格は、実provider、実token、tunnel、実relay、Macでの実行、dotsからの遠隔操作、配置の成功を示しません。


## レビュー後の限定修正

- Close は acquisition 開始から Connect を所有・追跡し、handshake 中の回収を待たずに成功しません。未完了なら cleanup unknown を返し、同じ owner の次回 Close が回収を再確認します。並行 Close は同じ未確認回収試行を共有します。
- CLI は登録 producer の終端を有限時間待ち、HTTP graceful shutdown が失敗したら接続を force-close し、その場合は終了値 1 とします。OS process 終了を graceful cleanup 成功に読み替えません。
- JWKS は各 JWK の alg を必須とし、key_ops/vendor 独自 member は拒否します。OAuth token の typ は必須です。Access assertion だけは typ の欠落を許容し、存在する場合の JWT 型検査と全署名・claim 検査を維持します。限定したprovider互換の実績を他provider・新設定へ転用せず、必要な形式の検討を経ず任意変換・制約緩和しません。
- 静的 JWKS 更新に再起動が必要な現在の構成では、既存 route と承認が失われ、各受け手の再承認が必要です。
- 取消通知では原則 JSON-RPC 応答を抑制する MCP の推奨と、JSON 単一応答型 HTTP の終了を両立させるため、元 POST に明示的 cancellation error を返します。実 client 互換は未確認です。開始の再確認には同じ request_id を使い、新 ID で再実行しません。

2026-10-31 local verification update: the official cloudflared CLI application token may omit JOSE typ. A separately authorized local inspector successfully verified a real human assertion against the pinned public keys, issuer, AUD, lifetime and expected-person binding. No token or actual identity is included in this source. This does not establish other users, live gateway authorization, Managed OAuth or dot MCP connectivity.
