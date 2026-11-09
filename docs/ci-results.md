# 固定ソースのCI結果を読み取り照合する

追跡: [CI配線 #9](https://github.com/O2-CONNECTIVE/Tether/issues/9) / [品質要件 #23](https://github.com/O2-CONNECTIVE/Tether/issues/23)

`scripts/check_ci_results.py` は保存済みJSONを読むだけの、依存追加のない検査です。
ネットワーク取得、workflow実行、merge、保護ルール変更は行いません。
既存のcheckout検証・Go JSON検証・各試験を再実装せず、必須workflowとjobの結果だけを集約します。
Python 3.11以上で利用し、開発tool全件試験は既存どおり3.13以上を使います。

## 呼び出し

```sh
python3 scripts/check_ci_results.py --snapshot ci-snapshot.json --policy ci-required.json \
  --repository example/project --event pull_request \
  --sha aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
python3 -m unittest discover -s scripts/tests -p test_ci_results.py -v
```

`--sha` は呼び出し側が最新PR headまたはmainを確認して固定した40桁の小文字SHAです。
branch名、短縮SHA、PRのsynthetic merge SHAへの読み替えはしません。
結果にはrepository、event、head SHA、選択したrun IDとattemptを記録します。
終了値は0（指定範囲がすべて成功）、1（未成功）、2（入力不正）です。
入力不正時は固定のエラーだけをstderrへ出し、原文や秘密を転記しません。

## 要件ファイル

```json
{
  "schema_version": 1,
  "workflows": {
    ".github/workflows/offline.yml": ["Linux", "Windows"]
  }
}
```

workflowのpathと実際のjob表示名を完全一致で指定します。空要件・空job集合・重複job名は拒否します。
これは限定fixtureの例であり、現リポジトリ全体の必須check一覧ではありません。
要件はレビューしたソースと照合して別に固定します。観測結果から自動生成すると欠落jobを見逃すため、その方法を合格根拠にしません。
workflowや共有開発入口への追加配線、repository全体の要件確定は後続工程です。
既存Python 3OS CIの開発tool discoveryは今回のfixtureも実行します。

## 入力snapshotの契約

```json
{
  "schema_version": 1,
  "repository": "example/project",
  "total_count": 1,
  "workflow_runs": [{
    "id": 10, "run_number": 5, "run_attempt": 1,
    "path": ".github/workflows/offline.yml",
    "head_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "event": "pull_request", "status": "completed", "conclusion": "success",
    "jobs": {
      "total_count": 2,
      "jobs": [
        {"id": 1, "run_id": 10, "run_attempt": 1, "head_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "name": "Linux", "status": "completed", "conclusion": "success"},
        {"id": 2, "run_id": 10, "run_attempt": 1, "head_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "name": "Windows", "status": "completed", "conclusion": "success"}
      ]
    }
  }]
}
```

GitHub Actionsのrun/job情報から必要項目を保存する正規化形式です。APIの単一レスポンスをそのまま投入する形式ではありません。
取得側は同一repository・eventについて、対象workflowの固定SHAの全runと各attemptの全jobをページ終端まで取得し、件数と対応を保存する必要があります。
古いSHAのrunを含めた場合はstaleの診断に使えます。対象SHAのrunがあれば古いSHAの新しいrunには置き換えません。
トップの`total_count`は保存対象runレコード（過去attemptを含める場合はattempt単位）の総数、job側はそのattemptの総数です。
途中ページだけ、APIエラー、取得中のrerunなどで一貫したsnapshotを作れない場合は合格判定に使用しません。
`run_id`・`run_attempt`・`head_sha`はjobと親runで一致必須です。旧attemptのjobの流用は拒否します。

この初期工程には取得adapterがありません。件数の一致は内部整合検査であり、取得元の真正性・最新性・省略なしを証明する署名ではありません。
保存後に新runが開始したこと、API側の削除・欠落、誤った要件ファイルや捏造snapshotは検出できません。
merge前には別途最新head・レビュー・実Actionsの取得完了を確認し、この出力単独でmerge可としません。

## 判定

- 同じpath/event/SHAでは最大run_number、同じrunの最大attemptを採用します。後のqueued/running/failureを過去successで隠しません。
- run不在はmissing、同path/eventで別SHAしかない場合はstaleです。
- 必須job不在はmissingです。workflow全体がsuccessでも必須jobの未成功で非0になります。
- skipped・cancelled・failure・timed_out・neutral・action_required・stale・startup_failureを成功に変換しません。
- queued/in_progress/waiting/pending/requestedはpendingです。未完了にsuccess等のconclusionが付いた矛盾は入力不正です。
- 同じrun/attempt、同じworkflow/run_numberの別run、同じattemptのjob名/ID重複、件数不足、未知status/conclusion、SHA不一致、JSON重複keyは入力不正です。

workflowの成功と要求jobすべての成功が両方必要です。個別stepの内容、テスト数・skip理由・秘密scan・依存scan・配布一致の検証は既存の各検査とレビューが担当します。
実GUI・実dot接続・本番利用・44条件・P2 runtimeの合格を意味せず、既存の未達と接続gate閉鎖を維持します。
