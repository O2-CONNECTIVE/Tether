# 一度の準備で二回まで起動する限定診断

この変更は Phase 1 の検証ループ整備であり、起動タイムアウトの原因特定や修正ではありません。
元のソースは `cee2e9baa22200c929f015199aac68626925a486`、tree は `ce3c61a0e56b2bec1fdc2a5990813dff17afb4ec` に固定します。
PR250 の三回上限を延長せず、専用の draft PR opened / 同一公開 repository / attempt 1 / 専用 head・base の一 job だけを入口にします。
35 分の job 上限を維持し、短い外側 watchdog は加えません。

## 実行と証跡の境界

- 元の準備・資格試験・driver / frontend / release build は一回です。固定ソースの Windows LastError を含む 41 条件と、有限 qualification JSON/hash の公開が native より先です。
- 元の child pwsh supervisor、全 native assertions、15000ms request、cwd、stable application / manifest path、features、既存の所有 cleanup を変更しません。
- 二回まで直列に呼びます。各回は未使用 tools / profile / evidence を使います。二つの EXE、provenance、固定 crate と検証済み patched source の明示的集合だけをコピーします。過去のログ、finish、profile、report はコピーしません。
- 固定 publisher の実装は変更しません。閉じた publication view は build / overlay だけを setup の実ファイルへ、それ以外の観測を当該 trial へ結びます。steps は実際の準備 12 行と当該回の実行 4 行の合成です。別の hash-bound receipt が reusedSetupStages / executedTrialStages / setupReceiptSha256 / diagnosticSha256 を明示します。16 段階が各回再実行されたという意味ではありません。
- source / overlay の元の identity 検査を再使用し、実際の staged driver / lock、stable EXE / manifest、harness、archive を前後で照合します。launch.json のコピーを再照合の代用にしません。
- 各回の元の有限 13 section と separate readiness receipt を公開します。raw native JSON、画像、process query は runner 外へアップロードしません。query はメモリ内だけで処理し、公開情報は閉じた Boolean / count / hash / outcome / duration です。

## 続行条件と限界

元の native 1/1、通常 close と app close が cleanup より前、app exit 0、driver stable / exit、cleanup / disposal 0 が必須です。
前後の raw child facts は observed 1..32、omitted 0、remaining 0、全行と一意連続 slot が一致しなければなりません。
前の集合は typed observations と集合整合の証拠です。最終集合の全行だけに verified handle / exited / snapshot absent を必須とします。
前の handle がまだ live でも最終退出が確認されれば、それだけを理由に拒否しません。

元の pwsh 終了後に候補限定の process/listener query を行います。
候補は app / driver / edge の exact path または固定 basename の和集合です。
関連 path 不明、Name 不明、取得失敗、4096 超過、曖昧さ、port 4444/4445 使用中は停止です。
無関係な system process の path 欠落だけでは拒否しません。
次回前には前回の exact paths と新しい paths の両方を再確認します。
再取得の繰返し、追加 kill、suspend、昇格はしません。

これは記録された子世代の退出と候補限定 snapshot だけです。
全 descendant の閉鎖、Edge retained handle の退出証明、snapshot と次回起動の原子性、実際にロードされた runtime identity は保証しません。
同一 VM の warm state は残ります。
二回とも完全な証拠と readiness を満たした場合だけ mechanical-reuse-demonstrated です。
二回の native 成功だけでは足りません。欠落・不明・失敗は次回を起動せず、二回目でも成功へ読み替えません。
全成功も再現率や原因・修正を証明しません。target-red、その他の native 失敗、証拠/readiness 不明を区別します。

## ローカル検証

新規 control の synthetic fixture は Windows 再現の証拠ではありません。
Mac に pwsh がないため、実 PowerShell parse、Windows query、41 条件の Windows qualification、staged driver の実検証、native の二回実行は未実行です。
それらは専用 job の preflight と実測で別判定します。環境不備、0 件、skip、不明を成功に数えません。
