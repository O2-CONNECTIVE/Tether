# Issue トリアージラベル

この文書は Tether の Issue 分類とトリアージ運用を定める。組織標準ラベル名・説明・色の正本は [`O2-CONNECTIVE/.github` の `standards/labels.yml`](https://github.com/O2-CONNECTIVE/.github/blob/main/standards/labels.yml) と [`docs/issue-labels.md`](https://github.com/O2-CONNECTIVE/.github/blob/main/docs/issue-labels.md)。

## Canonical role 対応

| Canonical role | Tether のラベル | 意味 |
| --- | --- | --- |
| `needs-triage` | `Needs-Triage` | maintainer の評価待ち |
| `needs-info` | `Needs-Info` | 追加情報待ち |
| `ready-for-agent` | `AFK` | 自律 agent が着手できる状態 |
| `ready-for-human` | `HITL` | 人の介入が必要 |
| `wontfix` | `Wontfix` | 対応しない |

## Area と Kind

`Area:*` は主な機能・技術領域、`Kind:*` は作業の性質を表す。母集団に含まれる各 issue に各軸を1個ずつ付ける。

### Area

| Label | 用途 |
| --- | --- |
| `Area:Resident` | Mac 常駐・待機・回収・コマンド実行（Mac agent 側） |
| `Area:Gateway-MCP` | gateway・MCP tool・agenttransport・署名 intent・Access 認証 |
| `Area:Guest-Isolation` | 一時 Guest・隔離・close 経路・背圧（W/L/U 受入） |
| `Area:Tauri-UI` | Tauri 画面・Rust IPC・native smoke・Mac/開発 GUI |
| `Area:Distribution` | 配布物・署名・評価版・ライセンス/notice |
| `Area:Testing` | CI ゲート・フレーク・fixture・検証入口 |
| `Area:Dev-Process` | issue 運用・開発手順・agent ハーネス・リポジトリ規約 |
| `Area:Product` | PRD・製品目標・ロードマップ・受入条件の定義 |

### Kind

| Label | 用途 |
| --- | --- |
| `Kind:Bug` | 意図しない動作・不具合の修正 |
| `Kind:Feature` | 新しい機能・できることが増える変更 |
| `Kind:Refactor` | 振る舞いを変えずに構造を変える |
| `Kind:Test` | テストの追加・分割・修正・テスト基盤の改善 |
| `Kind:Perf` | 実行速度・バンドルサイズ・クエリ効率の改善 |
| `Kind:Security` | 認可漏れ・秘密情報・攻撃面の是正 |
| `Kind:Docs` | ドキュメント・ADR・運用手順の記述 |
| `Kind:Ops` | 本番設定・デプロイ・依存/ランタイム移行・運用作業 |
| `Kind:Decision` | 裁定・方針決定そのものが成果物 |

分類に迷う場合は、中心となる領域を `Area:*` にする。テスト基盤自体が主題なら `Area:Testing`、機能のテスト追加ならその機能の Area と `Kind:Test` を使う。

## 運用上の判定

- **母集団**: `Invalid` / `Duplicate` / `Wontfix` のどれかが付いた issue は分類を要求しない。それ以外は open/closed を問わず、既知の Area がちょうど1個、既知の Kind がちょうど1個必要。
- **`Needs-Category`**: 未分類の目印。分類が不足・不正なら付け、分類が整えば外す。`Needs-Triage` とは別軸で、同時に付くことがある。
- **`Kind:Decision` と `Kind:Docs`**: issue を閉じるために新しい選択をするなら `Decision`。決定済みの内容を記録・説明するなら `Docs`。ADRも選択が未了なら `Decision`、確定内容の反映なら `Docs`。

## 自動検査

[`issue-triage-labels.yml`](../.github/workflows/issue-triage-labels.yml) は起票・編集・ラベル変更・再オープン時に、現状態を GitHub REST API から読み直して検査する。`Needs-Category` の付け外しと、HITL subtype がある場合の親 `HITL` 追加だけを自動修正する。親 `HITL` の追加は、`AFK`・`Needs-Triage`・`Needs-Info` と同居していないときに限る（どちらを残すかを人が選ぶ組合せを、片側に寄せないため）。意味の選択が必要な違反は修正せず、失敗として summary に出す。手動実行は `issues: read` だけを持つ別 job で、全 issue（closed を含む）の読取専用 audit を行い、drift を表にする。移管（transferred）はイベントが移管元に届くため対象にせず、移管先の分類漏れはこの audit で拾う。

## Actionable destinations

`AFK` は自律 agent が着手可能、`HITL` は人の介入を必要とする状態。両者は同居できない。`HITL:Decision` / `HITL:Manual-Action` / `HITL:Research` / `HITL:Verification` は介入の種類を示す。subtype には親 `HITL` が必要で、workflow は親だけを補う。`HITL` 単独は subtype を人が選ぶまで違反となる。

## Blocked

`Blocked:*` は進行を止めている理由を表し、HITL subtype とは別軸。`AFK` と `Blocked:*` は同居できない。HITL と Blocked の併用は可能。

## Lifecycle

- `Needs-Triage`: 未評価。
- `Needs-Info`: 追加情報待ち。
- `Wontfix`: 対応しないと判断し、理由を残して close。

`Needs-Triage` と `Needs-Info` は `AFK`、`HITL`、または HITL subtype と同居できない。lifecycle と destination の選択は人が整理する。
