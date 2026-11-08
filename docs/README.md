# Tether 文書索引

[用語と名前の境界](../GLOSSARY.md)

索引更新: 2026-11-08（参照source: `90f4e666`）。製品の方向・完了条件は[製品目標](product/README.md)、実装・検証の範囲は対象版の契約と証跡に分けます。Mac優先の常駐ホストを目指すことは、実GUI接続・実dot接続・Windows対応の達成を意味しません。

## 読む順序と正本

1. [プロジェクト入口](../README.md): 目標、対象環境、ローカル開発の入口。
2. **[限定pilotの検証記録・残件](tether-pilot-status.md)**: 2026-11-01時点の実証済み / source・fixtureのみ / 未完の記録。後続sourceの検証は各PRの固定headとCIで確認し、固定snapshotの証拠を後続buildへ引き継がない。
3. [agent CLI](agent-cli.md): 保護設定、前景起動、fresh同意、期限、終了、status pipeの契約。
4. [Mac runtime](mac-agent-runtime.md): 現在ユーザー・隔離なし、process群とI/Oの所有、回収不明の扱い。
5. [共通開発手順](agent-workflow.md): ファイル所有、検証、証跡、拒否と再開の扱い。

ここでの「現行」はこの開発ソースの方針・契約です。公開main統合済み、一般配布可能、試験用入口が稼働中という意味ではありません。
旧Python/Windowsの開始拒否、限定実演後の入口閉鎖、dot workspaceの403は、それぞれ別の状態です。

## 読者別の入口

これは既存文書への案内です。新しい利用者向けの完成済み手順書・配布許可ではありません。

| 読者 | 読む順序・章立て | 正本に残す内容 |
| --- | --- | --- |
| 初めて読む人 | [README](../README.md) → [製品目標](product/README.md) → [限定pilot記録](tether-pilot-status.md) | 価値・将来目標と、対象版の提供範囲・未検証を分ける |
| 評価する人 | [CLI](agent-cli.md) → [Mac runtime](mac-agent-runtime.md) → [viewer](../ui/mac-status/README.md) | 起動前提、本人同意、期限、停止と回収。実GUI・実dot接続の未検証を手順完了にしない |
| 運用する人 | [identity](tether-access-identity.md) → [鍵loader](agent-key-loader.md) → [gateway](agent-gateway.md) → [配布条件](distribution.md) | 登録・権限・鍵・公開入口と配布の条件。秘密や原ログを公開記録へ複製しない |
| 開発する人 | [共通開発手順](agent-workflow.md) → [配置方針](repository-layout.md) → [Go CI](go-ci.md) / [viewer](../ui/mac-status/README.md) / [Python](development.md) | 所有権・setup・対象別試験・証跡・統合。旧Pythonの成功を全製品の成功にしない |

### 停止・問題時に確認する文書

- 前景agentの期限・取消とgateway終了: [CLIの順序と所有](agent-cli.md)、[commandの取消契約](agent-command-contract.md)。この案内から新しい接続や実操作を開始しないでください。
- 終了理由と回収の区別: [Mac runtime](mac-agent-runtime.md)。command終了と全回収は別です。`cleanup unknown`は非成功・再開不可で、盲目的な再実行をしません。
- 表示が止まった・windowを閉じた: [viewerの契約](../ui/mac-status/README.md)。GUI閉鎖、EOF、残時間0はagent停止の証拠ではありません。
- 起動・登録・配布の前提が足りない: [限定pilot記録](tether-pilot-status.md)と[配布条件](distribution.md)。未登録identity、未承認の公開入口、未確認のartifactを保護回避で補いません。
- 旧Python / Windowsの回復: [旧回復手順](recovery.md)。Macの回復操作へ転用しません。

### 版・証跡・更新の読み方

source commit、アプリの版表示、配布物hash、実画面の確認環境は別の識別子です。
同じ版名や近いcommitだけでは同じ実行物と判断せず、対象PRの固定head、build入力、artifact hash、実施環境と検証範囲を対応づけます。
対応が確認できない項目は未検証のまま残します。文書の更新日だけで試験を再実行した扱いにはしません。

実装・契約を変える担当は該当する正本とこの索引を照合し、検証担当は対象版と実行範囲を記録します。
実画面・配布物・対応OSが変わった場合は利用手順をその版で見直します。恒常的な運用責任者・脆弱性受付窓口はこの索引では設定しません。
貢献規約・templateは[#8](https://github.com/O2-CONNECTIVE/rescue-host/issues/8)、読者別手順の版確定と実機確認は[#31](https://github.com/O2-CONNECTIVE/rescue-host/issues/31)に残し、[LICENSE](../LICENSE)・[CHANGELOG](../CHANGELOG.md)の存在を窓口運用や最新配布版の保証にしません。

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
- [typed launch requirement の準備](mac-launch-requirement-preparation.md): 未起動 Foundation owner と型付き条件の source fixture。実 publisher 未設定、通常起動・実 enforcement は未接続です。
- [Mac の原子的な匿名 pipe 取得](mac-atomic-preparation-pipes.md): macOS 27以上の直接 pipe2、取得直後と別 thread の固定 exec 回帰。旧 OS fallback と製品起動は追加しません。
- [未起動 owner の FFI と匿名 IO](mac-prelaunch-ffi-io.md): 元 thread と同じ Process の保持、匿名 pipe の close-once、unknown 保持の C fixture。実 producer の Wait / joins は後続です。
- [未起動 native owner の Rust 接続](mac-native-rust-owner.md): 実 C ABI と同じ owner を元 thread に保持し、retire / Drop と unknown を合成 library で検証。通常アプリは未接続です。
- [同じ native owner の byte IO](mac-native-byte-io.md): fd 所有を移さず、借用 Read / Write と control half-close を接続。raw EOF と起動後の回収証拠を区別します。
- [同じ native owner と Pump](mac-native-pump.md): 既存framing処理へ具体的な借用IOを接続するsource fixture。通常startup / native supervisorの終了証拠は未接続です。
- [Go license inventory](go-license-inventory.md)、[配布条件](distribution.md)。旧配布条件の合格を新しいMac packageへ転用しない。
- [検証状態](tether-pilot-status.md)のpackage節に、license閉包・署名・notarization・受領者起動の残件を集約。
- [旧Python開発手順](development.md)はPython部分の検証用。Go/Swiftの全試験を包含しない。

- [active native owner と Pump](mac-native-active-pump.md): 元 owner の実 IO を既存 shared driver へ接続し、wire / EOF / Wait / close / IO worker join を別々に検査。通常起動と native Supervisor の全 joins は未接続です。

- [同じ native IO worker の supervisor](mac-native-io-supervisor.md): 元 owner の Wait / IO 回収と実 join の component を接続。通常起動・outer joins・Quit 許可は未接続です。

- [同じ native owner の outer monitor](mac-native-outer-monitor.md): 実 IO join に続く monitor の実 join を接続。Unknown と Quit 拒否、通常未接続を維持します。

- [同じ native owner と typed port の寿命](mac-native-session.md): 元の監視所有者と状態ポートを同じ寿命で保持する source-only 組成。通常起動は未接続です。

## 保存する旧設計・履歴

SSH途中実装の既知の公開WIP保存先は `wip/ssh-owned-shell-20261031` です。これは保存済みcheckpointの案内で、今回remoteの最新状態は再照合していません。未完成コードの保存を製品対応・main統合と見なさないでください。

以下は参照対象を限定した履歴・別方式の契約です。旧条件の合否を維持し、現在の提供状況には上記正本を優先します。旧44条件や失敗をMac評価の成功で達成扱いにしません。

- [旧検証状態](status.md)。
- [旧構成図](architecture.md) / [旧SSH・Windows安全要件](security.md) / [旧脅威モデル](threat-model.md)。
- [SSH backend基盤](../backend/README.md) / [旧通信契約](communication-contract.md) / [Windows評価手順](windows-evaluation-runbook.md)。
- [Windows LocalService helper ADR](adr/0004-localservice-auth-helper.md) / [旧Tauri評価版](tauri-evaluation.md)。
