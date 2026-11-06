# PR220 profile証拠契約のsource-only分析

対象は診断evidence layerだけ。製品・native/supervisor・validator・acceptance・15000ms・request/response/error/AbortSignal・profileMatch・ownership/cleanupは変更しない。PR220/control e9498fdf4dee714e472ea11118eb477853b3efe5をbaseに、解析文書とfixtureだけを加える。公開・PR編集・CI/native/app/driver実行・installは行わない。

## 保存された事実と三つの契約

PR220 run42151818289/job127676593543/attempt1はnative1/1、session5334ms、normalclose/ownedcleanup成功、元timeout不再現。15-file canonical28316bytes/SHA256 `544150f45a78f72559a02d9ccec6b5b8014fdfcf38a64d22bdf9c80cca81797d` をbyte-for-byte意味保存したfixtureとして再生する。元WebView6件profileMatchfalse、独立6childEBWebViewrows/queryCompletefalse/setcoveragefalseで、set対応は未証明。schema2 capabilityはnestedstring/drive-prefix、observer6failureはcountguardとfalseyCommandLineguard。raw値・pollphase/order・falsey subclassは不明のまま。

### 1. input UDFとreturned capability

[Microsoft EdgeOptions固定source L85–104](https://github.com/MicrosoftDocs/edge-developer/blob/8f4be4dcd43226dff0ee1131be8b504a29bf8ef7/microsoft-edge/webdriver/capabilities-edge-options.md#L85-L104) はoptionalなWebView2 input `webviewOptions.userDataFolder` と、返却string `msedge.userDataDir` を別に説明する。input未指定時のtemporaryfolderは記載される。returned表は対応するEdge-specific値を列挙するが、WebView2でも必ず非空/drive絶対表記/rootと同じ値になる保証や正確なJSON nestingは記載しない。returned値を単純にoptionalと断言する根拠も無い。dotted表記はnested JSON不正の証拠ではない。

[実request webdriver.ts:47–52](../../ui/tauri/native-smoke/webdriver.ts) はuserDataFolderを送り、sessionId/browserVersionを検証する。固定[tauri-driver2.1.0 server.rs L49–67](https://github.com/tauri-apps/tauri/blob/447fa9f3f993fe77724189e355078b38ce20baea/crates/tauri-driver/src/server.rs#L49-L67) はinputをms:edgeOptionsへ移す。返却bodyのprofileを独自保証する契約ではない。公式EdgeDriver131/WebView2内部実装の値生成箇所は確認できていない。

現`path-validation.mjs`のdrive-prefixは「ASCII drive letter、colon、separator、少なくとも後続一文字」という診断側のlexical条件。後段もforbidden文字/colon/componentを拒否するが、OS存在・canonical identity・ownership・UDF同一性を証明しない。empty/relative/UNCなど異なるsyntheticstringは同じ第一拒否になり得る。これは保存runのstring復元ではない。

[WebView2 COM get_UserDataFolder](https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/icorewebview2environment7?view=webview2-1.0.4022.49#get_userdatafolder) はactualUDFのabsolutepathを返す保証を持つが、別APIである。EdgeDriver131の返却capabilityがこのAPI由来とする証拠はない。COM側保証をcapabilityへ移さない。

### 2. UDF root、runtime child、owned集合

[MicrosoftDocs固定source L408–414](https://github.com/MicrosoftDocs/edge-developer/blob/8f4be4dcd43226dff0ee1131be8b504a29bf8ef7/microsoft-edge/web-platform/devtools-mcp-server.md#L408-L414) はruntimeがEBWebView suffixを付加するlayoutを説明する。[UDF文書](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/user-data-folder#specifying-a-custom-udf-location) もbrowserdataが指定UDFのsubfolderに保存されると説明する。layoutはrootとchildの概念を分ける根拠であり、任意childの許可やrunの各ownedslotの対応証拠ではない。

固定wry0.57.1は[context.data_directoryを取得 L295–305](https://github.com/tauri-apps/wry/blob/2446feadae623d6f2ac8d2136e3ccef3d7197e0e/src/webview2/mod.rs#L295-L305)、[CreateCoreWebView2EnvironmentWithOptionsへ渡す L357–360](https://github.com/tauri-apps/wry/blob/2446feadae623d6f2ac8d2136e3ccef3d7197e0e/src/webview2/mod.rs#L357-L360)。固定tauri-runtime-wry2.12.3は[data_directoryをWebContext keyとして渡す L4714–4736](https://github.com/tauri-apps/tauri/blob/993038c49185307dfc895ab7657d77ca0d5ed7c7/crates/tauri-runtime-wry/src/lib.rs#L4714-L4736)。固定Tauri2.12.5の[data_directory builder](https://github.com/tauri-apps/tauri/blob/993038c49185307dfc895ab7657d77ca0d5ed7c7/crates/tauri/src/webview/mod.rs#L1040-L1045) と別に、製品builderは明示data_directoryを指定していない。

各crateはCargo.lock/tools.jsonのSHA256に一致する公式archiveをmemory内検証し、関連textだけ保存した。wryのvcs metadataはdirty=trueなのでcommitだけから同一性を仮定せず、上記fileのGit textとの一致も確認した。sourceをbuild/install/実行していない。

[WebView2環境API](https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/webview2-idl?view=webview2-1.0.3537.50#createcorewebview2environmentwithoptions) はUDFのrelative inputと環境変数overrideも説明する。これは実runのoverride使用の証拠ではない。ホストbuilderからruntimeまでのsource経路とdriver実測を混同しない。

現supervisor `close-observation.ps1:77–80` はexactrootを比較するためrootとEBWebViewchildを同一にしない。現diagnostic `profile_contract.accepted` もchildを拒否し、`phase2.native_profile_confirmed` は元owned集合の全profileMatchtrueを要求する。この条件を変えるのはpolicy/受入変更で、evidence collection修理として黙って行わない。

### 3. poll lifecycleとsticky failure

実observerはapp/driverが存在する各pollで`Get-OwnedApp`を呼ぶ。固定processes.ps1:121のWebViewcountguardとobserver:138のfalseyCommandLineguardは瞬間snapshotに対する条件。metadataは既にtrackedなviewでもtrackingcheckより先に再読する。catchはledgerへ追加してqueryFailedtrueを保持し、後続poll成功で消さず、最後にqueryCompleteを反転から作る。

したがってstartupのzero→stableとstable→teardownzeroが同じboundedledgerを作る可能性をsourceは持つ。null/empty/membergetter-normalized-nullもfalseyguardで合流し得る。PR217+公式PowerShell7.6.9binder研究はgetterの境界を裏付けるが、PR220の実falsey原因や順序を決めない。phaseを仮定して先行失敗を無視する変更はしない。

## Feedback loopと今回の変更

- **Real retained replay:** `test_profile_evidence_contract.py` はPR220のcanonicalrecord/filehashを照合し実`phase2.decision`へ入力する。分類complete/cleanuptrueでもprofilefalse/continuefalseを保持する。wrapperのobserverexit1によるgenericstopReason上書きも区別する。
- **Synthetic actual JS call site:** `profile-evidence-contract.test.mjs` は実normalize/classifierを呼び、rootとchildの両prefix合格/identity不一致、異なるstringの同じdrive-prefix、flat/nested/duplicated/missing/nonstringを検証。childsuffixだけという説明はここで反証される。
- **Synthetic actual PS call site (未実行):** `observer-poll.test.ps1` はASTから実ledger/Get-OwnedApp/Require-Live/Same-Process/Test-LineageCreationだけを抽出し、実poll本文と最終queryComplete代入をそのまま実行するfixture。OS境界のsnapshot/descendants/finish/waitだけをstubする。既にtrackedなsyntheticviewを使いhandle/argv/filesystemへ進ませず、Get-Process到達は固定markerで失敗する。zero→valid、valid→zero、null/empty/getter→valid、valid→validの6casesでstickyfailure、単回memberreads、未知stackのfailclosedを確認する設計。実stack/filelineは再現しないのでtrustedlocationmapperのWindowsruntime試験ではない。descendantsはsyntheticでpretrackedviewを使うため、lineage ownership・directory identity・cleanupの試験でもない。
- **Negative controls:** temporarysourcecopyからdrive-prefixだけ除去するとNode regressionが失敗。test内だけnativeexactsetgateをbypassするとPython negativecontrolが失敗。workingruntime sourceは変えない。

新規fixture不在でsource-contract試験がredになり追加後greenになったが、これはPSruntime red/greenではない。Macにはpwshが無く、PSparse/新pollfixtureは未実行。Python analogueをPS実証へ置換しない。旧strictfixtures/workflow/preflightへは新fixtureを自動追加しない。

## 判断と次の最小scope

今回はacceptance/runtime修正を正当化できない。safeな変更はretained regression、actual-call-siteのdiscriminatingfixtures、この契約文書だけ。root/childのlayoutnormality、返却path表記、元ownedset対応、pollの安定点を分けるpolicy判断が必要で、明示決定前は現gateを維持する。

まず新PSfixtureの実行確認が必要なら、一fresh標準Windows runnerでparse＋このpurefixture各一回だけ（app/build/driver/native0）が最小。問いは「固定実poll本文はsyntheticzero/falseyの後の成功でもqueryCompletefalseを保持し、対照valid→validはtrueか」。今回は行わない。

元のCI問題へ戻るnative証拠の問いは別で、「元supervisorが所有確認した同じcollectionの各identityについて、要求rootとruntimechildの対応が安定点で成立するか」。現recordはidentityを公開しないままslot対応を証明する情報がなく、syntheticfixtureでも答えられない。必要なら別決定で一freshrunner・一unchangedtrialだけを上限に、厳密なsameidentity対応をsanitizedclosedenumで保存する設計を独立reviewする。poll失敗は全保持し、子をrootに relabelせず、元15000ms/sessionfailure・profile/ownedcleanup gateを維持する。これは今回実装・公開・実行するworkflowではない。元timeout根因のnative red-capableloopは未成立、PR200blockedのまま。
