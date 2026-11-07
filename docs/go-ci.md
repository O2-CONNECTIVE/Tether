# Go基盤のCI

[Python CI](ci.md) / [Go基盤](../backend/README.md) / [Issue #9](https://github.com/O2-CONNECTIVE/rescue-host/issues/9)

公開Go基盤の専用workflowは[go-offline.yml](../.github/workflows/go-offline.yml)です。
Go 1.27.3と公式setup-go v7.0.0の照合済commitを固定し、標準3 OSで依存取得、vet、製品build境界、通常試験、race試験を実行します。
PythonとTauriのworkflowは独立しています。

各stepは`python scripts/go_ci.py prepare / build / test / race`の対応するコマンドを呼びます。

resident source準備の`internal/residentwork`は`intent.go`の1fileだけを製品inventoryへ追加します。
純粋なtranscript形式で、通常caller・署名検証・認証・grant・通信を有効化しません。
続く`internal/workerowner`は`owner.go`・`run_once.go`・`wait_source.go`の3fileを限定登録します。
同じ取得・Run・Closeとsource寿命の所有処理だけで、通常caller・policy承認・native Sessionは未接続です。
workerownerまでの前段は、GoCI標準のcgo有効時に43package、Linux324/Darwin325/Windows355 production fileでした。
package/fileの欠落や未知fileは引き続き失敗し、直前の境界から1package/3fileだけ増えたことを検査します。
正規transport ownerの次段は既存6 implementation file内の変更だけで、このinventoryのpackage/file集合を増やしません。
[共通Dispatch・部分取得とcleanup観測](mac-canonical-transport-owner.md)をmemory経路と既存CLI fixtureで検査し、resident認証を有効化しません。
fixed intentのSession／wire引継ぎも既存implementation file内の変更で、同じ43packageの厳密inventoryを維持します。
[正規namespace・runtime期限](mac-fixed-intent-handoff.md)の追加回帰はmemory経路だけで、通常residentやnative権限を追加しません。
resident transport/policy閉包では、既存4 packageへ17 implementation fileを厳密に追加します。
transport / policy 段階は43 package、cgo有効時 Linux341 / Darwin342 / Windows372 production fileでした。
旧39 roleの集合は不変、当該4 roleは列挙したfileだけを増やし、各file欠落と未知factoryを拒否します。
正のpolicy入力試験はDarwin/cgoの自己所有fileだけをnative readerで読み、実鍵や運用設定・OS trustを使いません。
詳しくは[境界と未実証](mac-resident-transport-policy.md)を参照してください。
実行対象は公開`backend/`だけで、存在しない対象やテスト0件を成功にしません。
native enrollment 選択では residentmac の共通 `enrollment.go` と、Darwin/cgo の `enrollment_darwin.go` または拒否側 `enrollment_other.go` を追加します。
現 inventory は 45 package、cgo 有効時 Linux350 / Darwin353 / Windows381 production file、Darwin の cgo 無効時は351 fileです。
不足・未知 file の拒否と package 集合は維持し、selector の合成 account 試験を実 policy・鍵・producer の検証に広げません。
通常試験は`-cpu=1`、raceは`-cpu=2`で、試験内のGOMAXPROCS観測と照合します。
race検出の有無と、指定した並列度の観測を区別します。
Goのrace runtimeは終了直前に既定で1000ms待つため、CIでは`GORACE=atexit_sleep_ms=0`を明示します。
これは[race runtimeの終了時追加sleep](https://go.dev/doc/articles/race_detector)を除く設定で、検出や異常終了コード、子の終了確認1秒の条件は変更しません。
packageとtestの並列はともに1に制限しますが、個別試験内のgoroutineは動作します。

job上限は20分、通常のpackage timeoutは2分、raceは5分、各Go subprocessにも外側の上限があります。
raceはcgoと対応C compilerが必要です。
現行matrixのLinux/macOS/Windows amd64はGoの対応対象ですが、runnerで前提が成立しなければ失敗を報告し、黙ってraceを省略しません。
新しいcompilerやOS設定を導入しません。

取得段階だけ公式Go proxy/checksum DBを利用し、go.mod/go.sumの変化とmodule cacheの改変を検出します。
試験段階はGOPROXY=off、GOTOOLCHAIN=local、GOWORK=off、明示したGOFLAGSで追加取得と暗黙の版変更を防ぎます。
これだけで試験プログラムの全ネットワーク通信を遮断するわけではありません。
公開試験は自己所有の高位IPv4 loopbackとメモリ内鍵に限定し、host22、実ネット、外部relay、既存サービスを使いません。
TestMainの資源記録は残留と禁止dialの件数、loopback境界だけを確認して削除し、生の接続記録をartifactへ公開しません。
試験が失敗した場合も資源件数と経過時間を確認してから削除し、試験失敗と資源異常を両方報告します。

Darwinの背圧fixtureは、自己所有の対象socketへ接続前だけ`TCP_NOOPT`を設定し、接続前後の有効状態を検査します。
これは[Appleの接続単位のTCP option](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/man/man4/tcp.4)を使う試験設定で、製品socketやOS全体の設定は変更しません。Linux/Windowsの設定は維持します。
accept側の受信準備後に固定65536 byteを送り、同じ初回TCP Write由来の先頭byte受信と、50ms間隔2回の未完了観測を対応させます。
現在のbuffer容量やMSSは診断値であり、それだけで通知済みwindowや背圧の成立を証明しません。
700ms以内に実Writeの未完了を観測できなければ失敗し、失効後1秒以内の同じWriteの中断・所有資源回収という条件は変更しません。
この限定fixtureの成功を、製品Windows背圧や全TCP条件の保証へ広げません。

準備期限の実測は、製品の単調時計・壁時計の先着判定に合わせ、少なくとも一方が5秒へ達したことと、単調時計で6秒未満の観測上限を検査します。
待機結果・Snapshot前のContext終了・両時計の経過・失効理由を分け、Snapshot自身の期限検査を自律失効の証拠にしません。
偽clockでは5秒直前・到達・壁時計だけの前進と失効後の復活拒否を確認しますが、実OSの時刻変更を再現したものではありません。
これは試験の時計領域を揃える変更で、製品の5秒期限・lease・maximumは変更しません。

通常buildに選択される39package、Linux/macOSでは309実装、Windowsでは340実装ファイルを照合します。
guestaccountは下記の共通・OS別実装を限定登録し、memory/native API seamで準備・失効・清掃順序を検査します。実account・保護rootからのjournal供給・動的GuestSID・有効化は未接続です。
同じdeep snapshotからの制御用Factory pair、SCM所有threadのfresh Witness照合、応答write bridgeの共通3実装を限定登録します。正常縦断は既存memory auth/write fixtureの証拠で、実Native入力の認証・実SSH成功には広げません。
operatorauthのsole_input_batch.goは具体SoleInputの単回Moveと部分所有保持を追加します。Moveや候補取得を認証proofにはせず、Serviceへの正規factory供給と実Windows保護は未実証です。
operatorauthのsupplier.go・supplier_config.go・helper_launch_source.goと、Windowsのsupplier_image/native/security・helper_launch_native、他OSのhelper_launch_native_otherはsupplier借用と子側同launch証拠のsourceです。fake試験とcompileだけで、実SCM起動・token・ACL・認証の成功は未実証です。
一時supplierのSCM登録・撤去も既存のsupplier.go・supplier_native_windows.go・supplier_security_windows.go・helper_entry_other.goに置き、実装fileは増やしません。取得と同じgateで閉じ、Windowsの登録・撤去sourceはcompile対象で、fake試験は共通の所有・再試行・退役だけを確認します。
追加の未接続CLI、coordinator、所有pipe入口も相対ファイル名を明示し、fixtureや将来のpackageを無差別に許可しません。
supervisorの追加はauthority.goの純粋grant判定だけで、service登録・IPC・timer・実OS保護を提供しません。
同じauthority.goの失効通知`Done`、単回専有`ClaimConsumer`、時刻を観測しない`Grant`は、memory内の状態遷移と通知だけで、実装fileは増やしません。
ephemeralの追加はissuer.goのmemory-onlyな単回発行だけです。durable store、実OSの承認口、サービス起動や実通信は追加しません。
serviceauthownerは共通owner.goだけを許可し、実issuerと共通gateの状態を扱います。正規profile sourceや実OSの認証は提供しません。
servicehostは共通control.go/host.go/approved.go/job.go/ownercontrol.go/issued.go/nativebootstrap.go/nativestdio.go/ownedstdio.go/scm_ready.go/service.go/outer.go/dispatch.go/dispatch_owner.go/reservation.go/delivery.go/auth_dispatch.go/workload_reclaim.go/auth_exchange.go/auth_exchange_endpoint.go/auth_exchange_worker.go/control_factory.go/control_readiness.go/control_exchange.go/preview.go/preview_dispatch.go/confirmation.go/confirmed_grant.go/confirmed_grant_snapshot.go/confirmed_target.go/guest_staging.go/guest_staging_dispatch.goと、go env GOOSに対応するSCM実装1件・nativebootstrap実装1件・SCM ready実装1件だけを許可します。
Linux/macOSはscm_other.go、Windowsはscm_windows.goを厳密選択し、未知GOOSを拒否します。
nativebootstrapは非Windowsのnativebootstrap_other.goまたはWindowsのnativebootstrap_windows.goを選択します。
SCM readyは非Windowsのscm_ready_other.goまたはWindowsのscm_ready_windows.goを厳密選択します。
servicehostのrequest_loop.goを全OSで限定登録します。
明示runtime構成だけがSCM Running後に有限の要求受付を始め、前要求の回収とworker終了を確認して次へ進みます。
待機と要求全体の予算は固定構成から別々に与え、preview経路へ暗黙追加しません。
source/fake試験は未構成helperの拒否と所有権・期限・停止joinを確認するもので、実Windowsのmessage認証成功を示しません。
SCMの登録や起動、実機の権限分離を検証したとは扱いません。
localcontrolは共通policy.go/boundary.go/wire.goと、go env GOOSに対応する明示実装だけを許可します。
Linux/macOSはnative_other.go、Windowsはnative_windows.goとio_windows.goを選び、未知GOOSは拒否します。公開Openの接続ゲートは閉鎖中で、CIは実pipeや実token/ACL検証を行いません。
通常laneのfixtureは`_test.go`だけです。実TCP/TLSの接続試験だけは、[MCP loopback CI](mcp-loopback-ci.md)の`ci_mcp_loopback` tagで明示実行し、既存offline suiteには含めません。
OS別のテストファイル制約を尊重し、fixtureや注入adapterを通常buildへ混ぜません。
runbackendはruntime.goだけ、childjobは共通child.go/plan.go/fixed_command.go/prepared.go/inspection.go/guest_token.go/guest_token_policy.go/guest_token_buffer.goと、非Windowsのnative_other.go、
Windowsのnative_windows.go/artifact_windows.go/prepared_windows.goだけを許可します。
preparedは同一の停止中targetを検査してから単回開始するsource候補です。
方式Cは[ADR-0003](adr/0003-owned-guest-workload.md)で設計採用済みですが、登録成功は製品入口への有効化や実Windowsの検査・停止・期限執行の成立を意味しません。
protectedkeyは共通acl.go/format.go/loader.go/profile.go/scope.go/source.go/validation.goと、
非Windowsのnative_other.goまたはWindowsのnative_windows.goの1件だけを許可します。
bootstrapはcodec.go/launch.go/native_receiver.go/strict.go/types.go/validate.goと、
非Windowsのnative_receiver_other.goまたはWindowsのnative_receiver_windows.goだけを許可します。
bootstrapioはio.goとnative_other.go/native_windows.goの対象別1件、
bootstrapauthはauth.go/image.go/kernel.go/profile.go/validation.go/prepared_service.goと、非Windowsのnative_other.go、Windowsのnative_windows.go/image_windows.goを厳密選択します。
childstdioはoperation.go/pipes.go/streams.goと、非Windowsのnative_other.goまたはWindowsのnative_windows.goを厳密選択します。
servicereadyはconfig.go/operation.go/plane.go/security.go/preflight.go/service_witness.go/endpoint.go/endpoint_input.go/endpoint_operation.goと、非Windowsのnative_other.go、
Windowsのnative_windows.go/connect_windows.go/listener_windows.go/endpoint_windows.go/endpoint_io_windows.goを厳密選択します。
cmd/rescue-backendはmain.go/native.goの2件です。通常buildに試験用adapterを混ぜません。
serviceprovisionはcompose.go/errors.go/record.go/record_decode.go/record_strict.go/record_validate.go/root.go/root_doc.go/root_io.go/root_validate.go/service.go/preview.go/confirmation.goと、
共通のexecution.go/execution_native.go/execution_owner.go/launch.go/launch_security.go、
Linux/macOSのroot_other.go/launch_other.go、Windowsのroot_windows.go/root_windows_io.go/launch_windows.go/launch_windows_security.go/launch_windows_threads.goだけを厳密選択します。
cmd/rescue-service-hostはmain.goの1件だけを許可し、未知package・未知file・対象OSと異なる実装を拒否します。
cmd/rescue-auth-helperもmain.goの1件だけを許可し、未設定の正規sourceを有効化しません。
servicecontrolは全OSでrequest.go/strict.goと応答専用response.go/response_fields.go/response_preview.goの5件だけを許可します。応答codecの成功は認証・承認・停止の証明ではありません。
純粋なDecodeUntrustedFrameの構造検証であり、認証・dispatch・issuer・transportへ配線せず製品gateを閉鎖したままにします。
native receiver・認証・所有I/OのsourceとAPI seam試験を、実Windowsでの保護主体・ACL・実EOF後PID照会の成立へ広げません。
保護stdio/stderrの所有・取消・EOF回収にはsourceとAPI seam試験がありますが、実Windowsでの保護成立や期限内回収は未実証です。
許可0のSCM readyとservice全体のissuer所有はsource候補とAPI seam試験の対象です。
固定provisioning読取りのsource候補は追加されていますが、実配置と`fixedEvaluationConfig`は未設定です。
保護preflight、強token取得、SCMの内側回収と元threadの外側回収ackを接続したsource/API seam試験を含みます。
固定実値、正規operator認証・承認source、受理transportは未設定・未接続で、既定の入口と製品接続gateは閉鎖を維持します。
実service/process ACLやlistenerの保護成立、元thread退役とOS上の物理停止をCIだけで確認済みとは扱いません。
Go JSONと終了コードを両方検査し、pass、fail、skip、no-test package、未終端を区別します。
選択された公開test sourceと実際のtop-level結果を照合し、全skipや欠落を成功にしません。
検査をJSON fixtureで検証する開発ツール試験は、製品のSSH境界のTDDとは別です。

## 既知の未達

double-closeの物理遮断はNOT_MET（#5）、Windows背圧は未検証（#6）のままです。
caseの無断除外、skip追加、continue-on-errorは行いません。
full suiteが既知fixture前提の不成立で失敗した場合も、その結果を保存し、他の試験の合格を全suite成功へ読み替えません。
未設定commandの単発試験不一致は原因未確定で、診断追加後の合格と分けて扱います。
故障が期待どおり再現できる回帰試験の成功も、実遮断や受入44条件の合格とは別判定です。

## 固定依存の一覧

| module | 固定版 | 権利確認の参照 |
| --- | --- | --- |
| golang.org/x/crypto | v0.58.0 | [LICENSE](https://github.com/golang/crypto/blob/v0.58.0/LICENSE) / [PATENTS](https://github.com/golang/crypto/blob/v0.58.0/PATENTS) |
| golang.org/x/sys | v0.49.0 | [LICENSE](https://github.com/golang/sys/blob/v0.49.0/LICENSE) / [PATENTS](https://github.com/golang/sys/blob/v0.49.0/PATENTS) |
| github.com/coder/websocket | v1.8.15 | [LICENSE.txt](https://github.com/coder/websocket/blob/v1.8.15/LICENSE.txt) (ISC) |

golang.org/x/crypto と golang.org/x/sys は公開担当が固定版の BSD 系 LICENSE と PATENTS を確認済みです。coder/websocket は 2026-10-31 に公式 Go proxy / checksum DB による取得、tag commit 9c8faadccd1b679e811a79ce506f8a10237251ad、ISC LICENSE.txt、追加 module 依存なしを確認しました。
依存ソースやバイナリをこのPRで同梱せず、実配布物のnotice確認は#24へ残します。
checksumの一致をlicense審査や脆弱性検査の代替にはしません。

公式根拠：[Go testとbuild制約](https://pkg.go.dev/cmd/go)、[race要件](https://go.dev/doc/articles/race_detector)、[module検証](https://go.dev/ref/mod)、[toolchain選択](https://go.dev/doc/toolchain)。

許可0のservice dispatchは同じService/issuerへのprivate登録とstatus・失効受付だけを追加します。native認証provider、承認発行、workload回収receipt、実transportは未接続です。CIのmemory試験を実認証・実SSHの証明へ読み替えません。

reservation.goはprivateな所有順序だけを扱います。同じServiceのstopと予約を同じmutexで順序付け、未確認workerの参照を保持します。認証/承認、policy、configDigest、grant、wire成功応答を生成せず、今回のlocal検証は実worker adapterや実機の証明ではありません。

delivery.goは既存の保護された承認sourceから同じHostを登録し、実Host.Run workerの終了と保持Host.Closeを分けて確認する共通実装です。controlled経路からlegacy承認へのfallback、認証・policy生成、transportや製品接続の有効化は追加しません。

operatorauthの共通・OS別実装は[検証入口](../scripts/go_ci.py)のPRODUCT_FILESとOPERATOR_AUTH_FILESを正本として限定照合します。
単回read/auth/復帰/report、launcher/report I/O、同Service adapterのsourceを含みますが、正規policy/認証供給・実transportの結合と実機保護は未接続・未実証です。
作者のローカル全module loopback bind試験はsandbox拒否のまま再実行せず、対象memory試験の保存結果と明示的に許可された既存CIの最終head結果を区別して確認します。

operatorauthのprofile照会は共通batch_scope.go/profile_provider.go/profile_query.goと、対象OSのprofile_query_other.goまたはprofile_query_windows.goの1件だけを追加します。正規policy/query leaseの供給や実Windows保護の検証を意味しません。

候補取得は共通endpoint_candidate.goと、非Windowsのendpoint_profile_other.go、Windowsのendpoint_candidate_windows.go/endpoint_profile_windows.goだけを追加します。正規AuthenticationId供給やSoleInputとの結合を実装済みと扱いません。


guestaccountは既存のcore 8件とaccount_query.go/account_query_buffer.go/account_query_read.go、
journal.go/journal_codec.go/journal_security.go、sid_codec.go/sid_run.go/sid_run_io.go/sid_script.go/sid_source.goを共通で選択します。
Linux/macOSではaccount_query_other.go/journal_other.go、Windowsではaccount_query_windows.go/account_query_windows_machine.go/journal_windows.go/journal_windows_pin.goだけを追加します。
childjobの固定commandは固定argvと依存file保持の構成を扱います。登録成功はguest作成・無効化・削除や実Windows保護の成立を証明せず、公開NewNativeの未設定拒否は維持します。


guest作成入口のsource候補はcreation_codec.go/creation_native.go/creation_query.go/creation_script.go/creation_source.go/sid_script_parts.goを共通登録します。
creation_query_other.goはLinux/macOS、creation_query_windows.goはWindowsだけで選択し、未知・欠落・他OS実装の混入を拒否します。
登録してもpublic NewNativeは未構成拒否を維持します。staging guestの限定権限検査は下記のsource候補へ接続しましたが、正規manifest/保持handleの供給、実行時token/handle gate、Service連携は未接続のため、製品の準備や削除の完了を成功扱いしません。
memory/source API seamとcrosscompileを、実Windowsアカウント操作やSSH接続成功へ読み替えません。


確認要求の再検査はservicehost/confirmation.goとinternal/serviceprovision/confirmation.goの2実装だけを追加登録します。
前者の単回予約/opaque ticketと後者の元owner RPC/保持Record再検査を結びます。製品runtime providerが未構成の場合はSERVICE_NOT_READYを維持し、以下の同issuer発行はsource/fake試験でのみ確認しています。
unit/raceの各レイヤの成功を実native認証と元threadを通したWindows縦断の成功へ合成しません。


一時guestの限定権限検査は、[対応範囲](guest-limited-inspection.md)の共通12実装とLinux/macOSの4実装またはWindowsの5実装だけをguestaccountへ追加登録します。
作成→journal→無効staging検査→SID清掃のsource/fake確認であり、未来の実行token/継承handle/runtimeの保護やSeparatedNonAdminGuestの成立をこの登録から推定しません。


[固定配置から無効guest準備へのadapter](guest-staging-adapter.md)は、guestaccountのstaging.go/staging_borrow.go/staging_native.go/staging_profile.go/staging_query.goと対象OSのstaging_native_other.goまたはstaging_native_windows.goだけを追加登録します。
bootstrapauth/prepared_lease.goは同じPreparedServiceのprofileと借用gateを保持する共通実装です。固定manifest/control検査leaseのsourceは以下へ接続しましたが、stagingへの呼出しと実guest token/resume前gateは未接続・未実装です。source/fakeやcrosscompileを実配置・実account・実SSHの成功へ広げません。


[固定guest設定と要求単位の検査借用](guest-fixed-source.md)は、guestaccountのfixed_layout.go/fixed_layout_strict.go/fixed_layout_validate.go/fixed_layout_wire.go、serviceprovision/guest_source.go、servicereadyのinspection_lease.go/inspection_lease_security.go/inspection_lease_token.goを共通で追加登録します。
Windowsだけinspection_lease_identity_windows.go/inspection_lease_windows.goを選択し、他OS・欠落・fixture混入を拒否します。固定配置の読取は強token取得前へ接続しますが、正規承認→同issuer Plan/session→staging、動的SIDの鍵設定と実sshd token/shell停止は未接続・未実装です。実配置・OS操作・実SSHの合格には広げません。


承認確認から同issuerのbinding発行へ進む共通実装は、servicehost/confirmed_grant.goとconfirmed_grant_snapshot.goだけを追加登録します。
private OS seamをfakeへ置換した実confirm/codec/issuer経路であり、製品runtime providerは未登録です。static staging結果だけをruntime証跡へ昇格せず、Launch/Authority/Host.Run/実SSHは開始しません。


同Endpointの入力分離に先立つ検査借用はserviceready/endpoint_staging.goだけを共通登録します。
NewInspectionLeaseをduplexが残るSplit前に取得し、後段の失敗・取消でも部分leaseを返します。認証や承認の証拠を生成せず、実機のpipe/ACL/SSH成功を示しません。


未起動準備用の所有ticketとdispatchはservicehost/guest_staging.go/guest_staging_dispatch.goだけを共通登録します。
同Service・issuer・確認予約・初回期限へ束縛し、実行受付を閉鎖したまま対象限定の失効・未起動確認を行います。ticket自体は認証やruntime保護の証拠ではなく、実Native stagerと元ownerの回収接続は各sourceの検証範囲を別に確認します。

未起動guest準備の呼出し境界はguestaccount/never_created.go/never_created_session.goと、serviceprovision/staging_execution.go/staging_preview.goを共通登録します。
検査lease・元要求・所有owner・設定digestの一致と実回収完了を各層で検査し、登録されたこと自体を認証や起動許可へ昇格させません。

方式Bの認証結果はoperatorauth/authenticated_report.go/operator_binding.go/sid_policy.go/sid_profile_frame.goだけを共通追加登録します。
profile/reportの明示v2と単回binding消費を検査し、policy構成をRoot証明やhelper起動可能性へ昇格させません。実Windows/SSH成功は示しません。

保護Rootから方式Bのpolicyを供給する接点はserviceprovision/operator_policy.go、Serviceの同一channel束縛はservicehost/operator_sid.goだけを共通追加登録します。
静的policyの登録はhelper起動資源や実message認証の成功ではなく、未構成ならEndpoint取得より前に拒否します。

LocalServiceと専用service SIDの分離は、登録済みoperatorauthのlaunch.go、native_batch.go、report.go、native_launch_windows.go、native_launch_binding_windows.goを変更します。
追加はhelper_service_identity_test.goのfixtureだけで、実装fileのallowlistは増やしません。source/childの現在のgroup検査を、正規supplierの初期保護や実Windows隔離の証明へ昇格させません。

process照会権とtoken objectの保護を対応させる検査は、servicereadyの共通`process_token.go`とWindows限定`process_token_windows.go`を登録します。
同じ保持processのtoken owner/DACLと同一性の検査、変化時の失効、部分取得と回収再試行をprivate kernel seamで試験します。
この追加に実Windowsでのtoken取得試験やservice起動は含めず、Windowsではcompileと同じfake回帰だけを行います。実配置・認証・実SSHは未検証です。

## agent command core

agentexecは全OS共通のcontract.go/output.go/run.go/session.goだけを登録し、OS別実装や試験用runtimeを通常buildへ混ぜません。
同じissuerの現在Authorityを保持したstart/status/output/cancelと、所有process回収のmemory/fake試験です。
固定agent policy digestへのmode束縛、Authority単位の単回Session、`Authority.Done`による外部失効の伝達を含み、公開`New`の試験は数百msの実timerを使います。
固定guest shellのnative runtime、Service/transport/MCPの呼出し、実リモートでのcommand実行・GUI・SSH・配布の成功は含みません。
[契約](agent-command-contract.md)の未接続境界を参照します。

現在利用者の境界は`supervisor.NewCurrentUser`と`ephemeral.NewCurrentUser`だけが扱い、既定の`supervisor.New`・`ephemeral.New`のLaunchと既存のguest前提consumerは拒否します。実装fileは増やしていません。

agent用の薄いadapterとして、全OS共通のagentwire（client.go/server.go/strict.go/wire.go）、mcpadapter（jsonrpc.go/server.go/tools.go）、cmd/rescue-mcp（main.go）を登録します。
macagentは共通のruntime.go/process.goと、darwinではnative_darwin.go、Linux/Windowsではnative_other.goの1件を厳密選択します。
試験はnet.Pipe/io.Pipeとmemory fakeだけで、認証済みchannel・relay・同意producer・実shell起動・MCP登録を実行しません。
CIの合格はMacの実command実行、遠隔接続、配布の成功を示しません。詳細は[MCP adapter](agent-mcp-adapter.md)を参照します。

## 検査と承認の未接続bridge

同対象の検査/単回開始の分離はchildjob/inspection.go、保持対象と同issuer challengeの結合はservicehost/confirmed_target.goだけを共通追加します。
認証要求→検査→明示fakeの同対象nonadmin証拠→実issuer Prepare→実issuer Launchと単回開始を、private syscall/target seamで検証します。
実token生成、nonadmin証拠のnative供給、製品constructor/dispatch起動、実SSH、Windows停止/期限執行を検証した意味ではありません。

## 保持target tokenの読取候補

childjobのguest_token.go/guest_token_policy.go/guest_token_buffer.goを共通、guest_token_windows.goだけをWindowsへ限定追加します。
OSから取得するgroup/privilege/identity等の有限bufferと、同じ保持target/元token/観測receipt/期限の照合をsource/fakeで検証します。
Windowsの追加試験はテスト自身のprocess tokenをTOKEN_QUERYで読むだけで、guest logonやaccount/credential/privilege変更を実行しません。観測SIDやtoken値は出力しません。
生成元はinner sshdで未決のまま、別のLogonUser/S4U/Go tokenは生成せず、実guest token producerやSeparatedNonAdminGuest成立を検証したとは扱いません。

## 固定された無害workloadの実行準備

方式Cのsourceでは、従来NETWORK用の契約を残し、別の保護runtime設定からBATCH期待値を選びます。
同Serviceの確認要求と元thread所有RPCを通して、一時guestの実receipt、権利差分、秘密入力、保持token、保護された停止中targetを同じownerに束縛します。
対象は固定PowerShellの`exit 0`で、SSH sessionや任意commandの製品入口ではありません。

追加登録はchildjobの共通`protected_guest.go`とWindowsの`protected_guest_windows.go`、
servicehostの共通`runtime_guest.go`、`runtime_guest_actor.go`、`runtime_guest_native.go`、`runtime_guest_prepare.go`、`runtime_guest_admission.go`、
serviceprovisionの共通`runtime_execution.go`、`runtime_native.go`、`runtime_source.go`です。
guestaccountは共通`runtime_close.go`、`runtime_credential_io.go`、`runtime_credential_run.go`、`runtime_credential_script.go`、
`runtime_expectation.go`、`runtime_guest.go`、`runtime_layout.go`、`runtime_native.go`、`runtime_ops.go`、`runtime_probe.go`、`runtime_rights.go`、`runtime_session.go`を追加し、
非Windowsでは`runtime_native_other.go`、Windowsでは`runtime_native_windows.go`、`runtime_rights_windows.go`、`runtime_token_windows.go`だけを選択します。
失効後の新規取得を拒否しつつ、元の回収scopeと部分取得参照を保持する試験を含みます。
source/fake・compile・CIの成功は、実Windowsでのaccount作成、BATCH logon、権利や保護SD、物理停止・撤去の証拠へ広げません。
実機操作と製品callerは有効化していません。

Agent-only encrypted channel inventory: `internal/agentchannel` contains channel.go, server.go, client.go, conn.go, keys.go and liveness.go. Tests use buffered in-memory connections and synthetic test-only keys; no listener/dial, enrollment or real relay is executed.
信頼済み合成の`internal/agentlink`はhost.go/client.goの2件を全OS共通で登録します。試験はMCP stdio→agentwire→暗号化agent channel（memory conn）→同じSession→fake Mac runtimeの全経路を通しますが、実relay、同意producer、実鍵登録、Macでの実command実行の成功は示しません。
2026-10-31の統合でchannelの失効順序の失敗を修正しましたが、修正後の結果は統合担当の実行で確認するまで未確定です。


## HTTP MCP gateway source inventory

`internal/agentgateway` の gateway.go/http.go/token.go/config.go、`cmd/rescue-gateway/main.go`、`internal/mcpadapter/http_bridge.go` を追加登録します。検証は直接 HTTP handler 呼出しと memory channel/fake runtime に限定し、実 listener、認証 provider、Tunnel、実 Mac command の成功とは区別します。


`internal/agentkeys` は共通 7 ファイルと OS 別 native 1 ファイルを登録します。Darwin の native ACL 検証 source は cgo 必須、CI build の CGO_ENABLED=1 を維持します。CGO 無効の Darwin は鍵読取りを拒否します。Linux は `native_linux.go` の pure Go / x/sys 実装を限定登録しますが、compile 成功を実 native 保護確認済みと扱いません。試験は fake kernel であり、実鍵や実 OS の owner/ACL は読んでいません。

### WSS registration transport
`internal/agenttransport` の protocol.go/stream.go/gateway.go/mac.go を登録します。試験は memory connection と fake HTTP transport に限定し、実 TCP listener・TLS・Tunnel・実 Mac 起動は未検証です。

### Concrete foreground agent CLI
`cmd/rescue-gateway` の main.go/config.go と `cmd/rescue-mac-agent` の main.go/config.go を登録します。実 reader/key loader/tty approval/WSS caller は source 上で接続し、試験では private dependency seam と memory listener のみを使います。実行許可・実鍵供給・public route・実 Mac command の検証は別です。


### Commandless Tether route probe
`cmd/tether-echo-probe` の main.go/probe.go を全 OS 共通の限定 inventory に登録します。Go の検証は fake reader/listener、memory connection と合成 assertion、Worker の検証は fake VPC binding/socket/timer を使います。command を実行しない疎通専用 probe であり、実 Access 認証・VPC/WSL 到達・Mac command の成功は別判定です。現時点の Worker gate は閉鎖を維持します。設定と実演 gate は [probe 手順](../deploy/cloudflare/probe/README.md) を参照してください。


### Local Access identity inspector
`cmd/tether-access-identity/main.go` を全OS共通に登録します。公開issuer/AUD/JWKSを明示し、stdinのassertionを既存AccessVerifierで検証した後に限定claimsだけ出力するローカル登録補助です。認可・鍵発行・自動登録は行いません。試験は合成RSA/JWTとmemory inputのみ。詳細は [検証器手順](tether-access-identity.md) を参照してください。


## Standby / owned pipe の source inventory

Standby / owned pipe の組成段階は45 packageで、residentmac は factory.go / standby.go / standby_types.go / wire.go、residentpipe は共通 codec.go / mapper.go / startup.go でした。
Darwin / cgo のときだけ owned_darwin.go / run_darwin.go を追加しました。通常 CLI / command entrypoint は登録しません。
この段階の cgo 有効時は Linux348 / Darwin351 / Windows379 production file、Darwin で cgo 無効なら349 fileでした。次の native enrollment 選択では residentmac に共通 / OS 別の2 fileを追加し、現在の350 / 353 / 381（Darwin cgo 無効351）へ進めます。他43 roleのfile集合は変えません。
各新fileの欠落・未知launcher、package結果の欠落をhelperで拒否し、fixture / 実producer idle/startup / Rust integration / 評価TLSをproductionや標準試験へ混ぜません。
source unit は自己所有pipe、専用の無害test child、checkout内の合成保護fileだけです。実producer / 実鍵 / 実policy / ON / 外部 / GUI / OS trustは使いません。
strict vet / build と runtime unit、cross compile、実機の証拠を分けます。詳細は [source組成](mac-resident-producer-composition.md) を参照してください。
