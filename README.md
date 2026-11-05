# Tether（テザー）

[用語と名前の境界](GLOSSARY.md)

[製品目標と完了条件](docs/product/README.md)では、Mac内セルフホストから外部接続へ進む目標と、現在の検証状態を分けて案内しています。

必要な間だけ、承認した相手から端末を操作できる仕組みを作っています。
**開発中・限定評価段階です。一般配布・本番利用はまだできません。**
現在は **AI agent から Mac を操作する経路**を優先しています。

## 2つのゴール

1. **dot がリモートホストのコマンドを操作できること。** MCP の start / output / status / cancel を、本人の承認・期限・失効に結びます。
2. **人間が開発 PC から、必要なときだけ操作 agent を派遣できること。** 最小経路は受領者による取得・前景起動・その回の明示同意です。無認証 remote install や恒久常駐を前提にしません。

人向け SSH 互換は現在の完成条件から外しています。途中の SSH / Windows 実装は削除せず、将来の再統合候補として保存しています。製品名の変更に伴う repository や実行ファイル名の一括変更はしていません。

## 現在地

| 境界 | 確認したこと | まだ確認していないこと |
| --- | --- | --- |
| Mac native command | 固定した旧 snapshot の1名・1 Macで正常実行、取消、要求なし期限切れ、実行中通信断の4 case | 任意 workload、他端末・他利用者、新しい build 全体の実動作 |
| Claude Code HTTP/OAuth | 実認証・接続と4 toolの discovery | Claude からの実 command。native 4 caseとは別の証跡 |
| dot → MCP | 実装・接続準備 | workspace の plugin 作成が403拒否。管理者による利用許可確認待ち |
| Mac status GUI | 読取専用 SwiftUI、build・合成状態・所有 window 終了、ローカル評価 `.app` | 実 agent と GUI の同時動作、Finder/Gatekeeper、署名済み配布 |
| 人間による一時派遣 | 前景 CLI、評価 package 作成ツールの source | 一般受領者の起動受入、初回 enrollment、配布条件の充足 |

4名用の認証方針があることと、4名全員の backend identity / 鍵登録が済んでいることは別です。
限定実演用の公開入口・専用 gateway は閉鎖・停止を確認済みで、常時接続サービスは提供していません。
この文書は現在の開発ソースを説明し、すべてが公開 main に統合済みという宣言ではありません。
詳細と固定証跡は **[現在の検証状態・残件](docs/tether-pilot-status.md)** を正本とします。

## 構成と対象環境

```text
MCP client → HTTPS 認証入口 → Go gateway / 薄い MCP adapter
                                  ↕ 認証済み agent channel / WSS
受領者の Mac: 前景 CLI → fresh 同意 → 有限 grant → command runtime
                    └→ 専用 status pipe → 読取専用 SwiftUI viewer
```

gateway は呼出主体と登録済みホストを照合しますが、Mac の同意を代行せず grant を発行・延長しません。
人向け SSH server や既存 sshd への fallback はありません。
詳しい責任分担は[文書索引](docs/README.md)、[gateway](docs/agent-gateway.md)、[CLI](docs/agent-cli.md)を参照してください。

| 対象 | 現時点の範囲 |
| --- | --- |
| Mac command target | 現在ユーザー権限、昇格なし・sandboxなし。現在の agent build 宣言は macOS **26.0** |
| Mac status viewer | macOS **13.0**以上を宣言。ただし13実機未検証で、agent全体の対応下限ではない |
| Linux / Windows | gateway の配置候補と source/build 検証を、command target対応と区別。Windows target追加は保留 |
| 旧 Windows Tauri / Python | 履歴として保持。現在のMac実行基盤へ接続されたGUIではない |

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

## 安全と配布の境界

- Macの実行は現在ユーザーと同じ権限です。そのユーザーがアクセスできるファイル・ネットワークへ届き、同一ユーザーからの干渉も防げません。Windowsの隔離guestと同等ではありません。
- 期限・取消・通信断で失効し、同じownerが所有process群とI/Oの回収を試みます。実行済み変更は戻らず、群を離れた子孫の停止は保証しません。
- cleanup unknownは非成功です。GUIの閉鎖・EOF・残時間0だけでは停止確認になりません。GUIは承認・開始・取消を行いません。
- packageに鍵・token・同意を入れません。license/依存閉包、配布署名・notarization、一般受領者の受入は未完です。評価 `.app` のlinker ad-hoc署名は配布identityの署名ではありません。

[文書索引と現行安全契約](docs/README.md) / [Mac runtime](docs/mac-agent-runtime.md) / [旧設計の安全要件](docs/security.md)

## 歴史資料

SSH/Windowsの過去の提供状況は旧検証状態に分けて記載しています。
旧受入条件の未達や過去の失敗は消していません。現在のMac限定評価と、旧SSH/Windowsの提供状況を合成しないでください。

[旧検証状態](docs/status.md) / [配置方針](docs/repository-layout.md) / [変更履歴](CHANGELOG.md) / [ライセンス](LICENSE)
