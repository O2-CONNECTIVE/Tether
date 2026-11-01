# agent CLIの前景composition（source only）

> **2026-11-01 更新:** 現在の限定実機検証・OAuth結果・一時派遣の残件は [Tether pilot 状態](tether-pilot-status.md) を正本とします。以下の「source only」「未実行」「producer 未実装」は初期実装時点の記録であり、現在の全体状態ではありません。権限・設定・回収の制約は維持します。更新された契約は各文書末尾の追補を優先してください。

[遠隔MCP gateway](agent-gateway.md) / [WSS登録transport](agent-transport.md) / [鍵loader](agent-key-loader.md) / [安全要件](security.md)

`backend/cmd/rescue-gateway`（Windows CI host上の共有gateway）と`backend/cmd/rescue-mac-agent`（受け手のMacの前景process）の構成です。
既存の`agentkeys`・`agentgateway`・`agenttransport`・`maclaunch`の公開APIをそのまま呼び、新しい承認経路、process policy、鍵生成、常駐、再接続は加えていません。
2026-10-31時点ではsourceとfake試験だけです。Access方式を含む関連16 packageのnormal/raceとvet、Darwin/Windowsのamd64/arm64 GoCI buildは成功。最終snapshotと件数はローカル引継ぎ証跡へ固定します。実鍵・実設定は未配置で、待受・接続・tunnel・OAuth・dots登録はしていません。製品gateは閉鎖したままです。

## 共通の入力規則

- 引数は`-config <path>`の2語だけです（gatewayに限り、代わりに`-pilot-config <path>`の2語も可）。鍵、token、承認、host・port、期限を引数・環境変数から受け取りません。pathの探索や既定位置はありません。
- 設定文書は`agentkeys.ReadTrustConfig`（期限付きctx、最大16KiB、秘密鍵と同じowner・ACL・同一handle検査）で読み、その値を使う前に厳格に検証し、検証後（成功・失敗とも）に読取りbufferを0で上書きします。内容は公開値ですが、改変されると認可・宛先が変わるため完全性が必要です。
- 厳格JSON：`agentwire.CheckJSON`（重複member・null・不正UTF-8を全深さで拒否）と`agentwire.Fields`（未知member拒否）に加え、CLI envelopeの全memberを必須とします（内側gateway.allowed_originsだけは省略可）。秘密鍵・secret・承認・再接続などのmemberは未知memberとして拒否します。
- 鍵は「path + 別経路で記録した`SHA256:`fingerprint」の参照だけです。読込みは`agentkeys.LoadCurrentUser`（期限付きctx）で、返ったsignerのfingerprintも照合します。設定文書と鍵のpathは文字列で区別し、大文字小文字違いも同一扱いです。filesystem aliasの非同一性をこの比較だけで証明せず、保護loaderとschema/fingerprint照合も必要です。
- 公開鍵は正規の1行`ssh-ed25519 <base64>`だけ（option・comment・証明書・他種別・非正規base64は拒否）。
- 診断は固定文だけです。path、label、subject、fingerprint、endpoint、鍵、tokenを出力しません。

## gateway（`rescue-gateway -config <path>`）

### 設定文書（placeholderだけの例）

```json
{
  "gateway": { "listen": "127.0.0.1:<port>", "auth_mode": "<oauth_bearer_jwt | cloudflare_access>", "resource": "https://<gateway-public-host>/<mcp-path>", "...": "方式ごとのagentgateway設定文書（agent-gateway.md）" },
  "registration_endpoint": "wss://<gateway-public-host>/<registration-path>",
  "principal_keys": [ { "label": "<member-label>", "path": "C:\\Users\\<ci-user>\\<dir>\\<member-key>", "fingerprint": "SHA256:<recorded>" } ],
  "hosts": [ { "label": "<host-label>", "public_key": "ssh-ed25519 <base64>", "principals": ["<member-label>"] } ]
}
```

| 検査 | 内容 |
| --- | --- |
| `gateway` | 既存の`agentgateway.ParseConfig`をそのまま適用（必須の`auth_mode`で方式を1つ選び、他方式のmemberは拒否。既定は数値loopbackの待受（保護設定の明示 `container_private` profileのみIPv4 private/wildcardを許可）、resource、OAuth方式はissuer・scope・token type・JWKS、Access方式はteam issuer・AUD tag・RSA鍵、1〜4 principal、label・subjectの重複拒否） |
| `registration_endpoint` | `agenttransport`と同じ正規`wss` URL規則。resource（公開MCP資源URL。Access方式でもAUD tagやteam issuerではない）と同じhost（port表記を含め完全一致）、pathはresource・`/.well-known/oauth-protected-resource`・その下のresource metadataと異なり、`/.well-known/`配下でもないこと |
| `principal_keys` | gatewayの各principalにちょうど1件（欠落・余剰・未知label・subjectでの指定を拒否）。path・fingerprintはmember間で重複不可、設定文書pathとも別 |
| `hosts` | 1〜4件。labelは重複不可かつprincipal labelと異なる。host鍵は重複不可かつどのprincipal鍵とも異なる。`principals`は1〜4件の設定済みlabel（重複・未知・subjectは拒否）。自動登録はありません |

resourceのpathに%エスケープ（`RawPath`）がある場合も拒否します。

### 起動順序

1. 引数 → `ReadTrustConfig` → 厳格検証 → buffer消去。失敗は終了値2（鍵も待受もなし）。
2. principalごとに`LoadCurrentUser`。1件でも失敗・fingerprint不一致なら終了値2で待受しません（他memberの鍵での代替や一部memberだけの起動はしません）。
3. `FileConfig.GatewayConfig`（`auth_mode`に応じて`agentgateway.NewVerifier`か`agentgateway.NewAccessVerifier`の一方だけ）→ 読み込んだsignerと`ChannelBudgets`を加えて`agentgateway.New` → `agenttransport.NewHandler`（`Registrar`はそのgateway、`DefaultBudgets`）。
4. 最後に保護設定で検証済みのaddressとprofileに従って待受します（既定loopbackは `tcp`、明示 `container_private` は `tcp4`）。失敗時は生成済みのhandler・gatewayを閉じて終了値1。
5. 1つのHTTP serverで完全一致のpathだけを振り分けます：registration path → `agenttransport.Handler`、resource path → `agentgateway.Gateway`、OAuth方式に限り`/.well-known/oauth-protected-resource`・`/.well-known/oauth-protected-resource<resource path>`も`agentgateway.Gateway`へ。Access方式ではmetadata pathも含め他は404（discoveryはAccess edgeの担当）。prefix一致・path正規化・redirectはありません。HTTP serverの上限は従来どおりです。

### 終了（SIGINT・SIGTERMまたはServeの異常終了）

1. `http.Server.Shutdown`を開始（listenerを閉じて新規接続を止める）。handler・gatewayも閉じた時点で新規を拒否します。
2. `agenttransport.Handler.Close`：登録中と受け渡し済みのWSS transport（hijack済みで`Shutdown`の管理外）を終わらせ、closeを確認します。
3. `agentgateway.Gateway.Close`：session・route・agent channelを終わらせます（Mac側は切断で失効し、再開しません）。
4. HTTPのdrainと`Serve`のgoroutineをjoinします。drainが期限内に終わらなければ`Close`で強制終了し、失敗として報告します。

各段階は有限（基準5秒、回収は10秒、drainは全体で15秒）で、再試行しません。未確認があれば固定文を出して終了値1です。正常な割込み終了は0です。

### 試行期限（`rescue-gateway -pilot-config <path>`）

- 設定文書と検査・起動順序・終了は`-config`と同じです。違いはgateway全体の寿命だけで、固定の最大15分（`pilotMaxLifetime`）です。期限は設定文書の読取り前に始まり、引数・設定文書・環境変数で設定・延長する手段はありません。productionの`nativeDeps`は15分固定で、試験の注入値は短縮だけが可能です（0以下・15分超は読取り前に終了値2で拒否し、丸めません）。
- 設定読取り・鍵読込みの期限はこの寿命から導出します（各10秒と残り時間の短い方）。読取り中に期限が来れば終了値2（bufferは消去、鍵も待受もなし）。読込み・構築後の待受前に期限切れなら待受せず、生成済みのhandler・gatewayを閉じて終了値1です。
- 待受後に期限が来ると、割込みと同じ`serve`・`finishOwned`でregistration（hijack済みWSS transportを含む）→gatewayの順に閉じ、HTTPをdrainします。回収を確認できれば固定文を出して終了値0、未確認なら終了値1です。回収の各段階（上記の有限期限）は寿命の後に続くため、process終了は最大で15分+回収期限です。
- `-config`は従来どおりで、期限はありません（暗黙の時間制限もしません）。
- gatewayの寿命は、Mac側の各grant（最大5分）とは別の上限です。どちらかの期限が他方を延長することはありません。
- 期限はprocess内のGo timerです。VMやhostの一時停止・sleep中の壁時計に対して厳密な強制終了は保証しません。外部からのprocess停止を代替しません。
- 実行環境側の構成では`restart=no`（終了後の自動再起動なし）とし、期限終了後の自動再起動・自動再登録（Macの再enrollを含む）は行いません。再開は別途の承認を経た手動起動だけです。この節はsourceとfake試験の範囲で、実環境での試行の成功を示しません。

## Mac（`rescue-mac-agent -config <path>`）

### 設定文書（placeholderだけの例）

```json
{
  "registration_endpoint": "wss://<gateway-public-host>/<registration-path>",
  "host_label": "<host-label>",
  "principal_label": "<member-label>",
  "principal_public_key": "ssh-ed25519 <base64 of the member's gateway key>",
  "host_key_path": "/Users/<name>/<dir>/<host-key>",
  "host_key_fingerprint": "SHA256:<recorded>",
  "policy_digest": "<agentexec.MacCurrentUserPolicyDigest()>",
  "work_dir": "/Users/<name>/<work-dir>",
  "home": "/Users/<name>",
  "session_seconds": 3600,
  "cleanup_seconds": 10
}
```

| 検査 | 内容 |
| --- | --- |
| endpoint・label | gatewayと同じ正規`wss`規則。labelは`[a-z0-9-]{1,32}`で、hostとprincipalは別の値 |
| `principal_public_key` | 承認画面に出し`maclaunch.Config.AllowedKeys`に入れる唯一の鍵。host鍵のfingerprintと一致すれば拒否 |
| `host_key_path` | 正規化済みの絶対POSIX path。設定文書pathと別 |
| `policy_digest` | 既存の`agentexec.MacCurrentUserPolicyDigest()`と完全一致（固定の現在利用者shell policyへの束縛。別policyの選択はできません） |
| `work_dir`・`home` | `maclaunch`と同じ規則（正規化済み絶対path、`/`以外） |
| `session_seconds` | 60〜86340（agent channelの24時間上限から1分の余裕）。承認待ちの時間を含む絶対期限です |
| `cleanup_seconds` | 7〜60（`macagent`既定のTERM・KILL・I/O待ち各2秒の合計を超える値） |

数値は先頭0・符号・小数・指数のない10進だけです。

### 順序と所有

1. 引数 → `ReadTrustConfig` → 厳格検証 → buffer消去 → `LoadCurrentUser`（host鍵）。ここまでの失敗は終了値2で、何も準備しません（`maclaunch.Pending`には破棄APIがないため、全検証を`Prepare`の前に済ませます）。
2. `maclaunch.Prepare`（`HostKey`は読み込んだsigner、`AllowedKeys`は設定したprincipal鍵1件、`Runtime`は`work_dir`・`home`、`Deadline`は現在+`session_seconds`、`Budgets`は`agentgateway.ChannelBudgets()`）。
3. `Pending.ApproveLocal(ctx)`：既存の`/dev/tty`確認だけです。設定・引数・遠隔要求に承認を与える経路はありません。ctxは前景processの寿命で、承認後にctxが終わるとgrantは失効します。失敗は終了値1です。grant発行後の回収失敗もあり得るため未発行と断定せず、command/channelの回収未確認は明示報告します。ownerが返らない失敗ではCLIから再回収できません。
4. `agenttransport.NewClient`（同じsigner、設定のendpoint・label、`DefaultBudgets`）。失敗時は同じownerだけをCloseし、失敗時に限り1回再試行します（各期限cleanup_seconds）。終了値は1です。
5. `Client.Run(ctx)`：1回だけ接続・登録・提供します。返却errorの種類に依存せず同じClient.Closeを必ず1回、期限cleanup_secondsで呼びます。既確認ownerは再処理せず、command/channel/issuerの回収未確認なら固定文で報告して終了値1。別owner・別接続を閉じることはなく、再接続・再開はしません。
6. 割込み（SIGINT）・SIGTERM・端末のSIGHUPはいずれも同じctxの取消で、同じ失効・回収を通ります。回収を確認した割込み終了だけ0、切断・期限・失効などは1です。

`maclaunch`と`macagent`はDarwin以外では`Prepare`が拒否するため、他OSでは手順2で終了値2になります。常駐・自動起動・昇格はありません。

## 決めたこと（変更する場合は再レビュー）

- gatewayは1人でも鍵を読めなければ起動しません（[鍵loader](agent-key-loader.md)の「失敗したprincipalにはsignerを入れない」より厳しい側。`ErrCleanup`時の終了要件とも一致）。
- MCP resourceとregistration endpointは同じ公開host上の別pathに限定します（1つの待受（既定loopbackまたは明示container_private）・1つのnamed tunnel経路を前提）。
- Macの期限・回収の上限値は上表のとおりで、既存APIの上限の内側です。
- Macは`SIGHUP`も取消として扱います（端末を閉じた時に回収を通すため）。

## 試験の範囲（fake検証、実機未実行）

試験は各cmdの`_test.go`です。設定読取り・鍵読込み・待受・`Prepare`・承認・`NewClient`はprivateな`deps`で注入したfakeで、file・実鍵・`/dev/tty`・process・`net.Listen`・networkを使いません。鍵は固定seedのmemory上の合成Ed25519鍵です。

- 設定：全member必須、未知（秘密鍵・承認・自動登録・再接続・shell）・重複・null・過大・後続値、不正endpoint（ws/https、別host、port表記、resource・metadata・well-known、query、userinfo、大文字host、root）、path（相対、改行、非正規、設定文書と同一・大文字小文字違い、member間の共有）、fingerprint形式・共有、label・subject・hostの取違え、host鍵とprincipal鍵の兼用、非正規・他種別の公開鍵、host 5件、policy digestの不一致、期限・回収値の範囲と数値形式。
- 順序：検証失敗では鍵読込み・準備・承認・接続・待受へ進まないこと。鍵の拒否・別鍵の返却で待受しないこと。読取りbufferの消去。
- gateway（Access方式）：同じfake listener経由で、Access文書から構成した実gatewayが、試験内生成RSA鍵で署名した合成assertionを認証・認可し（routeなしで503）、欠落・Bearerだけ・未設定subject・未設定鍵・識別headerだけ・重複headerを403（challengeなし）、metadata pathを404とすること。設定では方式の欠落・混在、必須値の欠落、AUDへの資源URL、resourceと異なるhostの登録endpointを拒否。
- gateway：fake listener（`net.Pipe`）経由で、実`agentgateway.Gateway`のmetadata（200）と未認証MCP（401）、Access assertionだけのrequestも401（fallbackなし）、実`agenttransport.Handler`の事前検査（subprotocolなし400、Authorizationあり403、upgradeなし）、他path 404。割込み後のlistener closeと終了値0。`finishOwned`のhandler→gatewayの順序、未確認の保持と報告、HTTPの強制close、Serveの未終了の報告。
- Mac：`Prepare`と`NewClient`が同じsignerを受け取ること、`AllowedKeys`が設定の1件だけ、承認のctxが前景ctxであること、承認拒否で何も閉じず接続しないこと、`NewClient`失敗で同じownerだけを有限回閉じること、Run返却errorの種類に依存せず同じClientを1回確認すること、割込みの終了値。
- gateway試行期限：productionの`nativeDeps`が15分固定で、`-pilot-config`だけが期限付きctxを受け取ること。0・負・15分超の注入値と不正な引数形（`--pilot-config`、`=`連結、大文字違い、両方指定、余剰語）を読取り前に拒否すること。`-config`は1nsの注入値でも読取り期限が短縮されず待受を続けること。fake listener上の実serveで、無通信の試行が期限で終了値0となりlistenerとidle接続を閉じること。期限まで返らない読取りが待受なしで終了しbufferを消去すること。期限後に返った鍵読込みでは待受しないこと。期限での`serve`終了がregistration→gatewayの順に閉じ、未確認なら終了値1とすること（既存の`ownedFixture`）。
- productionの`nativeDeps`が`agentkeys.ReadTrustConfig`と`agentkeys.LoadCurrentUser`そのものであること（呼び出さずに関数の同一性だけを確認）。

## 未確認・未承認の実操作

- 実Windows ACL上での設定・鍵読取り、実Macの`/dev/tty`承認・process回収、実TLS・named tunnel・WSS、OAuth provider・token、Cloudflare Access・Managed OAuth・実assertion、dotsのMCP登録は、いずれも未実行です。source/fake試験の合格はこれらを示しません。
- Access方式で資源と登録endpointを同じhostnameに置くと、Access applicationがMacのWSS登録path（OAuth clientではない）も保護対象にし得ます。登録pathのedge admissionの設計と確認は公開前の未解決事項で、この変更では登録側の認証を変えていません。
- 実鍵の作成・配置、fingerprintの独立記録、実設定文書の作成、tunnel routeの追加、gatewayの配置と待受、Mac向けの配布は、それぞれ別途の承認が必要です。
- `-pilot-config`の実起動、実環境の`restart=no`構成、期限終了後に実processが残らないことは未確認です。

## 検証境界

GoCIに両cmdのmain.go/config.goを登録済みです。fake試験は実鍵/tty/ACL/Windows console終了/実TLS/Tunnel/provider/dotsの適合を示しません。

## Mac status stream の明示 opt-in（2026-11-01）

従来の `rescue-mac-agent -config <path>` は変更しません。Mac CLI だけに、正確な3引数 `-config <path> -status-stdout` を追加しました。gateway の引数契約は変更しません。

実行する場合の接続形は次のとおりです。これは手順の例であり、新しい grant・実行・公開の許可ではありません。既登録の保護設定と、今回の操作範囲の承認が別途必要です。

```sh
/path/to/rescue-mac-agent -config /path/to/protected-config.json -status-stdout | /path/to/TetherStatus
```

- 同じ Mac の専用 write-only FIFO stdout を検査し、CLOEXEC の複製 descriptor だけに固定 JSONL を送ります。通常の terminal / regular file への status 出力は拒否します。stderr と本人同意の `/dev/tty` は別です。
- viewer は継承した専用 stdin pipe だけを読みます。任意の file / URL / agent 起動 / grant 発行 API はありません。
- 接続中は peer 検証と wire server 成立を根拠とし、単なる WebSocket upgrade を認証済み接続とはしません。
- snapshot getter は副作用なし。書込みは有限・非blocking、最新状態だけを保持し、途中frame / reader loss / clock異常で配信を終えます。既存の権限・期限・回収を GUI や pipe の速度に依存させません。
- terminal通知は高々1回。未配送・途中EOFならviewerは未確認であり、停止成功へ補完しません。terminal unknown は成功で上書きしません。
- 新しい実装の検証は [Tether pilot 状態](tether-pilot-status.md) と [Mac viewer](../ui/mac-status/README.md) を参照してください。以前の native PASS を新 binary の live GUI 成功に流用しません。


## Mac status app: local evaluation bundle (2026-11-01)

The read-only SwiftUI viewer now has a locally built `ui/mac-status/.build/local-evaluation/Tether Status.app`. This is a status viewer, not a GUI installer or an agent launcher. Starting it without a dedicated status pipe shows unavailable; it does not start an agent, request approval, or connect to a server. Closing it only ends its own reader/window and does not stop an agent.

For an already explicitly approved CLI session, the manual composition remains:

```sh
/path/to/rescue-mac-agent -config /path/to/protected-config.json -status-stdout | \
  '/path/to/Tether Status.app/Contents/MacOS/TetherStatus'
```

Fresh consent stays in the CLI's controlling terminal. This example is documentation, not a performed live test. Do not use `open` to carry the status pipe: LaunchServices does not establish this documented inherited-stdin composition.

The viewer binary and Info.plist declare macOS 13.0; running on macOS 13 hardware is untested. This does not lower the separately built Go agent's recorded macOS 26.0 minimum. The bundle retains a linker ad-hoc signature with no signing identity; resources are not sealed, and no notarization, Gatekeeper bypass, or installation was performed. Dependency/license closure and release notices remain distribution prerequisites. See `ui/mac-status/README.md` for packaging and fixture checks.
