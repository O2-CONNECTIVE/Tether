# 旧失敗ソースを一回だけ確認する限定診断

現在の入口は PR251 の旧失敗ソース回復です。以下の二回起動設計は過去段階の記録として残します。

- ソースは `008073aa952959f2a4937e31fe10de6609c1627a`、tree は `4dcfba754be4a150a28d1dfb770c2ea65e3fcaa4`。PR248 の元の準備・全 native assertions・15000ms・driver・build・cleanup・12 節 publisher を使います。
- 元ソースに実在する 31 試験を一度実行し、開始・成功・skip・失敗の閉じた資格 receipt を native 前に公開します。41 条件の pump 資格や 13 節目を生成しません。
- fresh VM 一 job、一回だけです。成功・失敗・不明のいずれでも追加起動しません。前回の二回起動 controller と readiness snapshot は呼びません。
- before は `8e5d95c1a09cd84523c6dc2bc4b9f7481d7643f3`。空でない after が新 head と一致する PR251 synchronize / attempt 1 / draft / 同一公開 repository / 固定 head・base だけを許可します。35 分上限、手動 dispatch・rerun・artifact・cache・secret なしです。
- これは永続的な exactly-once ledger ではありません。before head への reset / replay は禁止します。新 head の独立レビューと公開前の全 event run 照合が必要です。
- 資格・12 段階・source / harness / specimen の再照合が失敗したら起動前に停止します。元の always 条件による報告と最終 identity、失敗時の有限 publication を維持します。
- 旧ソースでの red 回復だけが目的です。green は再現できなかったという結果であり、observer effect、再現率、原因、修正は示しません。新 runtime 観測や変更はありません。

## 過去段階: 一度の準備で二回まで起動する設計

この変更は Phase 1 の検証ループ整備であり、起動タイムアウトの原因特定や修正ではありません。
元のソースは `cee2e9baa22200c929f015199aac68626925a486`、tree は `ce3c61a0e56b2bec1fdc2a5990813dff17afb4ec` に固定します。
PR250 の三回上限を延長せず、専用の draft PR251 synchronize / 同一公開 repository / attempt 1 / 専用 head・base の一 job だけを入口にします。
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
ただし remaining 0 と same-identity の同居、未検証 handle の live / exited、検証済み handle の different-identity、曖昧な snapshot は拒否します。

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
target-red は厳密な整数の native 1/0、元の timeout 観測、最終 identity と canonical record の source / specimen / observation binding を必要とします。
元の timeout 観測があっても binding 不明なら、observedSessionTimeout として分離して残し、failure-evidence-inconclusive とします。red に native 成功の合否は要求しません。
その他の native 失敗という分類にも、failed status、既知 phase、厳密な整数の観測値、欠落のない request 記録が必要です。canonical projection と一致していても unknown / null / 型不正の観測は証拠不明のままです。

## ローカル検証

新規 control の synthetic fixture は Windows 再現の証拠ではありません。
Mac に pwsh がないため、実 PowerShell parse、Windows query、41 条件の Windows qualification、staged driver の実検証、native の二回実行は未実行です。
それらは専用 job の preflight と実測で別判定します。環境不備、0 件、skip、不明を成功に数えません。

## 初回の事前検査結果

[PR251 の初回 run](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/42333272817) は、head `f73cde083542da63b2072c770b10d987e88ae813` で事前検査に失敗しました。
外部 control の試験 18 件中 1 件が、Windows の path 表記を `/` で分割した assertion の IndexError で停止しました。
資格証拠の公開、build、native は未実行で、setup / trial / final の公開 block もありません。
元の起動タイムアウトや target-red ではなく、再利用や速度改善は未実証です。
修正試験は PurePosixPath / PureWindowsPath の双方で同じ assertion を検証し、実行時 control や固定ソースは変更しません。

修正版は同じ PR251 で、既知の失敗 head からの一回の synchronize 遷移だけを許可します。
root before は `f73cde083542da63b2072c770b10d987e88ae813`、空でない after は PR head SHA と一致し、before と異なることが必須です。
PR 番号、draft / public / 同一 repository、既存の branch / base SHA、attempt 1 の条件も維持します。
opened、任意の後続 synchronize、失敗 run の rerun は入口ではありません。初回 native 0 回なので修正版も最大二回、合計でも二回までです。
これは修正 head の新しい限定資格確認であり、初回成功の扱いや元の起動タイムアウト再現への読み替えはしません。

根拠: [GitHub の pull_request event](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request)、[synchronize schema](https://raw.githubusercontent.com/octokit/webhooks/main/payload-schemas/api.github.com/pull_request/synchronize.schema.json)、[payload example](https://raw.githubusercontent.com/octokit/webhooks/main/payload-examples/api.github.com/pull_request/synchronize.payload.json)。
