# PythonオフラインCI

[開発手順へ戻る](development.md)

## 第1段階で確認する範囲

[Python offline tests](../.github/workflows/python-offline.yml)は、`main`向けのpull requestと`main`へのpushで既存のPythonオフラインテストを実行します。
開始時の基準は`6499e78f337b500785163fe8fb6ac669eb52ab28`で、既存テストは82件です。
既存テストの自動化であり、新しい振る舞いテストを後付けでTDDと呼ぶものではありません。

| 標準GitHub-hosted runner | Python | アーキテクチャ |
| --- | --- | --- |
| `ubuntu-24.04` | `3.13.17` | x64 |
| `windows-2022` | `3.13.17` | x64 |
| `macos-15-intel` | `3.13.17` | x64 |

PythonとTkinterのimportを確認し、共通入口`python scripts/dev.py check`を実行します。
入口は既存の`unittest`探索を詳細表示し、その後`src/`、`tests/`、`scripts/`を組み込み`compile()`で構文検証します。
構文検証はbytecodeを保存しません。
対象欠落、探索結果0件、全件skipも失敗とし、総数、実行数、合格数、skip数、失敗数、エラー数を表示します。
続けて`python scripts/dev.py tokens`で公開済みのtoken checkerを実行します。
現在の対象は120 tokensの参照規則とライトテーマの23組のコントラストです。
同じPythonの`-I`モードで起動し、`PYTHONOPTIMIZE`環境変数によるassert無効化を避けます。
`python -m unittest discover -s scripts/tests -v`は従来の異常条件7件とtoken checkerの3件を合わせた10件を別に実行します。
token checkerの3件は、正常例と、一時コピーでの不正参照、コントラスト1:1への低下を検証します。
各コマンドの非0終了はCIの失敗に伝わり、`continue-on-error`で隠しません。
この10件は既存82件と区別する開発ツールと静的checkerの検証で、製品の受入条件やUIのTDD実績へ加算しません。
token検証は生成物の一致、描画、Tauri native動作を保証しません。
追加のpip依存はなく、ビルド用の`requirements-build.txt`はインストールしません。
ローカルで同じテストを再現するコマンドは[開発手順](development.md#オフライン試験)を参照してください。

既存のskipはテストの詳細出力に理由とともに残ります。
WindowsではUnixのshebangを使う模擬Uptermのライフサイクル試験1件をskipし、残る81件を実行する想定です。
symlinkを作成できない環境では`test_dangling_state_symlink_not_absent`も追加で1件skipします。
LinuxとmacOSは82件、skip 0件を想定しますが、実際の件数と理由は各jobの結果で確認します。
失敗したOSがあっても他のOSを続行し、skipを合格件数へ加算しません。

**CI成功は、[親受入Issueの44条件](https://github.com/O2-CONNECTIVE/Tether/issues/1)が本番受入に合格したことを意味しません。**
テストはモックと模擬CLIを使用し、GUIの実描画、実SSH、認証と転送の強制、期限切れでの実切断、Windowsの権限境界、配布EXEを検証しません。
実PC、公開中継、TCPポート22、既存CI VMには接続せず、self-hosted runnerも使いません。
提供段階と未検証事項は[検証状況](status.md)の区別を維持します。

## 権限と依存の扱い

`GITHUB_TOKEN`は`contents: read`だけに制限し、checkoutの`persist-credentials: false`で後続処理へのGit認証情報の保持を無効にします。
forkのPRにも追加secretは不要です。
`pull_request`を使い、`pull_request_target`で未信頼コードを実行しません。
初回fork PRなどで既存のGitHub実行承認が求められた場合は、その状態を確認し、設定を変更して迂回しません。

公式の`actions/checkout`と`actions/setup-python`は、各公式リポジトリの`v7.0.0`タグと照合した40桁のcommit SHAに固定しています。
固定したactionの入力、Node.js 24の実行定義、認証処理、Python配布元を確認しています。
Pythonの取得は`setup-python`によるホスト済みキャッシュ、または公式`actions/python-versions`の配布を使います。
固定版は[公式manifest](https://github.com/actions/python-versions/blob/main/versions-manifest.json)で3 OSのx64配布を確認して選んでいます。
OS名とPython patch版は固定していますが、GitHubのrunner imageと配布キャッシュは更新されるため、環境全体の再現ビルドを保証しません。
更新時は公式のタグとSHA、実行定義、3 OSの結果を再確認します。

各jobの上限は10分です。
同じworkflowとrefの新しい実行で古い実行を中止し、重複実行を抑えます。
この公開リポジトリでは、GitHub公式が無料とする標準ホストrunnerだけを指定しています。
secret、OIDC、書込トークン、環境保護、branch protection、課金設定は新設も変更もしません。
追加支払いが必要と判明した場合は停止し、管理者へ報告します。

## 検証結果の読み方

静的なworkflow検証と、実際のActions実行結果を分けて記録します。
PRのChecksまたはActions画面で、対象head、run URL、各OSのjob、テスト件数、skip理由を確認します。
待機、承認待ち、中止、未実行を成功として扱いません。
ローカルの結果を他OSや別Python版の成功へ読み替えません。

作業端末の生ログや内部パスをコミットせず、検証結果は必要な件数と公開run URLだけを選別して共有します。
秘密、実ユーザー情報、実IP、端末固有の接続情報をテスト入力や公開証跡へ取り込みません。
将来のテスト追加時も、公開ログに出る入力を確認します。

## 第2段階の追加条件

Go基盤と限定実通信GUIは、公開対象の成果物、配置、依存版、合意したテスト境界が確定してから別PRで追加します。
存在しない`backend/`などのパスを条件付きで飛ばして成功扱いするjobは作りません。
Goが統合された後に、そのmoduleで実行する単体テストとrace検証の可否を決めます。

fixture通信は、接続先をCI内の一時fixtureに限定できること、認証と転送の境界、停止と期限切れの観測方法を先に定めます。
GUIの子プロセス試験と配布EXEの実動作試験も、対象物と起動終了の観測方法が確定してから追加します。
配布ビルドは、固定依存、ハッシュ、ライセンス、出力物と検証記録の対応を確定する別工程です。
現行CIにはビルド、署名、公開、配備、実relay接続を含めません。

## 公式根拠

- [GitHub Actionsの安全な利用](https://docs.github.com/en/actions/reference/security/secure-use)：最小権限、actionのSHA固定、未信頼コードの扱い。
- [Workflow構文](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax)：permissions、matrix、timeout、shell。
- [Concurrency制御](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency)：同一refの重複実行制御。
- [GitHub-hosted runner](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)：標準runnerラベルと公開リポジトリの無料利用。
- [イベントとfork PR](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflows-in-forked-repositories)：fork PRの権限とsecretの制約。
- [固定したcheckout](https://github.com/actions/checkout/tree/9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0)と[固定したsetup-python](https://github.com/actions/setup-python/tree/5fda3b95a4ea91299a34e894583c3862153e4b97)：公式actionの実行定義と入力。
