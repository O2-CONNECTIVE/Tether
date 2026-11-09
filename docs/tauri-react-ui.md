# React 画面層とアイコンの方針

[未配線 shell の契約](tauri-native-shell.md) / [デザインと a11y 基準](design-accessibility.md) / [tokens](tauri-design-tokens.md)

## 意図と受入

承認済みの React + TypeScript + Vite、shadcn/ui、Lucide を画面層へ採用します。
既存 Issue [#16](https://github.com/O2-CONNECTIVE/Tether/issues/16) と [#25](https://github.com/O2-CONNECTIVE/Tether/issues/25) の次段階です。
ライトの既存 tokens を正本とし、主状態、許可条件、停止、独立した診断の順に読める画面を維持します。
状態は確認中、バックエンド未接続、取得不能だけで、開始と停止は無効、通信終了は未確認です。
Go、SSH、sidecar、認可、TTL、保存、dark theme、配布更新は非目標です。
表示やカウントダウンを権限の根拠にしません。
受入は既存の厳密な IPC 応答検証、有限待機、破棄後の遅延応答無視、診断の keyboard 開閉、focus 保持、320 CSS px と文字200%、色に依存しない状態表示です。
ブラウザーと fixture を native 実機起動や通信終了の証拠に読み替えません。

## 部品と DI

primitive は Radix だけに統一します。
shadcn/ui の Button、Card、Collapsible の必要部分を所有ソースとして管理します。
固定元は `shadcn-ui/ui@d094cd36caf871f2fde3e339b87aa43b9af5a7d0` の `apps/v4/registry/new-york-v4/ui/{button,card,collapsible}.tsx` です。
公式 source の utility classes を既存 semantic CSS へ適合する変更で、公式 vanilla CSS recipe ではありません。
既定 palette、Tailwind reset、Next、SSR、大きな状態管理は導入しません。
native adapter は bootstrap だけで生成し、React へ props、controller へ引数として渡します。
controller は DOM、React、Tauri global に依存しません。
DI コンテナや旧 Python の直接 IO は持ち込みません。
IPC 文字列契約は公開境界試験で確認し、Knip を security 検証の代わりにしません。

## アイコン規則

製品 UI は絵文字禁止です。
固定版 Lucide の必要な SVG だけを named import し、外部 CDN、動的な任意 SVG、HTML 挿入は使いません。
寸法、stroke、色の継承、文字との gap は tokens、具体アイコンと文言は部品の意味として管理します。
初期値は通常20px、状態24px、補助16px、stroke 2、gap 8pxで、寸法は rem により文字拡大へ追従します。
色は既存 semantic foreground の `currentColor` を継承します。

| 意味 | Lucide | 可視ラベルの規則 |
| --- | --- | --- |
| 追加 | Plus | 「担当者を追加」のように対象を明記 |
| 編集 | Pencil | 対象が分かる「編集」 |
| 削除 | Trash2 | 「削除」と対象、必要な確認を併記 |
| 接続停止 | Square | 「接続を停止」。閉じるとは分ける |
| 閉じる | X | 閉じる。通信終了の証拠にはしない |
| 診断の開閉 | ChevronRight | 「診断情報」と展開状態 |
| 状態取得中 | LoaderCircle | 「状態を確認中」。reduced motion では静止 |
| 未接続 | CircleDashed | 「バックエンド未接続」 |
| 取得不能 | CircleAlert | 「状態を取得できません」 |

追加、編集、削除、閉じるの機能は今回追加しません。
check や shield を安全認証や終了確認の代わりに使いません。
色、形、可視文言を合わせ、色だけで意味を伝えません。
文字を伴う SVG は `aria-hidden="true"` と `focusable="false"` で装飾にします。
icon-only は明瞭な閉じるや開閉等に限定し、操作要素自体に accessible name を付けます。
tooltip だけで名前を代替せず、停止のような重要操作は可視 text を省きません。
開始と停止は native disabled、再確認の待機中は focus を保つ aria-disabled と controller の重複実行抑止を使います。

## 公式参照と検証の限界

[shadcn Button](https://ui.shadcn.com/docs/components/radix/button)、[Radix Collapsible](https://www.radix-ui.com/primitives/docs/components/collapsible)、[Lucide accessibility](https://lucide.dev/how-to/accessibility)、[React versions](https://react.dev/versions)、[Vite](https://vite.dev/guide/)、[typescript-eslint 対応版](https://typescript-eslint.io/users/dependency-versions/)を確認しました。
版、license、手順は [shell README](../ui/tauri/README.md)を参照します。
UI skill 本文を参照しましたが、core 三文書の resource 取得は失敗しており、既存基準と本文に沿って進めています。
AA 達成やスクリーンリーダー実測の宣言ではありません。

## 今回の限定した検証

GUI 公開境界の追加試験は移行前に診断 button が存在せず赤となり、移行後に同じ受入が緑になりました。
既存 controller の5件を TypeScript に移し、重複要求と timeout 後の遅延応答の回帰を1件追加しました。
ブラウザーは既存3件と Lucide / 展開状態、再確認中の focus 保持を確認します。
UI-02/03/09 は成功風応答・失敗・timeout の拒否、UI-04/05/06 は可視文言と SVG、診断の role/state、keyboard/focus、UI-07/08 は320 CSS px、文字200%、reduced motion と forced colors のブラウザー模擬、UI-10 は匿名の HTML 風応答を対象にします。
[UI 参照名](design-accessibility-verification.md)の全項目や既存44条件を完了にする結果ではありません。

品質ゲートの負例は、一時コピーへ型不一致、禁止 native import、未観測 Promise、孤立ファイル、未使用直接依存を個別に入れ、期待する診断と非ゼロ終了を確認しました。
元ソースへ probe は残していません。
通常と production の Knip はいずれも必須で、CI は frontend gate、GUI controller/build/browser、Rust fmt/clippy、公開 IPC 試験、Windows build を段階ごとに表示します。
controller/build/browser は同一 step 内で順に実行し、非0終了を伝播します。build と browser の別 step 化は後続の設計です。
React 移行時点では native 起動・close、実 WebView2 も未検証でした。
後続の [native smoke](tauri-native-smoke.md)では Windows hosted runner の限定した実起動・実 IPC・通常 close を確認しました。
支援技術、OS 設定、新 PC、SSH fixture、GUI と実 sidecar、配布 EXE は引き続き未検証です。
