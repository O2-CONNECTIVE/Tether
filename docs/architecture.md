# 構成と実装境界

> **対象範囲の更新（2026-11-01）:** 以下の旧SSH / Windows / Pythonに関する本文・受入条件は、その方式と記録時点の履歴として保持しています。現在のTetherはAI commandと一時派遣を目標にMacを優先し、現状の正本は[文書索引](README.md)と[検証状態](tether-pilot-status.md)です。旧ゲート閉鎖や未達を解除・達成扱いにはしません。Macは現在ユーザー・非sandboxであり、Windows隔離guestの保証を引き継ぎません。

[READMEへ戻る](../README.md)

採用済みの上位配置は[単発版の配置と責任分担](ephemeral-deployment-architecture.md)、補助受入は[配置の受入計画](ephemeral-deployment-acceptance.md)を参照してください。
配置の採用はWindows実行方式の採用や実接続の有効化を意味しません。

新規接続は無効です。
以下の図は公開済みソースの構成を示し、実GUIのスクリーンショットではありません。

![Rescue Hostの構成図。実装済みのGUI設定とオフラインモデル、常時拒否する開始ゲート、未実装の実SSHとWindows監督を区別。](assets/architecture.svg)

[PNGで開く](assets/architecture.png) / [編集可能なSVG](assets/architecture.svg)

実線は実装済み、赤い領域は常時拒否する開始ゲート、破線は未実装の計画です。
色のほか番号、文字、線種でも区別しています。
GUIから実セッションへの接続線はありません。

公開CLIと未公開の限定評価stdio、将来のTauri adapter案の区別は[通信契約](communication-contract.md)を参照してください。
保護資産と信頼境界、残る受入条件は[脅威モデル](threat-model.md)に整理しています。

| 部分 | 役割と境界 | 公開ソースまたは詳細 |
|---|---|---|
| 設定 | GUIで操作範囲を保存。`Settings.to_operation_policy()`で不変方針に変換するが、起動や認可はしない | [GUI](../src/rescue_host.py)、[設定と回復処理](../src/rescue_core.py) |
| オフライン制御 | 固定宛先と許可操作IDを判定し、準備、有効化、失効、終了、期限、競合、再試行をモデル化 | [操作方針](../src/rescue_policy.py)、[期限モデル](../src/rescue_lease.py)、[モデルのAPIと限界](OFFLINE_BACKEND_MODEL.md) |
| 開始ゲート | GUIとワーカーで実セッションの開始を拒否。設定や環境変数で解除できない | [require_secure_session_backend()](../src/rescue_core.py) |
| 旧記録の回復 | 記録の検証と停止要求を補助。同一ユーザーの監視は安全境界や確実な失効機構ではない | [監視処理](../src/rescue_watchdog.py)、[回復と画面終了](recovery.md) |
| 将来の安全境界 | 実SSH、固定宛先接続、内側sshdの制約、保護されたWindows監督は未実装 | [バックエンド契約](SECURITY_BACKEND_CONTRACT.md) |

## 操作モードと実権限

GUIで「制限付き診断・修復」と「フル操作」を選択して保存できます。
既定は制限付きで、旧設定の接続方式とは区別して移行します。
両方とも実接続は無効です。

制限付きの操作IDは将来のヘルパー仕様であり、診断や修復コマンド自体は未実装です。
フル操作は通常ユーザーのシェル操作を意図し、管理者昇格を認可しません。
GUIの選択や外側のTCP宛先判定だけでは、内側SSHの実操作権限は制御できません。
詳しくは[オフラインモデル](OFFLINE_BACKEND_MODEL.md)を参照してください。
