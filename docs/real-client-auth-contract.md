# 実client接続とcredentialの境界

[#190 接続検証](https://github.com/O2-CONNECTIVE/Tether/issues/190) / [#32 共通サービス](https://github.com/O2-CONNECTIVE/Tether/issues/32)の準備です。基点はmain `46d78377`。実ログイン・登録・トンネル作成・credential読取や保存の許可ではありません。native owner / publisher / enrollmentと実client認証は別の条件です。

## 具体的なtransport

| client / 経路 | 確認できたこと | 未確認・未提供 |
|---|---|---|
| Tether `tether-work` | private residentclientは明示CAの検証済みHTTPS、Streamable HTTPのPOST/DELETE、固定MCP `2025-06-18`、同じSID。redirect/proxy envを使わない | 通常entryは拒否。実credential入力・OAuth login/refresh・保存は未提供 |
| 公式Codex CLI | 公式資料はStreamable HTTPとOAuth、設定したbearerを送る方式を記載 | 利用する実version、callback、対象provider、実接続は別途確認。CLIの確認をdots cloudへ転用しない |
| dots / ChatGPT | 対応surfaceのcustom MCP接続が候補 | この利用者の接続設定・callback・権限・採用MCP版をこの調査では確認していない |
| 公開HTTPS | 既存gatewayを到達可能なHTTPS経路へ置く案。Access方式ならedgeでOAuth tokenを検証しoriginは署名assertionを検証 | endpoint公開や設定変更は別承認。TLS終端と認証、host WSSを同一視しない |
| Secure MCP Tunnel | 公式公開のprivate MCP経路。ネットワーク内clientから外向きに接続する候補 | このworkspace/orgでの利用可は未確認。API keyやtunnel作成を行っていない |

host登録/controlのWSSはMCP clientのStreamable HTTPとは別経路です。トンネルが通ってもprincipal登録・host鍵・operation policyを代替しません。

## 認証方式と必要な最小入力

[gateway認証](agent-gateway.md)は保護設定で一方式だけを選び、fallbackしません。

- bearer JWT方式: 対象resource、issuer、audience、scope、受理token type、pin済み公開鍵と登録subjectの一致が必要。GoogleのID tokenや任意API keyを代用できるとは仮定しません。
- Access方式: client→edgeのOAuth tokenとedge→originの`Cf-Access-Jwt-Assertion`は別物。gatewayはassertionの署名・issuer/AUD・期限・登録subjectを検証。cookie、email header、clientが自己申告したidentityでは認可しません。
- host側: 保護policyと登録済みhost/principal鍵の対応が別途必要。SSO成功から鍵・登録・操作許可を生成しません。

実評価票は、client製品/version、resourceと接続経路、auth mode、公開trust情報、登録対象、credentialを取得/受渡しする責任主体、保存の有無、expiry/revocation、独立停止手段、source/artifact版を固定します。secret値を票・argv・issue・原ログに載せません。安全な受渡し方法が未確定なら通常入口を開けません。特定providerの利用を前提にするのは実仕様を照合してからです。

## 期限・失効・終了は別のもの

1. gatewayは各HTTP要求を再認証。期限切れbearerは401、Access assertionは403。token期限とMCP session idle期限、host/control世代、operation期限は別です。
2. 認証失敗は新しい操作・status・cancel・DELETEを通さない一方、既存作業の物理停止を証明しません。資格情報が切れた後の停止を、その資格情報でのremote cancelだけに依存させません。
3. 有効な同主体credentialをserverが同SIDで受理することと、clientがrefreshできることは別。residentclientは失敗後のtool callを止め、元SIDのCloseだけを残し、再initialize/refresh/rebindしません。元credentialの失効後はそのDELETEも拒否され得ます。
4. 有効なDELETEで終わるのはMCP session。host grantや実行作業は終了しません。OFF/取消と元ownerの回収観測が別途必要です。
5. verifierは静的公開鍵/設定とclaimsを照合します。providerのlogout/失効をonline introspectionで即座に検出する契約はありません。Access edgeの状態、JWT期限、gateway設定、host側失効の各境界を別に評価します。短いtoken期限だけで即時失効を保証しません。
6. credential参照を破棄してもメモリの完全消去保証ではありません。現residentclientにはissuerやrefresh token保存機構はありませんが、外部OAuth client側の保存・削除はその製品の契約で確認します。

## 今回のsource / CI範囲

新しい[Access lifecycle回帰](../backend/internal/agentgateway/access_lifecycle_contract_test.go)は、実HTTP handlerに合成assertionを渡し、作業中の期限切れstatus/cancel/DELETE拒否、別主体のSID利用拒否、同主体の現在credentialによるSID保持と明示DELETEを確認します。拒否とDELETEが作業取消を捏造しないことを分けます。memory channel/fake processであり実Access edge、OAuth refresh、provider失効や実host停止の受入ではありません。

既存Bearer側のOFF/expiry回帰・discovery両方式の期限拒否と併せ、認証方式ごとの契約を明確にする後付けcharacterizationです。production、residentclient、CLI、native owner、公開API、credential設定は変更しません。

## 公式参照と適用範囲（2026-11-09確認）

- [Codex MCP](https://learn.chatgpt.com/docs/extend/mcp?surface=cli): Streamable HTTP、OAuthとbearer設定の仕様。設定可能な入力方法を、そのままTetherの保存/受渡し設計の採用とはしません。
- [Secure MCP Tunnel](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels): private server向けの経路。tunnel identityとruntime API key、組織のRead/Use権限等が必要。OAuth discoveryは中継可能でもauthorization serverを自動的にトンネル化しません。公開plugin配布の代替にはしません。現在のaccount適用可は別途確認します。

実資格情報の発行/配置や継続アクセスが必要になった段階で、対象・用途・保存期間・失効/削除・公開範囲を明示して必要な承認を取ります。現在の準備からその承認を推定しません。
