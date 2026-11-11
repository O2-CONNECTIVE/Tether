# Native CIの適用範囲と常時実行の判定

[CI結果の読み取り照合](ci-results.md) / [共通開発手順](agent-workflow.md)

Macを先行する方針に合わせ、監査済みのbackend変更からWindows GUI受入だけを分離します。
moduleや製品接続条件は分割せず、適用時の試験内容・件数・toolchain・runner・matrixを維持します。
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
| 下記の監査済みGo source/testとGo CI helperの完全一致path | Windows GUI受入だけ対象外。他laneは継続 |
| 上記以外のbackend、`ui/tauri`、契約、helper、workflow、lock、未知のpath | 全native/build処理 |

独立rootの中でも認識済みの依存lockは全処理です。複数分類を変更すると適用先を合併します。
空差分と不明な文字を含むpathも全処理です。新しいpathは除外せず、独立性を確認するまで全処理に倒します。
共有helperを変更すると両OSへ戻るため、既存workflowのpath一覧にhelperが抜けていた場合も検査されます。


## Windows共通検査とGUI受入の分離

`windows-shared`は`tauri-shell.yml`の既存`windows-shell` jobへ対応し、frontendの型・lint・Knip・単体/browser試験とRustのfmt・clippy・IPC試験・buildを維持します。
`windows-native`は`tauri-native-smoke.yml`、`tauri-evaluation.yml`、`tauri-startup-diagnostic.yml`のGUI起動・release評価・startup診断へ対応します。
`tools/native-driver-trace/**`は従来のWindows両方の範囲を維持します。
Go/PythonのWindows検査はこの分類を使わず常時実行します。

Windows GUI入力から独立と監査したpathは次の完全一致11件だけです。
新しいGo fileを拡張子や親directoryだけで除外せず、`backend/go.mod`、`backend/go.sum`と未知のbackend sourceも全laneへ戻します。

- `backend/cmd/rescue-gateway/access_refresh.go`
- `backend/cmd/rescue-gateway/access_refresh_test.go`
- `backend/cmd/rescue-gateway/discovery_pilot.go`
- `backend/cmd/rescue-gateway/main.go`
- `backend/cmd/tether-echo-probe/probe.go`
- `backend/cmd/tether-echo-probe/probe_test.go`
- `backend/internal/agentgateway/access_rotation.go`
- `backend/internal/agentgateway/access_rotation_config_test.go`
- `backend/internal/agentgateway/config.go`
- `scripts/go_ci.py`
- `scripts/tests/test_go_ci.py`

監査の基準はmain `84f2262b5f2ce0a2c1c7114d3ae4a3386e2ae16a`と[PR #341](https://github.com/O2-CONNECTIVE/Tether/pull/341)の15変更pathです。
9件のGo source/test、上記helper 2件、Markdown文書4件で、次のWindows GUI依存経路には入りません。
これはpath依存の静的監査であり、実GUIの成功証明ではありません。

- 3 workflowのbuild入口は`ui/tauri/package.json`のfrontend buildと`ui/tauri/src-tauri/Cargo.toml`のCargo build/testです。Windows側ではGoのtoolchain導入・compile・起動を行いません。
- npm buildは`ui/tauri/vite.config.ts`のVite buildと`ui/tauri/scripts/notices.mjs`のnotice生成を使います。Cargo依存、`build.rs`のmanifest、`tauri.conf.json`の`frontendDist`はTauri配下を入力とします。Rustのinclude fixtureはTauri配下に加え、rootの`contracts/control-v1/vectors.json`も読み込みます。契約変更は全laneへ適用され、上記Go source/helperは読み込みません。
- evaluationの`collect_tauri_licenses.py`はCargo metadata、lock、frontend notice、Rust licenseを収集します。`package_tauri_evaluation.py`と`verify_tauri_evaluation.py`はEXE、license、Tauri lock、`LICENSE`、`docs/tauri-evaluation-readme.txt`、source SHAを扱います。MarkdownやGo fileをarchiveへ取り込みません。
- native smoke/startupは`ui/tauri/native-smoke/`のPowerShell harnessとTauri testを使います。`scripts/go_ci.py`とそのtestはGo検査の入口で、これらのworkflow/helperからimport・実行されません。

依存経路を増やす変更はUI、native、workflow、契約、manifest/lock、共通helperの分類で全laneを実行し、同時にこの監査とallowlistを再評価します。
新しい依存を追加した後もGo fileを除外し続けてよいとは限りません。
混在差分は適用先の合併で、未知pathは全laneです。renameは旧pathも評価するため、UIから許可pathへの移動でもGUI検査を省きません。

[PR #341のWindows evaluation失敗](https://github.com/O2-CONNECTIVE/Tether/actions/runs/42962512638)は旧head `f2779c3`での実際の所有権失敗として残します。
この方針変更は再実行による成功化や既知未達の解除ではありません。
CI方針自身の変更は全laneへ適用されるため、このPRのWindows GUI失敗も対象外へ読み替えません。

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
監査済みbackend変更でもMac、Windows共通frontend/Rust、Go/Python等の検査を維持し、Windows GUIだけを明示的な対象外にします。

```sh
python3 -m unittest discover -s scripts/tests -p test_ci_scope.py -v
```

公開CLIでdocs-onlyの失敗から成功を確認し、その後に独立root、共有依存、rename/delete、300件超、PR共通祖先、履歴不足、head不一致、終端の欠落・失敗・skipを回帰検証します。
workflow配線の回帰検証は常時job生成、全作業stepの条件と終端集合への収載を確認します。
これはCI制御の検証で、実GUI・実dot接続・配布利用・P2 runtimeの合格ではありません。
