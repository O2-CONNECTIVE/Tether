# Tauri UI のデザイントークン案

[開発へ戻る](development.md) / [追跡 Issue #16](https://github.com/O2-CONNECTIVE/Tether/issues/16) / [親受入 #1](https://github.com/O2-CONNECTIVE/Tether/issues/1)

## 制作前の意図と範囲

遠隔サポートの現場で、許可相手・操作範囲・期限と現在の接続状態を読み取り、停止操作を見つけやすくします。
停止を要求したことと、通信の終了を確認したことは別に表示します。
視覚上の意図は、明るい背景、読みやすい日本語、余白のある操作領域、状態を文字と記号でも区別する落ち着いた道具です。
初回はデザイントークン → 最小の共通部品 → 状態見本の順に実装します。

この案はライトテーマのみです。
色・部品の形・ブランドの表示は、O2 CONNECTIVE の Derivation-Products（private リポジトリ `O2-CONNECTIVE/O2-CONNECTIVE-Derivation-Products` の main）を踏襲します（2026-11-18 決定、[#392](https://github.com/O2-CONNECTIVE/Tether/issues/392)）。
書体だけは踏襲せず、OS 標準にします（2026-11-18 決定、[#397](https://github.com/O2-CONNECTIVE/Tether/issues/397)）。
O2 の丸ゴシックは、OS 標準の書体で描くタイトルバー・メニュー・ダイアログから浮いて見えたためです。
以前の暫定案（`src/rescue_host.py` の色を参照し、O2 の他製品の色を要件としない）は、この決定で置き換えました。
出どころは `src/mvp.css` の「C2 friendly enterprise theme」、`src/Brand.jsx`、`public/gateway-mark.svg` です。
Derivation-Products にダークテーマが無いため、ダークテーマは範囲外です（Mac のネイティブ GUI は OS 標準の色のまま）。
画面の用語の決まり（Derivation-Products の `docs/ui-terminology.md` と同じ形の表）は別の Issue で扱います。

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

| 基礎 | 具体値 | 出どころと理由 |
| --- | --- | --- |
| Palette: 面 | white `#FFFFFF`（panel）、canvas `#F8FBFA`、sidebar `#E7F3EF` | O2 の `--panel`・`--friendly-canvas`・`--friendly-sidebar` |
| Palette: 文字と線 | ink `#33413D`、ink-muted `#56645F`、line `#DFE9E5`、line-control `#66887E` | ink・muted・line は O2 の値。line-control は操作部品の枠用に AA のため寄せた値（後述） |
| Palette: 操作 | green 700 `#167A65` / 800 `#106B59` | O2 の primary と hover。操作・focus・リンク・ブランドの副題だけに使い、状態色にしない |
| Palette: 状態 | blue 50 `#E8F1F4` / 700 `#065292`、purple 50 `#F0EBF7` / 700 `#6E5C83`、amber 50 `#FFF5D8` / 800 `#805000`、red 50 `#FDF3F3` / 700 `#9D3434` / 800 `#832C2C` | blue 700 は gateway-mark の青、blue 50 と purple は O2 のバッジ色、amber 50 は O2 の accent、red 50・700 は O2 の danger。amber 800 は今までの値（O2 に警告の文字色の定義が無いため据え置き）。red 800 は hover 用に red 700 を暗くした値で、O2 に対応が無い |
| Typography | 本文: `-apple-system`, `BlinkMacSystemFont`, `Hiragino Sans`, `Hiragino Kaku Gothic ProN`, `Segoe UI`, `Yu Gothic UI`, `Meiryo`, system-ui, sans-serif。等幅: `ui-monospace`, `SF Mono`, `Menlo`, `Cascadia Mono`, `Consolas`, monospace | OS 標準の書体（[#397](https://github.com/O2-CONNECTIVE/Tether/issues/397)）。本文は Mac が SF Pro とヒラギノ角ゴ、Windows が Segoe UI と Yu Gothic UI。等幅（鍵の指紋・コード）は Mac が SF Mono → Menlo、Windows が Cascadia Mono → Consolas。書体は同梱せず、外部 font 通信なし |
| Type scale | 0.875 / 1 / 1.25 / 1.75 rem、本文 line-height 1.7、見出しと button の weight 600 | O2 の本文の行間 1.7 と見出しの 600。本文は 16px 相当を基準 |
| Spacing | 0 / 4 / 8 / 12 / 16 / 24 / 32 / 48px 相当の rem | 変更なし。4px 刻みで、文字拡大と同じ単位で伸びる |
| Radius | 4px（inline）/ 10px（button）/ 11px（input）/ 16px（panel・状態の領域） | O2 の値。button は `.primary`・`.ghost`、input は入力欄、panel は `.panelCard` |
| Shadow | panel `0 9px 28px rgb(38 75 66 / 5.5%)`、primary button `0 4px 12px rgb(22 122 101 / 14%)` | O2 の panel と primary。panel は縁取りも残し、境界を影だけに依存させない |
| Motion | 0 / 120ms、ease-out、回転 1000ms | 変更なし。hover の色変化と、確認中の記号の回転のみ。停止・警告の点滅なし |

| Semantic role | ライトテーマでの割当 |
| --- | --- |
| surface canvas / default / subtle | canvas / white / sidebar |
| text primary / secondary | ink / ink-muted |
| border default / strong / subtle | line-control / ink-muted / line（subtle は panel の縁と区切り線だけ。後述） |
| action primary / hover / on-primary | green 700 / green 800 / white |
| danger foreground / surface / action / hover | red 700 / red 50 / red 700 / red 800 |
| warning foreground / surface | amber 800 / amber 50 |
| info foreground / surface | blue 700 / blue 50 |
| confirmation foreground / surface | purple 700 / purple 50 |
| neutral foreground / surface | ink-muted / white |
| focus ring / offset | green 700 / white |
| disabled foreground / surface / border | ink-muted / sidebar / line-control |

最小 component token は button の高さ・余白・radius・影・各色、status card の余白・radius・境界幅・状態色、panel の radius・影・縁、input の高さ・余白・radius・境界・error 色です。
O2 の「アイコンボタンはツールチップ付き」に当たる部品は、この Tauri の画面にありません（アイコンは文字つきの button だけ）。
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

## 状態色の割当と、primary の緑の使い方

primary の緑は操作の色です。「正常」を表す状態色と兼ねません。
`check_tokens.py` は、primary（通常と hover）が状態の色（info・confirmation・warning・danger・neutral の文字色と背景色）と同じ値であること、色相が 30° 未満の差であることを拒否します。
今までの案の「終了確認済」`#007F60` は、primary `#167A65` と同じ青緑なので採りませんでした。

| 状態の role | 色 | 理由 |
| --- | --- | --- |
| info（接続中） | blue 700 `#065292` | gateway-mark の青。今までの青（`#155A92`）とほぼ同じ |
| confirmation（終了確認済） | purple 700 `#6E5C83` | O2 のバッジ色。緑は使わず、✓ の記号と「終了確認済」の文言で示す |
| warning（停止中） | amber 800 `#805000` | 今までの値 |
| danger（終了確認不能・入力エラー・停止操作） | red 700 `#9D3434` | O2 の danger。停止は赤系のまま |
| neutral（バックエンド未接続） | ink-muted `#56645F` | O2 の補足の文字色 |

Mac のネイティブ GUI でも、「接続中」の文字色は OS の accent をやめて info の青にします。

## ブランドの表示

`src/Brand.jsx` の構成（gateway-mark と「O2 CONNECTIVE」、副題に製品名の「Tether」）を、`ui/tauri/src/Brand.tsx` と状態見本の見出しに置きます。
この Tauri アプリの CSP は `img-src` を許可していないので、画像ファイルではなくインライン SVG にします。
ロゴは装飾（`aria-hidden`）で、社名と製品名は文字で読めます。ロゴの色（`#065292`・`#45B035`）は商標の表示でありコントラストの対象外です。
ロゴの図形は O2 CONNECTIVE のブランドのロゴで、Derivation-Products（private）の `public/gateway-mark.svg` から取り込み、path と塗りは変えていません。
元と同じであることは `check_tokens.py` が hash で検査します（`ui/tauri/src/Brand.tsx` を正本に、状態見本の `index.html` と Mac の 2 つの `TetherBrand.swift` が同じ値）。
Mac のネイティブ GUI の見出し（`ui/mac-status`、`ui/mac-development`）にも、同じロゴと「O2 CONNECTIVE」の文字を置きます。Swift には SVG の path を `CGPath` にする小さな読み取りを置き、図形のデータはそのまま持ちます。
Tauri の操作側画面と host-status の見本画面には、まだ置いていません。
副題の「Tether」は primary の緑で、`#167A65` を面（canvas・白・sidebar）に対して 4.5:1 以上で測っています。

## AA のために O2 の値から変えたもの

文字と背景の組はすべて WCAG 2.2 AA（本文・補助文字 4.5:1、操作部品の境界と focus は 3:1）を満たします。
O2 の値のままでは満たさない組は、近い値に寄せ、理由をここに残します。

| O2 の値 | 問題 | この案の値と理由 |
| --- | --- | --- |
| 入力の枠 `#D6E4DF`、panel の縁と区切り線 `#DFE9E5` | 白に対して 1.31:1 / 1.24:1 で、操作部品の境界に求める 3:1 に届かない | 入力・無効 button の枠は line-control `#66887E`（O2 の focus 枠 `#78B6A7`（白に 2.32:1）と同系で暗くした値。白 3.90:1、canvas 3.74:1、sidebar 3.43:1）。panel の縁と区切り線は操作部品ではなく、中の文字が情報を担うので、O2 の `#DFE9E5` のまま装飾として使う（3:1 の対象外） |
| focus の枠 `#78B6A7` と primary 11% の薄い輪 | 3:1 に届かない | 今までの契約を保ち、3px の輪郭と 2px の間隔で、色は primary の緑（canvas 5.04:1、sidebar 4.61:1）。O2 の輪は足さない |
| 注意書きの文字 `#756D56`（accent 上で 4.73:1） | AA は満たすが、停止中の「注意」を示す色として弱い | warning の文字色は今までの `#805000`（accent 上で 6.30:1）を据え置く |
| danger の hover | O2 に定義が無い | red 700 を暗くした `#832C2C`（白の文字が 8.89:1） |
| mac-development のバッジの `systemOrange`（ライトの背景で約 2:1） | AA 未満（この案の前から） | ライトでは warning の `#805000`（canvas 上 6.58:1）。ダークは OS 標準のまま |

## Mac のネイティブ GUI の palette

`ui/mac-status`（SwiftUI）と `ui/mac-development`（AppKit）は、同じ名前の `TetherPalette.swift` を 1 つずつ持ちます。
別の SwiftPM package のため、共有 package にはせず、`// palette:start` から `// palette:end` までの値が 2 つで同じで、`tokens.css` の `--ref-*` と同じであることを `check_tokens.py` が検査します。

| palette | `--ref-*` | 使う画面 |
| --- | --- | --- |
| canvas / panel | canvas / white | 画面の背景、数値のタイル |
| ink / muted | ink / ink-muted | 本文、補助の文字 |
| line | line | 数値のタイルの縁 |
| info | blue-700 | 「接続中」の文字色 |
| warning | amber-800 | mac-development の注意バッジ |

ライトの外観だけ O2 の値にします。ダークの外観は、今までの OS 標準の色のままです。
書体は OS 標準（SF Pro とヒラギノ角ゴ）です（[#397](https://github.com/O2-CONNECTIVE/Tether/issues/397)）。
SwiftUI は `.system(size:weight:)`、AppKit は `NSFont.systemFont(ofSize:weight:)` で描きます。
カウントダウンの数値だけは桁がずれないよう、今までの等幅数字のままです。

## コントラスト比の一覧

`python3 ui/tauri/design/check_tokens.py` の出力です。`--markdown` で表だけを出し、試験が docs の一覧と一致することを確認します。
値を変えたら、この一覧を出力から作り直します。

<!-- contrast-table:start -->
| Foreground / boundary | Background | Contrast | Minimum |
| --- | --- | ---: | ---: |
| text-primary | surface-canvas | 10.27:1 | 4.5:1 |
| text-primary | surface-default | 10.69:1 | 4.5:1 |
| text-primary | surface-subtle | 9.40:1 | 4.5:1 |
| text-secondary | surface-canvas | 5.96:1 | 4.5:1 |
| text-secondary | surface-default | 6.21:1 | 4.5:1 |
| text-secondary | surface-subtle | 5.46:1 | 4.5:1 |
| danger-fg | danger-surface | 6.49:1 | 4.5:1 |
| warning-fg | warning-surface | 6.30:1 | 4.5:1 |
| info-fg | info-surface | 6.97:1 | 4.5:1 |
| confirmation-fg | confirmation-surface | 5.09:1 | 4.5:1 |
| neutral-fg | neutral-surface | 6.21:1 | 4.5:1 |
| action-on-primary | action-primary | 5.24:1 | 4.5:1 |
| action-on-primary | action-primary-hover | 6.42:1 | 4.5:1 |
| danger-on-action | danger-action | 7.06:1 | 4.5:1 |
| danger-on-action | danger-hover | 8.89:1 | 4.5:1 |
| disabled-fg | disabled-surface | 5.46:1 | 4.5:1 |
| border-default | surface-default | 3.90:1 | 3:1 |
| focus-ring | surface-default | 5.24:1 | 3:1 |
| border-default | surface-canvas | 3.74:1 | 3:1 |
| focus-ring | surface-canvas | 5.04:1 | 3:1 |
| border-default | surface-subtle | 3.43:1 | 3:1 |
| focus-ring | surface-subtle | 4.61:1 | 3:1 |
| disabled-border | disabled-surface | 3.43:1 | 3:1 |
| action-primary | surface-default | 5.24:1 | 4.5:1 |
| border-strong | surface-default | 6.21:1 | 3:1 |
| action-primary | surface-canvas | 5.04:1 | 4.5:1 |
| border-strong | surface-canvas | 5.96:1 | 3:1 |
| action-primary | surface-subtle | 4.61:1 | 4.5:1 |
| border-strong | surface-subtle | 5.46:1 | 3:1 |
| danger-fg | surface-default | 7.06:1 | 4.5:1 |
| danger-fg | surface-canvas | 6.78:1 | 4.5:1 |
| Mac ink | Mac canvas | 10.27:1 | 4.5:1 |
| Mac ink | Mac panel | 10.69:1 | 4.5:1 |
| Mac muted | Mac canvas | 5.96:1 | 4.5:1 |
| Mac muted | Mac panel | 6.21:1 | 4.5:1 |
| Mac info | Mac canvas | 7.67:1 | 4.5:1 |
| Mac warning | Mac canvas | 6.58:1 | 4.5:1 |
<!-- contrast-table:end -->

「Mac …」は Mac の palette の組で、それ以外は Tauri のトークンです。
この一覧は文字・操作部品の境界・focus だけを対象にします。panel の縁・区切り線・ロゴ・影は、情報を担わない装飾なので含みません。

## 展開と検証基準

- ライトのみ実装。dark theme は各 foreground / surface / border / focus の組合せを再測定してから追加する。
- `prefers-reduced-motion: reduce` で transition と回転を無効にする。状態の意味を motion に依存させない。
- `forced-colors: active` は OS の Canvas / CanvasText / ButtonText / Highlight を使い、色を失っても文言と記号、境界を残す。実 OS の高コントラスト表示は別途確認する。
- ブラウザーの実 HTML/CSS を広幅・狭幅・200% 相当で確認し、横方向の欠落、重なり、停止操作の欠落がないこと。
- keyboard で移動でき、focus が明瞭で、入力とエラー説明が対応すること。
- 本文・補助文字・有効 button は 4.5:1、操作境界・focus は隣接背景に対して 3:1 以上を検証する。無効部品も同じ文字可読性を目標にする。結果は上の一覧にあり、すべて満たしている。
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
