# Windows session-create timeout の一次資料調査

2026-11-06調査。対象はPR #240のhead `4ba768e910af595f26176eb69aaf2e21d663b726`、tree `a4bb126e61fad53330d78207b54d2a57e31e3795`。
これはローカルの資料調査であり、根因の確定、修正の実証、Windows/native起動の実施を含まない。
起動経路を変える前に、同じ失敗試行のどの待機点にいたかを区別する必要がある。

## 固定した対象と証拠の限界

| 対象 | 照合した版・出典 |
| --- | --- |
| アプリ | [Cargo.lock](../ui/tauri/src-tauri/Cargo.lock): Tauri 2.12.5、tauri-runtime-wry 2.12.3、Tao 0.37.2、Wry 0.57.1、webview2-com / sys 0.39.1、macros 0.8.1。appのhyper-utilは0.1.21 |
| native automation | [tools.json](../ui/tauri/native-smoke/tools.json): WebView2 Runtime / EdgeDriver 131.0.2903.86。tauri-driver 2.1.0は[既存の固定crate/hash契約](tauri-driver-stage-trace.md)に従う |
| driverの推移依存 | SHA256 `2f2e29bd6900fb718dfdc06f2e7a371d6087c085daaf83709847ef3e6b0c1953` の既存公式crateをread-onlyで展開せず読み、同梱Cargo.lock SHA256 `94bf7caee3f76ca1d558e2636a846f12738db64264abe6dd7fa930548d3f03e1` とhyper-util0.1.19を照合。app lockの0.1.21とは別 |

以下は親作業から引き継いだ観測で、原ログをこの文書へ複製・再検証していない。
通常debug/releaseとstartup-diagnosticsの複数試行で、元の15000ms session要求が失敗した。
driver traceは `forward-start` → `request-dropped`、`native-headers` / `native-error` は記録窓内にない。
PR #238のHost traceは `setup-enter=120537us` → `webview-build-start=120658us` のみ、Node失敗後にsealedした完備窓だった。
PR #240のstartup releaseも約15013ms、#239後mainの通常nativeも約15019msで同じ要求境界に失敗した。
Host1個とWebView子2個を相関観測したが、それだけではenvironment/controllerの成功を示さない。

この証拠が示すのは、窓を閉じるまでにHostのbuild結果記録が得られなかったことまでである。
`sealElapsedUs` がなく、Node/driver/Hostの時計の起点も異なるため「15秒全てをbuilder内で消費」とは計算できない。
`request-dropped` はservice futureのdropで、native socket切断やその原因を示さない。
通常controlや診断probeの成功は、失敗の根因の除外にならない。
元失敗EXEは未保管なので、新buildの観測で過去EXEのstackを復元したとは扱わない。
分類の厳密な意味は[Host stage契約](tauri-app-startup-stages.md)と[driver stage契約](tauri-driver-stage-trace.md)を正本とする。

## 固定版の呼出経路

以下の行番号は取得済みpublished crateのsourceに対する目安であり、Web表示の行番号ではない。
source名と関数名を識別子にする。

| 境界 | 固定sourceと正確なcall site | 最小の区別観測 |
| --- | --- | --- |
| Host → Tauri | [Host `run/setup`](../ui/tauri/src-tauri/src/lib.rs): `startup.record(BuildStart)` → `builder.build()` → `BuildOk/BuildError` | build-entryとreturnだけでは下記のどこかは分からない |
| Tauri → window/WebView | [runtime-wry 2.12.3 `lib.rs`](https://raw.githubusercontent.com/tauri-apps/tauri/tauri-runtime-wry-v2.12.3/crates/tauri-runtime-wry/src/lib.rs): `Context::create_window`（300付近）、`send_user_message`、`Message::CreateWindow`、`create_window`、`create_webview` → `webview_builder.build(&window)`（5190付近） | 同じmain threadならmessageを直接処理し、別threadならproxyへ送る。`rx.recv()`だけを見てUI callback停滞とはしない。UI thread側の祖先frameを要する |
| HWND準備 → environment | [Wry 0.57.1 `webview2/mod.rs`](https://raw.githubusercontent.com/tauri-apps/wry/wry-v0.57.1/src/webview2/mod.rs): `InnerWebView::new_in_hwnd`（105）→ `CoInitializeEx(COINIT_APARTMENTTHREADED)` → `create_container_hwnd` → `create_environment`（297）。既存environment指定なら作成を省略 | `create_environment`入場、COM API即時return、completion入場、wait終了を区別。APIの呼出中とcallback待ちを混同しない |
| environment → controller | 同Wry `create_controller`（381）→ Environment10のcastが成功すると `CreateCoreWebView2ControllerWithOptions`、失敗時は `CreateCoreWebView2Controller` → `wait_with_pump(rx)`（435） | APIの分岐、即時HRESULT、completionの成否、wait終了。子プロセス数から分岐を推定しない |
| controller → 初期化 | 同Wry `init_webview`（440）→ settings / handlers / protocol → initialization_scripts loop（518付近）→ `add_script_to_execute_on_document_created`（1412）→ `AddScriptToExecuteOnDocumentCreatedCompletedHandler::wait_for_async_operation` → navigation、visibility/focus、return | controller成功後もbuildが返らない余地がある。script登録待ちの祖先frameと、navigation発行/returnを分ける |

この表はsource上の順序であり、今回の失敗がその最後まで進んだという実測ではない。
Wryはbuildで `Navigate` を発行するが、全page-load完了を一律に同期awaitする設計ではない。
従って `first-page-load-finished` 欠落だけで初期HTML/JS不良へ原因を絞れない。

### 同じpumpに少なくとも3種類の起動待ちが入る

[webview2-com 0.39.1 `wait_with_pump`](https://docs.rs/webview2-com/0.39.1/src/webview2_com/lib.rs.html#52-81)は、channelの `try_recv` 後、blockingの `GetMessageA`、`TranslateMessage`、`DispatchMessageA` を反復する。
ここに独立の15000ms timerはない。
`GetMessageA` が0ならcancel、-1ならerrorを返す。
別threadから送信する場合はmessageでwakeさせる必要があるとsourceは注意している。
ただしWebView2 completionはUI thread上という契約があり、今回のsenderが別threadだった証拠はない。
従ってこのコメントだけで「lost wakeupが根因」とはしない。

[macros 0.8.1 `impl_completed_callback`](https://docs.rs/webview2-com-macros/0.8.1/src/webview2_com_macros/lib.rs.html#93-110)の `wait_for_async_operation` も、callbackからchannelへ結果を送り、同じ `crate::wait_with_pump(rx)` を呼ぶ。
environment、controller、script登録のいずれでも `GetMessageA` が現れ得る。
最上位API名だけでなく、その上の `create_environment` / `create_controller` / script登録関数を含むstackか、それと同等の境界記録が必要である。
1枚の待機stackは、その瞬間の待機を示すだけでdeadlockの証明ではない。

### same-STAのcallback成功後もpumpが返らない、という識別仮説

[GetMessageAの公式契約](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-getmessagea)は、posted messageを取得するまでsent messageを内部dispatchし、pending nonqueued messageも処理すると明記している。
これと固定pumpを合わせると、**もし**completionが `GetMessageA` 内部のnonqueued dispatch中に同じSTAへ届き、channel送信後にposted messageが来なければ、callbackが成功しても次の `try_recv` に戻らない、という経路を予測できる。
これはAPI契約からの推論であり、WebView2 Runtime131のcallback配送が今回その経路だったという証拠はない。
cross-thread senderだけをlost-wakeup候補とするのも過剰な限定になる。

最初の診断で必要なのは、同一wait invocationについてAPI即時return、callback入場、`tx.send`成功、callback exit、outer wait-returnを固定memoryへ記録すること。
成功send/exit後もwait-returnが欠落したら、次段階で `try_recv` のempty/receivedと `GetMessageA` enter/returnを相関させる価値がある。
送信成功前に止まる、またはGetMessageから戻って結果受領後に止まるなら、この取り残し経路は棄却する。
起点の異なる時計は使わず、overflow・record競合・seal前後の欠測を別に扱う。

[WM_NULL](https://learn.microsoft.com/en-us/windows/win32/winmsg/wm-null)はno-opのmessageである。
固定pumpは `GetMessageA(NULL,0,0)` なので、正しいqueueへ**posted**されたWM_NULLを取得すればreturnしてchannel再確認へ進む、というAPI上の根拠はある。
ただし `SendMessage` / `SendMessageTimeout(WM_NULL)` の応答確認は、sent-messageの内部dispatchだけでも成立し、posted-message取得によるreturnを保証しない。
[PostThreadMessage](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-postthreadmessagea)は正しいthread ID・既存queue・desktop/integrity条件を必要とし、modal loopではthread messageが失われ得る。
後からwakeを加えて成功しただけでは、callback不着、nested reentrancy、runtime側の進行を区別できない。
従って最初のprobeではwake/CoWait/PeekMessage置換を足さず、上記の観測を先に得る。これは実装許可や修正の採用を意味しない。

## 公式の制約・既知事例との照合

| 一次資料 | 版・文脈と今回への適合 | 反証できる観測・採用しない助言 |
| --- | --- | --- |
| [Tauri 2.12.5 WebviewWindowBuilder source](https://docs.rs/tauri/2.12.5/src/tauri/webview/webview_window.rs.html#53-100)、[Wry #583](https://github.com/tauri-apps/wry/issues/583) | 同期command / WebView event内の作成はdeadlockの既知条件。一方、同じTauri docsがsetup内のbuildを公式例示。Hostはsetupから初回作成している | stackに既存WebViewのevent/completion handlerがbuildの祖先として存在するか。未観測なら「async commandへ変更すれば直る」を採用しない |
| [Microsoft threading model](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/threading-model) | WebView2はUI STAとmessage pumpを要求。callbackは同threadで直列実行され、WebView callback内のnested loopは非対応。pumpを止める待ちはcompletionを止める | failing UI stackのcallback祖先、COM apartment状態、実際のwait APIを確認。初回setupをWebView callbackと同一視しない。Wryはpumpを持つので単純な `Task.Result` の例とも異なる |
| [Wry #1665](https://github.com/tauri-apps/wry/issues/1665)、[PR #1666](https://github.com/tauri-apps/wry/pull/1666) | Wry0.54.1、Snapdragon X Elite、ARM64、second controllerの投稿者報告。PRは調査時Open/requested changes。maintainerはx64 event/completion内deadlockを直さなかったと報告 | architecture、何個目のcontroller、callback祖先を照合。投稿のMsgWait/PeekMessage説明は固定0.39.1の実装表現とも異なる。未採用CoWait patchを今回の確定修正として適用しない |
| [Tauri #14915](https://github.com/tauri-apps/tauri/pull/14915)、[merged #15508](https://github.com/tauri-apps/tauri/pull/15508) | HWND登録前のreturn raceの提案は後者でclose済み。固定runtime-wry2.12.3のContext経路には既に応答channelと `rx.recv()` がある | `CreateWindow`処理前・後、window登録とreturnの順を測る。古い「応答channelを足す」提案を重ねない |
| [Tauri #8610](https://github.com/tauri-apps/tauri/issues/8610)、[tauri-driver2.1.0 main](https://raw.githubusercontent.com/tauri-apps/tauri/tauri-driver-v2.1.0/crates/tauri-driver/src/main.rs) | 旧Tauri1.5.9 / Runtime120で、driver cleanup後UDFが残り次回buildが停止する報告。固定driverにはkill-on-job-close付きJob Objectが既にある | 同試行前の所有process/UDF再利用と、正常close前後を区別。kill-on-job-closeの存在だけで全子終了の実測を代替しない。無関係process停止・全cache削除は提案しない |
| [WebView2 globals: SDK1.0.2903.40 view](https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/webview2-idl?view=webview2-1.0.2903.40#createcorewebview2environmentwithoptions)、[#5229](https://github.com/MicrosoftEdge/WebView2Feedback/issues/5229) | 同じUDFの共有browserと異なるenvironment optionsはERROR_INVALID_STATE。env/registryがruntime/UDF/追加argsを上書きする。#5229はRuntime135 / WPFでdispose直後options変更時の明示0x8007139Fで、今回の無応答と同一ではない | 有効runtime・UDF・argsの同一性を許可済み匿名分類で確認し、environment/controllerのHRESULTを分離。明示errorが届くなら本来WryはErrを返すため、無期限wait説を見直す。registry/securityを変更しない |
| [Microsoft WebDriver guide](https://learn.microsoft.com/en-us/microsoft-edge/webview2/how-to/webdriver)、[EdgeWebDriver #113](https://github.com/MicrosoftEdge/EdgeWebDriver/issues/113) | 公式launch方式は最初のavailable WebViewへattachする。runtime/driverの版対応が必要。#113は116のnavigate障害で117更新後解消の報告、今回131のsession作成前と異なる | 固定131.0.2903.86一致を保持し、Host build完了とnative session応答を別に記録。既に一致する本件へlatest更新・navigation timeout延長を処方しない |
| [Microsoft #3132](https://github.com/MicrosoftEdge/WebView2Feedback/issues/3132)、[#5027](https://github.com/MicrosoftEdge/WebView2Feedback/issues/5027)、[#5796](https://github.com/MicrosoftEdge/WebView2Feedback/issues/5796) | 順にRuntime109のenvironment callback欠落と失敗upgradeの自己報告、ARM/131.0.2903.112の明示FILE_NOT_FOUND、156.0.4314.58の初回controller約5–8秒停滞の当日投稿。131.0.2903.86の今回と同一原因を裏付けない | 環境作成かcontrollerか、即時/非同期HRESULTか、stackのruntime内待ち場所かを照合。runtime再導入、downgrade、component updater無効化はこの調査から導かない |

tauri-driver2.1.0の[server](https://raw.githubusercontent.com/tauri-apps/tauri/tauri-driver-v2.1.0/crates/tauri-driver/src/server.rs)は `tauri:options` をWindowsの `browserName=webview2` と `ms:edgeOptions` へ変換し、native HTTP requestをawaitする。
`forward-start` 後の無応答は、このawaitの先がどこで待つかをまだ区別しない。
また[webdriver.rs](https://raw.githubusercontent.com/tauri-apps/tauri/tauri-driver-v2.1.0/crates/tauri-driver/src/webdriver.rs)はnative driver子へautomation環境変数を設定するが、[Wry0.57.1 WebContext](https://raw.githubusercontent.com/tauri-apps/wry/wry-v0.57.1/src/web_context.rs)の `set_allows_automation` はWindowsではno-opである。
そのflagが存在することをWindowsのDevTools attach成功の証明にしない。

## 次の一観測で区別する仮説

順位は診断で先に区別する順であり、根因の事後確率を数値化したものではない。
いずれも成立未証明で、同じ固定variant・単一POST・元15000msのまま、失敗前かcleanup前の同じ所有試行へ対応付ける必要がある。

| 順位・仮説 | 予測 | これが得られれば棄却・更新 | 必要な最小追加観測 |
| --- | --- | --- | --- |
| 1. controller作成のAPI/callback/pump区間から返らない | UI stackに `create_controller` → `wait_with_pump` → GetMessage/dispatch、またはcontroller API内部の待ちがある。environment成功後、completion未到達、またはcompletion成功後waitだけ未return | 同じ試行でcontroller成功と `init_webview` 入場を確認したらこの待機点は棄却。callback祖先がなければ#583型原因は別途棄却 | nearest Wry ancestorを含むUI stack、またはcontrollerのAPI即時return / completion / send / callback exit / wait-return。runtime内native待ちとpump待ちを区別 |
| 2. 初期化script登録completionで止まる | environment/controller成功後、`init_webview` → script登録 → generated `wait_for_async_operation` → pump。navigation未発行の場合もある | script登録全件return、または停止点がenvironment/controller以前なら棄却 | stackのscript登録祖先、またはscript登録の入場/returnを内容なしで観測。script本文・URL・payload不要 |
| 3. environment作成completionが返らない | `create_environment`内API呼出中か、その後のpumpで待つ。controller入場なし | environment成功とcontroller入場が取れれば棄却。明示HRESULTを受けたならcallback不着説を変更 | environmentの即時return / completion / wait-returnと、同じ試行のruntime選択・UDF共有有無の匿名分類 |

上の3候補の外も残る。
Tauri/Tao側window作成、context mutex、COM同期call、focus/subclass、driverのDevTools接続待ち、観測の欠落をsourceだけで除外しない。
Host build-returnが同じ失敗窓で確認されるなら、優先先をEdgeDriverのlaunch/attach側へ移す。
その場合でもHost準備とnative response欠落の因果関係は別に証明する。

## 既存Windows stack観測手段の実現性

これは手段の比較だけで、導入・native実行・debug attach・dump取得の許可や実施ではない。
installed inventory、同一architecture、対象のEXE/PDB対応、所有PID/creation identity、採取の既存承認を先に照合する。
元失敗EXE/PDBがない場合、新variantのstackとして区別する。

- [CaptureStackBackTrace](https://learn.microsoft.com/en-us/windows/win32/debug/capturestackbacktrace)は呼出threadのcurrent stackを取るAPI。watchdog自身から呼んでもUI threadのstackにはならない。
- [GetThreadContext](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-getthreadcontext)は動作中threadから有効contextを取得できず、context構造・alignmentはarchitecture依存。単なるPROCESS_QUERY権限やthread列挙の成功をstack取得成功へ読み替えない。
- [SuspendThread](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-suspendthread)はTHREAD_SUSPEND_RESUMEを要求し、返却値は以前のsuspend count。mutex所有threadを止めると呼出側もdeadlockし得る。無制限のResume、自己所有外thread、失敗時も安全に戻るという保証は置かない。
- [StackWalk64](https://learn.microsoft.com/en-us/windows/win32/api/dbghelp/nf-dbghelp-stackwalk64)はcontextと対象memoryを用いたunwindが必要。frame上限だけではOS call・symbol処理の実時間上限を保証できない。符号解決不能のGetMessage1frameを成功証拠としない。
- [PssCaptureSnapshot](https://learn.microsoft.com/en-us/windows/win32/api/processsnapshot/nf-processsnapshot-psscapturesnapshot) / [capture flags](https://learn.microsoft.com/en-us/windows/win32/api/processsnapshot/ne-processsnapshot-pss_capture_flags)はWindows8.1/Server2012R2以降のOS API。thread context採取とstack memory採取は別で、VA cloneはprivate memory等も含む。無設定の軽量stack samplerや無影響機構ではない。
- [DebugActiveProcess](https://learn.microsoft.com/en-us/windows/win32/api/debugapi/nf-debugapi-debugactiveprocess)は全threadを一時停止し、debug event処理が必要。debugger終了は既定で対象終了を伴うため、「既存OS APIだけなのでread-only無影響」とはしない。
- [MicrosoftのWebView2 ETW手順](https://github.com/MicrosoftEdge/WebView2Feedback/blob/main/diagnostics/etw.md)はCPU sampling付きprofileを推奨し、elevated WPR開始/停止とprofile取得を要求する。WPR/WinDbg/ProcDumpの存在や承認を仮定しない。ETLは機微情報を含み得る。既存workflowの匿名artifactへ生ETL/dumpを追加しない。CPU samplingだけではsleep中のUI wait全体を十分に分類できない可能性も残る。

最小の成果物は、所有試行・採取時点・UI thread対応・最寄りWry呼出元・採取成功/不完全の分類である。
native stackを得られない場合は「採取未達」とし、タイムアウト根因の確定へ進めない。

## 調査・検証の範囲

AGENTS.md、agent-workflow、security、既存Host/driver stage契約、固定Cargo.lockとローカル取得済みcrate sourceをread-onlyで照合し、公式source/docsおよび所有projectのissue/PRを検索した。
このファイルだけを追加した。試験実行、CI dispatch、push、PR/Issue編集、install、GUI/native起動、runtime/registry/security変更は行っていない。
一次資料検索で今回の固定版と初回setupの再現条件が一致し、根因と修正が検証済みの事例は得られなかった。
失敗中のnative call siteの観測が未達のため、上の仮説はいずれも未確定である。
