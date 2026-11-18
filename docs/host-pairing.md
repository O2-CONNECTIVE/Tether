# ホストのペア登録

[[agent-gateway]] / [[agent-cli-resident]] / [[linux-container]]

この文書は、gateway側でホスト鍵を登録する契約と、ホストの`pair`コマンドの使い方を記載します。Worker（PR B）とホストのpairコマンド（PR C）は実装済みです。実IdP・実配置・Worker経由の通しは未確認です。

## 登録の流れ

1. ホストで `tether-host-pair` を実行します。
2. ホストはIdPのログインを経て、pairing用tokenを取得します。
3. ホストは新しいEd25519鍵でproofを作り、`POST /tether-agent/v1/pair`へ送ります。
4. gatewayは設定済みの登録簿へ新しい鍵を保存し、登録情報を返します。
5. 以後の接続は現在のホスト鍵を使うhandshakeで認証します。

この手順の実IdP連携とWorker経由の通信は確認していません。


## ホストでの実行

次の3つを指定します。`--gateway` は `https://host[:port]` 形式です。path、query、fragmentは指定できません。`--issuer` はIdPのHTTPS issuer URLです。`--client-id` はpublic clientのIDです。

```sh
tether-host-pair --gateway https://gateway.example --issuer https://idp.example/tenant --client-id host-client
```

ブラウザでIdPにログインすると、新しいEd25519鍵と常駐用policyが固定の配置先に保存されます。同じ利用者のpairを同時に実行すると、directory内のlockで後続を拒否します。成功後は常駐プロセスを止めて起動し直してください。

ログイン前にlockと既存policyの保存先保護を確認し、確認できない場合は「保存先の保護を確かめられませんでした。何も変えていません」と表示してbrowserやnetworkを使わず終了します。

4xx拒否では前のpairは変わりません。5xx、timeout、通信切断、不正な成功応答ではgateway登録の成否を判定できません。登録済みなら前の鍵は使えなくなっているため、もう一度pairを実行してください。pairが管理する旧鍵だけを削除します。`~/.ssh`など手で置いた鍵は削除しません。

## 登録口

登録口は設定に`pairing`がある場合だけ有効です。methodは`POST`だけです。それ以外は`405`で`Allow: POST`を返します。本文上限は4 KiBで、超過は`413`です。

要求はBearer tokenを1つと、次のJSONを送ります。未知memberや不正JSONは拒否されます。

```json
{
  "host_public_key": "ssh-ed25519 <base64>",
  "proof": "<SSH wire形式の署名をbase64url（paddingなし）にした値>"
}
```

`host_public_key`は正規形のSSH Ed25519公開鍵です。`proof`は新しい鍵によるEd25519署名です。署名対象は、各fieldをuint32 big-endian長さ付きで連結した次の値です。

1. `rescue-host.agentgateway.pair\u0000v1\u0000`
2. 登録口の公開URL（MCP resourceのoriginと`/tether-agent/v1/pair`）
3. SSH wire形式のホスト公開鍵
4. 送信Bearer tokenのSHA-256（32 byte）

登録口の公開URLは設定のMCP resourceから作り、要求の`Host`やURLから作りません。Workerは内部のURLへ転送するため、要求のURLは公開URLと一致しません。

| 状態 | 意味 |
| --- | --- |
| 200 | 登録成功。JSONで`registration_endpoint`、`host_label`、`principal_label`、`principal_public_key`を返します。公開鍵は`ssh-ed25519 <base64>`形式です |
| 400 | JSON、member、公開鍵、またはproofが不正です |
| 401 | Bearer tokenが欠落・重複・無効です。`WWW-Authenticate`に`invalid_token`を返します（headerなしの場合は`Bearer`のみ） |
| 403 | 検証済みsubjectに対応するprincipalまたは登録枠がありません |
| 405 | POST以外です。`Allow: POST`を返します |
| 409 | 鍵が他host/principal鍵と重複するか、対象principalが作業中です |
| 413 | 本文が4 KiBを超えています |
| 503 | 登録簿の保存に失敗しました。鍵と登録内容は旧状態のままです |

pairing verifierはBearer用OAuth設定から作られます。MCP verifierはpairing audienceを含むtokenを拒否します。pairing tokenの有効期間は最大600秒で、audienceは設定値1つだけです。

## 置換と接続

登録枠のprincipalに未回収作業がある、routeがある、または接続中の場合、置換は`409`です。保存中と旧接続の切断が終わるまでは枠をbusyとして扱い、新しい作業開始と新しいConnectを拒否します。

保存が成功して新鍵を反映した後、対象hostのtransport接続をすべて切断します。以後は新しい鍵でhandshakeします。旧鍵の接続は継続しません。旧MCP sessionは旧作業の結果照会だけに使え、新しい作業は開始できません。

## 設定

保護設定のtop-levelに任意の`pairing`を置きます。指定する場合、4 memberすべてが必須で、未知memberは拒否されます。

```json
{
  "pairing": {
    "registry_path": "/absolute/path/registry.json",
    "audience": "pairing-audience",
    "client_id": "pairing-client",
    "token_types": ["at+jwt"]
  }
}
```

`hosts`項目は`public_key`か`"paired": true`のどちらか一方です。`paired`は`true`以外の値を受け付けません。paired枠はprincipalちょうど1人を持ち、そのprincipalの`resident.host_label`は同じhost labelを指します。

設定エラーとなる条件は次のとおりです。

- paired hostがあるのに`pairing`がない。
- Access方式で`pairing`を指定する。
- `pairing.audience`がMCP audienceと同じ。
- `audience == client_id`で、`token_types`に`JWT`（大文字小文字と`application/`を正規化して`jwt`。tokenの検証と同じ正規化）が含まれる。
- `/tether-agent/v1/pair`がMCP resource pathまたは登録経路pathと同じ（metadata pathは`/.well-known/`で始まるため一致しません）。
- `registry_path`が設定または鍵のpathと同じ。

pairing用tokenはMCP入口に通しません。`audience`はMCP audienceと別にし、`client_id`とtoken typeの組合せにも上記制約を守ってください。

## 登録簿

JSONは`hosts`配列を持ち、各recordに`label`、`public_key`、`fingerprint`、`issuer`、`subject`、`registered_at`を含みます。`registered_at`はRFC 3339 UTCです。起動時、形式不正、未知またはpairedでないlabel、同じlabelの2回、鍵重複、principal鍵との重複、fingerprint不一致、issuer/subject不一致があれば拒否します。登録簿のファイルが無ければ、すべての枠が空のまま起動します。

登録簿は設定・鍵とは別の保護対象です。native Unix storeは`<registry_path>.lock`を同じディレクトリに作り、`O_RDWR|O_CREATE|O_NOFOLLOW`・0600で開いて非blocking exclusive flockを取ります。取得できなければ起動しません。空のlock fileには固定1行を書いてfsyncします。lockと登録簿は保護検査を行います。

保存はlockと既存の登録簿の保護検査後、同じディレクトリの0600一時ファイルへ書き、fsyncしてrenameで確定し、親directoryをfsyncします。rename失敗はerrorで、旧登録簿を維持します。親directory fsync失敗は診断を出してnilを返し、rename済みの新登録簿を採用します。非Unixではpairing設定時に起動できません。

## 脅威と確認範囲

次のリスクは受け入れています（[#391](https://github.com/O2-CONNECTIVE/Tether/issues/391)の計画第5節）。

- 利用者のIdPの資格情報が盗まれると、攻撃者は自分のホストをその人のペアにできます。本人のホストは切れるので気づけます。IdPのMFAに頼ります。
- pairing用tokenは有効期間（最大600秒）の間に盗まれると使われ得ます。誤った`--gateway`に渡したtokenも同じです。tokenをgatewayに結び付けられるかはIdPの対応次第です。
- 作業中に端末を紛失すると、その作業の回収を確認できないため置換は`409`になります。運用者がgatewayを再起動する必要があります（回収未確認の作業の今の扱いと同じ）。
- `pair`の応答を失うと、前のペアが使えなくなったまま、もう一度`pair`を実行するまで接続できません。
- IdPでユーザーを無効化しても、登録簿の鍵は残ります。MCPクライアントのtokenがIdPの有効期間で切れると作業は届かなくなります。gatewayが許すMCP tokenの有効期間の上限は24時間のままです。
- 登録簿の親ディレクトリのfsyncの失敗と電源断が重なると、登録簿が旧内容に戻り、旧鍵が再び有効になり得ます。

実配置でpairingを有効にする前に、IdP側で次を確かめます（計画第6節）。

- ホストアプリをpublic clientとして登録でき、loopback redirectのportを任意にできるか（RFC 8252 7.3節）。
- ホストアプリ宛てのaccess tokenの`aud`が、MCP宛てともホストアプリの`client_id`とも別の値になるか。同じなら、access tokenの`typ`が`at+jwt`か。どちらも満たさなければ、設定の検査で起動できません。
- resource indicator（RFC 8707）で、tokenをgatewayの登録口に結び付けられるか。

## 対象外

解除用DELETE、GUI、Windows、Secure Enclave、実IdP・実配置の確認はこの範囲に含みません。実IdP・実配置の確認はHITL:Verificationです。
