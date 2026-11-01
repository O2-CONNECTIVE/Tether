# Tether: Linux gateway container

実装済みの配置契約と限定smokeの範囲を示します。現行の通信実証・未達は[検証状態](../../docs/tether-pilot-status.md)を参照してください。Windows用準備toolとは別の配置経路です。

## ビルド入力とimage

- coordinatorが固定sourceから作成し、hashとbuild metadataを記録した **CGO_ENABLED=0 Linux/amd64** の単一binary `rescue-gateway`だけを使います。対応arch/静的link/対象sourceの確認は配布前の条件です。
- freshな専用staging directoryへ `Dockerfile.gateway` とそのbinaryの**2ファイルだけ**を置きます。repository全体、home、実config、鍵、token、CI secret、原logはbuild contextに含めません。
- `FROM scratch`、package/dependency installなし、base image pull不要のrecipeです。限定smokeの実績は末尾に記載します。別環境の挙動は保証しません。
- binaryはroot所有0555、runtimeは固定 `USER 10001:10001`。image内のconfig、鍵、token、shell、package manager、auto-enrollment、常駐設定はありません。
- `ENTRYPOINT`はgatewayだけで、既定のconfig pathや認証情報は埋め込みません。実起動の引数は、別承認した保護configへの `-config <container内の明示絶対path>` のみです。
- full repository向けの `.dockerignore` は追加しません。ignore漏れに依存せず、2ファイルだけのcontextを検査します。
- artifact hash、source manifest、build toolchain/flags、license noticeは別の配布台帳へ記録します。image成功は実鍵、実認証、Tunnel、dots成功とは別です。

## 実行時の保護条件

| 制御 | 必須候補 |
| --- | --- |
| runtime identity | UID/GID 10001:10001、root禁止。host userのhomeやcredentialを使わない |
| root filesystem | read-only。書込みが必要という理由で解除せず、必要性・対象を再確認 |
| privilege | 全Linux capabilityをdrop、no-new-privileges有効、privileged禁止、Docker既定seccomp維持。security optionをunconfinedにしない |
| resource | pids上限64、memory上限256MiB・swap追加なし、CPU上限1を初期候補にする。実負荷への適合は別検証 |
| network | 他workloadと共有しない専用bridge。host network禁止。待受は保護configの明示`container_private`だけ。hostへのpublishは確認済みportの **127.0.0.1のみ**。IPv6全addressや任意host公開を追加しない |
| inbound | 指定Tunnel経路からのMCP/登録pathだけ。認証とdevice proofはアプリで維持。network分離を認可の代替にしない |
| outbound | 必要な経路だけ。static Access verification鍵利用時に任意external fetchが必要とは仮定しない。許可経路が確定するまで広域networkアクセスを承認しない |
| mounts | config/keyの明示fileをread-only bind mount。親directoryの同一性・置換/削除権も検査し、専用Linux native filesystemで保護する |
| 禁止mount | Docker socket、host root、home、host credential store、CI secret、`/proc`等host管理面、Windows共有drive上の不適合なkey/config |
| 自動再開 | restart policyなし。終了・失効後の自動再接続や新grant発行をしない |
| 診断 | 固定の終了理由とowned evidenceだけ。token、private bytes、実config全文やcontainer全環境をlogへ出さない |

上記数値は最小検証用の提案で、実daemon設定変更やcontainer実行の許可ではありません。rootless/user-namespace等を利用する場合は、container UIDとhost filesystem ownerの対応を独立確認します。runtime名だけでhost UIDを推定せず、ownerを広く許すloader変更もしません。

## container-private待受profile

`gateway`文書（`agentgateway.ParseConfig`）の任意member `deployment_profile`が待受規則を選びます。値は`agentkeys.ReadTrustConfig`で読む同じ保護configからだけ取り、環境変数・argv・実行環境の検出からcontainer modeを推定しません。

| profile | `listen`の受理条件 | net.Listenのnetwork |
| --- | --- | --- |
| 省略（既定）または`"loopback"` | 数値のloopback address（`127.0.0.0/8`、`[::1]`（IPv4-mapped loopbackも既存どおり受理）） | `tcp`（従来どおり） |
| `"container_private"` | 10進dotted IPv4 literalで、`0.0.0.0`または`10/8`・`172.16/12`・`192.168/16`のいずれか。port 1024〜65535 | `tcp4` |

- 両profileとも、名前解決なし、portは先頭0・符号なしの10進1〜65535だけです。既定profileの`0.0.0.0`、private address、`[::]`は従来どおり拒否します。portの正規10進要件は今回の唯一の既定profile側の厳格化で、`127.0.0.1:8787`等の正規形は影響を受けません。
- `container_private`はloopback（publishから届かない誤設定）、IPv6（`[::]`、ULA、link-local、zone付き）、IPv4-mapped IPv6、public、`100.64/10`、`169.254/16`、broadcast、multicast、名前を拒否します。
- `tcp4`固定の理由：Goは`tcp`と`0.0.0.0`の組合せでdual-stackのIPv6 socketを開くため、IPv6からの接続も受けてしまいます。
- null、空文字、未知値、大文字小文字違い、文字列以外、重複memberは既存の厳格parserで拒否します。`auth_mode`・verifier・principalの要件は変更せず、profileで認証を省略・緩和する経路はありません。
- 可能なら専用user-defined networkで固定したcontainer private addressを指定し、wildcardより範囲を狭めます。wildcardを使う場合、container network namespace内の全interfaceで待受します。

### 残るrisk（network分離は認可ではない）

- 同じbridgeに接続されたcontainer、host上でpublish portへ届くprocess、host/Docker管理者はgateway portへ直接接続できます。Tunnel/Access edgeを経由しないrequestも届くため、gatewayのアプリ認証だけが防御です。bridgeにはgateway以外を置かない前提です。
- **host networkで`0.0.0.0`を使うと、hostの全IPv4 interfaceで待受します。** configからは検出できず、運用契約（host network禁止）だけが防ぎます。固定private addressも、hostが同じaddressを持てばhost network下でbindできます。
- hostのIP forwardingやDocker firewall規則によっては、同一LANの他hostがbridge addressへ直接routeできる可能性があります。127.0.0.1 publishだけで遮断されるとは扱わず、実環境で別途確認します（未検証）。
- Access方式では、盗まれた有効なassertionの期限内replayを防げません（[agent gateway](../../docs/agent-gateway.md)）。container profileでは送信可能な主体がbridge上の全peerへ広がります。

### 待受するroute（`cmd/rescue-gateway`の`routes`）

| path | 認証前に到達できる処理 | 認証・認可 |
| --- | --- | --- |
| MCP resource（POST/DELETE） | 同時処理枠（最大32、`MaxInflightRequests`）の取得、method/Origin/query検査、header上限16KiBでの署名検証 | OAuth：Bearer JWT（401、challenge付き）。Access：assertion 1件（403）。どちらも検証済みsubjectが設定principalと一致する場合だけ（不一致は403）。session作成・command実行はその後、かつ受信者が承認したrouteがある場合だけ |
| OAuth方式のprotected resource metadata（GET） | 公開設定値の静的JSONだけ | なし（秘密・commandなし）。Access方式は404 |
| WSS登録endpoint（GET） | precheck、未認証登録の同時枠（`MaxPending`=4、追跡上限`MaxTracked`=8）、upgrade、nonce challenge、2KiB以下の1 message読取りを登録期限10秒まで | 登録済みMac host鍵による、endpoint・nonce・dispatchへ束縛した署名proof。成功後もgatewayは登録済みsignerでagent channelを開き、host鍵をpinします |
| 上記以外 | なし | 404 |

認証なしでcommandを実行するendpointはありません。一方、以下のunauthenticatedな資源消費は**origin側でedge認可を代替しません**：

- 登録endpointの未認証枠4件を、bridge上のpeerが10秒ごとに占有し続けると正規Macの登録を拒否（503）させられます。rate limitやpeer単位の制限はありません。
- MCP pathでは未認証requestも同時処理枠32件と署名検証のCPUを消費できます。
- `http.Server`はheader 5秒・読取り30秒・idle 60秒の上限を持ちますが、TCP接続数の上限はありません。fd・memoryの上限はcontainer resource制限とhost側に依存します。

## 鍵・configの配置と信頼境界

- 設定と鍵はimageの外で、承認された専用Linux directoryに別fileとして準備します。leafはruntime UIDのみ読取り、管理主体の必要権限を含め、最終native loader契約に合う所有/permission/ACLを確認します。read-only mountだけでhost側の変更・置換を防げるとは扱いません。
- symlink/reparse相当、filesystem type、祖先のwrite/delete/replace権、mountと実objectの同一性を検査します。Windows `/mnt/c` などの共有filesystemで安全だと仮定しません。
- host管理者とDocker daemon管理者はcontainer memory、mountされた鍵、command/outputへ到達し得ます。containerizationをそれらからの隔離保証とは扱いません。
- Access app/AUD・4名issuer/sub・host/member鍵は既存の専用bindingを維持し、秘密をbuild args、環境変数、labels、image layerへ渡しません。
- 実鍵生成・mount元directoryのowner/ACL設定・公開鍵enrollment・config配置は、実targetとUID/SID対応が確定した個別scopeです。container source準備から実操作承認を導きません。

container終了だけで古いgrantや取得済み情報の消滅を証明しません。Macは同じownerの失効・回収を確認し、未確認なら再起動してclean扱いにせず報告します。

## 限定smokeの実績（2026-10-31）

source由来CGO=0 Linux/amd64 binaryとDockerfileだけのcontextでscratch imageをbuildし、上記の非root・network none・read-only・cap drop・no-new-privileges・資源上限を付けた単発起動を確認しました。設定なしではusage拒否のexit2、停止状態・OOMなし。public fixtureだけを追加した別imageでは、親0755/leaf0400はnative保護読取り後にschema拒否、leaf0444はnative読取拒否となりました。[限定native検証](../../docs/agent-key-loader-linux.md)を参照してください。

この実績には実鍵、enrollment、bridge公開port、Windows側forwarding、Tunnel、OAuth、dots登録、Mac操作は含みません。実imageを利用可能なgatewayとして配布・公開したものではなく、licenseの実配布notice収集も別条件です。source/test成果と本番通信成功を区別します。
