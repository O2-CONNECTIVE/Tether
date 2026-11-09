# 旧resident登録と権限モデルの移行境界

[#32](https://github.com/O2-CONNECTIVE/Tether/issues/32)の後続調査です。
[分離設計](host-permission-design.md)と[純粋モデル](../backend/internal/permissionmodel/model.go)は統合済みですが、旧登録から新しい認可を自動生成するadapterはまだ追加しません。

## 現行入力と不足

| 旧sourceが持つ情報 | 新モデルへ自動変換できないもの |
|---|---|
| `Principal.Label`、検証対象`Subject`、登録済み`Signer` | account統合後の安定UserID、別clientの認証済みClientID。label・email・MCP session IDをそのまま代用しない |
| `ResidentEnrollment`のhost label、principal label、host公開鍵 | accountから独立した安定HostID、鍵rotation/移管履歴、信頼できるidentity registry |
| host policyのbinding、peer鍵、固定operation/command digest/timeout | 管理側の独立した共有policy、host/accountそれぞれのrevision、失効状態と最新性 |
| control generation、request/operation ID、作業期限 | 新しいpolicy revisionや比較窓。generationをrevision、command timeoutを常設接続寿命へ置換しない |

旧identityと新identityの対応を任意入力で詰めるだけでは、その対応の真正性を証明しません。
旧host policyをhost/accountの両方へ複製して積集合を通しても、双方の独立した承認にはなりません。
不明なclientやrevisionへ固定値を補って`WithinBounds`を返す移行は行いません。

## 今回固定する現行挙動

[`resident_enrollment_compat_test.go`](../backend/internal/agentgateway/resident_enrollment_compat_test.go)は既存の実`New` / `ConfigureResident`を通す後付けcharacterization回帰です。新機能のred→greenや、新旧認可の同値証明とは扱いません。

- 不明/署名鍵なしprincipal、二重principal slot、不正host label/鍵を拒否。途中まで正常な複数entryでも、失敗した設定を部分導入しない。
- 初回成功後の再設定を拒否し、元の登録を保持。入力のslice/鍵bytesを変更しても登録済み値に反映しない。
- 同じhost label/公開鍵を別principalへ指定することは現登録mapでは受理するが、entryとwork mapと鍵bytesは独立。これは製品の共有許可・端末所持・同時接続・実行成功の証拠ではない。
- 合成bearerでHTTP initializeし、**現在保持中**のdiscovery-only sessionがあるときはresident設定を拒否し、sessionを後付けbindingしない。

最後の条件は実装の`len(g.sessions) != 0`に対応します。「過去に一度でもsessionを作ったら永久に設定不能」とは主張しません。
別principal/sessionや世代の実行結果分離は既存のHTTP認証・resident結果試験に残し、今回の登録回帰をその代用にしません。
合成鍵・memory HTTPのみで、listener、外部認証、ホスト起動、実credentialを使いません。production source、inventory、署名、公開API、通常起動gateは変更しません。

## 次に決める接点

1. 認証経路が確実に識別できるuser/clientと、旧principalからの対応を管理する主体。Access subjectだけで別clientを識別できるとは仮定しない。
2. host鍵に対応する安定HostIDと、再登録/rotation/復旧時の失効契約。
3. host側上限と管理側共有範囲の独立した取得元、revision、freshness、失効伝播。offline中の猶予は未決。
4. 既存の固定operationへ新しい操作カテゴリを対応づける範囲。包括的Executeで任意commandを許可しない。
5. 新規admissionと判定の原子性、停止/回収unknown、旧sessionの保持結果を読む権限。旧作業を新accountへ移管しない。

これらの信頼できる入力が揃ってから、旧1対1の範囲を超えないadapterを導入し、同じ固定入力で旧拒否が新許可にならないことを検証します。旧署名bindingは別版の移行設計まで維持します。

#189の登録/共有/SSO、#22の一時ホスト主導、#188の操作別公開範囲、#190の実接続受入は未完です。GUI/Windowsの並行作業とは独立し、既存greenを新identity導入の成功へ加算しません。
