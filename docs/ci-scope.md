# Native CIの適用範囲と常時実行の判定

[CI結果の読み取り照合](ci-results.md) / [共通開発手順](agent-workflow.md)

最初の段階ではmoduleや製品接続条件を分割せず、既存の試験内容・件数・toolchain・runner・matrixを維持します。
PythonとGoの3OS検査、日本語commit検査は従来どおり常時実行します。
特にGoのgatewayとSSHは単一moduleと依存を共有し、`ui/tauri`のWindows jobは共通frontendの品質検査も担当するため、名前からOS専用とは判断しません。

## 変更の分類

`scripts/ci_scope.py select`が、正確なheadの全Git差分から次の限定分類を行います。

| 変更範囲 | native/build処理の適用先 |
| --- | --- |
| ルート`README.md`と`docs/**/*.md`だけ | 対象外。Python/Go等の常時検査は継続 |
| `ui/mac-development/**` | 独立した開発用Mac画面 |
| `ui/mac-status/**` | 独立したMac status画面 |
| `tools/windows-owner-probe/**` | Windows owner probe |
| `tools/native-driver-trace/**` | Windows Tauri各lane |
| 共有backend、`ui/tauri`、契約、helper、workflow、lock、未知のpath | 全native/build処理 |

独立rootの中でも認識済みの依存lockは全処理です。複数分類を変更すると適用先を合併します。
空差分と不明な文字を含むpathも全処理です。新しいpathは除外せず、独立性を確認するまで全処理に倒します。
共有helperを変更すると両OSへ戻るため、既存workflowのpath一覧にhelperが抜けていた場合も検査されます。

PRはeventのbase/headとその共通祖先からheadまで、pushはeventのbefore/afterを比較します。
GitHubの300ファイル制限があるpath filterや最新commitだけには依存しません。
rename検出を無効にしたNUL区切りで旧pathの削除と新pathの追加を両方数えます。
履歴不足、無効なevent、head不一致、非線形pushは失敗とし、対象外へ読み替えません。

## 既存check名を維持する終端判定

11のnative系workflowは対象branchのPR/pushで必ず起動し、既存の14 job定義とmatrixを常に生成します。
workflow-levelのpath filterとjob-levelの条件は使いません。
同じjob名のままcheckoutと適用判定を行い、重いbuild・試験・artifact処理だけをstep条件で分けます。
対象外でもrunner割当てとfull checkoutは必要です。この段階では完全なrunner費用削減ではなく、不要なcompile/GUI処理を減らします。
既存のownerを保つ`cancel-in-progress: false`も変更しません。

終端の`ci_scope.py gate`は`always()`で動きます。
checkoutと適用判定の成功、厳密なboolean出力、全stepの存在を確認します。
適用時は必須stepすべての成功を要求し、元から条件付きのcache保存等だけはskipを許可します。
対象外のときは作業stepすべてがskipしたことを確認し、summaryに`not_applicable`と「試験未実行」を明記します。
失敗、cancel、欠落、予期しないskipを成功へ変換しません。workflow/job自体のskipも合格にしません。

`check_ci_results.py`の厳格な契約は変更しません。
jobのsuccessは適用判定も含むcheckの成功であり、全native試験の実行成功を意味しません。
試験実績を報告するときはjob summaryとstepを確認し、`passed`と`not_applicable`を分けます。
全件skipや0件の試験を合格と呼ばない、各試験自体の既存契約も維持します。

GitHubの必須check設定は権限不足により未確認です。
この変更は既存check名を残し、新しいcheck名への切替えや保護設定の変更を必要としない設計ですが、保護設定の検証済み宣言ではありません。
実CIで現headとmain merge後の全workflow・job終端を確認します。

## 並行PRと検証境界

古い[PR #326](https://github.com/O2-CONNECTIVE/Tether/pull/326)と[PR #180](https://github.com/O2-CONNECTIVE/Tether/pull/180)のworkflow差分は、この条件と現在mainの試験を消さないよう統合時に再照合します。
このPRは両branchの編集・競合解消・mergeを行いません。
進行中のbackend変更は共有分類なので両OS検査の対象です。

```sh
python3 -m unittest discover -s scripts/tests -p test_ci_scope.py -v
```

公開CLIでdocs-onlyの失敗から成功を確認し、その後に独立root、共有依存、rename/delete、300件超、PR共通祖先、履歴不足、head不一致、終端の欠落・失敗・skipを回帰検証します。
workflow配線の回帰検証は常時job生成、全作業stepの条件と終端集合への収載を確認します。
これはCI制御の検証で、実GUI・実dot接続・配布利用・P2 runtimeの合格ではありません。
