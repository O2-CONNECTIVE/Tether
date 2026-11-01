# Mac current-user agent runtime

agentexecの`ProfileMacCurrentUser`用runtimeです。契約、隔離がないこと、CleanupConfirmedの意味、信頼する構成の不変条件、未接続の境界は[Mac runtime](../../../docs/mac-agent-runtime.md)を正本とします。

- `New(Config)`はlocal構成ownerが呼びます。cwd（`WorkDir`）と`Home`を検証し、SIGCHLDが無視されていないことを読取り専用で確認します。何も起動しません。darwin以外では`ErrUnsupported`を返します。
- 1 commandごとに`/bin/sh -s`を新しいsessionとprocess groupで起動します（setsid、制御端末なし、PTYなし）。環境変数は固定の最小集合で、command bytesはstdinだけに渡します。
- spawnの直前に、継承されるdescriptor（3以上でclose-on-execでないもの）とSIGCHLDの状態を読取り専用で検査し、問題があれば`ErrHostState`で起動しません。
- 終了はkqueueで、回収せずに観測します。回収は`stopAndReap`だけが行い、自然終了の後も必ずgroupへのTERM、grace、KILLの後に回収します。回収後や所有を失った後はsignalを送りません。
- 所有するdescriptorは`ownedCloser`で閉じます。再試行できるのは、closerが「まだ所有している」と示した場合だけです。
- 共通のlifecycleは`process.go`、構成と検査は`runtime.go`、darwinのnative adapterは`native_darwin.go`（`golang.org/x/sys/unix`を使用）、他OSのstubは`native_other.go`です。

試験は、fakeの`osProcess`、fakeのhost検査、`io.Pipe`だけを使い、shell・process・signal・descriptor操作・socketを使いません。`native_darwin.go`はcompile対象にすぎず、実機では未実行です。
