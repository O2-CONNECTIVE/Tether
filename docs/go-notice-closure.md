# Producer記録とGo noticeの照合

[#24](https://github.com/O2-CONNECTIVE/Tether/issues/24) の限定検査です。
[`verify_go_notice_closure.py`](../scripts/verify_go_notice_closure.py) は、[固定producer build](mac-sealed-producer-build.md) の記録・imageと、[固定Go本文収集](go-license-inventory.md) の8 filesを読み取ります。
記録された選択moduleのnoticeを照合しても、**終了値2 / blocked**を維持します。build、Go command、cache参照、取得、image起動、署名、登録、package生成、uploadは行いません。

## 見つかった閉包の不足

固定7本文は、Go rootのLICENSE/PATENTSと、backend/go.modが固定する3moduleの本文です。これは実producerの依存閉包ではありません。

- build helperは `go list -deps -json` の結果からrepository packageと外部moduleの集合を選び、`go list -m -json` の版・module checksum・go.mod checksumを記録します。全module graphの一覧ではなく、そのrole・Darwin ARM64・cgo条件で選択されたmoduleの記録です。
- collectorは直接固定3moduleを収集するため、producerが使わないmoduleの本文を含むことがあります。選択集合と固定集合の完全一致を要求せず、選択されたmoduleがすべて同じ版/checksumで固定本文に覆われることを要求します。未選択の固定moduleは別に報告します。
- 外部moduleを持たないStandard packageはbuild helperのrecordに保存されません。Go stdlib内のvendor packageや各本文を、Go rootのLICENSE/PATENTSだけで覆ったと判定できません。native library、platform runtime、compiler/SDK、build toolもこの記録では網羅されません。
- recordのmoduleを削除しても、imageからmoduleを復元する検査ではないため完全性を証明できません。空集合も「記録されたmoduleが0件」として扱い、実binaryに外部依存がないという意味にはしません。record hashは独立した信頼できるbuild結果から取得する必要があります。

今回追加するのは、従来別々だったrecordと固定本文の整合検査です。実Go dependency graphの再取得、binary build-infoとの照合、stdlib/vendorの版・本文inventory、実Macでの最終notice収集は未実施・後続です。法律上の十分性やlicense互換性を認定しません。

## 入口と入力

```sh
python scripts/verify_go_notice_closure.py \
  --producer-root <BUILD_RECORD.jsonとresident-pipeのdirectory> \
  --notices-root <collector出力directory> \
  --module-root <対応するbackend-directory> \
  --source <外部確認した40桁source-SHA> \
  --build-record-sha256 <外部確認したrecordのSHA-256> \
  --producer-sha256 <外部確認したimageのSHA-256>
```

同じ入力からhashを取り出して渡すだけでは独立した来歴の根拠になりません。source SHAも対応宣言として照合し、Git checkout・sourceTree・source/recipeの実bytesや署名を認証しません。

- snapshotのpathはbuilderと同じPOSIX形式で照合します。root packageは `backend/activity.go` のように区切りを1個にし、record内の二重slash・dot・親参照を正規化して受け入れません。
- producer-rootは `BUILD_RECORD.json` と `resident-pipe` の2 filesだけです。現在のsealed producer schema、固定role/target/settings/flags、全blocked/false境界、snapshot/recipeのpath集合・hash形式、repository package宣言を検査します。
- imageの実bytesを外部期待hashとrecordのhash/sizeへ照合し、既存Mach-O parserでexecutable/CPU/modern macOS header宣言を確認します。loader検証・署名検証・source対応証明ではありません。
- module-rootの `go.mod` / `go.sum` を既存collectorの固定lock規則に照合し、その実bytesのhashをrecordとinventoryへ対応づけます。他のsource fileは読みません。
- 選択moduleはPath/Version/Sum/GoModSumの4項目だけです。未知module、版/checksum不一致、重複、replace/main/local情報を拒否します。未知依存を見つけても本文を自動取得・許可しません。
- notices-rootは固定7本文と `DEPENDENCIES.json` だけです。inventoryのsource/lock/版/checksum/URL/本文path/hash/false境界をcollector期待値へ厳密照合し、本文の実bytesを固定期待SHA-256へ照合します。inputが自己申告する本文hashには従いません。
- duplicate JSON key、NaN/Infinity、booleanを0/1へ置換した境界、未知field、欠落/余分なfile・空directory、symlink/reparse/special fileを拒否します。全親directoryのlinkも拒否します。

recordは256KiB、inventoryは64KiB、各lock/本文は16KiB、imageは64MiBが上限です。通常fileをbounded readし、producer/notice treeを前後で確認します。悪意ある同時変更に対する原子的snapshotやverify-to-execを保証せず、結果を後の起動許可へ使いません。入力の変更・削除や出力fileの作成はしません。

## 出力の意味

- 一致: exit2、`status=blocked`、`selectedModuleNoticesMatched=true`。外部期待hash、inventory hash、source宣言、記録のmodule件数、未選択の固定module一覧を返します。
- 不一致/不正: exit1、`status=refused`と固定codeだけを返します。path・例外・入力本文・原ログは複製しません。
- helpのみexit0。署名・起動・配布を許可する成功終了値はありません。

`scope=recorded-selected-modules-and-fixed-texts-only`、`binaryDependenciesVerified=false`、`noticeCompletenessVerified=false`、`distributionStatus=blocked`、`productionGateClosed=true` を固定します。残るsource/recipe対応、record閉包の完全性、実binary依存、stdlib/vendor、platform、toolchain、秘密/path、publisher、notarization、enrollment/実行、runtime/回収を固定の未検証一覧で返します。

[Mac材料v1](mac-evaluation-materials.md)、GUI/Swift配置、通常GUIのunbound、署名identity、production gateは変更しません。この検査で#24全体を完了にしません。

## 有限試験

新CLIの一致、選択subset、未知module拒否の3例を入口不在のredから同じ合成入力のgreenまで確認しました。その後の回帰は、module/lock/source/本文/image/record改変、重複・未知情報、偽の完了flag、サイズ、非通常file、原入力を出さない診断、読取専用性を検査します。

```sh
python -m unittest scripts.tests.test_go_notice_closure -v
python -OO -m unittest scripts.tests.test_go_notice_closure -v
```

root packageのpath回帰は、Git blob読取とrecipe用sourceをfixtureへ置き換えた実snapshot関数の出力をCLIへ渡し、修正前のexit1から同じ入力のexit2を確認します。固定のroot3 pathとnested pathを明示し、二重slash・dot・親参照の拒否も検査します。合成fixture自身はcheckerと別のpath組立てを使います。

匿名Mach-O bytesと既存固定license fixtureだけを使い、実compiler/cache/native/networkを使いません。既存Python offline CIの全helper discoveryがこのtestを収集します。POSIX symlink/FIFO試験はWindowsで明示skipとし、Windows reparseを実証済みにしません。合成試験の成功は実producer artifact・完全なGo graph・Mac notice閉包の証拠ではありません。
