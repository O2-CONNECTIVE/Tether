# Rust の所有子プロセスを検証する準備

[通信契約](communication-contract.md) / [未配線 shell](tauri-native-shell.md)

Issue [#25](https://github.com/O2-CONNECTIVE/Tether/issues/25) / [#9](https://github.com/O2-CONNECTIVE/Tether/issues/9) の GUI と子プロセス間の試験準備です。
製品から参照しない Cargo の `harness = false` integration test で、自分自身の既知の有限モードだけを子として起動します。
UI、Tauri command、capability、製品 gate、Go/SSH は変更しません。
任意実行 path、引数、shell、listener、鍵、ネットワーク、子孫プロセスは対象外です。

## 今回の試験専用契約

`fixtureVersion: 1` の boot と final を stdout の1行JSONとして使い、固定の停止要求だけを stdin に送ります。
これは製品 protocol や既存評価 fixture の protocol を採用したものではありません。
所有 `Child` の PID と boot、順序、版、構造、資源上限を確認する設計です。
停止要求の書込み、final 応答、exit code 0 と終了回収を別々に確認し、欠落や確認不能を成功にしません。
実 Go の artifact 検証、起動世代、再送、認可、期限、Windows Job、親死、子孫終了は未実装・未検証です。
PID と自己 pipe の照合は認証や同一ユーザー攻撃者の隔離ではありません。

| 対象 | 試験専用の制約 |
| --- | --- |
| 起動 | `current_exe()` と固定 enum の内部 mode のみ。環境変数は消去、stdin/stdout は所有 pipe、stderr は破棄 |
| 停止 | 固定の短い要求を1回 write/flush して stdin を閉じる。子も固定値と実 EOF を確認する |
| 応答 | UTF-8 JSON、改行込み1行128 bytes、合計192 bytes、boot→final の2件。未知/重複 field、不正型/版、PID 不一致、余剰行、不完全行を拒否 |
| 上限の検出 | 各行129 bytes まで読む。128超過は拒否し、2件後も実 pipe EOF を要求。上限による人工 EOF を成功にしない |
| 通常完了 | final の `stopped=true`、余剰出力なしの EOF、reader 結果と join、所有 Child の exit 0 と回収をすべて要求 |
| 予算 | 観測2秒、cleanup1秒の有限 poll。子は別に5秒の自己期限を持つ。OS の停止やブロッキング操作への独立した期限強制ではない |
| 失敗時 | 所有 Child handle だけに kill を試み、有限に回収/reader 完了を確認。確認不能は別の失敗で、強制 cleanup を通常成功にしない |

reader は1件の boot 通知だけを非 blocking の有限 channel に送り、stdout の一般ログを保存しません。
無期限の Child wait や、完了未確認の reader join は使いません。
子は他の子を生成せず、ネットワークを使わない既知の有限実行体です。
この制約を未知・任意の実行体に対する製品 supervisor の保証へ拡張しません。

## 最初の縦断

自己生成の無害な有限子が起動し、停止要求を受け、最終応答と正常終了を確認する1件から赤→緑を進めました。
ローカル Rust はないため、既存 Windows hosted CI の Cargo test が実行証跡です。
[赤の CI](https://github.com/O2-CONNECTIVE/Tether/actions/runs/40594741980) では既存 Rust IPC 6件成功後、新試験が `FIXTURE_LAUNCH_UNIMPLEMENTED` で失敗しました。
[最初の緑](https://github.com/O2-CONNECTIVE/Tether/actions/runs/40595733689) では同じ新試験1件と既存 IPC 6件が成功しました。
途中の run 40595409950 は rustfmt で停止し、機能の赤/緑には数えていません。

その後、実装済み拒否条件に対する回帰 fixture を加えています。
正常停止、改行なし129 bytes、final 後の余剰出力、final 正常でも exit 7、final 後も有限待機する子の5ケースを直列実行し、0件を拒否します。
負例はそれぞれ protocol 拒否、protocol 拒否、非0終了、期限切れが期待値です。
各失敗の返却前にも cleanup を確認し、回収不能なら期待した失敗へ変換せず `FIXTURE_CLEANUP_UNCONFIRMED` にします。
これらは正常系の TDD 証跡と区別した負例回帰で、最終 head の CI と実測は PR に記録します。

Windows の既存 toolchain では `ui/tauri` から次を実行します。

```text
cargo +1.99.1 test --locked --manifest-path src-tauri/Cargo.toml --features custom-protocol --test process_boundary
```

専用 harness は固定の内部 mode 以外の引数を拒否し、試験出力には固定の case 名と件数だけを使います。
製品 IPC の試験、WebView2 native smoke、新 PC、配布物、Go/SSH の受入は別です。

[Rust Child](https://doc.rust-lang.org/std/process/struct.Child.html) は drop だけでは終了や回収を行わないため、所有した Child を明示して扱います。
[Cargo の harness 設定](https://doc.rust-lang.org/cargo/reference/cargo-targets.html#the-harness-field)で test 専用の main を使い、製品 binary に fixture mode を追加しません。
