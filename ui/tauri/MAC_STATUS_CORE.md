# Mac status入力のpure Rust候補

Tauri共通画面へ向けた第2段の **source候補** です。共通presentation outputとMac status入力を混同しません。`src-tauri/src/mac_status` は固定公開commit `050e48f90e8182e15f7c2ac6374ac45bc6b7097a` のSwift `ui/mac-status/Sources/StatusCore/Status.swift` のDecoder / FeedState / Snapshotを参照し、PipeReaderのread chunk処理だけをメモリmodelにしています。Swift source・既存GUIは変更しません。

この文書が扱うDecoder、FeedState、Snapshot、Framingはpureな入力検証層です。
fd所有、native clock、read threadとjoinの内部前提は、後続の[Mac状態入力の読取前提](MAC_STATUS_INPUT.md)へ分けています。
通常起動とTauri commandへのnative入力は未配線で、既存shellの未接続と開始停止不可を維持します。

## 固定入力契約

- 1〜2048 bytes、flat objectの12keysのみ。`version`、`source_continuous_ms`、`seq`、`phase`、`reason`、`grant_remaining_ms`、`starting_workers`、`running_workers`、`stopping_workers`、`cleanup`、`terminal_cleanup_unknown`、`terminal`。
- byte parserがduplicateを上書き**前**に拒否します。JSON.parse / serde Valueによる先行正規化は使いません。stringはunescaped ASCIIのみ。Unicode escape・非ASCII UTF-8・不正UTF-8・制御文字・nested値・余分な末尾を拒否。JSON whitespaceはTAB/LF/CR/spaceだけです。
- numberはleading zeroのないunsigned整数。version=1、seqは1〜2^53−1、source timestampは0〜2^53−1。remainingはnullまたは0〜86,400,000ms、worker3種は各0/1かつ合計1以下です。
- phase/reason/cleanupの語彙、terminal iff closed/failed、terminal unknownの片方向含意、setup failureとconfirmed terminalの0worker/0remaining制約はSwiftそのままです。
- **現行互換性の注意:** cleanup=unknownだけではterminal unknownを強制しません。nonterminal cleanup=confirmedもSwiftは許容します。共通presentationの合成入力validatorとは別契約です。この段階で暗黙の厳格化・型cast・表示への転用はしません。

## 時間・世代・履歴

wireにgeneration fieldはありません。新FeedState instanceが新しいfeedの所有境界で、reset/reconnect APIはありません。seqは増加必須（gap可）、terminal後のframeとreplayはsequence fault。decode/clock/stale等のfail後はingestを不可逆に拒否します。

呼出元は同じcontinuous clock秒値を渡す契約です。
このpure層の試験はfake時刻を渡し、sleepを含むnative clockは別の入力前提層で扱います。
ingest時の非有限、rollback、未来source、source rollbackを拒否し、source age **3秒以下はfresh（3秒ちょうどを含む）**とします。
snapshot queryはreceived/sourceより前ならfreshにしません。
queryのstale表示だけではpoisonせず、同じfeedの新鮮な次frameは受け取れる点もSwiftと同じです。

残時間deadlineはsource+remaining_msで作り、既存deadlineとのminだけを採用。nullでも既存deadlineを消さず、期限を延長しません。残秒はceil、期限超過は0、非fresh時は値なしです。

sticky terminal-cleanup-unknown、retained terminal、source EOF、freshnessは別軸です。EOFをcleanup確認やagent終了と解釈しません。Swiftの明示 `fail` はpoisonを維持しながらfault labelを後続failで置換できるため、それも一致させます。実read loopは最初の終端で戻るので、pure framerは終了後の入力/EOFを無視します。

## メモリframerの範囲

実PipeReaderの1readは最大4096 bytes、残留partialは最大2048 bytes。候補は固定6144-byte bufferでappend→LF切出し→1行ごとのclock/rate判定→decode→残留上限確認の順をmodel化します。1秒windowは64framesまで、ちょうど1秒でreset。CRLFのCRはdecoder whitespace、空行・oversize行・不正UTF-8はmalformed、partialを残したEOFはmalformedです。

`push_read` はpositiveかつ4096 bytes以下のread結果専用です。空/過大chunkはcaller contract違反としてRead fault、EOFは別操作。これは新wire ruleではありません。OS poll/EINTR/EAGAIN、fd flags・close所有、実読取rate、reader join・実メモリ使用量はこのmodelから証明しません。

## 閉じたgoldenと初段候補の検証記録

以下の実行結果と未実行項目は、このpure層を準備した当時の記録です。
後続の入力前提層の検証は、その固定候補headの記録で別に判定します。

`src-tauri/tests/fixtures/mac_status_golden.tsv` は156行の固定入力・期待値です。期待値はRustから生成せず、契約から独立に記述しました。hex入力はduplicate・不正UTF-8も正規化せず保持します。`mac_status_reference.swift` は変更していないSwift Decoder/FeedStateを同じfixtureに通すpure実行harnessです。Rust `golden_tests.rs` は同じ期待値を比較するsourceで、自己生成した出力を正解にはしません。

```sh
# 比較元は固定公開commit。PR #178基点の同tree内Swiftは旧版なので使わない。
# リポジトリroot、導入済みSwift compilerのみ使用。専用一時directoryへ抽出。
reference_dir=$(mktemp -d)
git show 050e48f90e8182e15f7c2ac6374ac45bc6b7097a:ui/mac-status/Sources/StatusCore/Status.swift > "$reference_dir/Status.swift"
swiftc "$reference_dir/Status.swift" \
  ui/tauri/src-tauri/tests/mac_status_reference.swift -o "$reference_dir/mac-status-reference"
"$reference_dir/mac-status-reference" ui/tauri/src-tauri/tests/fixtures/mac_status_golden.tsv

# 既存toolchainを備えた環境での未実行確認セット
cargo +1.99.1 fmt --manifest-path ui/tauri/src-tauri/Cargo.toml --check
cargo +1.99.1 clippy --locked --manifest-path ui/tauri/src-tauri/Cargo.toml --all-targets -- -D warnings
cargo +1.99.1 test --locked --manifest-path ui/tauri/src-tauri/Cargo.toml mac_status
```

Rust1.99.1は既存`rust-toolchain.toml`とCIのpinであり、新たに導入した版ではありません。Cargo.toml / Cargo.lockは変更せず、新crateも追加しません。今回Swift golden156行は成功、Rust compiler/cargoが利用不可のため**Rust compile・fmt・clippy・fixture実行・Swift/Rust実行同値は未完**です。source reviewやSwift成功をRust成功へ昇格しません。Swift goldenは実pipe/clock/GUI/権限/OS回収の試験ではありません。公開承認後の既存hosted Rust jobs等で上記を確認するまで、製品接続可とは扱いません。

既存Swift CIはこの新golden harnessを呼びません。固定050e比較元でのローカルSwift156行成功、今後のRust golden CI、既存Swift viewer CIは別の検証です。同treeのSwift/Rust同値や新goldenの両言語CI接続済みとは主張しません。

[Mac Snapshot presentation mapping](MAC_STATUS_PRESENTATION.md)
