---
status: accepted
---

# 端末内の実行と利用時の監督を製品側で所有する

2026-10-30、端末内のSSH実行部分をRescue Hostで所有する方式Cと、利用中だけLocalSystem監督serviceを置く構成を採用した。
運営中継はcontainer、端末は同梱native、一時非管理者guest、切断後の新規承認という既決方針は維持する。
これは設計の採用であり、実service・account・資格情報・ACL・privilege設定の実行許可ではない。

> **ADR 0005による改訂（2026-11-19承認、未実装）:** 確立したレスキューの遠隔接続（担当者とホストのchannel）の通信の切断については、「切断後の新規承認」を[ADR 0005](0005-rescue-reconnect-grace.md)が「猶予（5分）の間を除いて、切断後は新規承認」に置き換えた。それ以外の切断と、この本文は変えていない。

## 選択した理由と負担

既存Windows OpenSSHのservice、設定、鍵、共有DefaultShell、portを変更せず、実行前の停止境界と同じJobの回収を製品ownerへ集約する。
stock sshdが作る未来のtokenを先にruntime証拠へ仮置きする循環や、別のLogonUser/S4U tokenをその代用品にする構成は採らない。
代わりにSSH sessionの要求制限、端末操作、stream、将来のterminal互換性を製品が保守する負担が生じる。
初版のSSH機能範囲と公開受付は、この無害workload sliceでは実装しない。

監督はhost operator/admin既侵害を保証対象にせず、遠隔の非管理者guestから承認・失効・停止処理を守る。
Jobだけを権限隔離と呼ばず、SYSTEM/service SIDによる保護配置と実objectのowner/DACL確認を別に必要とする。
監督がhangした場合、sleep中、OS停止中の強制期限や再起動後清掃まで成立したとは扱わない。

## 明示的に分けるログオン契約

従来の`rescue-guest-provisioning/1`と`NewGuestTokenExpectation`はNETWORKを要求する限定契約のまま残す。
新経路は保護された`guest-runtime-v1.json`と対応digestから`rescue-host-guest-runtime/1`、`logonProfile: local-batch/1`を読む。
期待groupはBATCHを含みNETWORKを含まない別集合とし、二つをORで許す互換fallbackを置かない。
LocalSystemを実行主体とする設定が必須で、messageの申告値や現在ユーザーから補わない。

実ownerは次を一続きで保持するsource候補である。

1. 元の認証済み確認要求と同Service/issuer/outer ownerを固定し、保護Recordとruntime設定のdigestを再照合する。
2. 作成前登録、無効accountの実receipt、durableな所有SID記録を保持してから、SID単位の必要なaccount-right差分を記録・適用する。
3. 保護された固定helperのstdinへ別binary segmentで一度だけ秘密を渡し、SID指定のpassword設定・一時有効化を行う。
4. local BATCH logonの実primary tokenを保持・検査し、保護duplicateと同じsuspended targetを作る。accountは再び無効化する。
5. 実target/tokenの属性とobject owner/DACLを照合し、同じissuerの承認challengeだけで単回開始する。
6. 不可逆な失効後、同じworker/target/Job/token/秘密copy/権利差分/accountの回収を確認する。短期の検査lease返却を全体清掃の証拠にしない。

秘密をargv、env、file、JSON、status、error本文へ出さない。
秘密writerは開始前から内部copyを所有し、実I/O完了とrelease成功後だけ消去する。
未完・部分取得・取消・panicでは所有参照を残し、早いRPC returnを実workerの終了や清掃成功へ読み替えない。
既存group/GPO/他accountの権利を変更せず、追加権利の復元失敗時も同名再作成や権限拡大で続行しない。

今回のnative targetは保護layoutのPowerShellから作る固定`exit 0`だけで、任意path/引数/primary tokenを受け取らない。
cleanup予算を実行TTLやaccount期限に読み替えない。
実機用の各期限、artifact/hash、実OperatorSID、service/owner ACL、必要privilege、撤去手順は対象固定の操作票で別途確定する。

## 寿命と検証の境界

targetのadmissionは同Serviceが所有し、固定HardDeadlineとService寿命を超えない。
要求の通常完了とguest全体の終了を分け、取消・書込み失敗・切断・正規stopはcanonical issuer/authの失効へ結ぶ。
失効したRecord/grant/endpointを同じSIDの再ログインやservice再起動で復元しない。

> **ADR 0005による改訂（2026-11-19承認、未実装）:** 確立したレスキューの遠隔接続の通信の切断に限り、上の「切断」を「猶予（5分）を過ぎた切断」と読む（[ADR 0005](0005-rescue-reconnect-grace.md)）。取消・書込み失敗・正規stopの扱いと、失効したものを同じSIDで復元しない規則は変えない。実装されるまでは、今の失効の契約のまま動く。

source/fake試験とWindows向けcompileは、実Windowsのlogon、保護SD、継承、Job停止、TTLや撤去の証拠ではない。
製品caller、GUI承認から実実行、実SSH dispatcher、配布と実機受入は未接続または未検証で、製品gateは閉鎖する。
既存のforward-only SSH基盤やwireのliteral宛先を、この採用で暗黙に変更しない。

公式要件: [LogonUserW](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-logonuserw)、[account rights](https://learn.microsoft.com/en-us/windows/win32/secauthz/account-rights-constants)、[CreateProcessAsUserW](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-createprocessasuserw)、[Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects)。

## 固定評価commandの開始境界

固定評価entryだけが確認応答の実書込み完了後に、同じ要求・issuer・targetの承認を単回消費して開始する。
準備専用のruntime API、preview、staging、legacy構成はこの継続を選ばない。
応答の承認receiptは、開始・実終了・清掃成功を表さない。取消・書込み失敗・失効では開始を拒否し、開始中の競合では同じownerの停止と実回収完了を待つ。
sourceの固定設定は空のまま、実message認証helperは未構成のままである。設定値の確定や実OS操作の許可をこの配線で代用しない。
