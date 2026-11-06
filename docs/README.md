# Tether 文書索引

[用語と名前の境界](../GLOSSARY.md)

更新: 2026-11-01。現在の目標は **AIによる遠隔command操作** と **必要時だけの人間によるagent派遣**です。Macを優先し、人向けSSH互換・Windows target追加は保留しています。

## 読む順序と正本

1. [プロジェクト入口](../README.md): 目標、対象環境、ローカル開発の入口。
2. **[検証状態・残件](tether-pilot-status.md)**: 実証済み / source・fixtureのみ / 未完の正本。固定snapshotの証拠を後続buildへ引き継がない。
3. [agent CLI](agent-cli.md): 保護設定、前景起動、fresh同意、期限、終了、status pipeの契約。
4. [Mac runtime](mac-agent-runtime.md): 現在ユーザー・隔離なし、process群とI/Oの所有、回収不明の扱い。
5. [共通開発手順](agent-workflow.md): ファイル所有、検証、証跡、拒否と再開の扱い。

ここでの「現行」はこの開発ソースの方針・契約です。公開main統合済み、一般配布可能、試験用入口が稼働中という意味ではありません。
旧Python/Windowsの開始拒否、限定実演後の入口閉鎖、dot workspaceの403は、それぞれ別の状態です。

## 現行構成と責任

| 層 | 正本 | 境界 |
| --- | --- | --- |
| MCP API | [adapter](agent-mcp-adapter.md) / [discovery](mcp-discovery.md) | start/output/status/cancel。discoveryはcommand実行ではない |
| HTTP認証・主体照合 | [gateway](agent-gateway.md) / [identity](tether-access-identity.md) | 検証したissuer/subjectを登録済みprincipalと結ぶ。メールdomain全員を認可しない |
| 配送と暗号channel | [WSS transport](agent-transport.md) / [agent channel](agent-channel.md) | relayは新しい認可源ではない。gatewayはMCPのpayloadを扱う信頼境界で、不透明relayと同一視しない |
| 承認と実行 | [command契約](agent-command-contract.md) / [CLI](agent-cli.md) / [runtime](mac-agent-runtime.md) | Mac本人のfresh同意がgrant発行元。gateway/adapterは発行・延長しない |
| 保護入力 | [鍵loader](agent-key-loader.md) / [Linux loader](agent-key-loader-linux.md) | 設定・鍵のowner/権限/同一性を検査。実登録とsource試験は別 |
| 状態表示 | [SwiftUI viewer](../ui/mac-status/README.md) | 専用pipeのみ、読取専用。生成時刻で鮮度を判定、EOFや画面閉鎖は回収証拠でない |
| 配置 | [専用deployment設定](../deploy/cloudflare/README.md) | 配置手順は実行許可ではない。新しい認証・route・公開窓は別途確認 |

Macのcurrent-user実行はsandboxではなく、同一ユーザー権限で読める情報・変更できる状態へ届きます。同一ユーザーによる干渉、既実行の副作用、群を離れた子孫は残るリスクです。terminal cleanup unknownは警告・非成功・再開不可で、正常回収へ読み替えません。

4名向けpolicyは4名の実backend登録完了ではありません。native4case、Claude HTTP/OAuthの4tool discovery、SwiftUIの合成GUI検証は別の境界です。現在のagent最低OS宣言26.0とviewer宣言13.0も分けます。

## 開発・配布

- [Go CI](go-ci.md)、[viewer build/checks](../ui/mac-status/README.md)、[配置規約](repository-layout.md)。
- [Mac resident sourceの段階計画](mac-resident-source-plan.md): 純粋transcript・worker ownerの公開準備と、未接続のproducer/policy閉包・build/package/配置信頼の残件。
- [正規transport ownerのsource閉包](mac-canonical-transport-owner.md): 共通Dispatch・部分取得保持と、各componentのcleanup観測／未接続範囲。
- [fixed intentのwire／runtime引継ぎ](mac-fixed-intent-handoff.md): 正規namespace、固定内容、元の有限期限とtimer所有の維持。
- [resident transport と protected policy](mac-resident-transport-policy.md): 相互proof・opaque receiver・同世代receiptと、strict source/workerの未接続閉包。
- [Standby と owned pipe の source 組成](mac-resident-producer-composition.md): 同じ owner の終了と codec / mapper / owned IO を結び、sealed entrypoint と実機の未達を分けます。
- [resident library の build record](mac-resident-library-build.md): 固定 source・lock・recipeとcompile-only archiveを結び、起動・配布はblockedのまま記録します。
- [native enrollment 選択と起動信頼](mac-resident-trusted-preparation.md): OS account 由来の固定 policy 選択を内部準備し、署名・配置・launch-time enforcement の残る条件を分けます。
- [Go license inventory](go-license-inventory.md)、[配布条件](distribution.md)。旧配布条件の合格を新しいMac packageへ転用しない。
- [検証状態](tether-pilot-status.md)のpackage節に、license閉包・署名・notarization・受領者起動の残件を集約。
- [旧Python開発手順](development.md)はPython部分の検証用。Go/Swiftの全試験を包含しない。

## 保存する旧設計・履歴

SSH途中実装の既知の公開WIP保存先は `wip/ssh-owned-shell-20261031` です。これは保存済みcheckpointの案内で、今回remoteの最新状態は再照合していません。未完成コードの保存を製品対応・main統合と見なさないでください。

以下は参照対象を限定した履歴・別方式の契約です。旧条件の合否を維持し、現在の提供状況には上記正本を優先します。旧44条件や失敗をMac評価の成功で達成扱いにしません。

- [旧検証状態](status.md)。
- [旧構成図](architecture.md) / [旧SSH・Windows安全要件](security.md) / [旧脅威モデル](threat-model.md)。
- [SSH backend基盤](../backend/README.md) / [旧通信契約](communication-contract.md) / [Windows評価手順](windows-evaluation-runbook.md)。
- [Windows LocalService helper ADR](adr/0004-localservice-auth-helper.md) / [旧Tauri評価版](tauri-evaluation.md)。
