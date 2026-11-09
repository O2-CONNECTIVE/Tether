# 操作側プロトタイプの確認範囲

[仕様と起動方法](README.md) / [UI基準](../../../docs/design-accessibility.md) / [追跡 #25](https://github.com/O2-CONNECTIVE/Tether/issues/25)

## 実行環境と限界

2026-10-28、macOS、Node 26.10.0、Chrome 156.0.8078.48で確認しました。
Pythonの静的確認は3.13.2です。CI固定版とは別のローカル証跡です。
今回の入口は独立したブラウザープロトタイプで、Tauri native起動の証跡ではありません。

| 対象 | 結果と確認範囲 |
| --- | --- |
| controller公開操作 | 9件成功。承認・準備・接続・停止要求・終了確認、古い応答、破棄後更新、失効後の復活禁止、履歴削除と新規接続gateの分離 |
| memory履歴 | 7件成功。3項目のみ、不変snapshot、個別／一括削除、30日ちょうどのprune、削除後の遅延更新による復活防止 |
| operator browser | 5件成功。コピー前確認、停止要求と証拠、初期化の禁止、read-only再確認、トレイのモック、入力破棄、focus、診断、狭幅と拡大 |
| 既存host回帰 | 既存 `npm test` 32件、host browser 8件成功。operatorの振る舞い試験とは別 |
| TypeScript / ESLint / Knip | strict型、typed/Hook lint、通常とproduction解析が成功。operatorの独立Vite入口も解析 |
| build / notice | host buildと正本noticeチェック後、別のoperator buildを生成。lock・notice CLI・hostの配布入口は未変更 |
| tokens / source-size | 134 tokens、既存23 contrast組合せ、例外なしのtracked source予算を確認。画面全体のa11y合格ではない |

controller / memory履歴の16件は `node --test tests/operator-controller.test.ts tests/operator-history.test.ts`、browserの5件は別build後の `node --test tests/operator-browser.test.ts` で実行します。
operatorのunit/browserは現行 `npm test` に含まれません。共有CIへつなぐ作業はtask-3 / 統合担当が所有します。
接続前の既存CI成功をoperatorの実行成功へ読み替えません。

## TDDと追加回帰

以下はローカルの同じ公開境界で実測したRED→GREENです。
CI上でREDを観測したという意味ではありません。元ログは非公開作業領域に保持し、個人の絶対パスや原例外を転記しません。

| 境界 | RED commit | GREEN commit |
| --- | --- | --- |
| サンプル受取り→確認 | `0aa539f` | `b787c1b` |
| 依頼受付・承認・接続完了の分離 | `1bea659` | `67402b3` |
| 停止要求だけで終了確認にしない | `421e38b` | `553dd5a` |
| 期限切れ・拒否・失敗・確認不能 | `95cfb58` | `11c09ca` |
| 遅延応答・破棄・要求失敗 | `3777830` | `fb5238b` |
| 確定仕様の単一接続・トレイ・read-only確認・失効後禁止 | `94812cd` | `87266f3` |
| 履歴削除で未確認gateを解除しない | `9626cee` | `7ba9794` |
| トレイ停止と削除キャンセル後のfocus | `d3d8cf8` | `e059e45` |

仕様更新では、終了の独立した確認応答と、遅れた単なる接続成功応答を分けました。
未確認・失効状態を接続成功応答で解除しません。同じ接続の終了証拠が届いた場合だけ終了確認へ進みます。
memory履歴moduleにも別のRED→GREENがあり、時計・不変snapshot・削除後更新の追加回帰と区別しています。

## 画像とアクセシビリティ

`e059e45c3e899ed8cdb25f121dcdf4fdf980e0d5` の実HTML/CSSを描画した以下の画像を別途提出します。
画像生成mockではなく、画面内の承認・接続・終了結果が明示的なmockです。

- `operator-review-wide.png`: サンプルの接続先・許可・期限と、確認後の依頼操作。
- `operator-unconfirmed-wide.png`: 終了未確認、無効な新規接続、診断、履歴。
- `operator-tray-narrow200.png`: トレイのモックを320px幅・200%文字で表示した検証用画像。

色に加えて文言・Lucide SVG、名前・展開状態、44px相当のtarget、focus outlineを確認しました。
320px幅、1200px幅での200%文字、320px幅と200%文字の組合せで横overflowがないことを自動確認しています。
reduced motion / forced colorsはChromeの模擬設定です。状態や有効／無効の色はアニメーションせず、読取を優先します。
スクリーンショットだけでkeyboard合格にはせず、実際のキー操作とfocus復帰を別に検証しています。

実screen reader、Windows/macOS/Linuxのnative WebView、実DPI、OS高コントラスト、AA達成は未検証です。
clipboardはbrowser APIへDI配線していますが、E2Eは隔離したclipboard fixtureを使い、実OSのclipboardは検証していません。
トレイ・重要通知・履歴保持の画面とモデルはmockです。実トレイ、OS通知、永続保存、常駐の30日削除処理は未実装です。
履歴の時間経過は固定のmock時計で再現し、OSの時計や実接続の期限を変えません。
実認証・実承認・Go/SSH・runtime bootstrap・製品gateの解除は含めません。

## 独立レビュー

規約と仕様/securityを別のagentが読み取りレビューしました。
規約レビューのP2（消える操作後のfocus復帰2経路）をbrowserでRED確認し、`e059e45`で5件すべてGREENにしています。
同headの差分レビューで規約・仕様/securityの未解決findingはありません。
静的レビュー、ローカル操作、今後の共有CIと実機は別々の証跡です。正式PR approveは行っていません。

## 情報量を整理した後続差分

`7e6171a`を基点に、通常画面を相手・端末、許可範囲・期限、状態と主操作へ絞り、技術情報・診断・履歴・通知見本・モック操作を「詳細を見る」へ移しました。
`68c1c49`の変更前UIで詳細triggerが0件（期待1件）となるREDを観測し、変更後のbrowserは6件成功しています。
コピー前確認、停止要求と終了証拠の分離、終了未確認時の新規接続禁止と、詳細のkeyboard開閉・focus復帰を確認します。
controller / adapter / 履歴store / 認可のモデルは変更していません。既存unit16件と型/lint/Knipは別に再実行します。

同じ1200×1000 viewport・同じ確認／終了未確認状態で変更前後を比較します。
変更前は上記 `operator-review-wide.png` / `operator-unconfirmed-wide.png`、変更後は `operator-review-after.png` / `operator-unconfirmed-after.png` です。
変更前画像を再生成せず、既存の別送画像をそのまま比較に使います。通常画面の主操作が初期viewport内にあることは画像だけでなくbrowserでも検査しています。
追加差分も実接続・nativeトレイ・OS通知・永続化の実装や、AA達成の証拠へ読み替えません。

独立レビューの通常画面停止後focus消失をbrowserでRED確認し、停止要求後は状態見出しへfocusを戻す差分を追加しました。トレイと通常画面の両経路をkeyboardで確認します。
