# Access署名公開鍵の更新とCLI所有

[gatewayの現行契約](agent-gateway.md) / [Go CI](go-ci.md)

## 範囲

[Issue #337](https://github.com/O2-CONNECTIVE/Tether/issues/337) / R-14へのsource-level候補です。`NewRotatingAccessVerifier` は既存の静的検証器と別の明示constructorです。
設定JSONの明示 `access_key_source: "cloudflare_certs"` を、通常gateway CLIの構成・起動順序・有限refresh ownerへ接続します。
これはsource-onlyの変更で、実Cloudflare、配置、credential、常時運用やGUIの実起動は行いません。
既存 `NewAccessVerifier` と `access_keys` の静的運用は省略時のままで、暗黙に新方式へ移行しません。

## 固定した境界

- constructorのissuerは `https://<一つのteam名>.cloudflareaccess.com` の完全一致origin。
  公開鍵はそのoriginの `/cdn-cgi/access/certs` だけから取得し、tokenのiss/jku/jwk/x5uを取得先にしません。
- 専用HTTP transport、system roots、TLS 1.2以上、proxy・redirect・cookie jar・compressionなし。
  GETはbody/credential/assertionなし。総timeout、header 16KiB、body 64KiB、既存RSA鍵数16の上限を保ちます。
- network I/Oは明示 `RefreshKeys(ctx)` だけです。`Verify`、未知kid、失敗tokenは取得やgoroutineを発生させません。
  同時取得は一つだけ。開始した失敗も試行間隔を消費し、並行呼出はqueueを作らず拒否します。
- 起動時は空。設定の静的鍵を「今取得した」ことにして寿命を延長しません。
  strict JSON、鍵の種類/algorithm、秘密member・duplicate kidなどを既存parserで検査した後、set全体を置換します。
- 旧鍵の独自unionや無期限保持をしません。providerが掲載している新旧鍵だけを受理し、成功した次のsetにない鍵は即時退役します。
- 失敗しても最後の成功setの期限は延びません。期限は成功応答の到着時ではなく当該request開始から計算します。
  観測した期限超過、または取得開始より前の時刻ではsnapshotを破棄し、時計が戻っても復活させません。
  取得開始以降の全ての後退を検出するものではなく、単調時計側の年齢上限も維持します。
- `MaxAge` は最後の取得開始からの総上限で、通常refresh間隔に加える猶予ではありません。
  単調時計と壁時計の双方を確認します。期限到達後は既知kidも拒否します。
- RSA署名、issuer/AUD、app種別、本人subject/email、時刻、有効期間、禁止header/service tokenの既存検査を共用します。
  principal/session/route/本人同意は変更せず、鍵更新自体はそれらを再起動・再発行しません。

## 明示設定と採用するsource方針

Access設定の `access_keys` を削除し、同じ階層に `"access_key_source": "cloudflare_certs"` を置いた場合だけ新方式を選びます。
両方の併記、空文字、null、未知のsource、任意URL、OAuth方式との混在は拒否します。
`access_issuer` は一つのCloudflare team originに制限し、AUD・resource・principal・resident・登録鍵・routeの規則は変えません。
`ParseConfig` と `GatewayConfig` はI/Oやgoroutineを開始せず、空の検証器を構成するだけです。

`ConfiguredAccessKeyRefreshPolicy()` はこの設定に限って以下の固定値を選びます。
これはfixture値の流用ではなく、retirement遅延を5分未満に抑えるsource方針です。
providerの可用性や実運用での適合は未検証で、配置前に別途確認します。

- `Timeout = 5秒`。一回の取得と応答本文読取の総上限です。
- `MinInterval = 60秒`。ownerは各試行の**完了から**60秒待ち、次を一回だけ試します。
  開始間隔は通常60〜65秒で、tickerの蓄積、即時retry、未知kidによる早期取得はありません。
- `MaxAge = 300秒`。成功したrequestの**開始から**の総寿命です。
  失敗後に300秒を足すものではなく、到達時は既知鍵も拒否します。

設定、引数、環境変数でこれらを上書きしません。
直接constructorを呼ぶ場合の `AccessKeyRefreshPolicy` は従来どおり全項目必須で、`0 < Timeout < MinInterval < MaxAge` を要求します。
旧candidate試験の1秒/10分などは直接API用fixtureで、CLI方針ではありません。

## 起動・終了の所有

`-config`、`-pilot-config`、`-discovery-pilot-config` は、それぞれの既存構成が完成した後、listen前に初回取得を一回行います。
初回失敗・timeout・取消では待受せず、構築済みhandler/gatewayを閉じて終了値2です。回収未確認は1へ変更します。
初回成功後だけ一つのgoroutineで待機とrefreshを直列に実行します。
継続中の取得失敗は固定診断を出しますが、元のsnapshot期限も次回待機時間も変えません。
静的modeは取得、timer、schedulerを開始しません。refresh ownerのない `tether-echo-probe` は新sourceを明示拒否します。

親contextの取消・期限は待機と取得へ伝播します。
listen失敗、Serve異常終了を含む全returnでownerを取消し、goroutine終了を最大10秒待ちます。
終了未確認を成功扱いせず、終了値1と固定診断を返します。停止のretryはしません。
HTTP・登録・gatewayの既存終了順序は維持し、その後owner joinを確認してCLIが返ります。
静的modeと同じく、鍵更新はMacの同意、5分のcontrol generation、15分の開発helper寿命を延長しません。

## 配置前の残る条件

通常transportはsystem rootsだけを使います。
現在の `deploy/cloudflare/Dockerfile.gateway` は `FROM scratch` にbinaryだけを置き、CA trust bundleを含みません。
このimageのままでは新方式のTLS検証は成立せず、初回取得でfail-closedになります。
信頼する公開CA bundleのpackagingまたは読取専用mountを別途レビュー・検証する必要があります。
本変更はDockerfile・OS trust・network・proxy・実設定を変更せず、TLS検証無効化や任意CA overrideも追加しません。

MaxAge以内でも既にprovider側で退役した鍵が最後のsnapshotに残る可能性があります。
このretirement遅延の上限と可用性のtradeoffを、Cloudflareの重複期間から自動決定しません。
外部障害で期限切れになれば認証は止まります。未知kidで即時refreshしないため、次回成功まで新鍵tokenを拒否する場合があります。

## 一次資料

2026-11-11確認: [Cloudflare Validate JWTs](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/validating-json/)。
既定の署名鍵rotationは6週間、前鍵の有効期間はrotation後7日とされ、certs文書はJWKの `keys` とPEMの `public_cert` / `public_certs` を持ちます。
この7日をlocal cacheのstale猶予にしません。[Access keys API](https://developers.cloudflare.com/api/resources/zero_trust/subresources/access/subresources/keys/) は設定/強制rotationの別操作で、本候補から呼びません。

## 検証の境界

通常CLIの文書受理と初回失敗での待受拒否は、同じexecute境界のbehavioral RED→GREENを記録します。
追加回帰は3入口の直列更新、初回取消、listen失敗後のowner回収、途中取得のcancel/join、固定診断、曖昧設定拒否です。
設定→実候補verifierの構成も匿名RSA/JWT、memory HTTP transport、偽時計で確認し、初回拒否、鍵重複期間、旧鍵退役、未知kid、refresh失敗、期限/時計逆行、redirect、過大応答、cancel/timeout/同時呼出を検査します。
HTTP fixtureはTLS通信やCloudflareの実応答の成立を示しません。通常transportの安全設定は構造検査です。
既存gateway/CLI回帰、race、vet/build、独立review、hosted CIはそれぞれ実行結果を別途記録し、未実行を成功扱いしません。
