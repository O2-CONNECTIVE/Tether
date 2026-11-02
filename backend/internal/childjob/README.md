# 保護所有者のWindows子/Job adapter

将来の保護supervisorが固定したartifact、既存primary token、標準pipe handleを渡すinternal module。
`Start`の入力をUI、argv、環境変数、保存stateから直接作らない。
tokenやACLの承認・取得、service登録、公開listener、認証情報や秘密鍵の生成は提供しない。
現在の製品入口は保護bootstrap未設定で拒否し、このmoduleの存在でgateを開かない。

## 起動と所有

- ローカルdriveの正規絶対exe pathとSHA-256を保護provisioningから固定する。
  artifactはread-onlyかつread sharingだけで開き、hashからresumeまでhandleを保持する。
  directory/reparse file、複数link、UNC/device/search path、余分なstream指定を拒否する。
- 既存の承認済primary tokenが必須で、typeを照合する。
  起動threadにimpersonation tokenがある場合や確認不能の場合は拒否する。
  token取得やprivilegeの有効化、別API/LocalSystemへのfallbackはしない。
- unnamed/non-inheritable Jobを作り、kill-on-last-handleだけを設定する。
  子は明示tokenでsuspended作成し、Job割当成功後だけresumeする。
- `STARTUPINFOEX/HANDLE_LIST`へ標準pipe 3本だけを列挙する。
  pipeはcallerが既に所有するinheritableなものだけを借用し、元handleを変更/閉鎖しない。
  Job、artifact、token、process/thread handleは子へ継承しない。
- Command/Bootstrap未指定のargvは固定imageと`--supervised-internal`だけ。
  environmentは空のUnicode block、cwdは固定image所在directory。
  親の秘密環境、任意引数、検索pathを引き継がない。

Go側の信頼済profileは、`NewFixedCommand(args, pinnedDependencies)`で固定した
`Spec.Command`を指定できる。これは認証proofではなく、UI等からの任意argvの入口にしない。
constructorとStartはsliceをcopyし、zero値とBootstrapとの併用を拒否する。
nil Commandの既存supervised/Bootstrap起動契約は変えない。
固定argvはWindows EscapeArg規則で引用し、最大64引数、32依存file、
command lineは末尾NULを除き32766 UTF-16文字までとする。
依存fileは正規絶対pathと非0 SHA-256を指定し、caseを無視した同pathとimage重複を拒否する。
異なるpathの同leaf名や同内容は許可する。この構文照合は物理file同一性の証明ではない。
各依存fileをimageと同じread-only/non-inheritable verifierで開き、hashを検査する。
write/delete sharingを許さない同じ依存handleをprocess退出かつJob active=0まで保持する。
module import、resume、leader退出だけでは解放しない。
部分検証の返却handleと閉鎖失敗の参照もChildに保持する。

`Start`成功はresumeまでの確認で、Go readyやSSH接続許可ではない。
起動contextはdeadline必須で、失敗時は独立した`CleanupTimeout`内で回収する。
回収を確認できない場合は`non-nil Child + ErrUnconfirmed`を返す。
呼出側はerrorだけでChildを捨てず、所有参照を保持して後続の回収を確認する。

`Wait(ctx)`はleader退出、Job active=0、所有handle回収を確認する。
非0退出は`ErrExit`であり、Go内Close応答を作らない。
`Close(ctx)`はleaderが退出済みでもJob全体を停止し、未割当/結果不明のprocessも停止する。
終了未確認では最後のJob handleとprocess参照を保持する。
同じOS handleを同時に二重閉鎖せず、PIDから他processを探索しない。

## 起動前検査のsource候補

`Prepare`は既存のsuspended作成とJob割当までで止まり、`Commit`が同じ保持process/thread/Jobを検査してから一度だけresumeする。
Windows候補はprocess/threadの対応、同じJobへの所属、固定image path、実process tokenのSID/AuthId/sessionと観測中のtoken変更を調べる。
SID/AuthId/sessionの一致だけで非admin権限を証明しない。
ここでのAuthIdは渡されたtokenのWindows logonの連続性だけで、operatorの認証資格や承認Recordではない。
正規guest logon/token生成、全group/privilege、承認Recordと接続認証、標準SSH機能への配線は含まない。
新APIの製品callerはなく、既存の信頼済helper用`Start`契約と製品gate閉鎖は維持する。

呼出側は承認済みprimary tokenを同じhandleで保持し、準備時のdeadline付きcontextを渡す。
そのcontextの取消し、`Revoke`または`Close`後は、新しいcall contextでも同じPreparedを再利用できない。
これはローカル準備物の単回性であり、正規承認Recordの発行・認証・再接続防止を代替しない。
`Revoke`は受付ラッチだけを閉じる。期限/切断を監視して`Close`まで実行する所有者は別途必要で、context取消しが自動停止を意味しない。
Commitと`Revoke`/`Close`の順序はatomic状態遷移で定め、失効ラッチが先ならresumeを拒否する。
context取消しは検査前後の観測で拒否するが、その取消し時点と開始CASの原子的な順序は保証しない。
最後の観測後に取消しと開始が競合し得るため、所有者は正規失効をラッチへ結び、同じ対象の`Close`まで確認する必要がある。
製品の期限・失効・停止要件は緩和せず、それを強制する正規所有者の製品配線は未実装のままである。
開始が先に成立した場合は同じtargetを回収し、観測できた取消し後の遅延結果を成功扱いしない。
これは物理的な実行開始との完全同時性や、OS hang/sleep中の停止保証ではない。
token属性の再読取りも、最後の観測からresumeまでの変更をOS側で原子的に封じるものではない。
同じ対象の保護DACL、承認済みtokenの保持と変更権限の管理は未実装の保護所有者が満たす必要がある。
開始/検査/閉鎖に失敗しても、部分取得したquery tokenを含む回収参照を保持する。
検査失敗の失効ラッチは所有permitを返す前に確定し、新しいcall contextでも後続Commitの再検査へ入らない。
`non-nil Prepared + error`を捨てず、独立した有効contextで`Close`を確認する。

## 検査と承認後開始の分離

`Inspect(ctx)`は同じ停止中targetを検査し、opaqueな`Inspection`をPrepared自身に保持して返す。resumeは行わない。
保護所有者が別途正規承認を得た後だけ`CommitInspected(ctx, observation)`を呼ぶ。receiptの同一性と元Preparedを照合し、同じ対象を再検査して一度だけ開始する。
新しいcontext、receiptのcopy、別target、失効/Close、期限切れから再利用できない。初回/再検査のquery handleは同じPreparedが回収する。
旧`Commit(ctx)`は既存generic契約用であり、承認bridgeから呼ばない。inspectionと旧Commitの開始経路を混在させない。

Serviceのprivate bridgeは認証済み確認要求と独立した同対象nonadmin証拠を必要とし、検査だけから非admin属性を生成しない。
source/owner/proof/retained grantが同じtargetを保持し、元の実issuerが発行したchallengeだけをLaunchで単回消費する。
停止はtarget受付を閉じ、canonical issuerを失効させてからworkerを取り消し、実commit返却をjoinして同対象をCloseする。
ローカルのCloseをwhole-workload成功にはせず、既存SCM回収順を維持する。
この経路はsource/fake限定、製品constructor/dispatcherの起動呼出しはない。方式Cは[設計採用](../../../docs/adr/0003-owned-guest-workload.md)済み。正規guest tokenと保護targetを結ぶsourceは開発中で、実Windowsの非admin保護・停止・実SSHは未実証。

## 保持tokenの限定観測

`NewGuestTokenExpectation`は候補network profileの期待値を整形するだけで、保護された設定、guest作成来歴、正規ログオン、issuer承認を証明しない。
未知group、管理者group、3種類の保守的許可集合以外のprivilegeを期待値へ指定しても拒否する。runtime profileの採用ではない。
`ObserveGuestToken`は同じPrepared/Inspectionが保持した実targetと元の借用primary tokenをOSから独立に照会する。
TokenUser/Owner/PrimaryGroup、AuthenticationId/session、TokenId/ModifiedId、group全体、present privilege全体、elevation/type/integrity/UIAccess/restricting SIDs/AppContainerを照合する。
`TokenRestrictedSids`の全SIDを有限bufferから検査し、一つでも存在すれば拒否する。取得失敗・不完全bufferも拒否する。
これは過去にfilter済みかを表す`TokenHasRestrictions`とは非同値で、filter履歴は不明のままである。
最初の候補に加えた「過去にfilter済みなら拒否」から限定観測の条件を狭めた変更であり、同等なAPI置換とは扱わない。
その履歴を正規token来歴や完全な非admin証拠へ読み替えず、未知・危険なgroup/privilegeの拒否は維持する。
有効なAdministrators membershipだけで判定せず、disabled/deny-only管理者groupや、disabledでも存在する危険privilegeを拒否する。
観測を最初の同targetへ固定し、開始直前に再照会してTokenId/ModifiedId等が変われば再基準化せず拒否する。部分取得query handleは元Preparedが保持・回収し、借用primary tokenを閉じない。
opaqueな`GuestTokenObservation`は別target/copy/失効後へ移せず、元のadmission lifetimeを新しいcall contextで延長できない。
issuer/confirmationとのbindingは既存Serviceの固定ownerが引き続き強制し、この観測だけからgrantや`SeparatedNonAdminGuest`を生成しない。

検証はsource/fakeとWindows2arch compileを基本とし、Windows CIの追加native試験はテストprocess自身のread-only queryだけである。
これは実guest logon、保護DACL、実sshdが生成するtoken/shell、期限内の物理停止を検証したものではない。
従来の未起動guestはdisabled/NoPasswordのままで、[過去の生成元の境界](../../../docs/guest-fixed-source.md#実guest-tokenとresume前gateの所在)を保持する。
方式Cの新経路は別runtime設定とBATCH期待値を明示し、正規作成receiptからの実logonを同ownerへ束縛する。NETWORK観測の代用品や旧未起動経路の迂回には使わない。製品callerは有効化せず、実機のOS変更・資格情報生成は実施していない。gate閉鎖を維持する。
最後の観測からresumeまでをOS側で原子的に封じるものではなく、token変更権限と対象DACLを守る正規ownerの成立は別の未達である。

- [GetTokenInformation](https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-gettokeninformation)
- [TOKEN_INFORMATION_CLASS](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ne-winnt-token_information_class)
- [TOKEN_GROUPS](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-token_groups)
- [TOKEN_PRIVILEGES](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-token_privileges)
- [LogonUserWの要件](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-logonuserw)

## 残る前提と検証境界

Jobは権限隔離ではない。
入力tokenが誰の承認済tokenか、service/guestの分離、保護rootと祖先directory/ACLの真正性は別の未完成provisioningの必須責務。
hashとshare modeだけでは、保護root外でのpath差替えや配布元の真正性を証明しない。
別session tokenでのhandle継承や必要privilegeが成立しなければ失敗し、設定を緩めない。
保護bootstrap transportとConfig/鍵の渡し方は未決で、新しいwireや鍵serializationは作らない。

試験はOS seamのin-memory応答とnativeが使う起動parameter組立の純粋試験だけ。
native WinAPI本体はcross compileの対象とし、実process、service、account、ACL、鍵、listener、SSHを試験で作らない。
通常CIの既存fixture成功も、P2専用再現、保護主体の成立、Job最後handleの実挙動、hang/sleep中の期限執行や物理停止の証明へ読み替えない。

## 公式APIの根拠

- [CreateProcessAsUserW](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-createprocessasuserw): 明示primary token、固定application、起動直後の状態と初期化完了は別。
- [UpdateProcThreadAttribute](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute): inherit対象handleを明示し、inheritable handleとTRUEの継承flagを要求。
- [Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects): Job所属とkill-on-last-handle、子孫の管理。最後のhandle閉鎖と監督hangは別。
- [CreateFileW](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew): share modeは保有handleの存続中に作用し、非共有write/deleteと既存openの競合を拒否する。
- [GetCurrentThreadToken](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-getcurrentthreadtoken): thread impersonation tokenのpseudo handle。取得handleの閉鎖は不要。

## Leader退出だけの観測

`Child.WaitExit(ctx)` と `Prepared.WaitExit(ctx)` は、同じ保持processの退出codeだけを返す。
nil errorの時だけuint32 codeが有効で、非0でもこの観測APIはErrExitを返さない。
取得失敗はErrUnconfirmedと原本errorを保持し、ctx終了も別のerrorとして保持する。
内部errorを未選別のままUIやwireへ転送しない。

このAPIはJob active=0、pipe EOF、handle回収、自然終了、command実行の成功を証明しない。
suspended targetの外部停止による退出もあり得るため、退出観測から承認済実行を推定しない。
Revoke後も同じ所有物の観測だけは可能で、admission、検査receipt、grantを復活させない。
有効な有限ctxを先に要求し、cacheがあっても取消済・無期限ctxでは成功を返さない。
OSの成功観測とctx終了が競合した場合、観測はcacheへ保持するが当該callは失敗にする。

既存Wait/Closeと同じowner gateでOS待機とcacheを共有する。再観測のためのPID検索、
handle再取得、kill、JobEmpty待機、close、自動retryは行わない。
Close後は保持済みの観測事実だけを返し、processも観測も無ければ未知のまま拒否する。
同時Wait/Closeとのgate待機にもctx予算が適用される。OS待機がctxに従う前提は既存と同じで、
OS hang中の強制停止を追加保証しない。

既存Waitはleader退出＋Job空＋handle回収、非0ならErrExitという契約を維持する。
この追加は将来のWindows agent adapterに必要な観測seamであり、
guest shell stdin/output adapter、FullPolicyDigest本人承認producer、service/session接続は未実装。
固定exit0評価経路、SYSTEM helper argv、token取得・設定は変更しない。
memory試験とWindows向けcompileは実Windows実行・権限分離・回収の証拠ではない。
