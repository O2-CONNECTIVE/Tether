# Tether の製品目標と完了判定

[プロジェクト入口](../../README.md) / [既存文書索引](../README.md) / [配置方針](../repository-layout.md)

更新: 2026-11-05。
製品の方向と機能別の完了条件は [製品目標 Issue #189](https://github.com/O2-CONNECTIVE/rescue-host/issues/189) に対応します。
通常用途は、認可された操作クライアントが MCP 経由で待機中の自分の Mac / Windows に仕事を依頼し、結果を受け取り、ホスト側で停止できる常駐ホストです。
必要なときだけ接続する単発派遣も、別の用途として維持します。
利用者の開発 PC である Mac を先に完成させ、その後 Windows を完成対象とします。
まず Mac 内のセルフホストで基本動作を検証し、その後に外部の操作クライアントから安全に届く経路を検証します。

**この索引は目標と現在の実装を分ける入口です。**
常駐版の完成、両 OS 対応、一般配布、本番利用可能を宣言するものではありません。

## 読む文書の選び方

| 確認したいこと | 正本と入口 | 読み取る範囲 |
| --- | --- | --- |
| 目指す体験と実施順 | [製品目標と完了条件](goals-and-acceptance.md)、[#189](https://github.com/O2-CONNECTIVE/rescue-host/issues/189) | MCP 接続と Mac GUI を先に完成させ、Windows GUI へ続ける目標 |
| 接続構成の段階検証 | [接続検証 #190](https://github.com/O2-CONNECTIVE/rescue-host/issues/190) | Mac 内セルフホスト、外部実クライアント、必要な追加機能の順に検証する |
| 実装と実機の確認状況 | [検証状態と残件](../tether-pilot-status.md)、対応 PR の固定 head と CI | 実証した版と境界。目標の記述から達成を推測しない |
| 現在の利用と開発の入口 | [既存文書索引](../README.md)、[CLI](../agent-cli.md)、[共通開発手順](../agent-workflow.md) | その版で利用できる手順と制限 |
| 現在の認可と停止の契約 | [command 契約](../agent-command-contract.md)、[gateway](../agent-gateway.md)、[Mac runtime](../mac-agent-runtime.md) | 本人同意、許可の寿命、所有と回収の現行ルール |
| 操作辞書の拡充 | [詳細 PRD #188](https://github.com/O2-CONNECTIVE/rescue-host/issues/188)、[共通 API #32](https://github.com/O2-CONNECTIVE/rescue-host/issues/32) | 後続の設計提案。提案された tool は提供済みと扱わない |
| 単発用途と過去の受入 | [単発版 #22](https://github.com/O2-CONNECTIVE/rescue-host/issues/22)、[旧検証状態](../status.md) | 単発版の条件、既知の未達、当時の記録 |
| 未決の判断 | [目標文書の未決事項](goals-and-acceptance.md#未決事項)、[Decision #36](https://github.com/O2-CONNECTIVE/rescue-host/issues/36) | 製品方向の合意だけでは決まらない個別仕様 |

既存 README や契約に残る「Mac 優先」「Windows target 保留」「常駐は候補」は、それぞれの版の実装範囲や当時の判断と照合します。
今回の目標は Mac / Windows の常駐ホストですが、未対応能力を有効にしたり、現行の許可を広げたりする根拠にはなりません。
現在の実装状態は固定版の証跡、将来の成果は #189、詳細設計の提案は #188 を参照します。

## 文書の配置と更新担当

`docs/product/` には、公開する製品目標、機能ごとの完了条件、その正本への索引を置きます。
実機の結果をこの索引へ重複転記せず、検証担当が [検証状態](../tether-pilot-status.md) と対応 PR に対象版と確認範囲を記録します。
契約の変更は各実装担当が契約文書と試験を同じ変更単位で更新します。
目標の優先順位や完了条件を変える場合は #189 とこのディレクトリを照合します。

既存の詳細文書、履歴、実装ディレクトリは現在の位置で参照します。
この追加で文書やコードを移動しません。
配置を変更する際は [配置方針](../repository-layout.md) と [編集所有権](../agent-workflow.md#開始と編集所有権) に従い、対象ファイルの担当と進行中 PR を確認します。
個別の作業メモ、原ログ、個人環境情報、キャッシュは公開文書へ含めません。
