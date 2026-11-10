# GUIが同じnative sessionを保持する組成

#25 / #189 の段階実装。通常Tauriのstatus/intent commandは、port単体ではなく
具体的な `ResidentState` を借用する。native variantではprivateな`NativeSession`全体を
state内に保持し、同じsessionのportだけを借用する。owner/portを別々に組み合わせる
入力、stateの再bind、ownerを取り出すAPIはない。

通常起動は[閉じたnative起動入口](mac-gui-trusted-startup.md)を呼ぶ。
nativeを明示linkしたMac buildだけが同じsessionを構成し、それ以外はunboundを維持する。
`ApprovedRelease.production=nil`、実enrollment未構成、通常接続gate閉鎖は変わらない。
zero-argument bootstrapがRefusedを返しても、単なるunbound状態へ
置き換えて終了許可を与えない。同じnative ownerを保持し、Unknownのまま扱う。

既存IPCのJSON・capability・caller制約は変えない。匿名GUI CIの裸portはfixture cfg内だけで
構成でき、元のkeeperが別途寿命を監督する。このfixture portだけから新stateのQuit許可は出ない。

## 終了の境界

通常Mac appは`run_return`を用い、ExitRequested時に同じmanaged stateへ終了要求を渡す。
unboundは起動履歴がないため許可する。nativeは元ownerのClose要求と実join判定のみへ委譲し、
Unknownや未完了ならprevent_exitを返す。受付receiptだけ、viewの破棄、Waitだけを終了証拠にしない。
build/setupより前に、同じstateのArcを保持する外側lifetime guardを作る。
正常return・unexpected return・unwindで元Closeを要求し、finish_requestedが偽なら元stateを保持し続ける。
Arcの存在やDropによるjoinだけを終了許可にしない。Unknownでは期限や再起動による解除を行わない。
safeな元終了証拠が揃いguardが完了してから、元run_returnのexit codeを伝播する。
Windows等の非Macは既存App::run経路を維持する。abort、別箇所のprocess::exit、OSの強制終了を保証しない。
同一署名releaseでの実起動、実製品Quitや自動終了通知は受入済みとしない。
source variantでのnative終了は最初のQuitがClose要求、確認後の次のQuitが許可となる。
自動main-thread wakeは後続とし、Unknownは次のQuitでも拒否する。
confirmed self-childは実lifetime guardの終了も通る。
別の有限return/unwind caseでは先にCloseやjoinを要求せず、外側guard自身だけがCloseを開始して待つ。
同じstateのArc viewを外側へ残すため、session自身のDropだけでは試験を通過できない。
元outerjoin/terminal/Wait1回を、guard終了後・viewを破棄する前に要求する。Unknown guardは意図的に永久待機するため、
今回の有限試験でそのDropを完走させたとは主張しない。

## 検証

既存のTauri IPC試験はmanaged stateの型を確認し、ResidentPort単独のstateがないことと、
従来のunbound返答・schema拒否・caller拒否を維持する。source variantの有限self-childは
同じ`ResidentState`経由でstatus/intent/Close/終了判断を通す。

- 通常defaultのunboundと、明示native prepareのRefused→Unknownを区別する。
- confirmed childは元IO退役・元Process Wait・元worker/outer join後だけ終了を許可する。
- unknown childは元outer joinのtyped proofが得られた後も、同じCloseを繰り返しても終了を許可せず、新規intentを拒否する。
- 両方とも元childのWait1回、四endpointのclose、同じretained recordとreplacement拒否を要求する。

これらは匿名self-childとsource/IPCの範囲であり、署名済みproducer、実policy/key、実dot、
通常GUIのnative起動、署名/notarization、ダウンロード評価版の完成ではない。
