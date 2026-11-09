# 未接続 UI のキー操作と focus の回帰

[Issue #25](https://github.com/O2-CONNECTIVE/Tether/issues/25) / [native 起動の証拠](tauri-native-smoke.md) / [UI 確認項目](design-accessibility-verification.md)

## 意図と境界

現在の未接続 shell で、診断を繰り返し使っても操作位置を失わず、開始や停止を誤って有効にしないことを確認します。
既存の単発 Tab/Enter、待機中 focus、拡大、native の mouse 開閉・実 IPC・通常 close は維持し、同じ試験を追加しません。
追加する境界は、キー入力から利用者が観測する focus・展開状態・再確認後の状態への遷移です。
browser fixture と Windows WebView2 の実キー入力を区別します。
製品の接続機能、Go/SSH/sidecar、tokens、ライセンス CLI、依存 lock、workflow/package scripts は変更対象外です。
UI 修正は実際の操作不具合を再現した場合だけ行います。

## 追加する受入

- 診断 trigger と再確認の間を Tab / Shift+Tab で往復し、無効な開始・停止へ focus が移らない。
- Enter / Space で診断を反復開閉し、閉じた後は trigger に focus を保ち、隠れた内容へ focus を残さない。
- native は毎回キー入力前に読み取り専用 MutationObserver を準備し、新しい aria-busy true→false を観測する。旧状態の維持だけでは合格しない。
- 完了した再確認を繰り返しても focus、無効操作、未接続と終了未確認を維持する。
- browser では取得失敗後の keyboard 再確認と回復を確認し、秘密を含む例外文を表示しない。
- keyboard による focus-visible と視認できる outline を確認する。

[APG Disclosure](https://www.w3.org/WAI/ARIA/apg/patterns/disclosure/)の Enter/Space と展開状態を参考にし、独自の Escape 操作は追加しません。
[WCAG2.2 focus order](https://www.w3.org/TR/2024/REC-WCAG22-20241212/#focus-order)と既存 UI-02/04/05/06/09 の限定した操作検証です。
[WebDriver Actions](https://www.w3.org/TR/webdriver2/#actions) は2026-07-02 Working Draftを確認した protocol の参考で、準拠認証を宣言する基準ではありません。
支援技術、発話、native OS のキー設定、DPI/高コントラスト、新 PC、他 OS の native は未検証です。

Observer は test-only の観測領域を使い、DOM の focus/click、UI 状態、Rust bridge を置換しません。
各回で切断して破棄し、開始または完了を観測できなければ失敗にします。
