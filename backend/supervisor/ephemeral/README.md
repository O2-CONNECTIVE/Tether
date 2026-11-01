# 初期評価用 nonresume issuer

このpackageは本人承認済みの初期評価方式を準備する。
PC/監督serviceの再起動時はその回のgrantと承認challengeを破棄し、許可0からfreshな再承認を必要とする。
自動resume、保存UI stateからの復元、旧grantのTTL resetを提供しない。
永続grantstoreのStore/Leaseへmemory実装を差し込むものではなく、#63の永続記録候補は別modeとして保全する。

## 同一性の仕様差分

以前の「Generation単独を全起動で再利用しない」という記述を、保護所有者のinstance epoch・per-boot generation・full Identity全体の非再利用へ明確化する。
Newはcrypto/randの32byte内部instance epochを生成し、generation0・grant0で開始する。
Prepareはlock内で正のgenerationを消費し、最大uint64からのwrapを拒否する。
LaunchIDはinstance epochの64桁hexとgenerationの正規十進文字列をhyphenで結合し、最大85 ASCII bytesとなる。
Identity.Epochは独立したbackend session epochとしてcrypto/randからuint64を新規発行し、既存の正規十進文字列へ変換する。
内部boot epochをIdentity.Epochに流用しない。
既存wireのfield/型/schema/vectorは変更せず、この発行APIと既知binding照合を統合ownerの確認対象とする。
nonce/epoch/LaunchIDはいずれも識別子で、本人承認・相手認証の証明ではない。

## 単回消費と失効

ApprovedPolicyは、保護側が今回の正規operator承認で解決した型付き値だけを受け取る。
UI任意path・生policy・TTL更新・保存Identityを受け取る入口はない。
Challengeはmemory内のopaque handleで、当該issuer/current entryに束縛する。
Launchはlock内でchallengeをburnした後にだけ既存supervisor.Newを呼び、callbackへ渡す。
clock/context/設定の失敗でもchallengeを再利用しない。
callbackはlock外で実行し、Revoke/Closeはcoreを先に失効してcontextをcancelする。
error/panic/結果不明のlaunchはburnを維持し、このissuerからの次のPrepareも拒否する。
新しいgrantを作る場合も、別の正規承認と所有資源の確認が必要で、moduleは自動再試行しない。
Lookupは現在memory map内のgenerationと完全一致Identityだけを照合し、旧callbackや別bootの同generationを拒否する。
pollは期限確認だけでrenewせず、hard deadlineはPrepare時の承認値から延長しない。

## 現在利用者issuer（2026-10-31）

`NewCurrentUser`は、既にログオン中の利用者として動く承認（`supervisor.CurrentUserNoIsolation`）だけを発行する別issuerである。
種別は生成時に固定し、切り替える入口はない。guest境界のpolicyは`Prepare`でgeneration消費前に拒否し、Authorityは`supervisor.NewCurrentUser`で作る。
既定の`New`の挙動は変えておらず、現在利用者の境界は従来どおり`Launch`内の`supervisor.New`が拒否する。
epoch・単回消費・Lookup・失効の規則は同じである。利用者本人の明示同意を得るproducerは未実装で、このissuerは同意を代用しない。

## 保証範囲と未実装

初期保証対象はremote non-admin guestからsupervisor/停止処理を守ることで、host operator/adminが既に侵害されている場合は対象外。
毎回別admin sign-inを初期必須にしないが、正規operator承認、guestのIPC拒否、peer identity/ACL、別service/guest SIDは必要。
このpackageは認証pipe、token/ACL、実service/Job/process起動、Go backend activation/lease更新/物理停止を実装しない。
callbackは保護所有者がcontext/core失効を尊重し、実資源を別に所有・回収する責務を持つ。
Closeは認可失効と取消であり、launch中は結果不明を返す。callbackの終了・process回収を捏造しない。
memory epochはJobの最後handle、旧process残留、監督hang、OS sleep、実停止の証明にならない。
製品gate閉鎖、P2専用/代替再現停止、既知NOT_MET/UNVERIFIEDを維持し、純粋testsを実機受入のpassedへ変更しない。
旧grantを再起動後に自動継続する可用性機能は後続へ延期する。
