# Tauri UI のデザイントークン案

[開発へ戻る](development.md) / [追跡 Issue #16](https://github.com/O2-CONNECTIVE/Tether/issues/16) / [親受入 #1](https://github.com/O2-CONNECTIVE/Tether/issues/1)

## 制作前の意図と範囲

遠隔サポートの現場で、許可相手・操作範囲・期限と現在の接続状態を読み取り、停止操作を見つけやすくします。
停止を要求したことと、通信の終了を確認したことは別に表示します。
視覚上の意図は、明るい背景、読みやすい日本語、余白のある操作領域、状態を文字と記号でも区別する落ち着いた道具です。
初回はデザイントークン → 最小の共通部品 → 状態見本の順に実装します。

この案はライトテーマのみです。
ブランド指定は見つからなかったため、既存 `src/rescue_host.py` の文字 `#172836`、青 `#155A92`、赤 `#B42337`、緑 `#007F60`、背景 `#F4F6F9` を参照した暫定案です。
O2 の他製品の色を要件としていません。

既存 Tkinter の評価経路、製品接続ゲート、Go の認可・TTL・失効責務は変更しません。
Tauri shell の IPC、子プロセス、実接続、配布物、ダークテーマの実装は非目標です。
状態見本の接続中・終了確認済は、表示仕様を検討するための固定例であり、実通信の結果ではありません。
実画面へ移す際は、バックエンド未接続を明示し、未配線の開始・停止を無効にします。
カウントダウン、選択値、ボタン状態は権限や終了の証拠になりません。

## 三層の責務

CSS の正本は [tokens.css](../ui/tauri/design/tokens.css) です。
`--ref-*` は色・寸法などの基礎値、`--sys-*` は意味、`--cmp-*` は部品への割当です。
部品と画面は基礎 palette を直接参照せず、semantic または component token を参照します。
色名と機能名を同一視せず、将来のテーマは semantic の割当を差し替えます。

| 基礎 | 具体値 | 理由 |
| --- | --- | --- |
| Palette | white `#FFFFFF`、slate 50 `#F4F6F9` / 100 `#E7EDF3` / 400 `#748598` / 600 `#526479` / 900 `#172836` | 背景・境界・本文の階調。補助文も読める濃さにする |
| Palette | blue 50 `#EBF3FA` / 700 `#155A92` / 800 `#104A79` | 主操作と情報の系列。接続中も情報として扱う |
| Palette | red 50 `#FCEEF0` / 700 `#B42337` / 800 `#921D30` | 停止操作と確認不能を示す系列 |
| Palette | amber 50 `#FFF6E5` / 800 `#805000`、teal 50 `#EAF6F1` / 700 `#007F60` | 処理中の注意と終了確認を区別 |
| Typography | `Yu Gothic UI`, `Hiragino Kaku Gothic ProN`, `Meiryo`, system-ui, sans-serif | Windows / macOS の日本語と代替 font。外部 font 通信なし |
| Type scale | 0.875 / 1 / 1.25 / 1.75 rem、本文 line-height 1.75 | 補助・本文・見出し・主状態。本文は 16px 相当を基準 |
| Spacing | 0 / 4 / 8 / 12 / 16 / 24 / 32 / 48px 相当の rem | 4px 刻み。文字拡大と同じ単位で伸びる |
| Radius | 4 / 8 / 12px 相当 | 入力・ボタン・状態領域のまとまり |
| Shadow | none / `0 2px 8px rgb(23 40 54 / 8%)` | 補助的な奥行き。境界を影だけに依存させない |
| Motion | 0 / 120ms、ease-out | hover の色変化のみ。停止・警告の点滅、常時アニメーションなし |

| Semantic role | ライトテーマでの割当 |
| --- | --- |
| surface canvas / default / subtle | slate 50 / white / slate 100 |
| text primary / secondary | slate 900 / slate 600 |
| border default / strong | slate 400 / slate 600 |
| action primary / hover / on-primary | blue 700 / blue 800 / white |
| danger foreground / surface / action / hover | red 700 / red 50 / red 700 / red 800 |
| warning foreground / surface | amber 800 / amber 50 |
| info foreground / surface | blue 700 / blue 50 |
| confirmation foreground / surface | teal 700 / teal 50 |
| neutral foreground / surface | slate 600 / slate 50 |
| focus ring / offset | blue 700 / white |
| disabled foreground / surface / border | slate 600 / slate 100 / slate 400 |

最小 component token は button の高さ・余白・radius・各色、status card の余白・radius・境界幅・状態色、input の高さ・余白・radius・境界・error 色です。
hit target は最低 44px 相当です。
無効なボタンを opacity だけで薄くせず、文字を読めるままにします。
focus は 3px 相当の輪郭と 2px 相当の間隔で示します。

## 状態から semantic role への対応

| 状態 | 記号・文言 | Role | 判断の根拠と操作 |
| --- | --- | --- | --- |
| バックエンド未接続 | ○ バックエンド未接続 | neutral | 相手・範囲・期限は未確認。開始・停止とも無効。終了確認済とは表示しない |
| 接続中 | ↔ 接続中 | info | バックエンドの観測結果が必要。「安全認証済」という意味を含めない。停止を見つけやすくする |
| 停止中 | … 停止中・終了を確認しています | warning | 停止要求だけで終了確認済へ移さない。重複操作の扱いは backend 契約で定義する |
| 終了確認済 | ✓ 終了確認済 | confirmation | 所有通信の終了を確認した結果が必要。実行済みの変更を取り消した意味ではない |
| 終了確認不能 | ! 終了を確認できません | danger | IPC 断・応答なし・終了証拠不足。画面を閉じるだけで解消したと扱わない |
| 入力エラー | ! 許可相手を確認してください | danger | 対象入力の近くへ理由と修正方法を置く。生ログや資格情報を表示しない |

「停止」は曖昧なので、操作ボタンは「すべて停止」、途中状態は「停止中」、観測結果は「終了確認済」または「終了を確認できません」とします。
詳細ログ・診断は別領域とし、最初に読む接続状態と競合させません。

## 展開と検証基準

- ライトのみ実装。dark theme は各 foreground / surface / border / focus の組合せを再測定してから追加する。
- `prefers-reduced-motion: reduce` で transition を無効にする。状態の意味を motion に依存させない。
- `forced-colors: active` は OS の Canvas / CanvasText / ButtonText / Highlight を使い、色を失っても文言と記号、境界を残す。実 OS の高コントラスト表示は別途確認する。
- ブラウザーの実 HTML/CSS を広幅・狭幅・200% 相当で確認し、横方向の欠落、重なり、停止操作の欠落がないこと。
- keyboard で移動でき、focus が明瞭で、入力とエラー説明が対応すること。
- 本文・補助文字・有効 button は 4.5:1、操作境界・focus は隣接背景に対して 3:1 以上を検証する。無効部品も同じ文字可読性を目標にする。
- 固定例は常に「状態見本」と明示し、実接続・実停止の証拠として扱わない。

測定と確認手順は [design README](../ui/tauri/design/README.md) を参照してください。
今回の測定結果と描画画像は[確認範囲](tauri-design-verification.md)にまとめています。
静的値・コントラスト・画面確認は振る舞い TDD の実績に加算しません。
GUI ↔ 子プロセス境界の TDD は、固定バックエンドとライフサイクル契約を確認してから赤 → 緑で別変更にします。

## 次の Tauri shell で維持する境界

Rust は狭い IPC adapter に限定し、任意 shell、広い filesystem、remote page、資格情報保存を追加しません。
command は application manifest と明示 capability で制限し、入力 version・型・上限を Rust 側で検証します。
CSP は bundled code と IPC に限定し、URL・ログ由来の文字列を HTML として挿入しません。
Go sidecar の起動・停止・親死・中断と終了観測が未確認の間は配線しません。
同一ユーザー攻撃者への隔離は UI 移行で完成しません。

WebView2 がない Windows、オフライン配布、OS ごとの font / WebView 差、署名・更新は残る設計課題です。
システムへの toolchain や WebView2 のインストール、設定変更は実行しません。
CI 成功を新しい PC の手動確認の代替にしません。

公式仕様の参照（2026-10-27）: [Capabilities](https://v2.tauri.app/security/capabilities/)、[CSP](https://v2.tauri.app/security/csp/)、[Rust commands](https://v2.tauri.app/develop/calling-rust/)、[Windows installer](https://v2.tauri.app/distribute/windows-installer/)。
