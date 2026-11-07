# Mac 状態出力から通常 App への読取専用経路

## 範囲

共通 Tauri の `main.tsx → App → StatusContent` が、Rust で固定した platform 経路を消費します。`get_status_presentation` は `request: { protocolVersion: 1 }` だけを受け付ける追加の read command です。platform/source/bytes/clock を含む未知 request field は拒否します。frontend の user agent、URL、storage や要求値で platform を決めません。

応答は version と source の exact-key union です。

- `windows-shell`: 既存 controller と `get_shell_status` へ委譲。元の five-field 応答、検査、開始停止不可は変更しません。
- `mac-status / source-unavailable`: Mac の通常 build はこの未接続表示だけです。native reader、source clock、agent の起動や接続はありません。
- `unsupported`: 非対応 platform の取得不能。
- `mac-status / synthetic-observation`: Rust の `cfg(test)` constructor だけが生成する閉じた fixture 出力。通常 App の browser 試験も同じ出力形を注入し、「表示テスト・実接続なし」を表示します。本番入力と混ぜません。

既存 bundled main capability に read command を追加します。別 window と remote origin は許可しません。新 permission identifier、外部 endpoint、mutable provider、環境スイッチ、path/bytes の入力口、依存は追加しません。frontend 内の mock は実 Rust IPC や origin 認証の証拠ではありません。侵害済み frontend による表示偽装全般を防ぐ設計ではなく、表示から権限を得る経路はありません。

## 検証済み入力と表示期限

`MacOutput::synthetic` は既存 Decoder / FeedState を通った Snapshot と固定試験時刻を受け、既存 pure mapper の heading/detail/icon を出力します。duplicate key、UTF-8、sequence、poison、3秒の source freshness、deadline の min/null保持、unknown、EOF の core は変更しません。Mac wire を JS で再decodeしたり、Windows/synthetic HostStatusSample に変換しません。

fixture の表示予算は source の3秒期限と retained grant deadline の早い方以下へ切り下げ、0–3000msに制限します。frontend は request 開始からの経過時間を差し引き、期限同値、非有限値、時計逆行では進行表示しません。timer、visibility change、復帰、timeout、失敗、不正応答は pending response と現在表示を無効化します。遅着応答で復帰しません。

Mac の通常未接続応答と fixture 応答は timer callback の実行順だけに依存せず、request 開始からの有限な経過時間が IPC timeout 以上なら採用しません。event loop が混雑して応答の Promise が先に解決しても、期限同値・期限超過の表示、履歴、cleanup field を取り込みません。時計異常の既存拒否と、timeout 後に同じ fixture を復活させない制約は維持します。通常の未接続応答も期限同値以降は `IPC_TIMEOUT` とし、新しい要求が期限内に返れば未接続表示へ戻れます。fixture の復活禁止は維持します。これは frontend の表示防御で、native の期限執行や実 GUI 受入の証明ではありません。

この未接続段階の observed fixture は controller の寿命につき一度だけです。再確認は同じ報告を再取得せず、期限を延長しません。これは将来の streaming 契約ではありません。既存 FeedState が stale snapshot query の後に新しい fresh frame を受理できる意味は維持します。native observed source を有効にする時は、continuous clock、reader owner と本物の新しい報告の同一性を別途設計・検証します。

cleanup unknown の警告は current 表示の期限から独立して保持します。EOF terminal の最終報告も「履歴」として別に保持し、current 状態は未確認へ落とします。履歴の confirmed は現在の接続や OS 全体の回収の独立証明ではありません。EOF 単独、画面終了、0秒、IPC 受付は停止や回収の証拠ではありません。JS timer は防御的な表示破棄だけで、OS suspend を含む native freshness の証明ではありません。

## 受入と検証境界

通常 App の Mac route 受入は、基点が `get_shell_status` を呼んだため同じ browser test が失敗し、新 caller が `get_status_presentation` を呼ぶ変更後に成功しました。pagehide 後の復帰で古い進行表示が残る不具合も、同じ通常 App 試験の失敗を確認後、破棄前の表示無効化で修正しました。その他の追加試験は後付け回帰です。

frontend controller の閉じた試験は platform と exact-key 検査、未接続、繰返し、表示期限同値、遅着、timeout、破棄、無効化、unknown と履歴を扱います。通常 browser は main/App から表示し、label、進行消去、focus、無効 button、狭幅/200%文字、forced colors/reduced motion を検証します。別の見本ページや DOM 差替えによる成功ではありません。

Rust の追加試験は byte→state→output と mock-Tauri の実 command 呼出し、request 拒否、window/origin 拒否を別に持ちます。**ローカル Rust 不在のため compile/fmt/clippy/これらの試験は未実行です。** 基点の CI、Node/browser 成功や source review を代用しません。ブラウザー注入と Rust IPC 試験ソースを結んだ live E2E の主張もしません。

狭幅200%は横overflowを防ぎますが、日本語の長い縦折返しという既存制約を残します。native Mac/Windows、screen reader、OS DPI、pipe/clock/cancel/join、実agent、Windows operator integration、installer、署名、配布は未検証・範囲外です。Swift の追加、既存 source 削除、公開、merge、依存/toolchain導入は含みません。
