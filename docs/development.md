# 開発とオフライン検証

> **対象範囲:** 以下は旧Python部分（v0.2.1-dev）の開発・オフライン検証手順です。現在のTether全体のバージョン・提供段階や、Go/Swiftの検証入口を示しません。[文書索引](README.md)、[Go CI](go-ci.md)、[Mac viewer](../ui/mac-status/README.md)を用途別に参照してください。

[READMEへ戻る](../README.md)

エージェントで開始・再開・編集委譲・レビューを行う場合は[共通エージェント開発手順](agent-workflow.md)を参照してください。

対象は新規接続を無効にしたv0.2.1-devです。
Python 3.11以上とTkinterを想定し、実行時の追加Python依存はありません。
全OSや全Python版での検証済み宣言ではありません。
公開済みの結果と環境は[status](status.md)へまとめています。

## オフライン試験

共通入口は追加pip依存のない`scripts/dev.py`です。
新しいcommitのsubjectと説明bodyは[日本語commit方針](commit-messages.md)で別々に検査します。
開発時の下限はPython 3.11、既存CIの固定参照版はPython 3.13.17 x64です。
参照版は[CI workflow](../.github/workflows/python-offline.yml)と揃え、更新時は両方を照合します。
別のpatch版で実行した場合は版の差を表示し、3.11未満は失敗します。
Pythonの導入やOS設定変更はコマンドに含みません。

| 環境 | 既存テストと構文の共通チェック | 実行条件 |
| --- | --- | --- |
| Linux / macOS | `python3 scripts/dev.py check` | `python3`が3.11以上を選ぶこと。CIと揃える場合は3.13.17を明示的に選ぶ |
| Windows PowerShell | `py -3.13 scripts/dev.py check` | Pythonランチャーと3.13系が導入済みであること。実際のpatch版は出力で確認する |

リポジトリのルートから実行します。
別のカレントディレクトリからは`scripts/dev.py`の場所を指定でき、内部の対象解決は呼出元に依存しません。
`test`は既存unittestだけ、`syntax`は`src/`、`tests/`、`scripts/`のPython構文だけを確認します。
構文チェックは組み込みの`compile()`を使い、import、GUI起動、bytecodeの保存を行いません。
`check`はテスト成功後に構文チェックを実行し、いずれかの失敗で非0終了します。
テストにはTkinterのimportが必要で、GUIの実画面は開きません。
対象不存在、空の対象、テスト0件、全件skipは成功扱いしません。
テストの総数、実行数、合格数、skip数、失敗数、エラー数を区別し、skip理由は詳細ログに表示します。

開発ツール自身の異常条件は`python3 -m unittest discover -s scripts/tests -v`で別に検証します。
この全件試験はsource-sizeの検証を含むためPython 3.13以上が必要です（CI固定版は3.13.17）。既存の`check`の下限Python 3.11とは区別します。
build-lockの有限fixtureは同じPythonのpipも使い、ネットワークなしでhash不一致を拒否することを確認します。
Windowsでは同じ引数を`py -3.13`に渡します。
対象欠落、空対象、テスト0件、全件skip、テスト失敗、構文不正などで、実際のコマンドが非0終了することを確認します。
これは開発ツールの検証であり、製品試験や新しい製品境界のTDDへ加算しません。
既存の3 OS CIも共通の`check`とこの開発ツール試験を呼びます。

公開済みデザイントークンの静的検証は`python3 scripts/dev.py tokens`で実行します。
Windowsでは`py -3.13 scripts/dev.py tokens`を使います。
このコマンドはTkinterをimportせず、既存checkerを同じPythonの隔離モードで起動します。
checkerが使用するassertを`PYTHONOPTIMIZE`環境変数で無効にしないため、[Pythonの隔離モード`-I`](https://docs.python.org/3.13/using/cmdline.html#cmdoption-I)を指定しています。
CIも独立stepでこの入口を使い、非0終了を失敗として扱います。
トークン関係の開発ツール試験は正常例、不正参照、白文字と白背景のコントラスト低下を確認します。
開発ツールの全件試験は共通入口、トークン、source-size、build-lock、commit-message、Go CIのJSONイベントと資源記録の検証を含みます。
実行件数、skip、失敗は、対象SHAの[PythonオフラインCI](../.github/workflows/python-offline.yml)のログで確認します。
不正例は一時コピーにだけ作成し、公開tokenを変更しません。
検証範囲は既存checkerの参照規則と指定されたライトテーマのコントラストで、生成物の一致、ブラウザー描画、Tauri実機、製品UIのTDDではありません。

以下の既存コマンドも維持します。
環境変数の指定とテスト対象の確認は呼出側で行います。

```bash
PYTHONPATH=src python3 -m unittest discover -s tests -v
python3 -m compileall -q src tests
```

Windows PowerShellの場合：

```powershell
$env:PYTHONPATH = 'src'
py -3 -m unittest discover -s tests -v
```

模擬Uptermとモックによる試験で、公開中継、実PC、TCPポート22には接続しません。
模擬ライフサイクルだけは開始ゲートをテスト内で置き換えます。
この成功は実接続の安全性を証明しません。
モデルだけの試験は[既存の実行手順](OFFLINE_BACKEND_MODEL.md#実行)を参照してください。

既存テストの3 OS自動実行、skipの読み方、未検証範囲は[PythonオフラインCI](ci.md)を参照してください。

公開`backend/`の検証は[Go基盤のCI](go-ci.md)と[専用workflow](../.github/workflows/go-offline.yml)を正本とし、依存取得、vet、build境界、通常試験とrace試験を照合します。
Pythonで行うGo CI helperのJSON検証は、Go試験そのものの実行とは別です。
Go基盤はmainへ統合済みですが、GUIからの起動、製品IPC、製品の実通信は未配線です。
P2のruntime未実証、既存44条件の未達・未検証と製品接続gate閉鎖を維持し、コード統合やCI成功を実通信の合格へ読み替えません。

Tauri移行は[デザイントークン案と状態見本](tauri-design-tokens.md)から進めます。
ライトテーマの状態見本と、[Tauriの最小未配線shell](tauri-native-shell.md)を分けて確認します。
shellにも接続や停止のバックエンドは配線していません。
共通部品と画面を変更するときは[デザインとアクセシビリティの基準案](design-accessibility.md)を参照してください。
自動検査と実機確認の分担は[確認方法と証跡](design-accessibility-verification.md)へまとめています。

## GUIとWindowsビルド

`python src/rescue_host.py`はGUIを開きます。
保存された旧セッションがあると回復処理を実行するため、本番PCでは承認前に実行しないでください。
回復動作は[回復と画面終了](recovery.md)を参照してください。

[build_windows.ps1](../build_windows.ps1)はWindows x64向けの未検証スクリプトです。
想定前提はWindows 10/11 x64、CPython 3.11–3.15 x64、Pythonランチャー`py`、インターネット接続です。
EXEは未生成で、ARM64も未検証です。
実行時に固定版Upterm v0.39.0とPyInstallerを取得します。
`-UseLocalUpterm`には独立に確認した`-LocalUptermSha256`と対応ライセンスが必要です。
ダウンロードと同じ経路のチェックサムだけでは配布元侵害を防げません。
Windows向けPyInstallerと推移依存6個（計7個）は版とwheel hashを固定し、一時venv内で導入・整合検証します。
Python/Tk等を含む環境全体の固定と再現ビルドは未実装です。
ビルドしても新規接続は有効になりません。
未署名の配布候補に必要なmanifest、ハッシュ、license、依存一覧、sourceとの対応は[配布候補の準備条件](distribution.md)を参照してください。
開発コマンドはGUIやビルドを自動実行しません。

## 詳細と履歴

- [構成と実装境界](architecture.md)
- [リポジトリの配置方針](repository-layout.md)
- [接続再開のバックエンド契約](SECURITY_BACKEND_CONTRACT.md)
- v0.2.1の引き継ぎ（過去の参照資料・本文非同梱）
- 前段候補の引き継ぎ（過去の参照資料・本文非同梱）（履歴。現仕様はv0.2.1を優先）
- [変更履歴](../CHANGELOG.md)

既存の引き継ぎにある元HANDOFFと元レビューの未提供事項は、この文書整理では解消していません。
2026-10-26の公開はソース候補の公開のみで、配備、稼働サービスの外部公開、本番PC接続ではありません。
今後の実接続と有効化は[安全要件](security.md)に従う別工程です。

[React 画面層・Lucide 規則と品質ゲート](tauri-react-ui.md) / [実行手順](../ui/tauri/README.md)

[Windows WebView2 native smoke の実測と未検証範囲](tauri-native-smoke.md)
