# Access署名公開鍵の更新候補

[gatewayの現行契約](agent-gateway.md) / [Go CI](go-ci.md)

## 範囲

[Issue #337](https://github.com/O2-CONNECTIVE/Tether/issues/337) / R-14へのsource-level候補です。`NewRotatingAccessVerifier` は既存の静的検証器と別の明示constructorです。
CLI、設定JSON、gateway起動、実Cloudflare、配置、credential、常時運用へは接続しません。
既存 `NewAccessVerifier` と `access_keys` の静的運用は不変です。
refresh呼出を所有するschedulerとshutdown/join、運用値、実到達性は未採用・未検証です。

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

## 運用前に決める項目

`AccessKeyRefreshPolicy` の `Timeout`、`MinInterval`、`MaxAge` は全て必須で既定値なしです。
`0 < Timeout < MinInterval < MaxAge` を要求しますが、数値を製品の安全/可用性方針として採用していません。
更新schedulerの間隔、許容する取得障害の長さ、期限切れの観測/通知、shutdown回収を運用側で決める必要があります。
試験の1秒/1分/10分などは匿名fixture値で、推奨運用値ではありません。

MaxAge以内でも既にprovider側で退役した鍵が最後のsnapshotに残る可能性があります。
このretirement遅延の上限と可用性のtradeoffを、Cloudflareの重複期間から自動決定しません。
外部障害で期限切れになれば認証は止まります。未知kidで即時refreshしないため、次回成功まで新鍵tokenを拒否する場合があります。

## 一次資料

2026-11-11確認: [Cloudflare Validate JWTs](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/validating-json/)。
既定の署名鍵rotationは6週間、前鍵の有効期間はrotation後7日とされ、certs文書はJWKの `keys` とPEMの `public_cert` / `public_certs` を持ちます。
この7日をlocal cacheのstale猶予にしません。[Access keys API](https://developers.cloudflare.com/api/resources/zero_trust/subresources/access/subresources/keys/) は設定/強制rotationの別操作で、本候補から呼びません。

## 検証の境界

匿名RSA/JWT、memory HTTP transport、偽時計で初回拒否、鍵重複期間、旧鍵退役、未知kid、refresh失敗、期限/時計逆行、redirect、過大応答、cancel/timeout/同時呼出を検査します。
HTTP fixtureはTLS通信やCloudflareの実応答の成立を示しません。通常transportの安全設定は構造検査です。
既存gateway/CLI回帰、race、vet/build、独立review、hosted CIはそれぞれ実行結果を別途記録し、未実行を成功扱いしません。
