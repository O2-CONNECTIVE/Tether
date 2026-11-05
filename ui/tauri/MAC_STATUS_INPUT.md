# Mac状態入力の読取前提

この候補は、専用の読取descriptorを所有する内部入力層を追加します。
通常のMac起動とIPC応答は`source-unavailable`のままで、入力を登録する経路はありません。
既存の[状態出力契約](MAC_STATUS_APP.md)とWindowsの5フィールド応答を維持します。
後続の[子processとreaderの親所有](MAC_STATUS_OWNER.md)も内部前提に留め、通常起動へのsource登録は行いません。

| 内部実装 | 責務 |
| --- | --- |
| `continuous_clock.rs` | 同一bootの`sleep`を含む時刻を`mach_continuous_time`から取得し、既存producerと同じ整数演算でmillisecondへ変換する。無効なtimebaseとwireの安全整数範囲外は拒否する。 |
| `native_input.rs` | 呼出側から所有移譲された読取専用FIFOだけを受け、close-on-execとnonblockingを設定する。path、stdin、環境変数から入力を探索しない。 |
| `provider.rs` | 1回4096 byteまでの読取を既存framingへ渡す。時刻異常、不正入力、EOF、読取失敗、停止で同じ入力を閉じ、再openしない。 |
| `reader_owner.rs` | 読取workerの取消と実joinを分ける。未完了時はhandleを保持し、遅延入力で停止状態を取り消さない。Drop時は取消と同期joinを行う。 |

FIFOの種別とdescriptorの所有は、producerの真正性を証明しません。
同じopen-file descriptionを共有しているとnonblocking設定が共有元にも影響するため、将来のcallerは専用descriptorの移譲を保証する必要があります。
`Input`とclockの内部契約はnonblockingを前提とし、任意のblocking実装を中断する機構はありません。
workerのDropは待つ可能性があるため、将来のGUI callerはUI close経路で破棄せず、別の所有者が取消後のjoinまで保持する必要があります。

保持したsnapshotを現在の観測として使うには、新たに取得したcontinuous clockでfreshnessを判定する必要があります。
EOF、readerのjoin、表示期限切れはhostや通信のcleanup確認を意味しません。
この候補はproducer起動、認証、policyや鍵の読込、resident admission、ON/OFF、duplex、Quit、packagingを追加しません。

clockの整数演算、有限memory入力、取消と遅延入力の競合をGUIなしの試験で扱います。
native入力の5試験は自己所有の匿名pipeだけを使う閉じたfixtureで、製品producerやGUIの受入とは別です。
実行した範囲と未実行の範囲は固定候補headの検証記録で判定し、古いSwiftUIやbackendの実績をこの候補の実GUI検証へ持ち越しません。

## GUIなしの検証入口

frontend build後、既存のRust toolchainで両構成のlibrary試験を実行できます。
5件のnative入力試験は自己所有の匿名pipe、固定memory frame、システム時刻と`/dev/null`の種別検査だけを使い、host、listener、credential、利用者の実データに接続しません。

```sh
cargo +1.99.1 test --locked --offline --manifest-path src-tauri/Cargo.toml --lib
cargo +1.99.1 test --locked --offline --manifest-path src-tauri/Cargo.toml --features custom-protocol --lib
```

mock IPCのcontextは`generate_context!(test = true)`で生成します。
固定依存のこの指定はruntime専用plistの埋込みを除き、生成したconfigとcapabilityの検査を保持します。
通常起動のcontext生成は変更していません。
`tauri-shell.yml`のMac jobは、依存をlockedで取得した後、両構成の全library試験とall-target clippyおよびbuildをofflineで行います。
製品GUIを起動する試験はこのjobへ含めません。
