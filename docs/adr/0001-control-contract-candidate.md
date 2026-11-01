# ADR 0001: rescue-host-control/1 の実装候補

状態: **準備コードの実装候補。実backend activation・実機変更・接続の承認ではありません。**
日付: 2026-10-28。関連: [#17](https://github.com/O2-CONNECTIVE/rescue-host/issues/17)、[#36 DEC-01/02](https://github.com/O2-CONNECTIVE/rescue-host/issues/36)、既存提案（過去の参照資料・本文非同梱）。

## 正本と採用する技術範囲

正本は [schema.json](../../contracts/control-v1/schema.json) と [vectors.json](../../contracts/control-v1/vectors.json) です。
Go/Rust担当は同じ固定headを参照し、wire差分を正本のownerへ戻します。
製品用候補の名前は `rescue-host-control/1`。既存shell v1・試験fixture v1とは別で、暗黙fallbackはありません。
このADRはこれまでの比較案から実装境界を具体化しますが、認証や保護主体が成立したとの宣言ではありません。

JSON Schema 2020-12は復号後のshapeを定義します。raw UTF-8、重複key、LF、byte数、深さ、状態はcodec/stateで別に検査します。
LF込み16 KiB、rootを深さ1とするobject/array深さ8、1frame1object、BOM・途中EOF・途中の実LF・余剰JSONを拒否します。
CRLFのCRはJSON空白として許容します。未知/重複field、field名のcase違い、未知kind、不正型は拒否します。
JSON Schemaの整数判定で `1.0` を整数へ変換することはせず、連番・request ID・epochは正規uint64十進文字列です。
sequence/request IDは1以上、epochは0を含むuint64範囲とし、先頭0・符号・指数・小数・桁あふれを拒否します。
launchId/policyRefはASCII tokenの1–128 bytes、generationは正のuint64十進文字列、digest/nonceは64桁lowercase hexとします。
これはwireのsubsetで、既存Go Identity APIが許す値を正規化して広げません。設定値がwire制約に合わなければ開始前に拒否します。

## bootstrapとenvelope

保護された所有者から事前設定したgeneration、Identity、artifact hash、channel nonceを両端で照合します。
wire generationはsupervisor Binding.Generationと同じ値を表し、別の世代モデルや新しい発行権威を作りません。未信頼wireをProvisionedGrantへ直接変換しません。
ownerのhello→Goのhello_ackの2frameで同じexact protocolを確認し、その前の通常要求、交渉の繰返し、別versionへの切替を拒否します。
hash/nonceはchannel bindingであり、PID/pipe/同一userを含め相手の認証や隔離の証明ではありません。
未確定のservice identity、IPC承認経路、信頼元や実設定をこのADRで作成・採用しません。

確認後はprotocol/kind/generation/sequence/Identity/payloadを必須とし、要求と応答にはrequestIdを付けます。state_changed eventには付けません。
各送信方向のsequenceは1から連続増加し、bootstrapは含めません。欠落・重複・逆順・wrap・context不一致は閉鎖へ進めます。
requestIdは全操作共通の起動内単調IDです。schema/contextが正しい新IDは業務拒否でも最大値へ反映します。
同じID・同じ内容の保持済みstart/stop受付だけを返却可能とし、異なる内容や、保持していない最大値以下のIDは拒否します。
各1件の成功受付を起動回収まで保持し、eviction後の再実行・無制限台帳・終了後の復活を許しません。
再送frameにも新sequenceが必要で、古いreceiptを現在の状態や認可にしません。自動再試行は採用しません。

## 既存Go APIへの対応

| wire | 対応と守る条件 |
| --- | --- |
| start | 保護側の固定policyRef/Identityと準備条件を確認した後だけReady→Activateへ対応可能。現在のruntime gateは閉鎖し、実SessionのActivateを呼ばない |
| snapshot | Snapshotだけを読む。Go state/件数とGo Closeの確認を返し、Renewや期限延長を呼ばない。内部Reason/errorの文字列は返さない |
| stop | 同じIdentityへ不可逆なRevokeを要求し、Close(ctx)の結果を別に追跡。receiptは終了証明ではない。start準備中も止められる独立の受付を必要とする |

dispatcher/coordinatorは既存Sessionのmethod signatureへ対応するinterfaceを使い、純粋試験ではin-memory実装を注入します。
既存Session APIへ対応づけたコード、mock試験、実Sessionを起動/Activateした実証を混同しません。
GUI由来のrenew/TTL/任意path/引数/接続先/鍵を受けるwireはありません。lease更新者の保護契約が成立するまで製品startを許可しません。
supervisorを承認済みimmutable grant・generation・hard deadline・不可逆失効の権威とし、Goはその上限内で短leaseと現在のSession状態を強制します。
policyRefは保護側で検証済みの不変grant/方針への参照だけです。GoやUIがhard capを延長する経路、UI pollingによるrenew、二重の承認権威を作りません。
grantの保護された受渡し、時計の対応付け、具体serviceidentity/token/承認口は未決で、schemaのbindingやpolicyDigestだけから成立を推定しません。
通常queueは最大15frame/48 KiB、停止予約は1frame/16 KiB、合計16frame/64 KiBです。parse中buffer等は別計上で、総メモリ保証ではありません。
長いstartや通常queueの背圧で停止受付を塞がず、独立失効・物理回収はDEC-01の保護主体へ残します。

## errorと終了確認

既案にASCII codeの確定一覧はなかったため、既案の六分類に対応する候補名を正本へ固定します。
VERSION_MISMATCH、INVALID_MESSAGE、CONTEXT_MISMATCH、REQUEST_REJECTED、DEADLINE_EXCEEDED、TERMINATION_UNCONFIRMEDだけです。
内部errorやGo Reasonをcode/messageへ流さず、UI文言は別の固定表へ対応づけます。retryableはfalseです。
bootstrap前や相関を信頼できない不正入力には、未信頼IDをechoする応答を作らず閉鎖します。上のcodeはローカル観測にも使います。

Go stateはprepared/ready/active/revoking/terminatedです。Rustの応答待ち・停止要求済み・IPC異常・終了確認不能・process回収観測と別の正本です。
goCloseConfirmed=trueは同一IdentityのClose成功後だけで、stateはterminatedを要求します。Owned=0、pipe EOF、stop receiptだけから推定しません。
正常終了はさらに実EOF・reader完了・所有processのexit0と回収を必要とし、強制cleanup・確認不能を正常成功にしません。
実際に必要な所有範囲・保護supervisor/guestの回収証明はDEC-01と別の実機受入で確認します。

## 未達と実行gate

別保護supervisorと非管理者guestを分ける設計方向で進めますが、具体serviceidentity/IPC承認/ACL/設定値は設計中です。
同一user stdio/Jobだけで既存の別主体要件を代替せず、管理者guestを同OSserviceから隔離可能とは主張しません。
OS設定、アカウント/鍵/資格情報、サービス導入、実SSH、製品UI activation、公開serviceは実行しません。
P2専用runtime未実証、拒否された専用/代替再現の停止、double-close NOT_MET、Windows背圧UNVERIFIED、44条件と配布/本番の別判定、製品gate閉鎖を維持します。

根拠: [JSON Schema 2020-12 Core](https://json-schema.org/draft/2020-12/json-schema-core)、[RFC8259](https://www.rfc-editor.org/rfc/rfc8259)。
schemaのshape検査をraw JSONの重複名や実時間/認証の保証へ拡張しません。
