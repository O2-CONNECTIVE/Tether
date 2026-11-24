# ホストのペア登録

[[agent-gateway]] / [[agent-cli-resident]] / [[linux-container]]

この文書は、gatewayで常駐ホスト鍵を登録するHTTP契約を記載します。このPRではgateway側を実装します。`tether-host-pair`、Worker、Keychain、Tauriの挙動は変更しません。実IdPと実配置を使った確認は未実施です。

## 二段階の登録

1. 常駐ホストは新しいEd25519鍵を作り、pairing用Bearer tokenと鍵のproofを `POST /tether-agent/v1/pair/operations` に送ります。gatewayはアカウント、登録枠、busy状態、鍵の重複、登録簿revisionを確認し、表示用情報と一時operationを返します。この段階では登録簿を書きません。
2. 利用者のブラウザで応答の `consent_url` を開きます。gatewayは設定済みIdPへAuthorization Code + PKCE S256で遷移し、callbackでcodeを交換してpairing verifierでtokenを検証します。issuerとsubjectがoperationのアカウントと一致すると、確認ページを表示します。
3. 確認ページにはアカウント、登録枠、置換する鍵と登録日時（または「新規」）、新しい鍵のfingerprintが表示されます。利用者が「登録を確定」または「取り消す」を選びます。最初の決定だけ有効で、同じoperationの他のform tokenも失効します。
4. 確定後、ホストはoperationと新しい鍵で署名したproofを `POST /tether-agent/v1/pair/confirm` に送ります。gatewayは同意、鍵、期限、元の登録簿revision、busy状態を再確認してから登録簿を保存し、鍵を反映して旧ホストの接続を切断します。

operationはgatewayのメモリにだけ保存され、有効期間は10分です。同じ枠の新しい事前確認は前の未完了operationを無効にします。gateway全体で最大64件です。OAuth stateとform tokenもメモリ上に保持し、それぞれgateway全体で最大64件です。再起動後の再送は、登録簿のoperation IDとproofで完了済み登録を照合します。

## HTTP契約

全ての公開URLはMCP resource設定のoriginから作ります。要求の `Host` やURLから公開originを組み立てません。

### `POST /tether-agent/v1/pair/operations`

Bearer tokenを1つ付け、次のJSONを送ります。未知memberや不正JSONは拒否されます。

```json
{
  "host_public_key": "ssh-ed25519 <base64>",
  "proof": "<SSH wire形式の署名をbase64url（paddingなし）にした値>"
}
```

proofの署名対象は、各fieldをuint32 big-endian長さ付きで連結した値です。

1. `rescue-host.agentgateway.pair-operation\u0000v1\u0000`
2. operationsの公開URL
3. SSH wire形式の新しいホスト公開鍵
4. Bearer tokenのSHA-256（32 byte）

成功応答には `operation`、アカウント（issuer、subject、principal label）、`host_label`、置換対象、`new_key_fingerprint`、`consent_url`、期限を含みます。busyなら `409 replace_blocked`、既存鍵と重複すれば `409 key_in_use` です。事前確認は登録簿を変更しません。

### `GET /tether-agent/v1/pair/consent?operation=...`

gatewayはIdPのauthorization endpointへ302で遷移し、stateとPKCE verifierを生成します。`__Host-tether-pair-consent` cookieはSecure、HttpOnly、SameSite=Lax、Path=/で設定されます。callbackと決定POSTは、state/form tokenに結び付いた同じcookieを要求します。

### `GET /tether-agent/v1/pair/consent/callback`

gatewayはstateを一度だけ消費し、cookieを照合してtoken endpointでcodeを交換します。pairing verifierで検証したissuerとsubjectがoperationと一致しなければ確認ページを表示しません。確認ページは `Cache-Control: no-store`、`X-Frame-Options: DENY`、`Content-Security-Policy: default-src 'none'; form-action 'self'; frame-ancestors 'none'` を返します。

### `POST /tether-agent/v1/pair/consent`

`application/x-www-form-urlencoded` の `operation`、`form_token`、`decision=confirm|cancel` を受け付けます。確定前は登録簿を変更しません。取消後のconfirmは `403 consent_cancelled` です。

### `POST /tether-agent/v1/pair/confirm`

Bearerなしで `{ "operation": "...", "proof": "..." }` を送ります。proofは各fieldをuint32 big-endian長さ付きで連結した次の値への署名です。

1. `rescue-host.agentgateway.pair-confirm\u0000v1\u0000`
2. confirmの公開URL
3. operation ID
4. SSH wire形式の新しいホスト公開鍵

| 状態 | 意味 |
| --- | --- |
| 202 `consent_pending` | ブラウザでの決定待ち |
| 200 | 登録完了。既存pairと同じ `registration_endpoint`、`host_label`、`principal_label`、`principal_public_key` を返します |
| 403 `consent_cancelled` | 利用者が取消を選択済み |
| 404 | 未知、期限切れ、または無効化されたoperation |
| 409 `revision_changed` | 表示後に登録枠が変わりました |
| 409 `replace_blocked` | 確定時点で枠がbusyです。結果は終端化されます |
| 409 `key_in_use` | 新しい鍵が別hostまたはprincipal鍵と重複します |
| 503 `save_failed` | rename前の保存失敗。旧登録内容を保ち、同じ結果を返します |
| 500 `registration_unknown` | rename後の親directory fsyncが失敗し、永続化を確認できません。同じoperationの再送も500です |

期限後・置き換え後のoperation IDに対する404は、「今は登録されていない」ことが確定した結果です。helperは通常の拒否と同じ扱いにします。

同じoperationへの並行confirmや再送は、確定を最大1回にします。MCP用Bearer tokenはpairing用APIで使えません。pairing verifierはissuer/subjectでアカウントを特定し、pairing audienceのtokenをMCP入口に通しません。

### 一段階APIとの互換

`POST /tether-agent/v1/pair` は `tether_development` buildでだけ有効です。開発buildでの既存挙動は維持します。製品buildではrouteを登録せず、404になります。`tether-host-pair` はこのPRで変更していないため、製品buildの二段階API呼び出し元にはなりません。

## 設定

保護設定のtop-levelに `pairing` を置きます。`registry_path`、`audience`、`client_id`、`token_types`は従来どおり必須です。製品buildでは `consent` も必須です。開発buildでは既存の一段階pair設定を維持できます。

```json
{
  "pairing": {
    "registry_path": "/absolute/path/registry.json",
    "audience": "pairing-audience",
    "client_id": "pairing-token-client",
    "token_types": ["at+jwt"],
    "consent": {
      "client_id": "browser-public-client",
      "authorization_endpoint": "https://idp.example/authorize",
      "token_endpoint": "https://idp.example/token"
    }
  }
}
```

`consent`は3 member全て必須で、未知memberを拒否します。authorization endpointとtoken endpointはHTTPSでなければなりません。callback redirect URIとconsent URLはMCP resourceの公開originから作られます。

ブラウザでのログイン後、gatewayはIdPのtoken endpointへHTTPS接続してauthorization codeを交換します。この接続には信頼済みCAが必要です。`deploy/cloudflare/Dockerfile.gateway`のscratch imageにはCA bundleが含まれないため、確認画面を有効にする配置ではCA bundleの用意が前提です。配置手順の変更はこのPRでは行いません。

paired枠のprincipalは `resident_owner` の役割を持つ必要があります（[principalの役割](agent-gateway.md#principalの役割)）。`hosts`項目は `public_key` か `"paired": true` のどちらか一方です。paired枠はprincipalちょうど1人を持ち、そのprincipalの `resident.host_label` は同じhost labelを指します。

設定エラーとなる条件は次のとおりです。

- paired hostがあるのに `pairing` がない。
- 製品buildで `pairing.consent` がない。
- Access方式で `pairing` を指定する。
- `pairing.audience` がMCP audienceと同じ。
- `audience == client_id` で、`token_types`にJWTが含まれる。
- `/tether-agent/v1/pair` または二段階APIのpathがMCP resource pathまたは登録経路pathと同じ。
- `registry_path` が設定または鍵のpathと同じ。

## 登録簿と永続化

JSONは `hosts` 配列を持ち、各recordに `label`、`public_key`、`fingerprint`、`issuer`、`subject`、`registered_at`を含みます。二段階pairのrecordには任意の `operation` も保存します。既存登録簿のoperationなしrecordも読み込めます。revisionはrecordを正規化したJSONのSHA-256 hexです。

起動時は既存の登録簿fileと親directoryをfsyncします。どちらかが失敗したら起動しません。保存は同じdirectoryの0600一時fileへ書き、fileをfsyncしてrenameし、親directoryをfsyncします。rename前の保存失敗は旧登録内容を保ちます。二段階pairでrename後の親directory fsyncが失敗すると、メモリ上のrecordを永続化未確認として扱い、成功を返しません。一段階pairの従来動作（親directory fsync失敗時は診断を出して成功扱い）は変えません。非Unixではpairing設定時に起動できません。

## busy判定と接続

登録枠のprincipalに未回収作業、route、接続がある間は置換できません。事前確認と確定の両方でbusyを確認します。保存中および旧接続の切断が終わるまで枠をbusyとして扱います。保存と鍵反映の後、対象hostのtransport接続を切断します。

## 確認範囲

合成IdPとTLS loopbackのgateway試験で、PKCE、cookie/state、アカウント一致、二段階状態、競合・再送、revision、fsync失敗を検証します。実IdPと実配置、ブラウザGUIからの一連の操作は別途確認が必要です。
