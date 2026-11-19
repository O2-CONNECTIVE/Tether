# 構成と実装境界

> **対象範囲の更新（2026-11-01）:** 以下の旧SSH / Windows / Pythonに関する本文・受入条件は、その方式と記録時点の履歴として保持しています。現在のTetherはAI commandと一時派遣を目標にMacを優先し、現状の正本は[文書索引](README.md)と[検証状態](tether-pilot-status.md)です。旧ゲート閉鎖や未達を解除・達成扱いにはしません。Macは現在ユーザー・非sandboxであり、Windows隔離guestの保証を引き継ぎません。

[READMEへ戻る](../README.md)

採用済みの上位配置は[単発版の配置と責任分担](ephemeral-deployment-architecture.md)、補助受入は[配置の受入計画](ephemeral-deployment-acceptance.md)を参照してください。
配置の採用はWindows実行方式の採用や実接続の有効化を意味しません。

新規接続は無効です。
公開CLIと未公開の限定評価stdio、将来のTauri adapter案の区別は[通信契約](communication-contract.md)を参照してください。
保護資産と信頼境界、残る受入条件は[脅威モデル](threat-model.md)に整理しています。

| 部分 | 役割と境界 | 公開ソースまたは詳細 |
|---|---|---|
| オフライン制御 | 固定宛先と許可操作IDを判定し、準備、有効化、失効、終了、期限、競合、再試行をモデル化 | [操作方針](../src/rescue_policy.py)、[期限モデル](../src/rescue_lease.py)、[モデルのAPIと限界](OFFLINE_BACKEND_MODEL.md) |
| 旧 Python 実装 | 旧 Tkinter GUI は #407、設定（`Settings`）・開始ゲート（`require_secure_session_backend()`）・Upterm の起動と停止の `rescue_core` と、期限監視の `rescue_watchdog` は #416 で削除した。Python に実セッションを開始する経路は残っていない | [旧記録の扱い](recovery.md) |
| 将来の安全境界 | 実SSH、固定宛先接続、内側sshdの制約、保護されたWindows監督は未実装 | [バックエンド契約](SECURITY_BACKEND_CONTRACT.md) |

## 操作モードと実権限

旧 GUI で選んで保存していた 2 つのモードは、GUI と `Settings` ごと削除しました。
残るのは `OperationPolicy`（`rescue_policy.py`）が 2 つのモードを表すオフラインのモデルだけで、既定は制限付きです。
両方とも実接続は無効です。

制限付きの操作IDは将来のヘルパー仕様であり、診断や修復コマンド自体は未実装です。
フル操作は通常ユーザーのシェル操作を意図し、管理者昇格を認可しません。
モードの選択や外側のTCP宛先判定だけでは、内側SSHの実操作権限は制御できません。
詳しくは[オフラインモデル](OFFLINE_BACKEND_MODEL.md)を参照してください。
