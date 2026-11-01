# 固定provisioning sourceの準備と未設定の入口

初期評価の対象は、別の非管理者guestによる監督・停止処理への干渉である。
host operator/adminの既侵害とadmin guestは保証範囲に含めない。
製品gate、実Windowsでの保護成立、既存44条件の判定は変更しない。

## 入力と信頼根

`Anchor`は保護された配布・配置側が固定するservice名、recordと独立digestの絶対path、期待volume、有限の読取り・回収予算である。
`fixedEvaluationConfig`のAnchor・表示予算・回収間隔は全値未設定であり、環境変数、argv、UI、current directory、実行fileの所在から補完しない。
実配置先、実SID、実volume serialをこのsourceで選定してはいない。

この候補の信頼根はOSが保護する固定配置であり、runtimeでの署名検証ではない。
recordとそのSHA-256を同じ未検証fileから取り出して信頼する方式ではなく、別の固定digest fileも同じ保護条件で検査する。
両fileを同時に更新するtransactionは提供しないため、更新途中の不一致は拒否する。
host operator/adminによる両fileの巻戻しを防ぐjournalでもない。

正規の配布・配置者は、実行file・record・digestの出所とintegrityを独立に確認し、guestに変更される前から固定配置を保護する必要がある。
配布署名の検証、配布物と期待hashの照合方法、実行主体・service object/process/token objectの保護を実機設定とともに確定する必要がある。
現在のACL照会だけで、配置前の改変、既存の書込みhandle、OS管理者の介入を排除したとはいえない。
service自身のimage hashは独立recordに置き、実行fileへそのfile自身のhashを埋め込む循環を作らない。

## 固定record候補

一時guest向け固定sidecarの読取・full入口での必須化は[固定source](../../../docs/guest-fixed-source.md)を参照する。
このsourceを正規承認や実guest tokenの取得へ接続したとは扱わない。

`rescue-service-provisioning/1`は固定fileの非秘密の形式であり、operator承認wire、既存control/1、bootstrap/1を変更しない。
UTF-8の単一object、最大256KiB、深さ16、先頭`{`と末尾`}`に任意の単一LFだけを認める。
未知・重複・欠落・大小文字違いのfield、不正UTF-8、null、余剰record、BOM、範囲外の値は拒否する。
整数とdurationのnanosecond値は正規10進文字列、時刻は正規UTC、hashは小文字hexとして扱う。
正確なfield集合は`record_decode.go`の型が所有する候補であり、共有schemaへ独自の承認commandを追加しない。

recordには固定profile、service/process保護方針、固定artifactのpath/hashと祖先、既存host鍵参照と公開fingerprint、許可raw Ed25519公開鍵、backend上限と各予算だけを含める。
秘密鍵、token/handle、grant、Identity、boot epoch、保存済み承認、絶対hard deadlineは含めない。
`DecodeRecord`と`ValidateRecord`の成功は構造検証だけで、配置元の認証や実行許可ではない。

## 接続するsourceと残る実行条件

`PrepareFixed`はcallerが固定する元OS threadで固定rootを開き、digest照合後のrecordを復号し、観測したservice/user SIDとrecordの一致を要求する。
そのquery-only primary tokenを既存`bootstrapauth.PrepareService`の照合に借用し、現在のservice imageと配置証拠を検査する。
取得途中の失敗でも返した`Prepared`が同じ所有参照を保持し、同じthreadの`Close`で回収する。
fatalならthreadを再利用せず、未確認の回収を成功へ変換しない。
保持中の`Prepared.ComposeService`は同じrecordを再照合後に既存Serviceの構成へ渡し、部分準備・Close後・fatalの状態から構成を作らない。
このquery-only handleは起動用へ転用しない。別の`LaunchFactory`と`Execution`が、下記の保護検査と全回収までの所有を担当する。

`ComposeService`は、保護供給元が別途所有する既存primary handleと認証済み承認sourceを受け取る純粋constructorである。
同じ固定recordからnative stdio、native bootstrap、JobRuntime、Serviceを組み合わせる。
構成では承認sourceを読まず、issuer・grant・listener・processを生成しない。
入力が正規の供給元から来たことやtokenに必要な権限があることをconstructorの成功で証明しない。

`rescue-service-host`の既定入口は、固定Anchorが未設定ならnative APIより前に拒否する。
固定Anchorだけを後から配置しても、有限の回収再確認間隔と認証済み承認sourceが未設定ならnative rootの読取りにもSCMにも進まない。
query-only handleを起動用handleへ転用するfallbackはない。
新しいserviceの起動は許可0であり、保存recordから旧grantやdeadlineを復元しない。
UI pollingを承認・renewへ変換しない。

次の配線に必要なのは以下である。

- service側とoperator側の双方の認証、有限codec、新しい人の承認を発行する入口。既存localcontrolのhello/snapshot/stopをStartやrenewへ拡張しない。
- 固定profileの実値と、child側の保護profile供給・承認済みtransportの実供給。仮の成功callbackや秘密値のserializationで補完しない。

## 検証の境界

構造検証とnative API seamの通常unit/race試験、およびWindows向けcompileを扱う。
実service登録・起動、token/ACL/account/FW変更、鍵作成・実鍵読取り、SSH接続は行わない。
実WindowsのACL、reparse拒否、元thread、取消completion、独立guest保護の成立は未実証である。
通常CIをP2専用試験、Job最後handle、物理停止、監督hangやOS sleepの証拠へ読み替えない。

## 強い起動handleとSCMの全回収owner

`NewNativeExecution`は固定Anchor、正の回収再確認間隔、認証済み承認sourceを必須とする純粋constructorである。
`NewNativeExecution`は従来の承認・強token構成の専用入口として残す。
`RunFixed`は固定評価用のruntime入口へ接続するが、既定値は全て未設定のため取得前に拒否する。
環境変数・argv・UIによる上書き、仮のSID/handle/承認sourceによる補完は行わない。

`Execution.Run`は元callerのNoThreadTokenと実service SID所属を拒否検査した後、専用の元OS threadで`PrepareFixed`を行う。
固定record・digest・imageを保持検査した同じ`Prepared`からquery-only tokenを借り、`PreparePreflight`へ渡す。
この段階はservice object、現在のprocessのPID/creation/image/DACLをqueryだけで照合し、listenerや許可を作らない。

`LaunchFactory`はまず既存primaryを`READ_CONTROL | TOKEN_QUERY`で開き、token objectのowner/DACLとTokenOwner/TokenDefaultDaclを照合する。
process保護だけで既存threadの保護を代用せず、現在のservice processに属する全threadを有限snapshotで列挙し、保持handleのPID/TID/creation/alive/owner/DACLを確認する。
未知主体にtoken変更・duplicate・assign・impersonate、thread終了・suspend・context変更等を許すACL、NULL DACL、未対応ACE/rightsは拒否する。
未知主体のgeneric権限は、別object種へのgeneric mappingをreadonlyと解釈せず拒否する。
固定profileのtrusted主体（guestを除くservice/user/operator、SYSTEM、Administrators）には既知generic権限も許す。
DefaultDaclをobject種ごとの読取り権限の同義語として扱わない。
thread数はprocess内1024/system内65536に制限し、前後の集合変化やthread消滅でも拒否するため、thread churnは可用性上の拒否原因となる。

これらの検査後だけ、別handleを`TOKEN_QUERY | TOKEN_DUPLICATE | TOKEN_ASSIGN_PRIMARY`（0xb）で開く。
actual granted access、非継承、同一token ID/authentication ID/modified ID/user/groups、保持process binding、保護状態を再照合し、`Borrow`時にも再確認する。
query-only handleの権限変更、token生成、ログオン、LocalSystem fallback、権限調整や失敗後のbaseline更新は行わない。
[CreateProcessAsUserW](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-createprocessasuserw)にはcaller側primaryの追加権限と、必要privilegeの一時有効化がありうるため、明示handleの0xbだけからOS内部の全権限やprivilege不変を主張しない。
不足時の起動失敗やmodified ID変化は既存の失効・回収経路へ入り、成功するまで権限を広げない。
この可用性と実Windowsの具体的挙動は未検証である。

同じ強handleを`Prepared.ComposeService`へ借用し、一つのServiceへ`Execution`のcleanup-only RPCを一度だけbindする。
SCMの内側はissuerの発行拒否と全失効を先に行い、Host/Job/listener/承認workerを回収する。
`InnerCleanupProof`は同じServiceの実inner終端だけを表し、zero値・別Service・active borrowerから外側の回収を承認できない。
`Execution.Finalize`はこのopaque proofを確認して、取得を行った元OS threadへ有限context付きRPCを送る。
そのthreadで強token/preflight/保持threadを先に閉じ、次に`Prepared`のquery token/file/imageを回収する。
SCM Executeが戻る前にこの外側ackまで待ち、`svc.Run`のreturn後だけに後始末を置かない。
STOP_PENDING後の追加Stop配送には依存せず、同じownerが回収だけを再確認する。

部分取得、起動前取消、dispatcher開始失敗でも同じownerを保持し、回収未確認で`Run`をmainのexitへ戻さない。
Fatalはstickyで、元threadを他の仕事へ再利用せず参照を保持する。新しいthreadで認証し直して回収済みにしない。
個々のcleanupは有限contextを持つが、未確認や戻らないnative APIがある場合のRun全体の終了期限は保証しない。
実際のSCM、OS保護、過去に漏出したhandle/書込みmappingの不存在、将来の独自thread SD、監督hang/OS sleep/Job最後handleをsnapshotやpure試験で証明したとは扱わない。
operator認証のRevertToSelf失敗時policy、process-wide終了、承認/起動dispatch、receiptのproducerはこの差分へ接続しない。


## 保持Recordからprepare-intentの表示候補へ

同じ元threadの `Prepared.ComposePreviewService` は、保持query-only tokenとscopeを再照合し、同じRecordから表示専用descriptorを解決して実 `servicehost.NewPreviewService` へ渡す。
configDigestは照合済みexact bytesのSHA-256、policyDigestは既存 `backend.PolicyDigest`、keySetDigestは同じraw Ed25519鍵から既存 `runbackend.KeySetDigest` で計算する。
表示fingerprintも同じ鍵から生成し、重複を除いて整列する。
絶対hard deadlineは構成時点とRecordのMaximumDurationから固定し、再prepareで延長しない。
LeaseDurationは既存の上限制約として保持し、wireへ独自fieldを追加しない。
表示intentのTTLと要求・起動回数予算は保護呼出側の `PreviewLimits` で明示し、Recordの別予算や環境変数から補完しない。

descriptorは構成時の表示snapshotであり、毎要求のfile再認証、認証済みoperator、承認、grantを表すcapabilityではない。
認証/sequenceを通ったprepare-intentの表示までを扱い、ConfirmApprovalと実起動は未構成拒否を維持する。
既存ComposeServiceの承認/launcher検査にpreviewへのfallbackを追加しない。
単独の `NewNativePreviewExecution → nativeExecutionStage.Initialize` は、表示専用modeを明示して `PrepareFixed → ComposePreviewService` へ接続する。
このmodeは強token取得・旧Approval・Launcherを呼ばず、同じrootのquery-only tokenだけを借りる。
legacy `NewNativeExecution` のnil Approval拒否にfallbackを追加しない。
PreviewLimitsと回収再確認間隔も固定供給元が明示する必須値で、現RunFixedでは未設定のままである。

preview構成でも元Execution ownerを `BindOuterFinalizer` へ結び、同じopaque inner proofを確認してから元threadでPreparedを閉じる。
SCM側は自分のthreadで別のWitnessを取得し、元Prepared.scopeを移送しない。
query tokenとroot/file参照はinner終端まで保持し、回収未確認なら元ownerを残す。
source/fake試験は、実preview constructor・Execution RPCとSCM起動失敗の回収モデル、およびservicehost内のmemory WitnessによるServe/stopを別々に扱う。
両者を実WindowsのSCM縦断成功や本人承認の成功へ合成しない。
実行fileの既定Anchor、正規operator入力、実Windowsの保護設定と承認sourceは未接続のままであり、このsource/fake試験を実機の接続成立と扱わない。


本人承認sourceの次の接点は、既存previewIntentと認証済みconfirm要求の間である。
同じboot/channel/intent・発行したexact digest・有効期限を同じstop gateで検査し、確認試行を単回消費する必要がある。
保持Recordの再解決は元Prepared ownerへの有限要求で行い、dispatch threadでscopeを直接使わない。
解決後も同じgateでstopとintentを再照合し、guest bindingと実authority producerがない間は成功receipt・ApprovedPolicy・grantを発行しない。
解決元を持たないNewPreviewServiceのconfirmは未構成拒否のまま。
元Executionをouter ownerに結んだ構成だけが、opaque PreviewConfirmationTicketを既存owner loopへ有限RPCで渡す。
構成時のsnapshotと保持Recordを再照合し、初回hard deadlineを再計算で延長しない。
query token・scope・Recordの照合は元threadで行い、取消済みauth workerへの返答待ちでcleanupを塞がない。
source panicや既知fatalは分類を保持し、元threadと所有参照を保持して退役する。別threadで健康な成功へ作り替えない。

再検証が成功してもSERVICE_NOT_READYを返す。本人承認受付receipt、ApprovedPolicy、Challenge、grant、live Authorityは発行しない。
source/fake証跡は、Service内の合法auth model→実confirm→memory resolverと、Execution/元stageのRecord再検査・不正ticket拒否を分ける。
実native認証から元ownerまでを通した単一のWindows縦断試験ではない。
新しい汎用Approval interfaceや自己申告の認証値でこの欠落を埋めない。

guest準備のPlanが要求する正generationは、[既存journalの識別値](../../../docs/temporary-guest-native-boundary.md)であり、issuerのgenerationと同一にする要件ではない。
guest作業のepoch/intent/counterを供給し、検査後の所有SID・policyと後発の同issuer grantを不変に関連付ける保護producerが必要である。
未検証guestをSeparatedNonAdminGuestと仮定したApprovedPolicyやChallengeを先に作らない。
この未接続を理由に、既存Issuer/Challengeの意味変更や新しいnamespace選択を必須の意思決定へ追加しない。
独立AuthenticationId登録を新しい必須条件へ追加したり、既存の認証検査を緩めたりもしない。

## 方式Bの固定policy供給

明示preview/staging入口の元Execution threadは、保持Rootのexact Record・query token・scopeと初回表示snapshotを再照合し、固定OperatorSID/GuestSIDをService公開前に一度だけ渡す。
元owner以外の供給、可変cacheやmessage本文からの期待policy構築、別Serviceへの供給は拒否する。
Serviceは認証後のopaque bindingを同じCollector/owner/issuerとstop gate内で照合し、後続要求のAuthId等が変われば失効する。
指定SIDの別ログオン資格は既存許可の引継ぎではなく、新規channelでの新規承認だけに使う。
専用helper token・same-launch evidence・helper entryの保護sourceは未構成であり、Endpoint取得/Moveより前に拒否する。Service起動tokenを専用helper tokenの代用品にしない。
Recordが`helperSupplier`を持つ場合、同じ元threadが公開前に`operatorauth.RegisterHelperSupplier`を呼び、得た登録（部分を含む）をstageが保持する。取得gateが閉じている既定では何も作らずbindへ進み、Prepareは拒否を続ける。撤去はinner proof後（または未公開時）のstage `Close`の最初に行い、未確認なら他の参照も保持したまま`ErrRootUnconfirmed`、faultなら`ErrRootFatal`で元threadを退役させる。
登録関数だけを置き換えるprivate seam（本番は未設定で上記関数）により、元owner・stageの保持・エラー分類・撤去順序をfake試験する。保持Recordとpolicyはstageが渡し、実SCMの登録・撤去は未実証のまま。fatal時の元thread退役はstageの`ErrRootFatal`までを試験し、owner loopの退役自体はこの試験で実行しない。
source/fake試験と実Windows・実runtime/inner sshd・実SSHの成立を分け、後者のgateは閉鎖を維持する。

policyの再検査失敗や取消でServiceを公開しない場合も、inner Close workerと元threadのcleanup-only RPCを分離し、実inner proof→元Root回収→Close workerの実returnを待って起動エラーを返す。
outer未登録の失敗では元threadがRootを回収し、fatalなthreadや未確認参照を再利用しない。


## 固定評価入口と同じ確認からの単回開始

`rescue-service-host → RunFixed → NewNativeRuntimeExecution` は、sourceに固定する評価構成だけを選ぶ。
Anchor・表示予算・回収再確認間隔・runtimeの要求受付予算は未設定のままで、引数・環境変数・UI・現在の端末から補完しない。
実機用の値を後から与える汎用CLIや暗黙fallbackは追加しない。
設定が揃ったsource構成では、元Execution ownerを `BindRuntimeGuestEvaluationOwner` に固定し、runtime用の保護設定と同Serviceの認証・確認へ接続する。
既存の `BindRuntimeGuestOwner` は準備専用のまま、preview/staging/legacyから評価開始へ自動昇格しない。

runtimeのnative構成は `NewRuntimeService` を選び、SCM Running後に既存の認証exchangeを呼ぶ。
`RuntimeRequests` は待機上限と要求全体の上限を明示する。両方とも正で1分以下、待機上限は要求全体を超えない。
前要求の回収と専用workerの実終了を確認した後だけ次要求を受ける。エラー・期限切れでは停止し、要求を自動再試行しない。
停止は新規受付を閉じ、要求の取消・所有参照の回収・loop終了を待つ。未終了workerを成功として外側ownerへ返さない。
`NewPreviewService` とstagingは要求受付を始めず、runtime予算が欠けた構成をpreviewへfallbackしない。

評価modeは成功したconfirm応答にexact grantを内部保持し、同じauth workerの実書込み完了とdispatch完了後にだけ単回開始する。
応答は承認receiptであって開始・終了成功の証明ではない。開始失敗・取消・失効は同じissuerとownerの停止・回収へ戻す。
別要求、二重確認、書込み失敗、古い証拠を新しい開始へ利用しない。
成功したresume観測を、固定 `exit 0` の実終了codeや全回収の確認へ読み替えない。

試験は固定entryからSCM構成・元thread回収までと、合成message認証から実dispatch/issuer・target操作modelまでを分ける。
両者を一つの実native認証成功へ合成しない。
実message認証helperの正規起動資源は未構成で、SID policy付きnative factoryは取得前の拒否を維持する。
評価用固定値・配布artifact・実OS保護・実終了観測・実SSH session処理と製品入口は未接続または未検証である。
