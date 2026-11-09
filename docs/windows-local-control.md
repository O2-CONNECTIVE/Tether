# Windowsローカル制御IPCの認可境界候補

[Issue #25](https://github.com/O2-CONNECTIVE/Tether/issues/25) / [共通契約](adr/0001-control-contract-candidate.md) / [安全要件](security.md)

`backend/internal/localcontrol` は設定検査・kernel token照合・既存dispatcherへの権限境界です。
public Openは常に拒否し、configや環境変数の解除flagはありません。
private側にはoverlapped accept/read/write・取消・共通codec/stateの接続を実装しています。
public gateからは呼ばれず、実native pipe/APIによる動作は未検証です。
pipe/service/account/password/ACLを実際に作成する実行、実SSH、OS設定変更は行っていません。

## 2026-10-28版の脅威範囲

初期版は、遠隔作業を行う非管理者guestから監督serviceと停止処理を保護する範囲を対象にします。
hostoperatorや管理者が既に侵害されている場合、管理者guestは対象外です。
別保護service、guest SID、immutable grant、hard TTL、許可鍵と固定宛先、不可逆revokeの要件を維持します。
正規hostoperatorの承認、guestからのIPC拒否、ACLとpeer identity照合は必要です。
毎回の別admin sign-inは追加の必須条件にしません。
本書と純粋試験の成功は、そのOS保護や従来の全受入条件が達成された証拠ではありません。

## 明示的な設定とpipe plan

Configはoperator/service/guestの固有SID、generation、既存Identity、channel nonceを必須にします。
SIDの未設定・同値・broad well-known SID・非正規表現を拒否します。
初期の対応subsetは通常account SID（S-1-5-21配下）と固有virtual service SID（S-1-5-80の5 subauthority）です。
ServiceSIDはprocess/client tokenの**TokenUserと一致する主アカウントSID**です。service groupに含まれるだけでは許可しません。
未知のservice identity、LocalSystem、共有service SIDへのfallbackはありません。
設定したguestが実際に非管理者か、service accountが正しく分離されているかは実機受入で別に確認します。

pipe名は固定local namespaceとgeneration/nonceだけから内部生成し、UIからpath/commandを受け取りません。
hash/nonceを相手の認証やgrantの正当性とは扱いません。
protected DACLはguestを明示deny、serviceに個別FILE_ALL_ACCESS mask、operatorには0x00100183だけを許可します。
operatorのmaskはread/write data、read/write attributes、synchronizeで、pipe-instance作成権を含みません。
clientも同じ個別maskで開く必要があり、GENERIC_READ/GENERIC_WRITEでは開けません。
GENERIC_WRITEがFILE_CREATE_PIPE_INSTANCEと同じbitを含むため、generic rightsへ広げない設計です。[Microsoftの権限仕様](https://learn.microsoft.com/en-us/windows/win32/ipc/named-pipe-security-and-access-rights)

native create adapterはmessage/read-message/local-only、first-instance、overlapped、最大1 instance、非継承handleを指定します。
既存名との衝突から別instanceや名前へfallbackしません。
16KiBは要求buffer値であり、kernelの実確保量や総メモリの上限ではありません。[CreateNamedPipeW](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-createnamedpipew)
現段階ではcreate adapterもpublic gateから呼ばれず、実DACLやclient拒否は実行未検証です。

## token確認とdispatch

Endpointはfactoryが所有したhandleだけを保持し、外部のhandleをattachする入口はありません。
readから認証・Revert・dispatchまでと、closeは同じgateで直列化します。
message-modeの1 messageを1個のJSONL frameとして固定16KiB bufferへreadし、そのread完了後に認証します。
分割messageを結合せず、超過（ERROR_MORE_DATA）・複数frame・LF欠落・不正UTF-8は拒否します。
clientは1 frameを1回のWriteFileで送る必要があります。
read/token/codec/writeの間に別readや接続交換を挟まない構造ですが、read-token相関が実Windowsで成立したという実証ではありません。
GetNamedPipeInfoはserver/messageの防御検査だけで、作成時ACL・remote拒否・instance同一性の証明には使いません。

process TokenUserが明示ServiceSIDと一致することを確認します。これをclient tokenのfallbackには使いません。
OS threadを固定し、既存thread tokenがあれば処理を止めます。
pipe作成前にも同じthread検査を行い、clientの実効contextで作成しません。[Access Tokens](https://learn.microsoft.com/en-us/windows/win32/secauthz/access-tokens)
ImpersonateNamedPipeClientの成功後だけ、同じthreadのOpenThreadToken(TOKEN_QUERY, OpenAsSelf=true)を使います。
client TokenUser・TokenType・impersonation levelを照合し、SID自己申告、PID、group名を認可根拠にしません。
Identification以上のlevelで本人確認だけを行い、clientのcontextで保護資源を操作しません。[ImpersonateNamedPipeClient](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-impersonatenamedpipeclient)、[OpenThreadToken](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-openthreadtoken)、[level定義](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ne-winnt-security_impersonation_level)

Impersonate失敗時はtoken照合や要求実行へ進みません。
token handleを閉じ、Revert成功を確認してからthread固定を解除し、server側のdispatcherを呼びます。
既存thread token・復元不能・token/pipe handle終了不明ではpoisonedにし、nativeでは固定exit codeでprocessを終了します。
Revert失敗のthreadをpoolへ戻さず、mockでfatalがreturnしても後続要求を拒否します。[RevertToSelf](https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-reverttoself)

操作は同じgeneration/Identityのsnapshotとstopだけで、start/renew/任意PID/path/commandは提供しません。
旧Command/Next経路を除き、Endpoint.Exchangeを共通rescue-host-control/1のDecodeとAdapterへ接続します。
NewNativeには保護ownerが事前保持するWireContextを渡し、Configとのgeneration/Identity/nonce一致を検査します。
helloと、その後の同じgeneration/Identityのsnapshot/stopだけを許可します。
共通Adapterがhello binding、sequence、request IDと応答相関を検査します。
channel hash/nonceをSID認証の代わりにはしません。
認可拒否と、認可済み要求の完了未確認を固定errorで区別し、nativeの生errorやSIDを公開診断へ返しません。
Endpoint.CloseはpipeのI/O完了後に所有Adapter.Closeも呼び、同じSessionの失効・Go側終了確認へ進みます。
失敗はErrUnconfirmedとして保持し、後続Closeで成功へ変換しません。所有process回収の成功とは扱いません。

## 証跡と残る境界

TDDは明示設定のplan、kernel由来operatorの限定stop、認可済みstop失敗の完了未確認をRED→GREENで確認します。
factoryの既存thread tokenも負例で拒否前のFAILを観測し、作成前検査でGREENを確認します。
その後のSID/世代/操作範囲、API失敗、復元順序、close競合はin-memory回帰です。
Windows専用試験はSDK定数とfake APIでのoverlapped制御を検査し、native APIを呼びません。
Windows amd64 crosscompileはABI型のコンパイル確認であり、native実動作や実guestの拒否を証明しません。

accept/read/cancel/writeと共通codecのsource接続は追加済みですが、実ACL/peer確認、SCM/guest分離設定、独立失効、hard deadline執行、process回収は未接続・未実証です。
APIの非returnやOS停止に対する時間保証も未実証です。
一時runner pipeを使う実API試験が必要なら、接続数・寿命・cleanup・対象APIを先に別報告します。新guest/password作成や既存OS security設定変更は行いません。
P2専用runtime未実証と拒否された専用/代替再現の停止、double-close NOT_MET、Windows背圧UNVERIFIED、製品gate閉鎖、44条件と配布/本番の別判定を維持します。

## OVERLAPPED所有と有限終了

connect/read/writeはそれぞれmanual-reset・非継承eventと固有OVERLAPPEDを使います。
Windows APIが保持するOVERLAPPEDとbufferをruntime.Pinnerで固定し、I/Oの完了を確認してから解放します。
ERROR_PIPE_CONNECTEDはaccept開始前の接続競合として扱い、架空の完了通知を待ちません。
保留中は短いevent waitでcontextを確認し、取消時には対象OVERLAPPEDだけへCancelIoExを呼びます。
CancelIoEx成功やERROR_NOT_FOUNDは完了証拠ではないため、event通知とGetOverlappedResultを確認します。
取消要求後に通常完了した競合も、取消済み要求の成功にはしません。
[公式overlapped仕様](https://learn.microsoft.com/en-us/windows/win32/ipc/synchronous-and-overlapped-input-and-output)、[CancelIoEx](https://learn.microsoft.com/en-us/windows/win32/api/ioapiset/nf-ioapiset-cancelioex)、[ConnectNamedPipe](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-connectnamedpipe)

取消完了待ちは固定1秒を上限とし、完了不明・wait失敗・event終了不明ならnative fatalでprocessを終了します。
これは処理性能の合否閾値や、OS API自体の非returnを強制中断する保証ではありません。
Closeは先にoperation contextを取消し、read/auth/revert/dispatch/writeのgateを取得してからpipeを閉じます。
Closeの呼出しは不可逆であり、短いClose contextが満了しても要求や接続を再開しません。
既存token-scope競合試験も「closeがtoken scopeへ割り込まない」に加えて、取消後にはdispatchしないという期待へ強化しました。

同時接続数は1、factoryの作成も1回だけで、disconnect/reconnectのloopはありません。
read、認証、codec/state、writeのどこかが失敗・取消したら所有handleを閉じ、nonresumeとします。
応答writeは全byte数を確認し、short writeを成功にしません。
pipe終了後はfreshな固定1秒contextでAdapter.Closeを1回実行します。
これはGo内の終了確認だけで、process回収は別ownerの責務です。
このcontextを無視してreturnしない任意handlerを強制中断する仕組みではありません。

## この変更の試験境界

最初の所有Endpointのread/token/codec/response試験は、Exchange未実装によるcompile REDを観測してからGREENにしました。
不正frame・guest・generation/start拒否・共通Stateのhello/sequence・read中close・Windows取消完了競合は追加の回帰検査です。
Windows専用試験はAPI関数を全てfake化して同じoverlapped制御処理を呼び、event/pipe/ACLの実作成を行いません。
共通Stateとの接続試験も純粋handlerを使い、鍵・listener・実SSH Sessionを生成しません。
実Coordinatorとのstate/dispatch適合は既存control package試験、NewNativeの実Adapter結合はsource/crosscompileの確認と区別します。
実named-pipe試験は今回実行していません。実行時には実ServiceSID、実効権限、正規clientの個別access mask、寿命・cleanupの確認が別途必要です。
既存OS設定・アカウント・ACL・鍵を変更せず、製品の起動gateを開く前に統合ownerの認証/manifest決定と実Windows受入が必要です。

ReadFile/WriteFileはasync時の転送countをNULLで渡し、GetOverlappedResultからだけcountを取得します。
このためcountを呼出直後に参照するx/sysのconvenience wrapperは使わず、SystemDLLの固定APIを呼びます。
[ReadFile](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-readfile)、[WriteFile](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-writefile)、[GetOverlappedResult](https://learn.microsoft.com/en-us/windows/win32/api/ioapiset/nf-ioapiset-getoverlappedresult)

所有Adapter cleanupの欠落は独立レビューで発見し、read失敗時にfresh deadline付きCloseが必要な試験の実行RED→最小修正→GREENを確認しました。

認証に失敗したframeを要求としてHandle/dispatchへ渡すことはありません。
一方、その所有channelは異常終了となり、failclosed cleanupで同じSessionを失効させます。
「未認証peerがSessionに一切影響できない」またはguestからのDoS耐性を保証するものではありません。

Closeは独立した所有mutexでactive operationのcancelを同期的に呼び、非同期callbackの実行待ちに依存しません。
`TestCloseCancelsActiveContextBeforeReturning`で旧実装の実行REDを観測し、同期取消へ変更してGREENを確認します。
取消前に既に受理・実行されたstopを巻き戻す保証はありません。
