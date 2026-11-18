# Tauri デザイントークンの状態見本

[設計・具体値・状態対応](../../../docs/tauri-design-tokens.md)

ライトテーマの提案です。色・部品の形は O2 の Derivation-Products を踏襲し（[#392](https://github.com/O2-CONNECTIVE/Tether/issues/392)）、書体は OS 標準にします（[#397](https://github.com/O2-CONNECTIVE/Tether/issues/397)）。
Tauri runtime、npm 依存、接続機能、JavaScript はありません。
実アプリへ配線する前にトークンと状態の意味を確認するための HTML/CSS です。
開始・停止を含め、実通信の操作はありません。

```bash
# リポジトリルートから静的検証・コントラスト測定
python3 ui/tauri/design/check_tokens.py

# ローカル表示。作業終了時に Ctrl+C で止める
python3 -m http.server 8765 --bind 127.0.0.1 --directory ui/tauri/design
```

ブラウザーで `http://127.0.0.1:8765` を開きます。
すべての「接続中」「終了確認済」は明示された固定の状態見本です。
接続機能を mock で有効化したものではありません。
外部 script、font、画像、外部 URL は読み込みません。
CSP で script・通信・form 送信を許可していません。

## ファイルの責務

- `tokens.css`: 基礎 `ref` → 意味 `sys` → 部品 `cmp` の正本。reduced motion / forced colors の semantic 差替えもここ。
- `components.css`: button / status card / input の最小部品。
- `preview.css`, `index.html`: 部品を確かめる専用の状態見本。製品画面ではない。
- `check_tokens.py`: alias 参照・三層依存・文字と境界のコントラストを標準ライブラリのみで測定。

## 画面で確認する項目

広幅 1200px、狭幅 360px、200% 相当の拡大で横の欠落・重なりがないこと。
Tab / Shift+Tab でリンク、読み取り専用の入力、診断の開閉へ移動でき、focus を見失わないこと。
開始・停止は無効であり、無効の理由が近くに残ること。
Enter でリンクと診断開閉を操作でき、エラーが対象入力へ `aria-describedby` で対応すること。
reduced motion では transition が 0 になり、forced colors でも境界・文言・記号が残ること。
これらはブラウザー上の表示検証です。
macOS / Linux / Windows の各 native WebView、実 OS の高コントラスト設定、実 DPI、支援技術の実機確認は別工程です。

ダークテーマは未実装です。
`color-scheme: light` を固定し、OS が dark の場合もライト案を表示します。
静的検証やブラウザー表示の成功を、Tauri build・配布 EXE・停止実動作・受入44条件の達成へ読み替えません。
