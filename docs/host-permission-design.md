# ホストidentityと利用権限の分離案

現行main 53211a34 の principal→1 host 登録と署名bindingを維持したまま、次段の共通権限モデルを整理します。製品仕様の全確定や登録機能の完成ではありません。

- Host identityは端末鍵・安定ID。人のSSO account、操作するclient、許可を分ける。
- 一時接続はホスト主導。本人確認した相手へ今回の有限範囲を許可し、常設への昇格は別承認。
- 常設はホスト側上限とaccount側利用権限、今回のsession/client範囲の共通部分。双方が拒否・失効できる。「共同管理」は相互にPCを操作する意味ではない。
- SSO成功、同じemail、端末の表示、IDの所持だけでは操作を許可しない。issuer+subjectを内部userへ対応づけ、account linkingは別手続き。
- 表示、状態、結果、実行、取消、共有管理を分ける。host local OFF・停止とunknown owner保持を維持。

### 第一PRの境界
新しい内部の純粋モデルと拒否テストのみ。ネットワーク・永続保存・鍵発行・token検証・新規grant・実runtime callerを追加しません。入力の真正性はこのモデルで証明できず、将来の認証済み組成が供給する必要があります。製品gateは解除しません。

### 後続の依存順
1. 内部型/判定と拒否試験。
2. 旧1対1を権限拡大なしで表現する互換adapterと同値試験。
3. 登録/失効registry、版付きhost/control/work protocol。既存署名からprincipalを削除する移行はしない。
4. SSO・所持証明・共有・account切替の製品導線。
5. CLI/MCP/GUI、匿名CIから実認証/実hostへ段階受入。

### 未決
管理者/復旧主体、client identityの信頼根拠、権限失効の伝播時間とoffline方針、鍵rotation、複数管理者、保存先/監査期間、SSO provider設定。未決を広いallowや自動fallbackで埋めない。

### 必須受入
別user/client/hostの情報・結果の非公開、旧policy/世代・失効/期限・replay拒否、account切替で旧session権限非継承、双方から新規admission停止、取消受付と実回収の区別、unknown時の元owner保持、旧MCP互換。

#189 はUX/製品受入、#22 は一時接続、#188 は操作別権限/公開情報、#190 は段階接続検証。進行中GUI #295 は既存契約のまま継続し、この変更の依存にしません。PR作成後ここへ紐づけます。

## 設計上の境界

ホストの安定IDと端末鍵は人のaccountから独立させる案です。ただし管理主体なしで誰でも接続できる意味ではありません。ローカル管理者または管理組織による登録・復旧の責任が必要です。複数利用者の共有を自動的に有効化しません。

純粋判定の「条件内」は認証成功・実行許可の証明や再利用できるcapabilityではありません。入力の本人性、鍵所持、policyの真正性・最新性、失効伝播を確立する組成は後続です。判定に用いる有限の有効窓は、常設接続全体を一定時間で終了する仕様ではありません。

一時接続でも利用者とclientの本人確認は必要です。account側の常設共有policyを持たないことと匿名操作の許可を混同しません。常設では事前に承認した範囲の操作ごとに手動承認を要求せず、範囲拡大と新clientの扱いを別に決めます。

account切替では旧sessionとcacheを分離し、新accountの許可されたhostだけを取得します。進行中作業を新accountへ付け替えません。ログアウト、待機OFF、個別取消、共有解除、端末登録解除は別の操作です。失効直前の作業に対する停止方針と回収観測は別々に定義します。

端末一覧の既定情報は表示名、OS種別、接続状態、確認時刻、利用可能操作に限定する案です。IP、OS username、serial、個人path、秘密、他利用者の結果を既定公開しません。接続状態と操作可能状態を分け、host申告値を強いattestationと表示しません。

## 現行sourceへの対応

- [resident登録](../backend/internal/agentgateway/resident.go)はprincipalごとに1hostを保持します。
- [resident transport](../backend/internal/agenttransport/resident_mac.go)は登録済みhost鍵・principal鍵を使います。
- [署名intent](../backend/internal/residentwork/intent.go)はendpoint・host・principalを束縛します。単純なfield削除では移行しません。
- [認証仕様](agent-gateway.md)のAccess subjectは人の照合に用います。同じ本人の別clientを区別できる証拠は別途必要で、自己申告headerをその代用にしません。
- [通常CLI](foreground-host-cli.md)と[仕事CLI](work-control-cli.md)はproduction trustを未構成のまま拒否します。

## 追跡先

[#32 共通設計](https://github.com/O2-CONNECTIVE/Tether/issues/32)、[#189 製品受入](https://github.com/O2-CONNECTIVE/Tether/issues/189)、[#22 一時接続](https://github.com/O2-CONNECTIVE/Tether/issues/22)、[#188 操作辞書](https://github.com/O2-CONNECTIVE/Tether/issues/188)、[#190 接続検証](https://github.com/O2-CONNECTIVE/Tether/issues/190)。この文書は提案と分割計画であり、SSO設定、登録機能、共有、GUI配布、実接続の完了証跡ではありません。
