# 開発 Mac GUI の回復と、回収未確認時の画面退出案

対象は [Tether Development](../ui/mac-development/README.md) だけです。
通常製品、署名 gate、実行許可、backend の unknown の意味は変更しません。
現状の実装と、未実装の退出案を分けます。この文書は実機操作の承認ではありません。

## 三つの判断を分ける

1. **操作受付の停止**: 古い状態や不正な観測から ON/OFF を再開しません。
2. **元 owner の回収**: strict confirmed terminal、実 EOF、元 child の正常 exit と
   reap が揃い、cleanupInvalid がない場合だけ確認済みです。
3. **画面だけの退出**: 回収を確認したことにはしません。未確認 incident を永続保持し、
   新しい owner を遮断したうえで、利用者が明示した場合に限る別の将来経路です。

終了ボタンの連打、時間経過、helper の不在、exit 0、GUI の再起動は、単独でも
組合せでも、失われた terminal や unknown を成功にしません。

## source-only controller

`DevelopmentProcess/RecoveryController.swift` は既存 ResidentProcess を包みます。
実 AppKit の main への配線はまだなく、現在起動している旧アプリは変わりません。

- 初回準備は一度だけ開始し、起動中・close 中・証拠不足中は新しい owner を作りません。
- child 未起動の launch failure では、明示操作で別の owner による準備を試せます。
- 完全な元 owner の回収証拠が揃った後だけ、明示操作で新しい準備を始められます。
  旧 Feed の fault や実行許可を復活させず、直前 owner の固定診断を保持します。
- cleanupInvalid は置換不可です。EOF/reap/exit 0 があっても変わりません。
- 自動再起動、時間切れ retry、kill は追加しません。承認・enrollment・server 側期限を
  変更せず、新しい準備も既存の実行前提と個別承認範囲に従います。

`swift run RecoveryChecks` は本番 controller と Feed parser に、内部の有限 lifecycle
fixture を渡します。production constructor は固定 ResidentProcess だけを作り、
fixture factory を引数・環境変数・設定から選べません。実 helper、pipe、GUI 描画は
実行せず、AppKit 配線、実回収、横断的な再起動遮断を証明しません。

同じ fixture と one-shot baseline controller の組は 27 条件中 7 条件で失敗し、
修正後はその 27 条件と初回・連打等の追加条件に合格しました。
独立レビューで指摘された準備前 close の後の起動も、2 条件の red/green で拒否を確認しました。追加した controller 境界の red/green であり、
旧 AppKit を自動操作して失敗を再現したという意味ではありません。

## 実際の回収未確認状態に必要な次の設計（未実装）

terminal がなく、cleanupInvalid があり、元 child が reap 済みで pipe が終了した場合、
元 child から将来の証拠が届くことを期待し続けるだけでは画面の滞留を解消できません。
しかし `mayQuit = true` や `Feed()` への置換は解決ではありません。
次の順序を全て実装・検証してから、画面だけの退出を検討します。

### A. 複数アプリに共通の admission と記録

- bundle の位置やコピーごとではなく、同じ OS ユーザーの開発 identity 全体に一つの
  固定 recovery store と OS lock を使います。caller の引数や環境変数で切替不可です。
- **helper 起動より前**に、排他下で既存の未解決記録を確認し、今回の owner reservation
  を永続化します。crash による lock 解放は reservation の解決を意味しません。
- reservation には新しい instance UUID、schema/revision、固定の理由と観測項目、
  検証済み build 識別を持たせます。PID は補助診断であり、再利用される PID を owner
  の同一性や停止対象の唯一の根拠にしません。秘密、policy 本文、endpoint、caller、
  raw frame、stderr を保存しません。
- 各 instance の記録を分け、別 instance の更新で上書き・消去しません。新規 owner は
  全体の未解決 reservation/incident が一つでもある間は禁止です。
- UI を閉じた記録は unresolved のままです。次回起動時は記録を表示し、新規準備を
  無効にします。再起動、再インストール、別 bundle コピーを解除手段にしません。
- `clear`、全件削除、自動期限切れ、PID 不在による自動解決は提供しません。

### B. 永続化に失敗したとき

正規の固定 directory と owner、mode、通常ファイル、symlink 不使用を検証し、
同じ排他と保持 directory handle の下で bounded schema を扱う必要があります。
一時ファイルの作成、全 bytes の書込み、file flush、atomic rename、必要な directory
flush、read-back と revision 照合までを一つの成功条件とします。
macOS と対象 filesystem に必要な durability primitive は実装時に一次資料で確定します。

書込み不足、disk full、flush/rename/read-back 失敗、破損、未知 schema、lock 取得不能、
競合 revision は、記録できたことにしません。退出も新規準備も無効にし、元 owner と
画面を保持します。失敗後に別場所へ保存して回避したり、既存記録を切り詰めたりしません。
保存結果不明なら再読込みし、同じ incident ID への重複・上書きを避けます。

既存 store の消失を「初回利用」と扱うと再起動遮断を迂回できるため、初期化と既存
store 喪失を区別する provisioning/migration 契約が必要です。その契約はまだありません。
同一 OS ユーザーによる悪意ある削除・旧バイナリ実行への防御を、ローカルファイルだけで
保証できるとは主張しません。

### C. 記録を残した UI-only departure

最初の対象を、元 child が実際に reap 済みで、元 pipe の**実 read EOF**を観測し、
ローカル I/O の所有処理が終了した場合だけに限定します。生存中の helper や callback
の所有を捨てません。監督の移譲を必要とする経路は別設計です。

現在の ResidentProcess の `eof` は read error の経路でも true になります。その値だけを
新しい退出 gate に転用せず、実 EOF と read failure、handle の終了を区別する必要が
あります。exit 0 は UI の退出を回収成功に変える根拠にはしません。

1. 同じ owner の未解決 incident を永続 commit して read-back する。
2. 「回収は未確認です。この画面だけを閉じます。新しい開発ホストは準備できません」
   と表示し、今回の incident revision に束縛した利用者の明示操作を受ける。
3. 排他下で同じ incident/revision と所有状態を再確認する。途中で状態が変わった場合は
   退出せず再提示する。取消や連打は状態を変更しない。
4. `mayQuit` や `closeConfirmed` を変えずに、別の `departedUnresolved` 経路として
   画面を閉じる。記録に退出の事実があっても cleanup は unknown のままにする。

ボタン案は「回収未確認の記録を残して画面だけ閉じる」です。「停止して終了」や
「復旧済み」と同じ表示にしません。保存失敗時はこの操作を許可しません。
本 controller はこの経路を提供しません。

### D. 旧版と複数の滞留アプリ

旧 binary はこの ledger を読みません。新しい source の更新や診断記録
だけで、旧 GUI の Quit、既存 helper の回収、他の旧 app の準備を制御できません。
複数の画面を一件としてまとめず、元 app/owner ごとの観測を保持する必要があります。

旧版が準備可能なまま混在すると、共有 ledger による全体遮断は成立しません。
移行前の旧 instance の棚卸し、残る owner の扱い、古いコピーを起動可能にするかという
rollout/decommission 判断が必要です。単なるプロセス名の走査では十分な保証にならず、
既存プロセスの停止や削除をこの source 設計から自動実施しません。

未解決 incident の解消は、明示的に範囲を定めた診断・decommission とその証跡の契約を
別途確定してから行います。操作主体の同意だけで、旧 cleanup を confirmed に書換えたり
再使用してよい identity と推定したりしません。解消記録も元の unknown を保存します。

## 配線前の受入条件

- AppKit: 初回・起動失敗 retry・完全回収後の明示準備、連打、Quit/Close 中、取消、
  新しい状態との競合を確認する。旧 owner の未知状態からの置換は常に拒否する。
- store: 各永続化段階の crash/例外、書込み不足、破損、symlink、権限異常、削除、
  不明な schema、read-back 不一致、revision 競合を fixture で確認する。
- 複数 instance: 同時準備、別 incident の保存、退出中の別 app 起動、crash 後 relaunch、
  旧版混在を確認し、どの失敗でも新しい owner が起動しないことを示す。
- UI-only departure: 実 EOF と read error を分け、保存失敗・取消・状態変更・生存 child
  では拒否する。退出後も unknown と再起動遮断を維持する。
- 独立レビュー、適用 CI、固定した head の検証を済ませる。実 GUI/helper の操作は
  別途の具体的な承認範囲で行う。source/fixture の green で実回収を宣言しない。
