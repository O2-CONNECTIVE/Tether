# 前提 fixture の診断専用候補

元の固定 head `3b0ec99a367b71d4edfb31ddfd479e7740b30e6c` の
[初回 run 42967354064](https://github.com/O2-CONNECTIVE/Tether/actions/runs/42967354064) は
compile 後の fixture 実行で `APP_OWNER_FIXTURE_FAILED` となりました。
4ケースのどこか・失敗した API は元の出力から確定できません。
実 app の compile/setup/attach は未実行です。診断版の成功でこの unknown を消しません。

この別 branch の変更は診断専用です。既存 `app-owner.test.ps1` の出力契約への置換や
通常の実 app workflow への統合を目的としません。新しい
`windows-owner-prerequisite-diagnostic.yml` には fixture と artifact 保存しかなく、
actual-app setup、build、attach の step はありません。診断 green の場合も gate は閉鎖したままです。
公開・実行の前に独立レビューが必要です。

## 変更する観測

- 既存4ケース・順序・assertion・API引数・5秒/20秒予算は保持。
- bool API の結果直後に保存された Win32 error を固定 category に分類。
  access-denied / invalid-handle / invalid-parameter / other のみで数値の raw error は公開しません。
- wait の戻り値、root exit、Job active count をそれぞれ有限 category に分類。
- assertion は固定 method 名+source 行番号で特定。API operation は同 phase で最初に観測した失敗で、
  後続の assertion の直接原因と同一だとは推定しません。行番号は実 source hash に結びます。
- primary Observe failure と finally Cleanup failure を別々に保存。
  後者が前者を上書きせず、どちらか一つでも存在すれば失敗です。
- source SHA、各 source/binary hash、完了ケース数、最後の scenario、固定 phase と分類だけを
  allowlist 検査後の artifact に保存。child 内部の未取得情報は推測しません。
- 最初の run/head を artifact に残し、`actualAppExecuted=false` / `actualAppGate=closed` を固定。

## 限界

これは失敗原因を得るための計測追加で、所有権修正でも根因確定でもありません。
`Active()==1` 直後照会の accounting race は仮説に留まります。待機追加・assertion 緩和はしません。
子 helper 内の失敗は追加の私的 channel で取得していません。親で観測できた失敗と区別します。
計測自体はタイミングに影響し得ます。green のみなら根因は未確定のまま停止します。
当地では Windows compile と実 API 実行は未実行です。元の branch と PR head は変更していません。
