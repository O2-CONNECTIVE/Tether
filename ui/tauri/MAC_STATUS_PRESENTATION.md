# Mac Snapshot の共通表示向け pure mapping

## 範囲と受入

固定基点 `7420cf2d219aee5f98a861fe2f8c01d17673e35a` の Mac Decoder / FeedState / Snapshot を入力とする、表示だけの候補です。`mac_status/presentation.rs::present(&Snapshot, now)` は状態を変更せず、内部値 `MacPresentation { heading, detail, icon }` を返します。`StatusContent.tsx` の heading/detail と checking/neutral/warning の出力形に対応しますが、serde 型・新しい通信 wire・Tauri command は追加しません。

Mac input を合成 `HostStatusSample` に変換しません。Windows decoder、共通 component、通常 App caller、権限・開始停止ボタンは変更しません。now は既存 Snapshot と同じ continuous clock 秒値を呼出側が渡す契約です。この段階の試験は固定値だけで、実時計は読みません。

1. sticky unknown または retained frame の cleanup=unknown を最優先し、EOF/stale/読取失敗後も「回収を確認できません」を維持。
2. frame がなければ待機・未確認。非freshの非EOF-terminalは古い報告を現在の状態にせず、processing iconを出さない。
3. fresh terminal または EOF terminal は「最終報告」として表示。confirmed/pending/not_started を区別するが、現在の接続やOS全体の回収の独立確認へ昇格しない。EOF単独・非terminal confirmedは終了証拠ではない。
4. 非terminalで残時間0なら停止・回収未確認。setup進行の表示より優先。deadlineは既存minとnull保持から算出し、mapperは変更しない。
5. freshなsetup進行だけchecking icon。本人承認待ちは説明だけで、GUI承認機能ではない。stoppingも完了扱いしない。
6. terminal後の不正入力や再開をmapperが救済せず、FeedStateの不可逆faultを表示する。

Swift比較元は固定 `050e48f90e8182e15f7c2ac6374ac45bc6b7097a` の Status.swift / StatusView.swift です。unknown優先、fresh/EOF terminal歴史、setup各見出し、staleで進行停止という仕様を参照しました。**画面全文・icon・優先順の完全同値とはしません。** 共通出力は見出しと説明だけなので、Swiftの別欄cleanupを説明へ集約し、接続・回収を「報告」と明示します。期限0はSwiftのstopping見出しよりも未確認を優先します。setup失敗の詳細手順はここでは再掲せず、最終報告とcleanup分類を表示します。

## 検証と未接続

`presentation_tests.rs` の10試験は実Decoder/FeedStateを通した閉じた入力を使い、Snapshotを直接組み立てません。3秒境界、時計異常、期限0/null/延長拒否、EOFとterminalの組合せ、unknown優先、setup進行/失敗、terminal後の再開拒否、nonterminal confirmedを含みます。

ローカルRust toolchainは未導入・利用不可のため、この追加コードのcompile/fmt/clippy/testは未実行です。実行red/greenやSwiftとの実行同値を主張しません。基点CIの成功はこの差分の成功ではありません。公開承認後は既存 `tauri-shell.yml` の fmt/clippy/test（custom-protocol）で検査でき、新workflowやcrateは不要です。

通常 App はまだMac出力を取得しません。将来はMac専用read-only応答の境界検査とプラットフォーム分岐からStatusContentへ渡す必要があります。今回追加する内部値をそのまま公開wireとして扱いません。

## 次工程の境界（未実装）

native ownerはFIFO限定のstdin所有、O_NONBLOCKの共有file-descriptionへの影響、poll/read、sleepを含むMach continuous clock、reader cancel/join、確認後のcloseを個別に扱う必要があります。Swiftの1秒join拒否時はownerと画面の鮮度更新を保持する契約を参照し、EOF・reader join・agent cleanupを別軸に保ちます。Tauri終了と更新継続への接続も別工程です。

既存lockのlibcは推移依存に過ぎず、直接依存化・Mach API公開範囲・OS compileは未確認です。この変更では依存追加、実pipe、GUI起動、installer、再接続、control/consent、新grantを実装しません。
