# 一時guestのWindows境界と清掃journal

[一時guestのcore](temporary-guest-account.md)に接続するsourceの境界を定める。
accountの変更は[SID指定清掃候補](temporary-guest-sid-mutations.md)をsourceに準備するが、保護provisioningと製品配線は未設定で、`NewNative`は未設定拒否を維持する。
OperatorSID／AuthenticationIdの認証根は変更しない。
実Windowsのaccount、資格情報、ACL、service、SSHは今回操作しない。

## journalの限定方式

journalは、保護provisioningが事前に用意した固定ledgerを、保持directoryから相対openする。
runtimeはmissing、空、別installation、破損したledgerを新規の空状態へ作り直さない。
`NtCreateFile`のRootDirectory、固定basename、FILE_OPEN、非継承、shareなしを使い、保持rootとledgerのvolume／file ID、reparse、型、link、owner／DACL、実行主体を照合する。
同じhandleと排他を、sourceの実returnと回収確認まで保持する。
journalの全寿命は、正規rootを保護取得した同じcaller-owned locked OS thread上に置く。
SCM等の別threadで得たscopeを、account workerの健全なscopeとして代用しない。
任意UI pathや環境変数でrootを選ばせない。

既存ledgerへbounded recordを追記し、全長writeと`FlushFileBuffers`成功後にだけcoreの保存receiptを返す。
recordは版、installation、sequence、前recordとのhash連鎖、全binding、stageを照合する。
checksumとhash連鎖は破損検出であり、認証やrollback防止の秘密ではない。
同じbindingの許可された単調遷移だけを保存し、同じrecordの再保存は冪等とする。
新intentは旧intentの終端確認後に限定し、既使用intent／SIDの再利用を拒否する。
recordの32byte sessionEpochとgenerationの組をjournal内の識別値として扱い、同epochでは以前のgeneration以下を新intentに使わせない。
この規則は当該epochをOSのboot IDや既存control/1の項目と認定するものではなく、認可入力の正規な発行元は別途必要である。

作成意図を予約する前に、そのintentのSID保存・準備・削除意図・終端までに必要な枠を確保する。
枠不足、部分write、flush不明、破損した末尾では新規作成へ進まない。
末尾を切り捨てて以前のstageへ戻したり、同名の別ledgerを開いて再開したりしない。
取消は同期I/Oの実returnの代用品にならず、非return中も所有を保持する。
予期しないpendingでもnative out-parameterやbufferをpinして保持し、回収ackを拒否する。
このpendingの実completion確認・join経路は未実装で、同期flagsだけを非pendingの実証にしない。
Serviceの許可失効はこのworker待ちと独立して行う必要があり、journalだけで監督hangやOS sleepのTTLを証明しない。

runtimeは既存file内を更新するだけで、新規fileのdirectory entry発行やatomic renameにdurabilityを依存させない。
初期headerの作成・durability・rootとfile IDの発行は別のprovisioning作業である。
`ReplaceFile`のWRITE_THROUGHフラグを保証として扱わない。
保護root／ACLの現況一致や`LockFileEx`では、保護前に取得されたwrite handleやmappingの不存在を証明できない。
それらがない状態で作成・保持・引渡ししたという保護producerの契約が未設定なら、製品へ接続しない。
完全に整形式の旧record境界まで戻すbackup復元・disk rollbackはhash連鎖だけでは検出できない。
保護storageと復元運用が前提であり、host operator／admin既侵害は初期保護範囲外である。

## 外側の所有者と接続条件

coreのaccount清掃完了と、native sourceの回収完了は別である。
将来のcompositionは、部分取得を含むjournal owner、queryのnative buffer／policy handle、借用rootを同じ外側ownerへ保持する必要がある。
失敗と同時に返ったownerを捨てず、sourceの実returnとfile／bufferの回収確認後にだけ外側の終了を報告する。
借用rootはjournalより先に閉じず、未確認sourceを別workerや別ownerで開き直さない。
このcompositionと保護capabilityの正規producerは未配線であり、source constructorの成功だけで製品の起動許可にしない。
query中に実行主体やthreadの健全性が不明になった場合は、native Free／Closeを再開せず参照を保持し、privateな退役markerを返す。
そのcallはOS threadをhealthy poolへ戻さず、caller goroutineを終了する契約である。
将来の外側compositionはこのmarkerを認識・伝播する必要があり、未接続のまま正常停止や別thread再回収の成功を報告しない。
これはSupervisor即終了やRevertToSelf失敗時policyの採用ではない。
再起動時の旧workload回収も別の正規producerを要求し、新しい空Ownerを代用しない。
journalはgrantの保存・復元・TTL延長を行わない。

## accountの照会と変更を分ける

`NetUserGetInfo(NULL, name, 23)`で得たSID／flagsは、現在その名前で見えた診断値である。
「このcreateが作成したSID」、SID条件付きmutation、確定不在、完全な実効権限のreceiptにしない。
同名別SID、別machine、domain controller、権限不足、取消、native bufferの回収未確認を拒否する。
not-foundも、rename済みの旧SIDが存在しないことの証明へ昇格しない。
実効group／privilege／logon right／保護対象writeのproducerがなければ、coreのcompleteな権限snapshotを発行しない。

公開Win32の`NetUserAdd`は作成SIDや保持user handleを返さず、`NetUserSetInfo`／`NetUserDel`は名前指定である。
照合とmutationの間の同名置換を、coreのmutexや直前lookupだけで防いだとは扱わない。
名前指定mutationはexact操作の代替として配線しない。

公開PowerShell LocalAccountsのSID指定は、無効化・削除adapterのsupported surfaceとしてsource候補を実装する。
第一者sourceではSIDからRID／SAM user handleへ進み、delete／disableで名前へ戻らない構成を確認した。
ただし確認したsource版と配備binaryの同一性、固定moduleの保護、Service所有のJob／I/O／終了確認が必要である。
作成中の例外で部分SIDを返せない場合は、no-SID unknownとして隔離し、自動削除・盲目的createをしない。
cmdletのnot-foundだけでは確定不在としない。
同梱版との互換性や必要なproducerを確認せず、この方式を採用済みとはしない。

Goからsamlibの内部exportを直接呼ぶ案は、SAMRの公開RPC仕様をDLL ABI保証へ読み替えない。
独自RPC clientを採る場合も、認証、NDR、context handleと異常終了回収の別設計が必要である。
今回のsource準備は、実配置での利用可判定や実機権限付与ではない。

## source試験と実機受入

合成kernelは、同じjournalを既存Lifecycleへ接続する順序、取消、部分結果、競合、破損、再起動復旧、別SID拒否を検証する。
Windows向けcompileは実API呼出コードの型・link確認であり、実APIを動かした成功ではない。
合成fileやtemporary directoryの試験を、Windows ACL／disk durability／account非管理者性の証明へ広げない。
別途承認するprovisioning計画（過去の参照資料・本文非同梱）を満たすまで、製品gateは閉じたままとする。

## 一次資料

- [NtCreateFile](https://learn.microsoft.com/en-us/windows/win32/api/winternl/nf-winternl-ntcreatefile): RootDirectory、open dispositionとoptions。
- [FlushFileBuffers](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-flushfilebuffers)／[LockFileEx](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-lockfileex): flushと排他の保証・限界。
- [ReplaceFile](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-replacefilew): WRITE_THROUGH未対応。
- [NetUserGetInfo](https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netusergetinfo)／[NetUserAdd](https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netuseradd)／[NetUserDel](https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netuserdel)。
- [Remove-LocalUser](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.localaccounts/remove-localuser?view=powershell-5.1)／[Disable-LocalUser](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.localaccounts/disable-localuser?view=powershell-5.1)／[New-LocalUser](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.localaccounts/new-localuser?view=powershell-5.1)。
- [PowerShell v7.4.0 Sam.cs](https://github.com/PowerShell/PowerShell/blob/v7.4.0/src/Microsoft.PowerShell.LocalAccounts/LocalAccounts/Sam.cs)／[SamApi.cs](https://github.com/PowerShell/PowerShell/blob/v7.4.0/src/Microsoft.PowerShell.LocalAccounts/LocalAccounts/SamApi.cs): 第一者sourceの確認であり、別binaryの検証ではない。
