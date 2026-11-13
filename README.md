# Tether（テザー）

[用語と名前の境界](GLOSSARY.md) / [製品目標と完了条件](docs/product/README.md) / [文書索引](docs/README.md)

必要な間だけ、承認した相手から端末を操作できる仕組みを作っています。
**開発中・限定評価段階です。一般配布・本番利用はまだできません。**
現在は **AI agent から Mac を操作する経路**を優先しています。

この README は、機能ごとに「何が実装済みで、どこまで確かめたか」を示します。
統合の経緯と日付ごとの記録は[統合と検証の経緯](docs/integration-history.md)に分けています。

## 目標

1. ChatGPT などの MCP クライアントから、自分の開発 PC でリモート開発ができる。
2. 被支援者が GUI で待機と接続を許可し、支援者がエージェントを通じてその PC を支援できる。

入口は GUI と MCP を軸にします。GUI は待機・許可・停止・表示を担い、MCP はエージェントの入口です。
CLI は開発と CI の検証用です。人向け SSH 互換は現在の完成条件から外しています。
目標の正本は[製品目標](docs/product/goals-and-acceptance.md)、この2点の裁定は[コマンド実行の設計](docs/run-command-design.md)にあります。

## 機能ごとの実装と検証

凡例: **実機** は実 ChatGPT と実 Mac（開発用 build、1名・1 Mac）での確認、**CI** は自動試験、**source** はコードと試験だけで実機未確認です。
実機の確認はすべて未署名の開発用 build によるもので、製品の署名・配布の受入ではありません。

### MCP から使える操作

| 機能 | 操作名 | 実装 | 検証範囲と残り |
| --- | --- | --- | --- |
| コマンド実行 | `tether_run_command` | 作業 root 配下で任意の shell コマンドを1件実行する。`cwd` と `timeout_ms` を指定できる。Mac 側で時間制限付きの許可（最大15分）が ON のときだけ動く | 実機: 実行・状態照会・取消・待機 OFF・許可 OFF・実行記録の6項目。CI: 実 producer の4 lane。残り: 作業 root を GUI で選ぶ（[#359](https://github.com/O2-CONNECTIVE/Tether/issues/359)） |
| 固定作業 | `tether_fixed_start` | 決まった検証コマンドを1件実行する | 実機: BEGIN → 5秒待機 → END を1回。CI |
| 状態と結果 | `tether_fixed_status` | 同じ実行の状態・終了コード・出力・回収の状態を返す | 実機、CI |
| 取消 | `tether_fixed_cancel` | 実行中の作業を止める。取消の受付は停止の完了ではない | 実機、CI |
| 一覧 | `tether_fixed_list` | 同じ主体・host・接続世代の保持記録を1回最大32件返す。作業内容と出力は返さない | CI。対応 backend のみ |
| 出力なしの照会 | `tether_fixed_get_execution` | 出力本文を複製せずに終了・停止・回収の状態を返す | CI。対応 backend のみ |
| 出力の読取 | `tether_fixed_read_output` | 保持した出力を cursor 指定で1回最大4096 bytes 読む。読んでも消えない | CI。対応 backend のみ |
| 解放 | `tether_fixed_release` | 終了した作業の保持記録を解放する | CI |

契約: [コマンド実行](docs/run-command-design.md) / [一覧](docs/resident-execution-list.md) / [状態照会](docs/tether-mcp-get-execution.md) / [出力読取](docs/resident-read-output.md) / [出力の形式](docs/resident-mcp-output-contract.md)

### 接続・待機・停止

| 機能 | 実装 | 検証範囲と残り |
| --- | --- | --- |
| MCP 接続と認証 | MCP クライアント → 認証入口 → 中継 → gateway → Mac の常駐。gateway は呼出主体と登録済み host を照合する。接続できることや待機 ON を、作業の許可とは扱わない | 実機: 実 ChatGPT から接続。残り: 制御接続は5分で切れて自動で再接続する。無人運転と途切れない日常利用は未達 |
| 待機 ON/OFF | Mac の常駐が待機を切り替える。OFF で新規受付を止め、実行中の process を止める | 実機: 実行中の待機 OFF で2秒以内に停止。CI。残り: 署名済みの製品起動、通常 GUI からの実接続 |
| 実行の許可 | コマンド実行は、Mac 側で時間制限付きの許可を ON にした間だけ受け付ける。許可は期限と待機 OFF で失効する | 実機: 許可 OFF での拒否。期限だけの失効は source |
| 停止と回収 | 期限・取消・通信断で失効し、同じ所有者が process と入出力の回収を試みる。Mac 側と gateway 側の回収が両方確認できたときだけ終了とし、不明のままなら次の実行を拒否する | 実機、CI。回収不明は成功扱いしない |
| 通信断からの復帰 | 通信断で届かなかった回収の証明を、常駐が次の接続で送り直す。gateway は元の接続の認証値で検証する | 実機: 実行中の通信断の後、gateway を再起動せずに次の実行が通る（2回）。残り: 常駐を強制終了した後は、gateway を再起動するまで拒否が続く |
| 実行記録 | Mac が実行したコマンドを、時刻・ディレクトリつきでローカルに記録する | 実機: 記録と GUI 表示の一致 |

### 入口

| 入口 | 実装 | 検証範囲と残り |
| --- | --- | --- |
| MCP | 上の操作を提供する。開発環境の入口は [Cloudflare の構成](deploy/cloudflare/README.md) | 実機: 実 ChatGPT の開発コネクタ |
| Mac 開発 GUI（[ui/mac-development](ui/mac-development/README.md)） | 待機と実行の許可の切替、実行したコマンドの表示 | 実機: コマンド表示。許可の切替は pipe 経由の受理まで確認。GUI のボタン操作の実機確認は残り |
| Mac 状態表示（[ui/mac-status](ui/mac-status/README.md)） | 読取専用の SwiftUI viewer。承認・開始・取消はしない | build と合成状態。実 agent との同時動作は残り |
| Tauri GUI（[ui/tauri](ui/tauri/README.md)） | 待機 ON/OFF を実 Go の待機処理へつなぐ | CI: Mac 専用の実 WebView と IPC。通常起動・配布は未接続 |
| CLI（`tether-work` ほか） | 同じ MCP session で固定作業の開始・結果・取消・解放を扱う。[契約](docs/work-control-cli.md) | CI。通常起動は認証入力が未構成のため拒否する |

### 未提供・準備中

| 機能 | 状態 |
| --- | --- |
| ファイルの一覧・読取・検索・編集 | 読取の core と Mac 用 reader を非公開で準備。MCP には未接続で、提供していない |
| 端末一覧、対話入力 | 未実装（[操作辞書 #188](https://github.com/O2-CONNECTIVE/Tether/issues/188)） |
| 他人の Mac への一時許可 | 方針のみ裁定済み。実装は後続 |
| Windows | source と独立 probe まで。製品の実接続受入は未完 |
| 単発派遣（[#22](https://github.com/O2-CONNECTIVE/Tether/issues/22)） | 前景 CLI と評価 package 作成ツールの source。配布受入は未完 |
| 署名・notarization・一般配布 | 未受入 |

追跡先: [製品体系と受入 #189](https://github.com/O2-CONNECTIVE/Tether/issues/189) / [共通サービス #32](https://github.com/O2-CONNECTIVE/Tether/issues/32)

## 構成

```text
MCP クライアント（ChatGPT など）
  → 認証入口（開発環境は Cloudflare Access）
  → 中継（Worker。制御接続は5分上限）
  → Go gateway（主体と登録済み host の照合、作業の枠と回収の判定）
  → 署名付きの要求
Mac の常駐（待機 ON/OFF、実行の許可、process の所有と回収、実行記録）
  └→ GUI（待機・許可・表示）
```

gateway は Mac の同意を代行せず、許可を発行・延長しません。
人向け SSH server や既存 sshd への fallback はありません。
責任分担の詳細: [gateway](docs/agent-gateway.md) / [通信](docs/agent-transport.md) / [Mac runtime](docs/mac-agent-runtime.md)

## リポジトリの構成

| ディレクトリ | 内容 |
| --- | --- |
| `backend/` | Go の本体。gateway、Mac の常駐、MCP adapter、CLI。実行ファイルの入口は `backend/cmd/` |
| `ui/` | GUI。`mac-development`（開発 GUI）、`mac-status`（状態表示）、`tauri`（Tauri GUI） |
| `deploy/cloudflare/` | 開発環境の入口と gateway コンテナの構成 |
| `ci-producer/` | CI で実 producer をつなぐ試験の fixture |
| `contracts/` | `control-v1` の schema と試験 vector |
| `scripts/` | CI と検査の script、その試験 |
| `tools/` | 調査用の独立 probe と trace |
| `docs/` | 設計・契約・受入記録。入口は[文書索引](docs/README.md) |
| `src/`、`tests/`、`build_windows.ps1` | 旧 Python 実装。履歴として保持 |

## 対象環境

| 対象 | 現時点の範囲 |
| --- | --- |
| Mac command target | 現在ユーザー権限、昇格なし・sandboxなし。旧pilot agent build宣言はmacOS **26.0**。native pipe取得は**27以上**を要求し、旧方式へfallbackしない。製品セット全体の互換性は未受入 |
| Mac status viewer | macOS **13.0**以上を宣言。ただし13実機未検証で、agent全体の対応下限ではない |
| Linux / Windows | gatewayの配置候補とsource/build検証をcommand target対応と区別。製品目標はMac先行、その後Windows。Windows製品の実接続受入は未完 |

## ローカル開発の最短入口

既存の対応 Swift toolchain がある Mac で、リポジトリルートから：

```sh
cd ui/mac-status
swift build
swift run StatusCoreChecks
python3 scripts/package_local.py
```

最後の行は既存 build からローカル評価 `.app` を作るだけで、起動・install・署名・agent接続をしません。既存出力は上書きせず停止します。
[viewer の手順と制約](ui/mac-status/README.md)を先に確認してください。通常起動には専用 status pipe が必要で、入力がなければ接続状態は不明です。

Go の入口は `backend/cmd/rescue-gateway`、`backend/cmd/rescue-mac-agent`、stdio adapter は `backend/cmd/rescue-mcp` です。
実接続は保護設定・鍵・identity・今回の同意・許可された入口がそろってから行います。clone/buildだけでは接続できません。
[Go検証](docs/go-ci.md) / [CLIの設定契約](docs/agent-cli.md) / [開発手順](docs/agent-workflow.md)

## 保証しないこと

- Macの実行は現在ユーザーと同じ権限です。そのユーザーがアクセスできるファイル・ネットワークへ届き、同一ユーザーからの干渉も防げません。Windowsの隔離guestと同等ではありません。
- 期限・取消・通信断で失効し、同じownerが所有process群とI/Oの回収を試みます。実行済み変更は戻らず、群を離れた子孫の停止は保証しません。
- cleanup unknownは非成功です。GUIの閉鎖・EOF・残時間0だけでは停止確認になりません。読取専用SwiftUI viewerは承認・開始・取消を行いません。Tauri resident GUIのON/OFFは専用CIでの限定検証で、通常起動の認可gateを解除しません。
- packageに鍵・token・同意を入れません。license/依存閉包、配布署名・notarization、一般受領者の受入は未完です。評価 `.app` のlinker ad-hoc署名は配布identityの署名ではありません。

[文書索引と現行安全契約](docs/README.md) / [Mac runtime](docs/mac-agent-runtime.md) / [旧設計の安全要件](docs/security.md)

## 経緯と歴史資料

- [統合と検証の経緯](docs/integration-history.md): この README に以前あった、PR ごとの統合記録と実接続の確認記録。
- [限定pilotの検証記録・残件](docs/tether-pilot-status.md): 2026-11-01 時点の実機の固定証跡。
- [旧検証状態](docs/status.md): SSH/Windowsの過去の提供状況。旧受入条件の未達や過去の失敗は消していません。現在のMac限定評価と合成しないでください。

[配置方針](docs/repository-layout.md) / [変更履歴](CHANGELOG.md) / [ライセンス](LICENSE)
