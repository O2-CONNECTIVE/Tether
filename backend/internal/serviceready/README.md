# sessionなしのservice準備

`serviceready`は、固定serviceの自主体・配置・OS objectの保護と、service所有listenerのkernel準備を保持する。
operator命令、operatorに対するserver認証、承認codec、session、grant、SSH接続を提供しない。
要求単位の読取不能な`InspectionLease`は[固定sourceと借用](../../../docs/guest-fixed-source.md)を参照する。
既存のPlaneはlistenerが接続済みでもpayloadを一切読み書きせず、命令受理や認可の成功と呼ばない。

`NewNative(Config)`は静的検証とdeep copyだけを行う。
Profile、既存PrimaryToken、Endpoint、ServiceCommandLine、service/processの明示SecurityPolicy、CleanupTimeoutが必須である。
ProfileのSID、catalog、両imageの祖先・ACLを複製し、入力の後からの変更を採用しない。
Endpointは`\\.\pipe\rescue-service-<ServiceName>`に限定する。
名前やepochは認証証拠ではない。
ServiceCommandLineはSupervisorImage.Pathのquoted exact、またはspaceのないbare exactだけを許し、追加argsや曖昧な解釈を採らない。
別のsupervisor executableを想定するsource候補であり、実service identityや登録方式の採用ではない。

`CheckServiceCaller(ctx)`は保持したauth factoryの元SCM caller guardへ委譲する、拒否専用wrapperである。
proofやowned refsを作らず、元callerのimpersonation等をworker作成前に拒否するためだけに使う。
workerの完全なPrepare、固定image/hash/ACL/token確認、operator server認証を代替しない。

`Prepare(ctx)`はcaller所有の固定OS threadでauth.PrepareServiceを行う。
現在のservice principal、借用primary token、固定supervisor imageを確認し、SCMがRUNNINGであるとは仮定しない。
local SCMの固定serviceをREAD_CONTROL/QUERY_CONFIG/QUERY_STATUSだけで開き、現在processをREAD_CONTROL/QUERY_LIMITED_INFORMATION/SYNCHRONIZEだけで保持する。
held serviceのraw binarypathを明示ServiceCommandLineと照合し、own-process type0x10だけを許す。
SCMの実service account名を入力profileへ採用しない。
QueryServiceConfigの情報は次回起動の設定でもあり、現在imageの証明は独立したauth preflightが担う。

service/processのowner・protected DACLを明示expected policyへexact照合する。
信頼主体はUser/Service/Operator/SYSTEM/Administratorsで、Guestを常に除外する。
host operator/adminの既侵害は今回の脅威scope外であり、追加の別admin承認を実装しない。
未知主体・GuestのALLOWはserviceで0x2008d、processでREAD_CONTROL|SYNCHRONIZE|QUERY_INFORMATION|QUERY_LIMITED_INFORMATION（0x121400）を上限とする。
processに未知主体・GuestのQUERY権がある構成は、後述する同じprimary token objectの独立検査をPrepareで通るまで受理しない。
genericとMAXIMUM_ALLOWEDの例外は作らない。
PROCESS_QUERY_LIMITED_INFORMATIONはOpenProcessTokenのprocess側の前提であり、tokenに得られるアクセス権はtoken object自身のDACLで別に判定される。
query許可だけでtoken奪取が成立したとは扱わず、process側のqueryとtoken側の使用・複製・変更権限を別々に検査する。
trusted User/Service/Operator/SYSTEM/Administratorsのquery権限と、native照会自身のdesiredAccessは維持する。
既取得・継承・漏出したprocess/token handleが無いことは別の前提であり、このACL検査で不在を証明しない。
STOP、CHANGE_CONFIG、WRITE_DAC/OWNER、DELETE、VM_READ/WRITE/OPERATION、DUP_HANDLE、TERMINATE等を未知主体へ許さず、DENYで危険なALLOWを相殺しない。
未知ACE、継承flags、未知mask、NULL DACL、非protected policyを拒否する。

processのQUERY公開時は、既に保持する同じprocessからTOKEN_QUERY|READ_CONTROLだけでprimary tokenを開き、同じprocess ownerに保持する。
新しいtokenの生成・複製・impersonationや強い起動handleの取得は行わない。
token objectそのもののowner/DACLを取得し、TokenDefaultDaclとは区別する。
未知主体・GuestはTOKEN_QUERY|READ_CONTROL（0x20008）までとし、ASSIGN_PRIMARY、DUPLICATE、IMPERSONATE、ADJUST系、WRITE_DAC/OWNER、DELETE、QUERY_SOURCEを拒否する。
SYNCHRONIZE、generic、MAXIMUM_ALLOWED、未知ACE・継承flag、NULL DACLも拒否し、危険ALLOWをDENYで相殺しない。
保持tokenと借用PrimaryToken、現在processのprimary tokenのTokenId/ModifiedId一致、照会前後の不変を確認する。
初回のtoken owner/DACL・Protected値・TokenId/ModifiedIdを複製して固定し、再検査で変化したら不可逆に失効する。
token objectのProtected値には新たなtrue必須条件を加えず、実値を固定して変更を拒否する。既存service/processのProtected必須は維持する。
token取得・検査の途中失敗やclose失敗でも、同じprocess ownerに部分handleを残して回収を再試行する。
これらは現在の保持対象の検査であり、生成直後からの全履歴や侵害済み別serviceからの完全隔離を証明しない。

`PreparePreflight(ctx)`は、`Prepare`の同じquery-only prefixを共有し、auth scopeとheld service/process refsをopaqueな`Preflight`として保持する。
元の固定thread、no impersonation、profileの自主体・固定image、service binarypath・type・DACL、current processのalive・PID・creation・DACLを既存と同じ規則で検査する。
借用primary tokenはquery-onlyでよく、ここで強いtoken handleを開いたり、既存query handleを強いlaunch handleへ昇格させたりしない。
listenerの作成・connect・RUNNING待機・`ConfirmRunning`は行わない。
既存query検査が許すstateはSTART_PENDING/RUNNINGだけで、START_PENDINGのSCM PIDを証拠にせず、既にRUNNINGならcurrent PIDの一致条件を維持する。
Preflightの成功をlistener/SCM readiness、operator/server認証、approvalの証明と呼ばない。
`Prepare`はこの同じprefixの成功後にだけlistener準備へ進む。

`Preflight.Recheck(ctx)`と`Binding(ctx)`は同じscopeのquery証拠を再照合する。
`ProcessBinding{PID, Creation}`は保持processの相関値であり、値の構築や複製だけでは認証やtokenの取得元を証明しない。
取得元の証明はopaque Preflightに残し、strong-token supplierは強handleを開く前後に同じscopeを再検査する必要がある。
processのQUERY公開時だけtoken objectを上記の規則で検査する。TokenDefaultDaclや強handleのdesired accessは検査しないため、strong-token supplierの別検査を省略できない。
ctx/admission失敗、query不一致、元thread・主体の変化で受理をstickyに失効させ、並行処理も成功したBindingを返さない。
失敗時のBindingはzero値であり、部分取得のPreflightも同じpointerを保持してCloseする。
PreflightのCloseはlistenerを持たない同じ有限cleanup経路を使う。

`PrepareServiceWitness(ctx)`もquery-only prefixを保持するが、SCM RUNNINGの確認を独立して提供する。
`ConfirmServiceRunning(ctx)`はSTART_PENDINGを有限期限内で待ち、held serviceの実RUNNINGとcurrent PIDの一致を確認する。
`RecheckService(ctx)`は確認済みの同じservice/process/auth scopeを再照合し、元thread・主体・image・設定・ACL・process creationの変化を拒否する。
実deadlineを入口・query後・成功直前に検査し、canonical witnessのctx/admission/query失敗はstickyに失効する。
value copyを拒否し、copyから元witnessの失効や回収を行わない。部分取得や回収不明ではcanonical pointerを保持して`CloseService(ctx)`を再試行する。
method名を分離しているため、既存のlistener込み`Plane`やservicehostの`servicePlane`の代わりにはならない。
listenerを作成・保持せず、operator認証、sole-reader移譲、応答書込み、次要求の受入可能性を証明しない。
現在のServiceへこのwitnessだけを接続してreadinessを成功扱いする変更は含まない。

sole-reader移譲中はhelperが読取端点を所有し、SCMのservice再照会だけで端点全体を再検査したとは扱わない。
次要求の受理には、同じ交換の認証済み読取消費・応答書込みの実完了・worker終了・端点閉鎖の確認と、fresh service再照会・同一issuer/Ownerの有効性が必要である。
現在は端点のwrite/close所有APIが未接続であり、状態boolや任意callbackでこれらの確認を生成しない。
既存のlistener込み`Prepare`/`ConfirmRunning`/`Recheck`契約は維持する。

`PrepareEndpoint(ctx)`は、当workerのquery-only prefixと1回の物理pipeを所有する独立APIである。
`AwaitConnected`はconnectの実終端とOVERLAPPED/event解放を確認してからduplexを再照会する。
`Split`は非継承read `0x120089`とwriter `FILE_WRITE_DATA (0x2)`を複製し、実権限照合と元duplex閉鎖後にだけ`SoleInput`を返す。
writerへread/query権を足して既存listener検査を成立させず、writerを旧`inspectListener`へ渡さない。
`SoleInput.Move`は同thread・単回の破壊的所有移動であり、借用getterでも認証でもない。
返却handleはerror併返時も受取側が直ちに保持・回収する。移動後はEndpointが同じreaderを閉じない。
`WriteResponse`はMove完了後の単回・16 KiB以内の物理書込みだけを扱い、全byte転送・実終端・解放が揃ってからopaqueな`WriteReceipt`を返す。
これはclient受領証明ではなく、partial writeを残りの再送で成功へ変えない。
`CloseEndpoint`は当Endpointのoperation/query参照/残存handleだけを有限回収し、unknownなら同じ所有者を保持する。
回収後の`EndpointClosed`は移動先Batch/helperのreader閉鎖・認証・`Collect`/`Consume`を証明しない。
`MatchesCompletion`は同じEndpointの実writeとclose receiptに限定し、失敗後の再回収でclosedとなっても正常turnoverへ昇格しない。
Ownerの原子的admission、same-profile ServiceScope、正規provider、Batch/Serviceとの結合は未配線で、このAPI自体に登録済み・認可済みの保証はない。
各物理取得の前後で実期限と保持queryを再検査するが、inline OS API自体の非returnは解決していない。
memory試験とWindows両arch compileは、実pipe/権限/実機保護の動作確認ではない。製品gate閉鎖を維持する。

listenerは非継承、DUPLEX、OVERLAPPED、FIRST_INSTANCE、REJECT_REMOTE_CLIENTS、MESSAGE/READMODE_MESSAGE/WAIT、単一instanceで作る。
ownerはProfileのProcessUserSID、protected DACLはGuest DENY0x1f01ff、Service ALLOW0x1f01ff、Operator ALLOW0x120183の順序とする。
OperatorにFILE_CREATE_PIPE_INSTANCE0x4やgeneric WRITEを与えない。
GetFileType、server/message mode、buffer、instance数、GetHandleInformation、kernel server PID、owner/DACLをconnect開始前と再検査時に照合する。
creation identityは未解放のCreateNamedPipe返却handleとkernel server PIDで保持し、pathを開き直したりFileIDを捏造したりしない。
16 KiBはbufferの指定とactual照合であり、Windows message長の強制ではない。
将来のoperator protocolには独立したpeer/server認証とframe上限が必要であり、今回は未実装である。

`ConfirmRunning(ctx)`はstatus channelへ送った値をkernel ACKとせず、held serviceの実SCM RUNNINGとPID=currentPIDを確認する。
START_PENDINGはcallerの有限deadline内だけ待ち、その間のSCM PIDを一致根拠に使わない。
他state、停止、PID・process creation・ACL・binarypath・listenerの変化、query失敗を拒否する。
RUNNING確認後にSTART_PENDINGへ戻ることも拒否する。
`Recheck`または`ConfirmRunning`の失敗はctx/admissionを含めて同じPlaneの受理権を不可逆に失効し、並行して進行中の検査も復活成功を返さない。
上位層はこの失効をissuer閉鎖へ合流させる。

`Close(ctx)`はcaller deadlineとCleanupTimeoutの短い方でGo mutex admissionと回収を制限する。
CancelIoEx成功やNOT_FOUND、handle closeをcompletionとせず、実GetOverlappedResultの既知の終端を確認する。
未完了connectのOVERLAPPEDをpinし、event/listener handleも保持する。
Planeを初回利用後にコピーせず、同じpointerを保持する。
op mutexもTryLockで取得し、別のquery/pollのGo mutexを無期限に待たない。
query refsとauth scopeはconnectの回収とは独立に試み、部分取得や不明状態では同じPlaneを保持する。
auth scopeのexact `ErrFatal`単独だけは同scopeの全refs回収済み契約に従って参照を解放し、Fatal自体はstickyに保持する。
`Fatal+Unconfirmed`やunknown errorとのjoinをこの例外へ広げず、同じscopeを保持して再回収する。
ErrUnconfirmedならfresh finite contextで同じCloseを再試行し、別operationで回収済み扱いにしない。
Fatalはctx不正、既取消、foreign thread、回収後も通常拒否へ縮小しない。
Fatal時のOS thread退役は上位entryの責務である。
worker、fatal exit、panic、Close-as-completionを追加しない。
inline native API自体の非returnやOS強制終了は解決・実証していない。

試験はprivate kernelのfakeと合成policyだけを使う。
公開factoryはnative/auth factoryへ固定し、valid profileでPrepareやcaller guardの実OS照会を行う試験はない。
従来の10組とPreflight/query-only prefix・確認済みFatal回収の追加2組の同入力red/greenに加え、ACL mask、kernel証拠変化、ctx/admission失効、部分取得、独立回収、Fatal保持を回帰確認する。
Windows両archのcrosscompileは実DLL、実pipe、SCMの動作試験ではない。
実service/account/ACL変更、鍵生成、SSH/listener実行、P2再現、製品有効化を行っていない。

- [Service security and access rights](https://learn.microsoft.com/en-us/windows/win32/services/service-security-and-access-rights)
- [Process security and access rights](https://learn.microsoft.com/en-us/windows/win32/procthread/process-security-and-access-rights)
- [QueryServiceConfig](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/nf-winsvc-queryserviceconfigw)
- [QueryServiceStatusEx](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/nf-winsvc-queryservicestatusex)
- [Named pipe security](https://learn.microsoft.com/en-us/windows/win32/ipc/named-pipe-security-and-access-rights)
- [CreateNamedPipe](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-createnamedpipea)
- [GetNamedPipeHandleState](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-getnamedpipehandlestatew)
- [GetNamedPipeInfo](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-getnamedpipeinfo)
- [GetNamedPipeServerProcessId](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-getnamedpipeserverprocessid)
- [GetHandleInformation](https://learn.microsoft.com/en-us/windows/win32/api/handleapi/nf-handleapi-gethandleinformation)

- [OpenProcessToken](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-openprocesstoken)
- [Access-token object rights](https://learn.microsoft.com/en-us/windows/win32/secauthz/access-rights-for-access-token-objects)

現candidateはservice/processの`SE_DACL_PROTECTED`も必須とする。
このflagは継承ACEによるDACL変更を抑える条件であり、単独でguestのstop/tamper拒否を証明しない。
中心条件はtrusted owner、expected ACL完全一致、危険なALLOWの排除である。
対象Windowsのdefault値、process/service objectでの必要性・実現性は未実証で、この変更は追加DACL設定を実施/承認したものではない。
条件が満たされなければ拒否し、実証なしにflag条件を緩めたりOS設定を変更してgreenにしたりしない。
このcandidateの採否と必要な実設定は、対象環境での根拠と別のaction-time承認に残す。
