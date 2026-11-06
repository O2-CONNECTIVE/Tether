# WebView2 completionと待機loopのwake契約

2026-11-06、source-only調査。基点は診断control `ea2fda12231044ad6d3907ac4db1a10cf02fb2e6`。
新しいWindows/SDK/app/CI実行、production修正、timeout変更、Mac branch編集は行っていない。
#245の3成功は既に終了した別観測であり、失敗窓のWry prefixは未観測のまま。

## 結論

**正しいUI STA上でcallbackが動くことと、外側のGetMessageがreturnしてreceiverを再検査することは別である。**
Microsoftの確認した資料は前者とmessage-pump依存を定めるが、各completionにretrievableなposted wakeが必ず付くとは定めていない。
固定crateのPostMessageという説明を、そのままSDKの保証へ昇格できない。
ただし資料中に保証が見つからないことは、実SDKがnonqueued配送を使った証明でも、元timeoutの原因確定でもない。

## exact sourceと所有・thread境界

公開crate archiveをCargo.lock checksumと照合し、展開・実行せず読み取った。

| source | archive SHA256 | 読んだfile SHA256 |
| --- | --- | --- |
|Wry0.57.1|759c50ddad0ef9913bb5496cb9d509a50433a3b78cb0238d61105526589cf0e9|webview2/mod.rs:9ef674af12e52bc3519b0e300752c1042798b7f336e0e0dafca6a8944f348888|
|webview2-com0.39.1|3f89fca7a704cee10dcb3654c1dbb8941d1783132f1917358af75bec37a7d7e6|lib.rs:de9ec5585a990725576de47557f93529cddbdf267ab3847835689e2a01c854fe|
|macros0.8.1|67a921c1b6914c367b2b823cd4cde6f96beec77d30a939c8199bb377cf9b9b54|lib.rs:3e0fb6feaaea5811a304205b8eaba424a5763947143fc27357f0106db3681ed3|

- [Wry new_in_hwnd:114–150](https://github.com/tauri-apps/wry/blob/wry-v0.57.1/src/webview2/mod.rs#L114-L150)はSTA COM初期化を要求するが、そのHRESULTを捨てる。これだけで実試行のapartmentを証明しない。Tauri runtime-wry2.12.3のContextは作成threadをmain_thread_idへ保持し、同threadのwindow要求を直接処理する。ここにRust workerへcompletionを転送する処理はない。[Context:233–285](https://github.com/tauri-apps/tauri/blob/tauri-runtime-wry-v2.12.3/crates/tauri-runtime-wry/src/lib.rs#L233-L285)
- [environment:338–377](https://github.com/tauri-apps/wry/blob/wry-v0.57.1/src/webview2/mod.rs#L338-L377)と[controller:388–435](https://github.com/tauri-apps/wry/blob/wry-v0.57.1/src/webview2/mod.rs#L388-L435)はそれぞれlocal mpsc channelを一組作り、move closureがSenderを所有する。API即時成功後、同じcallがReceiverをwait_with_pumpへmoveする。callbackはHRESULT/interfaceから作ったinner Resultをsendし、send失敗をE_UNEXPECTEDへ変換する。wake用PostMessageや別Rust threadは追加しない。controllerのWithOptions/legacy分岐は同じhandlerを使う。
- [callback.rs:20–74](https://docs.rs/webview2-com/0.39.1/src/webview2_com/callback.rs.html#20-74)はHRESULTをResultへ変換し、渡されたCOM interfaceをcloneして所有値にする。[macro:78–127](https://docs.rs/webview2-com-macros/0.8.1/src/webview2_com_macros/lib.rs.html#78-127)はFnOnce closureをUnsafeCell<Option<_>>に保持し、Invokeでtakeして引数変換後に同期呼出する。二度目のInvokeはclosureを再実行しない。このadapter自体はthread切替・message postを行わない。SDKがいつどのdispatchからInvokeするかはこのRust sourceの外側である。
- Senderはcallback closureにあり、Receiverはwaitにある。[Rust channelのbuffer契約](https://doc.rust-lang.org/std/sync/mpsc/struct.Receiver.html#method.recv)では、成功send後のSender dropはqueued値を消さない。一方、即時API error、callback未実行や早期dropの実挙動をこのsourceだけで元失敗へ割り当てない。interface AddRef/引数変換やcallback内errorを、callback未到達と同一視しない。
- [script登録:1412–1423](https://github.com/tauri-apps/wry/blob/wry-v0.57.1/src/webview2/mod.rs#L1412-L1423)もmacroのwait_for_async_operation経由で同じpumpを使う。environment/controller以後に止まる代案を保持する。

## 保証と推論の分離

| primary source | 確認できること | 確認できないこと |
| --- | --- | --- |
|[Microsoft WebView2 threading](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/threading-model)|UI STA、同threadのcallback、serial実行とpump依存。WebView handler内の同期nested loopは非対応|completionごとのPostMessage、実Runtime131のdispatch経路、実失敗のcallback祖先|
|[GetMessageA](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-getmessagea)|内部でnonqueued sent messageを処理し、取得できるmessageまで待つ|callbackを処理しただけで必ず呼出元へreturnする保証|
|[PeekMessageA](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-peekmessagea)|nonqueued処理後、queued messageがなくても待たずに戻る|これへ置換すると本件が直ること|
|[COM STA](https://learn.microsoft.com/en-us/windows/win32/com/single-threaded-apartments)|通常のGetMessage/DispatchMessage loopもSTA callを処理し、再入し得る|「Win32 pumpはCOMを一切dispatchできない」という一般化|
|[COWAIT_FLAGS](https://learn.microsoft.com/en-us/windows/win32/api/combaseapi/ne-combaseapi-cowait_flags)|DISPATCH_CALLSはASTA向けで、通常STA等では無視される|flag名からx64の本件への有効性を推測すること|

[固定pump:52–81](https://docs.rs/webview2-com/0.39.1/src/webview2_com/lib.rs.html#52-81)はreceiverを一度try_recvしてからblocking GetMessage、Translate/Dispatch、次のtry_recvへ進む。channelとWin32 queueのwakeを統合していない。
そのため、**もし**sendがGetMessage内部のnonqueued処理中に起き、その後retrievableなmessageがなければ、値はreadyでも再検査へ戻れない。
これは条件付きのsource/API推論であり、SDKの実配送を観測した結果ではない。

[Wry#1666](https://github.com/tauri-apps/wry/pull/1666)はopenで、元報告はARM64/second controller。maintainerのx64 event/completion内試験は未解決だった。
COMを全くpumpできないという説明やCoWaitへの複合変更を、本件の確定根因・修正として採用しない。
[windows-rs#4897](https://github.com/microsoft/windows-rs/issues/4897)→[#4905](https://github.com/microsoft/windows-rs/pull/4905)のcallback API追加は、dialog/accelerator/XAML固有処理をgeneric nested loopが省く問題で、本件のwake欠落の証拠ではない。

## agent-runnableなlocal model

```sh
python3 -I scripts/check_wry_pump_model.py --archive <checksum-pinned-webview2-com-0.39.1.crate> --rustc <existing-rustc>
```

上記はarchive/libのhashを照合し、関数bodyをbyte変更せず取り出し、**Win32関数を模擬したRust test**へincludeする。crateをWindows上で実呼出する試験ではない。
Mac上の既存Rust1.99.1で3 cases成功、pollをGetMessage後へ移す意図的mutationはready caseで失敗した。
元関数body SHA256は4d3cc45fbd1351d11b75950ddbfd8ea31d236209979e19dbbae3285aaa827010。

| model case | 判定 |
| --- | --- |
|開始前にchannel値がready|GetMessageを呼ばずにreturn|
|模擬posted Dispatchがsend|Dispatch後にreceiverを再検査してreturn|
|模擬nonqueued sendをGetMessage内のbarrierで保持|channel send完了後も外側は未return。模擬retrieved messageを許可するとreturn|

barrierがfake APIを保持するための決定的modelで、待機時間からdeadlockを推定していない。stdlib channel/thread以外のSDK/Windows/app/GUIは未実行。compile20s/実行10sの外側上限があり、model gateも有限。
この試験はloopの制御フローの確認であり、元session timeoutのred-to-green回帰ではない。元症状のfast/deterministic loopは依然未達である。

## 次の最小の実観測案（未実装・未実行）

一問は「実SDK completionのsend時に、同じwaitはAPI内／GetMessage内／retrieved-message Dispatch内のどこにいるか」。
既存8markerへsame-waitのbounded phaseだけを対応付け、callback中はphase enum・same-thread・InSendMessageExの固定分類だけをmemoryへ保持する案が最小。
InSendMessageEx=0はpostedの証明ではなく、Invoke直前のmarkerもFFI returnの証明ではない。unknown／別thread／nested epoch／複数呼出は結合せず不明にする。
実apartmentは未計測なので、[CoGetApartmentType](https://learn.microsoft.com/en-us/windows/win32/api/combaseapi/nf-combaseapi-cogetapartmenttype)の読取結果をSTA／main-STA／other／unknownの固定enumへ分類する案を含める。COM初期化の振る舞い自体は変えない。
最初の観測でwake、PeekMessage、CoWait、retry、timeout延長は加えない。成功sampleはそのinstanceのdispatch境界だけを示し、普遍的SDK保証や元失敗原因を示さない。
callbackが単に遅い、未到達、またはcontroller後の別初期化が遅い可能性も残る。deadlockを前提にせず、元失敗のprefixが得られるまで順位を確定しない。

将来実行するならexact pinsとoverlay差分の独立review、phase分類の純fixture、record制約、重複admission確認を先に行う。
一つのfresh standard public windows-2022、attempt1、native最大1、全preflight成功後のみ、欠測／identity／cleanup不確実で停止という上限を明示する。artifact/cache/EXE転送・stack/memory dump・追加権限なし。
これは提案であり、このsource-only結果から新runを開始していない。#245のcapは消費済み、#240の失敗とmain#239の失敗、Mac stack／実機受入の未達を維持する。
