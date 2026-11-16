# READMEにあった統合と検証の経緯（2026-11-13まで）

[文書索引](README.md) / [現在の機能一覧](../README.md)

READMEを機能ごとの一覧に書き直したとき（2026-11-13）に、READMEの「現在地」節をそのまま移した記録です。各項目は記載した固定SHA・日付の時点の事実で、最新mainの状態ではありません。現在の機能と検証範囲は[README](../README.md)を、コマンド実行の受入は[コマンド実行の設計](run-command-design.md)を見てください。

## 実ChatGPTからMacへの固定接続確認（2026-11-12 UTC）

通常のChatGPTプロジェクト内の実チャットから、認証済みMacの限定開発用GUIへ固定の BEGIN → 5秒待機 → END を1回実行しました。対象は [#348](https://github.com/O2-CONNECTIVE/Tether/pull/348) の merge `8d94a50ceb59e7b9df888d928e4c962e14fa9981` です。

- チャットの要求と予測不能なコマンドIDを保持記録と独立に照合し、同じ実行の `mac_current_user`、exit 0、finished/terminal、command・owner・gatewayの各cleanup confirmedを確認しました。チャットにはBEGIN/ENDが表示されました。追加の独立した出力bytes読取はsession上限の429で拒否されたため、全bytesの独立取得完了は主張しません。
- OFF確認、GUI/helperの正常終了、serverの限定pilotの期限前exit 0、常駐登録の閉鎖、認証付きoffline catalogへの復帰を確認しました。常時運転は提供していません。
- #348はpending/connectingの観測をcleanup unknownと誤分類する問題の修正です。別の [#347](https://github.com/O2-CONNECTIVE/Tether/pull/347) はcleanup latchの修正です。今回の成功で、別途保持している過去3件のcleanup unknownを解消扱いにしません。
- #348の統合前後CIはそれぞれ14 workflow / 25 jobs成功です（[Go 3 OS・通常/race](https://github.com/O2-CONNECTIVE/Tether/actions/runs/43122018604)、[開発Macビルド・限定テスト](https://github.com/O2-CONNECTIVE/Tether/actions/runs/43122018612)）。CIと今回の実接続観測は別の証拠です。

**達成はこの固定作業の実接続に限定します。** 未署名の開発用信頼モデルであり、製品のDeveloper ID署名・notarization・通常Tauri・一般配布の受入ではありません。4アカウントのAccess利用資格と、操作できる登録済み本人/Macの対応は別です。自動起動なし・`restart=no`、Worker edge relayの制御接続に5分の寿命上限があり、fresh initializeの制限が残り、無人運転や途切れない日常利用は未達です。session上限429の原因調査・修正も別残件です。

[#349](https://github.com/O2-CONNECTIVE/Tether/pull/349) の開発GUI再準備導線は後続のsource統合であり、上記固定SHAの実接続試験には含めません。実機での回復受入、取消を含む広い機能条件、製品受入は未完了のままです。

## 以前のsource統合記録（以下は各固定SHA時点の履歴）

進捗確認: 2026-11-09、source基点 `9834ca2e`（[#321](https://github.com/O2-CONNECTIVE/Tether/pull/321)、#319・#320を含む）。この固定merge SHAの対象5 workflow/10 jobsは全attempt 1成功です。[Mac native sourceと非実行build](https://github.com/O2-CONNECTIVE/Tether/actions/runs/42699323616)を含みますが、path対象外のGUI縦断やWindows nativeをこのSHAで再実行したという意味ではありません。統合・失敗履歴・機能別残件は[進捗と完了条件 #189](https://github.com/O2-CONNECTIVE/Tether/issues/189)を参照してください。実利用dot接続・通常GUIの実接続受入・一般配布は未完了です。

- **通常GUIの同一session管理:** [#319](https://github.com/O2-CONNECTIVE/Tether/pull/319)で通常Tauriに `ResidentState` とMac外側終了guardを導入。同じnative sessionのport・元ownerを保持し、Unknownや未完了では終了を許可しません。[#313](https://github.com/O2-CONNECTIVE/Tether/pull/313)の同じownerから固定producerへ進む起動組成と合わせてsourceを統合しました。[#329](https://github.com/O2-CONNECTIVE/Tether/pull/329)でnative-linked GUIの通常起動を同じsessionへ結び、未設定時は起動せず終了可能、Unknownでは元ownerを保持します。nativeをlinkしないbuildはunbound、`ApprovedRelease.production=nil` のままです。匿名self-child・IPCの検証を署名済みproducer起動や製品Quitへ加算しません。[同一sessionと終了の境界](mac-gui-session-state.md)。
- **配布候補の材料を準備:** [#317](https://github.com/O2-CONNECTIVE/Tether/pull/317)でGUI＋producerの固定配置・hash・notice材料を照合し、[#321](https://github.com/O2-CONNECTIVE/Tether/pull/321)で固定sourceからDarwin ARM64の実producer Mach-Oを非実行buildする入口を追加。どちらも一致・build成功でなおblockedです。CIはJSON記録のみを保存し、生成imageは配布しません。[同じsourceの通常GUI組立](mac-gui-candidate-build.md)は、GUI・Swift・sealed producerを一つのappへ固定配置し、三imageのbytesとsource SHA/treeをbuild record v2で対応づけます。依存・notice閉包、署名・実登録・受領者Macの評価は残ります。[非実行buildと残工程](mac-sealed-producer-build.md)。
- **署名後producerをGUIへ固定する組立:** [専用の署名recipe](mac-signed-release-build.md)は、承認された既存Developer IDで固定producerを先に署名し、Appleが検証したCDHashを生成Swift入力へ固定してから通常GUIとnativeを組み立てます。通常buildのnil設定は維持し、CIは合成入力のcompileと不一致拒否だけです。実署名identity・実署名・notarization・登録・実dot接続・配布は未受入です。
- **Windowsの独立所有probe:** [#320](https://github.com/O2-CONNECTIVE/Tether/pull/320)で生存する固定親が元child handleを保持し、同じJobの継承と通常終了／resume前取消を確認する専用probeを追加。製品driverや通常GUIへは配線していません。#319初回のchild-older所有確認失敗は原因未確定の履歴として保持し、別probeや後続CIの成功で解消扱いにしません。
- **同じ仕事接続から辞書を読む:** [#314](https://github.com/O2-CONNECTIVE/Tether/pull/314)で `tether-work` の同じMCP sessionへlist/get_execution/read_outputを接続。対応toolだけを明示要求し、1回最大32件／4096 bytes、解放・不明・出力gapを区別します。[仕事操作CLIの契約](work-control-cli.md)。通常credential入口は未構成拒否で、端末一覧・file tool・新しい認可や実dot受入を追加したものではありません。

- **固定producerの専用入口:** [#309](https://github.com/O2-CONNECTIVE/Tether/pull/309)でゼロ引数の専用 `cmd/resident-pipe` を固定登録の内部組成へ接続。`5da76d67`（#309 merge）のpostmerge8 workflow/16 jobsは全attempt 1成功です。後続#313で固定bundle launchのsourceを追加しましたが、通常CLI/Tauriの信頼起動、実登録・署名製品起動・実dot・配布は未受入です。旧GUI off:receipt失敗と[#311](https://github.com/O2-CONNECTIVE/Tether/pull/311)の拒否観測/receipt順序修正のexact因果は未確定として保持します。併合済み[#310](https://github.com/O2-CONNECTIVE/Tether/pull/310)は#308の期限試験oracle修正のみで、製品deadlineを変更しません。
- **同一native ownerの終了証拠:** [#307](https://github.com/O2-CONNECTIVE/Tether/pull/307)で有限self-childの元Wait・EOF・close receiptからnative terminal証拠を作り、元IO workerとouter monitorの実joinが揃うまでQuitを拒否。Unknown履歴やpanicは成功へ変えません。`b498c4a4`（#307 merge）の対象10 workflowは全attempt 1成功です。[Mac nativeの検証](https://github.com/O2-CONNECTIVE/Tether/actions/runs/42610283640)とGUI/実GoのCI組成は別で、署名済み製品launchへの一体接続試験ではありません。通常releaseは未構成でstartup gateを維持し、実登録・実dot・製品全体のQuit・署名配布は未受入です。
- **固定登録から共通ownerへの内部組成:** [#305](https://github.com/O2-CONNECTIVE/Tether/pull/305)で固定OS accountの登録選択から内部bootstrapへ接続し、既存明示policy経路と同じowner loopを使う接続先を追加。`9b8591e4`（#305 merge）のpostmerge対象8 workflowは全attempt 1成功です。通常CLI/Tauriは未接続で、実登録の読取り・通常信頼起動・native27署名launch・実dot・製品Quit・配布は未受入のままです。
- **Mac GUIと実Goの限定CI縦断:** [#301](https://github.com/O2-CONNECTIVE/Tether/pull/301)で実WebView / Tauri IPCから実Go Standby・匿名loopback TLS gatewayへ接続。`af5f5a2b`（#303 merge）の専用CIではON/OFF適用と元owner回収を確認し、対象10 workflowは全attempt 1成功です。ただし[#301のpostmerge失敗](https://github.com/O2-CONNECTIVE/Tether/actions/runs/42585463422)は原因未確定で、#303は診断分類の追加であり原因修正ではありません。通常ResidentPortはunbound、通常信頼起動・実利用者認証・実dot・native作業実行・製品Quit・署名配布は未受入です。[配線と限定受入](resident-gui-wiring.md)を参照してください。
- **接続の隔離検証:** [#252](https://github.com/O2-CONNECTIVE/Tether/pull/252)の実loopback TCP/TLSと、[#253](https://github.com/O2-CONNECTIVE/Tether/pull/253)のMac実producer・固定command・OFF・元所有者終了確認を統合。合成認証と試験用CA/overlayを使い、実dotや通常製品認証は未受入です。#253初回のfailed-unknownは原因未解明のまま保持します。
- **共通所有と停止:** [#267](https://github.com/O2-CONNECTIVE/Tether/pull/267)のMac公開stack、[#269](https://github.com/O2-CONNECTIVE/Tether/pull/269)の故障後有限出力回収、[#270](https://github.com/O2-CONNECTIVE/Tether/pull/270)のHTTP失効・OFF回帰、[#271](https://github.com/O2-CONNECTIVE/Tether/pull/271)の原子的pipe取得を統合。実GUI/dot・署名済みlaunch・製品全回収/Quitの完了ではありません。
- **両プロファイルの共通契約:** [#274](https://github.com/O2-CONNECTIVE/Tether/pull/274)でMac current-user / Windows guestの同じ実行coreとmemory通信を照合する回帰を追加。取消受付と回収、unknown時の新規拒否、失効した単発許可の非復活、別grantの分離を検査します。実OS・GUI・認証通信の受入ではありません。
- **固定作業の一覧操作:** [#273](https://github.com/O2-CONNECTIVE/Tether/pull/273)で追加した一覧は `tether_fixed_list` から利用できます。同じ認証主体・登録host・control世代の保持記録を1要求最大32件で見つけ、既存status/cancel/releaseへ進めます。payload解放済み記録も表示しますが、作業内容・出力は列挙しません。[利用契約と制限](resident-execution-list.md)を参照してください。対応backendのみの操作で、新しいhost一覧・任意実行・永続記録・実dot受入は追加しません。
- **出力なしの状態照会:** [#277](https://github.com/O2-CONNECTIVE/Tether/pull/277)で追加した状態照会は `tether_fixed_get_execution` から利用できます。一覧で得たrequest_idから、保持出力本文を複製せずexit・stop・各cleanupを照会します。作業の失敗と照会自体の失敗、収集終了とcommand終端・回収確認を分けます。[状態照会の契約](tether-mcp-get-execution.md)。
- **必要な保持出力だけ読む:** [#276](https://github.com/O2-CONNECTIVE/Tether/pull/276)で追加した出力読取は `tether_fixed_read_output` から利用できます。同一主体・登録host・control世代の保持bytesをcursorと1要求最大4096 bytesで非破壊読取し、ack・解放・期限更新をしません。欠落や解放済みを区別し、出力完全性は推測しません。[保持出力の契約](resident-read-output.md)。両操作は対応backend限定で、旧statusを維持し、実dot・製品GUI受入や任意command・新認可を追加しません。
- **停止受付と待機期限:** [#280](https://github.com/O2-CONNECTIVE/Tether/pull/280)で共通command coreの早期Terminateを同じcommandの所有下で非同期化し、短いClose期限が長い停止待機に隠される問題を修正。未完了はunknownとして同じownerを保持します。取消受付は停止・回収成功ではなく、実OSの停止時間や応答しないTerminateの無期限保持を解決した保証ではありません。
- **Mac起動直前の再確認:** [#282](https://github.com/O2-CONNECTIVE/Tether/pull/282)で読取専用preflight中の取消・terminal状態をspawn直前に再確認し、既に停止した要求の起動を拒否します。確認とspawnは原子的ではなく、確認後の停止や後着processは同じownerが保持します。memory/fake OS境界の検証で、実OSのfork競合・実dot・GUI受入は未実施です。
- **後着shellへの入力抑止:** [#284](https://github.com/O2-CONNECTIVE/Tether/pull/284)でspawn/observerの失敗、取消、terminal unknownを観測した後はcommand bytesの送信開始を抑止し、後着process・pipe・元errorを同じownerで保持します。確認とWriteは原子的ではなく、既送信の副作用は取り消せません。入力抑止を停止・回収成功と同一視せず、実OS起動競合・実dot・製品GUIは未受入です。
- **未公開のファイル読取準備:** [#279](https://github.com/O2-CONNECTIVE/Tether/pull/279)の有限読取coreに、[#286](https://github.com/O2-CONNECTIVE/Tether/pull/286)で非公開Darwin readerと匿名一時folderの試験を追加。別途承認・取得されたdirectory handleから、独立した有限許可の再確認、no-follow取得、48KiB上限、失敗時の出力抑止を検査します。実利用者folderの選択・実認可provider・MCP・検索・GUIは未接続で、提供済みfile toolではありません。秘密全般の排除、連続したroot拘束、ACL、alias/mount/hardlink競合、特殊file差替え時のopen副作用、原子的snapshot・失効と返却は製品公開を止める残件です。
- **操作辞書を早期拡充:** [#261](https://github.com/O2-CONNECTIVE/Tether/pull/261)と[#265](https://github.com/O2-CONNECTIVE/Tether/pull/265)で既存操作の出力契約を明示済み。[#188](https://github.com/O2-CONNECTIVE/Tether/issues/188) / [#32](https://github.com/O2-CONNECTIVE/Tether/issues/32)に沿い、実接続受入と並行して専用操作を小さいPRで実装・検証します。端末一覧やファイル読取・検索・編集等が提供済みという宣言ではありません。

## 旧pilotと実利用の未確認範囲

| 境界 | 確認したこと | まだ確認していないこと |
| --- | --- | --- |
| Mac native command | 固定した旧 snapshot の1名・1 Macで正常実行、取消、要求なし期限切れ、実行中通信断の4 case | 任意 workload、他端末・他利用者、新しい build 全体の実動作 |
| Claude Code HTTP/OAuth | 実認証・接続と4 toolの discovery | Claude からの実 command。native 4 caseとは別の証跡 |
| dot → MCP | 実装・接続準備。旧pilotではplugin作成403を記録 | 実利用dot接続。過去403を現在の権限状態と断定せず、現行の利用可否は別確認 |
| Mac status GUI | 読取専用 SwiftUI、build・合成状態・所有 window 終了、ローカル評価 `.app` | 実 agent と GUI の同時動作、Finder/Gatekeeper、署名済み配布 |
| 人間による一時派遣 | 前景 CLI、評価 package 作成ツールの source | 一般受領者の起動受入、初回 enrollment、配布条件の充足 |

4名用の認証方針があることと、4名全員の backend identity / 鍵登録が済んでいることは別です。
限定実演用の公開入口・専用 gateway は閉鎖・停止を確認済みで、常時接続サービスは提供していません。
この文書は現在の開発ソースを説明し、すべてが公開 main に統合済みという宣言ではありません。
実機の固定証跡は **[限定pilotの検証記録・残件](tether-pilot-status.md)** を参照してください。記録の対象日は2026-11-01で、最新mainの再試験結果ではありません。後続sourceの確認には対象PRの固定headとCIを使い、製品目標は[製品文書](product/README.md)と区別します。
