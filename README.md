# Tether（テザー）

[用語と名前の境界](GLOSSARY.md)

[製品目標と完了条件](docs/product/README.md)では、Mac内セルフホストから外部接続へ進む目標と、現在の検証状態を分けて案内しています。

必要な間だけ、承認した相手から端末を操作できる仕組みを作っています。
**開発中・限定評価段階です。一般配布・本番利用はまだできません。**
現在は **AI agent から Mac を操作する経路**を優先しています。

## 読者別の入口

[4読者の案内](docs/README.md#読者別の入口)から、評価する人・運用する人・開発する人の正本へ進めます。停止と回収不明の確認先も同じ案内にまとめています。

## 2つのゴール

1. **dot がリモートホストのコマンドを操作できること。** MCP の start / output / status / cancel を、本人の承認・期限・失効に結びます。
2. **人間が開発 PC から、必要なときだけ操作 agent を派遣できること。** 最小経路は受領者による取得・前景起動・その回の明示同意です。無認証 remote install や恒久常駐を前提にしません。

人向け SSH 互換は現在の完成条件から外しています。途中の SSH / Windows 実装は削除せず、将来の再統合候補として保存しています。リポジトリ名は `Tether` へ変更済みです。Go module・実行ファイル名は今回変更していません。

## 製品と3つの入口

Tetherの共通サービスにCLI・GUI・MCPを接続し、認可・失効・実行所有・結果・停止回収のルールを入口ごとに複製しない構成を目指します。

- **CLI:** 単一process・単一ownerの前景adapterとして、FIFO入出力によるstatus/on/off/closeを[#291](https://github.com/O2-CONNECTIVE/Tether/pull/291)で統合しました。TTY対話や別process操作、新IPCは未採用。[#293](https://github.com/O2-CONNECTIVE/Tether/pull/293)では別の仕事操作CLI `tether-work` を追加し、同一MCP sessionで既存fixed start/result/cancel/releaseとsession closeを扱います。通常起動は認証入力未構成で拒否し、実credential方式は未決です。
- **GUI:** 別Macへダウンロードして実接続できる評価appはまだ用意されていません。共通サービスの状態と作業結果を表示・操作する入口。画面閉鎖、待機OFF、個別取消、ホスト終了を区別します。画面を閉じた後の常駐継続など、寿命の未決事項は実装時に明示します。
- **MCP:** 認証済みエージェントの要求を同じサービスへつなぐ入口。接続できることや待機ONを作業許可と同一視しません。

**CLI先行・Mac CIでの再現を優先**します。CLI契約試験と、実OS producerを結ぶ有限作業・結果・取消/OFF・回収の検証を分け、対象OS・版・許可fixture・観測範囲を固定します。利用者個人のMacを必須環境とせず、実GUI、署名・OS権限、実利用dot接続は別の受入として残します。[#291](https://github.com/O2-CONNECTIVE/Tether/pull/291)ではCI限定overlayの実producerをJSON/CLI×normal/raceの4laneで検証し、同じ版の結果と元Close/EOF/Wait0を確認しました。[#293](https://github.com/O2-CONNECTIVE/Tether/pull/293)ではCLI host laneに実tether-work子processを接続し、同一sessionの仕事操作・結果・取消・release/tombstone・Close/EOF/Waitからhost OFFまで確認しました。匿名fixture限定で、session終了は作業回収とは別です。製品CLI全体の完成ではなく、実signal配送も未受入です。通常起動のenrollment/publisher gateも維持します。

常駐ホストを主用途とし、その都度許可する単発派遣も維持します。常駐/単発は入口とは別の利用モードで、常駐だけで許可を永続化・復活させません。操作は実行管理、選択folderの限定読取/検索、その後の入力/限定編集へ段階的に進めます。非公開準備と提供済み操作は以下の現在地で区別します。

[製品体系と受入 #189](https://github.com/O2-CONNECTIVE/Tether/issues/189) / [共通サービス #32](https://github.com/O2-CONNECTIVE/Tether/issues/32) / [操作辞書 #188](https://github.com/O2-CONNECTIVE/Tether/issues/188) / [単発版 #22](https://github.com/O2-CONNECTIVE/Tether/issues/22)

検証版は正式なversion numberingを使わず「カレントの検証版」と表記する方針です。source commit・build日時・artifact hashなどの追跡識別を記録し、更新前後の検証結果を取り違えないようにします。この表記方針は評価appの作成・配布・実接続完了を意味しません。

## 現在地

### 実ChatGPTからMacへの固定接続確認（2026-11-12 UTC）

通常のChatGPTプロジェクト内の実チャットから、認証済みMacの限定開発用GUIへ固定の BEGIN → 5秒待機 → END を1回実行しました。対象は [#348](https://github.com/O2-CONNECTIVE/Tether/pull/348) の merge `8d94a50ceb59e7b9df888d928e4c962e14fa9981` です。

- チャットの要求と予測不能なコマンドIDを保持記録と独立に照合し、同じ実行の `mac_current_user`、exit 0、finished/terminal、command・owner・gatewayの各cleanup confirmedを確認しました。チャットにはBEGIN/ENDが表示されました。追加の独立した出力bytes読取はsession上限の429で拒否されたため、全bytesの独立取得完了は主張しません。
- OFF確認、GUI/helperの正常終了、serverの限定pilotの期限前exit 0、常駐登録の閉鎖、認証付きoffline catalogへの復帰を確認しました。常時運転は提供していません。
- #348はpending/connectingの観測をcleanup unknownと誤分類する問題の修正です。別の [#347](https://github.com/O2-CONNECTIVE/Tether/pull/347) はcleanup latchの修正です。今回の成功で、別途保持している過去3件のcleanup unknownを解消扱いにしません。
- #348の統合前後CIはそれぞれ14 workflow / 25 jobs成功です（[Mac native](https://github.com/O2-CONNECTIVE/Tether/actions/runs/43122018604)、[GUI限定縦断](https://github.com/O2-CONNECTIVE/Tether/actions/runs/43122018612)）。CIと今回の実接続観測は別の証拠です。

**達成はこの固定作業の実接続に限定します。** 未署名の開発用信頼モデルであり、製品のDeveloper ID署名・notarization・通常Tauri・一般配布の受入ではありません。4アカウントのAccess利用資格と、操作できる登録済み本人/Macの対応は別です。自動起動なし・`restart=no`、Worker edge relayの制御接続に5分の寿命上限があり、fresh initializeの制限が残り、無人運転や途切れない日常利用は未達です。session上限429の原因調査・修正も別残件です。

[#349](https://github.com/O2-CONNECTIVE/Tether/pull/349) の開発GUI再準備導線は後続のsource統合であり、上記固定SHAの実接続試験には含めません。実機での回復受入、取消を含む広い機能条件、製品受入は未完了のままです。

### 以前のsource統合記録（以下は各固定SHA時点の履歴）

進捗確認: 2026-11-09、source基点 `9834ca2e`（[#321](https://github.com/O2-CONNECTIVE/Tether/pull/321)、#319・#320を含む）。この固定merge SHAの対象5 workflow/10 jobsは全attempt 1成功です。[Mac native sourceと非実行build](https://github.com/O2-CONNECTIVE/Tether/actions/runs/42699323616)を含みますが、path対象外のGUI縦断やWindows nativeをこのSHAで再実行したという意味ではありません。統合・失敗履歴・機能別残件は[進捗と完了条件 #189](https://github.com/O2-CONNECTIVE/Tether/issues/189)を参照してください。実利用dot接続・通常GUIの実接続受入・一般配布は未完了です。

- **通常GUIの同一session管理:** [#319](https://github.com/O2-CONNECTIVE/Tether/pull/319)で通常Tauriに `ResidentState` とMac外側終了guardを導入。同じnative sessionのport・元ownerを保持し、Unknownや未完了では終了を許可しません。[#313](https://github.com/O2-CONNECTIVE/Tether/pull/313)の同じownerから固定producerへ進む起動組成と合わせてsourceを統合しました。[#329](https://github.com/O2-CONNECTIVE/Tether/pull/329)でnative-linked GUIの通常起動を同じsessionへ結び、未設定時は起動せず終了可能、Unknownでは元ownerを保持します。nativeをlinkしないbuildはunbound、`ApprovedRelease.production=nil` のままです。匿名self-child・IPCの検証を署名済みproducer起動や製品Quitへ加算しません。[同一sessionと終了の境界](docs/mac-gui-session-state.md)。
- **配布候補の材料を準備:** [#317](https://github.com/O2-CONNECTIVE/Tether/pull/317)でGUI＋producerの固定配置・hash・notice材料を照合し、[#321](https://github.com/O2-CONNECTIVE/Tether/pull/321)で固定sourceからDarwin ARM64の実producer Mach-Oを非実行buildする入口を追加。どちらも一致・build成功でなおblockedです。CIはJSON記録のみを保存し、生成imageは配布しません。[同じsourceの通常GUI組立](docs/mac-gui-candidate-build.md)は、GUI・Swift・sealed producerを一つのappへ固定配置し、三imageのbytesとsource SHA/treeをbuild record v2で対応づけます。依存・notice閉包、署名・実登録・受領者Macの評価は残ります。[非実行buildと残工程](docs/mac-sealed-producer-build.md)。
- **署名後producerをGUIへ固定する組立:** [専用の署名recipe](docs/mac-signed-release-build.md)は、承認された既存Developer IDで固定producerを先に署名し、Appleが検証したCDHashを生成Swift入力へ固定してから通常GUIとnativeを組み立てます。通常buildのnil設定は維持し、CIは合成入力のcompileと不一致拒否だけです。実署名identity・実署名・notarization・登録・実dot接続・配布は未受入です。
- **Windowsの独立所有probe:** [#320](https://github.com/O2-CONNECTIVE/Tether/pull/320)で生存する固定親が元child handleを保持し、同じJobの継承と通常終了／resume前取消を確認する専用probeを追加。製品driverや通常GUIへは配線していません。#319初回のchild-older所有確認失敗は原因未確定の履歴として保持し、別probeや後続CIの成功で解消扱いにしません。
- **同じ仕事接続から辞書を読む:** [#314](https://github.com/O2-CONNECTIVE/Tether/pull/314)で `tether-work` の同じMCP sessionへlist/get_execution/read_outputを接続。対応toolだけを明示要求し、1回最大32件／4096 bytes、解放・不明・出力gapを区別します。[仕事操作CLIの契約](docs/work-control-cli.md)。通常credential入口は未構成拒否で、端末一覧・file tool・新しい認可や実dot受入を追加したものではありません。

- **固定producerの専用入口:** [#309](https://github.com/O2-CONNECTIVE/Tether/pull/309)でゼロ引数の専用 `cmd/resident-pipe` を固定登録の内部組成へ接続。`5da76d67`（#309 merge）のpostmerge8 workflow/16 jobsは全attempt 1成功です。後続#313で固定bundle launchのsourceを追加しましたが、通常CLI/Tauriの信頼起動、実登録・署名製品起動・実dot・配布は未受入です。旧GUI off:receipt失敗と[#311](https://github.com/O2-CONNECTIVE/Tether/pull/311)の拒否観測/receipt順序修正のexact因果は未確定として保持します。併合済み[#310](https://github.com/O2-CONNECTIVE/Tether/pull/310)は#308の期限試験oracle修正のみで、製品deadlineを変更しません。
- **同一native ownerの終了証拠:** [#307](https://github.com/O2-CONNECTIVE/Tether/pull/307)で有限self-childの元Wait・EOF・close receiptからnative terminal証拠を作り、元IO workerとouter monitorの実joinが揃うまでQuitを拒否。Unknown履歴やpanicは成功へ変えません。`b498c4a4`（#307 merge）の対象10 workflowは全attempt 1成功です。[Mac nativeの検証](https://github.com/O2-CONNECTIVE/Tether/actions/runs/42610283640)とGUI/実GoのCI組成は別で、署名済み製品launchへの一体接続試験ではありません。通常releaseは未構成でstartup gateを維持し、実登録・実dot・製品全体のQuit・署名配布は未受入です。
- **固定登録から共通ownerへの内部組成:** [#305](https://github.com/O2-CONNECTIVE/Tether/pull/305)で固定OS accountの登録選択から内部bootstrapへ接続し、既存明示policy経路と同じowner loopを使う接続先を追加。`9b8591e4`（#305 merge）のpostmerge対象8 workflowは全attempt 1成功です。通常CLI/Tauriは未接続で、実登録の読取り・通常信頼起動・native27署名launch・実dot・製品Quit・配布は未受入のままです。
- **Mac GUIと実Goの限定CI縦断:** [#301](https://github.com/O2-CONNECTIVE/Tether/pull/301)で実WebView / Tauri IPCから実Go Standby・匿名loopback TLS gatewayへ接続。`af5f5a2b`（#303 merge）の専用CIではON/OFF適用と元owner回収を確認し、対象10 workflowは全attempt 1成功です。ただし[#301のpostmerge失敗](https://github.com/O2-CONNECTIVE/Tether/actions/runs/42585463422)は原因未確定で、#303は診断分類の追加であり原因修正ではありません。通常ResidentPortはunbound、通常信頼起動・実利用者認証・実dot・native作業実行・製品Quit・署名配布は未受入です。[配線と限定受入](docs/resident-gui-wiring.md)を参照してください。
- **接続の隔離検証:** [#252](https://github.com/O2-CONNECTIVE/Tether/pull/252)の実loopback TCP/TLSと、[#253](https://github.com/O2-CONNECTIVE/Tether/pull/253)のMac実producer・固定command・OFF・元所有者終了確認を統合。合成認証と試験用CA/overlayを使い、実dotや通常製品認証は未受入です。#253初回のfailed-unknownは原因未解明のまま保持します。
- **共通所有と停止:** [#267](https://github.com/O2-CONNECTIVE/Tether/pull/267)のMac公開stack、[#269](https://github.com/O2-CONNECTIVE/Tether/pull/269)の故障後有限出力回収、[#270](https://github.com/O2-CONNECTIVE/Tether/pull/270)のHTTP失効・OFF回帰、[#271](https://github.com/O2-CONNECTIVE/Tether/pull/271)の原子的pipe取得を統合。実GUI/dot・署名済みlaunch・製品全回収/Quitの完了ではありません。
- **両プロファイルの共通契約:** [#274](https://github.com/O2-CONNECTIVE/Tether/pull/274)でMac current-user / Windows guestの同じ実行coreとmemory通信を照合する回帰を追加。取消受付と回収、unknown時の新規拒否、失効した単発許可の非復活、別grantの分離を検査します。実OS・GUI・認証通信の受入ではありません。
- **固定作業の一覧操作:** [#273](https://github.com/O2-CONNECTIVE/Tether/pull/273)で追加した一覧は `tether_fixed_list` から利用できます。同じ認証主体・登録host・control世代の保持記録を1要求最大32件で見つけ、既存status/cancel/releaseへ進めます。payload解放済み記録も表示しますが、作業内容・出力は列挙しません。[利用契約と制限](docs/resident-execution-list.md)を参照してください。対応backendのみの操作で、新しいhost一覧・任意実行・永続記録・実dot受入は追加しません。
- **出力なしの状態照会:** [#277](https://github.com/O2-CONNECTIVE/Tether/pull/277)で追加した状態照会は `tether_fixed_get_execution` から利用できます。一覧で得たrequest_idから、保持出力本文を複製せずexit・stop・各cleanupを照会します。作業の失敗と照会自体の失敗、収集終了とcommand終端・回収確認を分けます。[状態照会の契約](docs/tether-mcp-get-execution.md)。
- **必要な保持出力だけ読む:** [#276](https://github.com/O2-CONNECTIVE/Tether/pull/276)で追加した出力読取は `tether_fixed_read_output` から利用できます。同一主体・登録host・control世代の保持bytesをcursorと1要求最大4096 bytesで非破壊読取し、ack・解放・期限更新をしません。欠落や解放済みを区別し、出力完全性は推測しません。[保持出力の契約](docs/resident-read-output.md)。両操作は対応backend限定で、旧statusを維持し、実dot・製品GUI受入や任意command・新認可を追加しません。
- **停止受付と待機期限:** [#280](https://github.com/O2-CONNECTIVE/Tether/pull/280)で共通command coreの早期Terminateを同じcommandの所有下で非同期化し、短いClose期限が長い停止待機に隠される問題を修正。未完了はunknownとして同じownerを保持します。取消受付は停止・回収成功ではなく、実OSの停止時間や応答しないTerminateの無期限保持を解決した保証ではありません。
- **Mac起動直前の再確認:** [#282](https://github.com/O2-CONNECTIVE/Tether/pull/282)で読取専用preflight中の取消・terminal状態をspawn直前に再確認し、既に停止した要求の起動を拒否します。確認とspawnは原子的ではなく、確認後の停止や後着processは同じownerが保持します。memory/fake OS境界の検証で、実OSのfork競合・実dot・GUI受入は未実施です。
- **後着shellへの入力抑止:** [#284](https://github.com/O2-CONNECTIVE/Tether/pull/284)でspawn/observerの失敗、取消、terminal unknownを観測した後はcommand bytesの送信開始を抑止し、後着process・pipe・元errorを同じownerで保持します。確認とWriteは原子的ではなく、既送信の副作用は取り消せません。入力抑止を停止・回収成功と同一視せず、実OS起動競合・実dot・製品GUIは未受入です。
- **未公開のファイル読取準備:** [#279](https://github.com/O2-CONNECTIVE/Tether/pull/279)の有限読取coreに、[#286](https://github.com/O2-CONNECTIVE/Tether/pull/286)で非公開Darwin readerと匿名一時folderの試験を追加。別途承認・取得されたdirectory handleから、独立した有限許可の再確認、no-follow取得、48KiB上限、失敗時の出力抑止を検査します。実利用者folderの選択・実認可provider・MCP・検索・GUIは未接続で、提供済みfile toolではありません。秘密全般の排除、連続したroot拘束、ACL、alias/mount/hardlink競合、特殊file差替え時のopen副作用、原子的snapshot・失効と返却は製品公開を止める残件です。
- **操作辞書を早期拡充:** [#261](https://github.com/O2-CONNECTIVE/Tether/pull/261)と[#265](https://github.com/O2-CONNECTIVE/Tether/pull/265)で既存操作の出力契約を明示済み。[#188](https://github.com/O2-CONNECTIVE/Tether/issues/188) / [#32](https://github.com/O2-CONNECTIVE/Tether/issues/32)に沿い、実接続受入と並行して専用操作を小さいPRで実装・検証します。端末一覧やファイル読取・検索・編集等が提供済みという宣言ではありません。

### 旧pilotと実利用の未確認範囲

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
実機の固定証跡は **[限定pilotの検証記録・残件](docs/tether-pilot-status.md)** を参照してください。記録の対象日は2026-11-01で、最新mainの再試験結果ではありません。後続sourceの確認には対象PRの固定headとCIを使い、製品目標は[製品文書](docs/product/README.md)と区別します。

## 構成と対象環境

```text
MCP client → HTTPS 認証入口 → Go gateway / 薄い MCP adapter
                                  ↕ 認証済み agent channel / WSS
受領者の Mac: 前景 CLI → fresh 同意 → 有限 grant → command runtime
                    └→ 専用 status pipe → 読取専用 SwiftUI viewer
```

gateway は呼出主体と登録済みホストを照合しますが、Mac の同意を代行せず grant を発行・延長しません。
人向け SSH server や既存 sshd への fallback はありません。
詳しい責任分担は[文書索引](docs/README.md)、[gateway](docs/agent-gateway.md)、[CLI](docs/agent-cli.md)を参照してください。

| 対象 | 現時点の範囲 |
| --- | --- |
| Mac command target | 現在ユーザー権限、昇格なし・sandboxなし。旧pilot agent build宣言はmacOS **26.0**。#271のnative pipe取得は**27以上**を要求し、旧方式へfallbackしない。製品セット全体の互換性は未受入 |
| Mac status viewer | macOS **13.0**以上を宣言。ただし13実機未検証で、agent全体の対応下限ではない |
| Linux / Windows | gatewayの配置候補とsource/build検証をcommand target対応と区別。製品目標はMac先行、その後Windows。Windows製品の実接続受入は未完 |
| Tauri resident GUI / 旧 Python | TauriはMac専用CIで実Go Standbyへ接続してON/OFFを検証。通常起動・実dot・配布は未受入。旧Pythonは履歴として保持 |

## ローカル開発の最短入口

既存の対応 Swift toolchain がある Mac で、リポジトリルートから：

```sh
cd ui/mac-status
swift build
swift run StatusCoreChecks
python3 scripts/package_local.py
```

最後の行は既存 build からローカル評価 `.app` を作るだけで、起動・install・署名・agent接続をしません。既存出力は上書きせず停止します。
[viewer の手順と制約](ui/mac-status/README.md)を先に確認してください。通常起動には専用 status pipe が必要で、入力がなければ接続状態は不明です。

Go の入口は `backend/cmd/rescue-gateway`、`backend/cmd/rescue-mac-agent`、stdio adapter は `backend/cmd/rescue-mcp` です。
実接続は保護設定・鍵・identity・今回の同意・許可された入口がそろってから行います。clone/buildだけでは接続できません。
[Go検証](docs/go-ci.md) / [CLIの設定契約](docs/agent-cli.md) / [開発手順](docs/agent-workflow.md)

## 安全と配布の境界

- Macの実行は現在ユーザーと同じ権限です。そのユーザーがアクセスできるファイル・ネットワークへ届き、同一ユーザーからの干渉も防げません。Windowsの隔離guestと同等ではありません。
- 期限・取消・通信断で失効し、同じownerが所有process群とI/Oの回収を試みます。実行済み変更は戻らず、群を離れた子孫の停止は保証しません。
- cleanup unknownは非成功です。GUIの閉鎖・EOF・残時間0だけでは停止確認になりません。読取専用SwiftUI viewerは承認・開始・取消を行いません。Tauri resident GUIのON/OFFは専用CIでの限定検証で、通常起動の認可gateを解除しません。
- packageに鍵・token・同意を入れません。license/依存閉包、配布署名・notarization、一般受領者の受入は未完です。評価 `.app` のlinker ad-hoc署名は配布identityの署名ではありません。

[文書索引と現行安全契約](docs/README.md) / [Mac runtime](docs/mac-agent-runtime.md) / [旧設計の安全要件](docs/security.md)

## 歴史資料

SSH/Windowsの過去の提供状況は旧検証状態に分けて記載しています。
旧受入条件の未達や過去の失敗は消していません。現在のMac限定評価と、旧SSH/Windowsの提供状況を合成しないでください。

[旧検証状態](docs/status.md) / [配置方針](docs/repository-layout.md) / [変更履歴](CHANGELOG.md) / [ライセンス](LICENSE)
