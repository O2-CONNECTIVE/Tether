# SCM probe の配置と回収の契約

この文書と controller は、既定で実行を拒否する source-only の提案である。
作成時に Windows 実行、service 登録、ACL 変更、昇格、回収は行っていない。
将来の実行時承認では、レビュー済み script、manifest、実行ファイルの hash、固定 path、実行可能な phase を明示する。
一つの明示的な承認 packet で Prepare、単回の Run、正常系 Cleanup をまとめて許可でき、承認済み phase ごとの再確認は不要である。
`-Approved` は今回の phase がその承認内であることを表す実行 guard であり、承認取得、署名検証、承認範囲の拡大を行わない。

診断では一つの一時 LocalService `OWN_PROCESS` service と、既に昇格済みの人間の PowerShell controller を使う。
[ADR 0004](../../docs/adr/0004-localservice-auth-helper.md) で採用した製品の二 service 設計は維持する。
製品 gate の解除や、最初の観測より前の保護成立は、この診断の成功条件に含めない。

## 固定する配置

| 対象 | 固定値 |
| --- | --- |
| service 名と表示名 | `RescueHostProtectionProbe-20261030` |
| account | `NT AUTHORITY\LocalService` |
| service type / start / error | `OWN_PROCESS` / `DEMAND_START` / normal error |
| service SID type | `SERVICE_SID_TYPE_UNRESTRICTED` |
| root | `C:\ProgramData\RescueHost-ProtectionProbe-20261030` |
| image command | `"C:\ProgramData\RescueHost-ProtectionProbe-20261030\scm-protection-probe.exe" --service` |
| 事前作成する report | root 配下の `reports\subject.json`。初期状態は 0 byte |
| 私的証跡の保管先 | `C:\ProgramData\RescueHost-ProtectionProbe-20261030-evidence` |
| controller 排他 object | `Global\RescueHostProtectionProbe-20261030-controller` |

対象は Windows 10/11、Windows PowerShell 5.1 の新しい `-NoProfile` process、local NTFS の `C:` である。
対象機に PowerShell module や toolchain を追加せず、自動昇格もしない。
controller は既存の Windows PowerShell `Add-Type` で埋込み C# を compile する。
通常の compiler 一時ファイルは診断 root の所有対象外であり、Cleanup でまとめて削除しない。

controller はレビュー済み artifact bundle から実行する。
`Invoke-ProtectionProbe.ps1`、`manifest.json`、`scm-protection-probe.exe` は同じ directory に置く。
module 内の source copy は、そのまま実行用 bundle とは扱わない。
manifest の最上位 key は `schema`、`sourceCommit`、`executableSha256`、`toolchain`、`goos`、`goarch`、`signed`、`windowsExecuted`、`files` の九つに固定する。
値は schema 1、小文字 40 桁の source commit、小文字 SHA-256、`go1.27.3`、`windows`、`amd64`、二つの boolean は false とする。
boolean は build 時点の provenance を示す。
files map は controller と配置する実行ファイルの hash を拘束する。
別の arm64 artifact は compile-only であり、この controller から選択しない。

次の例は、対象 phase が明示的な承認 packet に含まれる場合だけ実行する。

```powershell
powershell.exe -NoProfile -File .\Invoke-ProtectionProbe.ps1 -Phase Prepare -Approved
```

`Run` と `Cleanup` は、同じ承認 packet の範囲で別々に呼び出す。
自動的に次の phase へ進まない。
`-Approved` または phase を省略すると、administrator 検査、native compile、Windows 変更の前に停止する。
execution policy の bypass は指定しない。
host policy に拒否された場合は blocker として扱い、policy を変更して続行しない。

## 所有者と作成時の保護

file と directory の作成時に、明示的な security descriptor を `SECURITY_ATTRIBUTES` へ入れ、`CreateFileW(CREATE_NEW)` または `CreateDirectoryW` に渡す。
通常の作成後に ACL を修復する手順を使わず、既存 object の上書きや引取りも行わない。
subject は controller が事前作成した report だけを開く。

所有する全 file/directory の owner と primary group は Builtin Administrators（`S-1-5-32-544`）に固定する。
DACL は protected、非 null とし、次の flags 0 の allow ACE だけを許可する。
継承 flag や無関係な principal は許可しない。

| 対象 | SYSTEM | Administrators | 専用 service SID |
| --- | --- | --- | --- |
| root、`reports`、実行ファイル | `0x1f01ff` | `0x1f01ff` | RX `0x1200a9` |
| `reports\subject.json` | `0x1f01ff` | `0x1f01ff` | `FILE_GENERIC_WRITE`、`0x120116` |
| manifest、receipt、証跡 directory/file | `0x1f01ff` | `0x1f01ff` | ACE なし |

report の権限は `GENERIC_WRITE` の file 用 mapping であり、write data、append data、write EA、write attributes、read control、synchronize を含む。
`FlushFileBuffers` が `GENERIC_WRITE` で開いた handle を要求するため、この mapping を予定権限とする。
DELETE、WRITE_DAC、WRITE_OWNER と directory 変更権限は付与しない。
この source 上の権限選択を、実施済み ACL 変更とは扱わない。

専用 SID は、固定 SCM 登録を開くか作成した後、`NTAccount("NT SERVICE", fixedRegistrationName).Translate(SecurityIdentifier)` から得る。
SID 引数や subject token から期待 SID を補わず、共有 LocalService への write 権限拡大や別 account への代用もしない。

新しい service object の owner/group は Administrators、DACL は SYSTEM と Administrators に対する flags 0、full access `0x000f01ff` の二 ACE とする。
SCM の作成 API は `SECURITY_ATTRIBUTES` を受け取らないため、新規作成された停止中 service の既定 descriptor を置き換え、その後に SID 設定と起動許可へ進む。
この SCM API から process/token の初期 security descriptor を指定できるとは主張しない。
file system には `DACL_PROTECTED` を要求するが、service object 検査では `SetServiceObjectSecurity` の information mask が扱わない継承保護 flag に依存しない。

各 phase は `C:\`、`C:\ProgramData` と所有 directory/file の handle を保持し、delete sharing を許可しない。
共通祖先には read/write sharing を許可して無関係な application の処理を妨げず、directory であることと reparse point でないことを確認する。
global ACL の変更や正規化はしない。
所有 object は handle 経由で owner/group/DACL の完全一致を検査し、通常 file には hard link 数 1 を要求する。
実行ファイル、report、祖先の reparse point を拒否する。
Administrator/SYSTEM による干渉は、この probe の隔離保証外である。
これらの主体は security policy の変更や script 外からの service 起動が可能なためである。

## 各 phase の遷移

**Prepare** は承認済み manifest と controller の hash binding を検査する。
source の実行ファイルを一度だけ読み、その byte 列の SHA-256 を照合し、同じ byte 列を保護済み `CREATE_NEW` 宛先へ書く。
root、証跡宛先、service 登録の既存 object は拒否する。
停止中 service を作成して SID を導出し、directory、image、空 report を作成した後、`manifest.json` と `owner.json` を作成する。
owner receipt は固定 root、service 名/SID、source commit、script hash、manifest hash、実行ファイル hash を拘束する。
設定と STOPPED/PID 0 を検査して終え、Prepare から `StartService` は呼ばない。

**Run** は同じ service と object を開き直し、owner/DACL、image/manifest の bytes、receipt の binding、directory の全 entry、STOPPED/PID 0、空 report を再検査する。
`StartService` より先に、Administrators/SYSTEM 専用の `consumed.json` を `CREATE_NEW` で作成し、`Flush(true)` を完了する。
marker が既存なら実行を拒否する。
起動に失敗しても、その登録の単回実行枠は消費済みとし、script は再試行しない。

controller は `StartService` の直前から最大 25 秒観測し、active なら協調 STOP を要求した後、最大 5 秒追加で観測する。
native subject は別に capture の協調期限 15 秒と、hard self-exit 20 秒を持つ。
SCM RPC 自体は期限引数を受け付けないため、これらは観測時間の上限であり、OS/RPC の hang も 30 秒以内に返るという保証にはしない。

controller は service が RUNNING として報告した PID に対し、`SYNCHRONIZE | PROCESS_QUERY_LIMITED_INFORMATION` handle を取得する。
image path、起動試行以後の creation time、再度取得した SCM RUNNING/PID の一致によって同じ process 世代へ拘束する。
保持 handle の signaled と SCM STOPPED/PID 0 の両方を確認するまで、process 終了を認定しない。
任意 process の探索や PID 指定の強制終了は行わない。
native wrapper は最初の capture の成否にかかわらず、controller が handle を取得できるよう RUNNING の観測区間を 5 秒設ける。
RUNNING は制御を受け付けられる状態を示し、capture 成功を示さない。
その区間より前の失敗、process handle の取得不能、controller 中断の場合は世代の終了証拠が欠けるため、後から SCM が停止を返しても自動回収を許可しない。

終了を確認できた場合だけ `exited.json` を作成し、process 世代、process exit code、SCM exit code を保存する。
「process exit confirmed」を capture 成功へ読み替えない。
非 0 exit、空 report、部分的または失敗 report、上限超過、必須 field/sample の欠落は、失敗または不明のままとする。
report は私的に扱い、読取りと copy を 256 KiB に制限する。
script は byte 数、hash、exit 状態を出力し、生 report を表示しない。

**Cleanup** は削除前に、同じ設定、artifact、ACL、receipt、directory entry を検査する。
consumed/exit marker がなく report が空の未実行配置、または有効な保護済み exit receipt がある消費済み実行だけを許可する。
marker の欠落や矛盾は自動回収を拒否する。
まず未存在の固定証跡 directory を作成し、名前を固定した `manifest.json`、`owner.json`、`subject.json` だけを copy し、終了証拠と report hash を含む `exit.json` を作成する。
copy は一度読み取った bytes、保護済み `CREATE_NEW`、flush、読戻し SHA-256 照合で行う。
証跡の upload や自動削除は行わない。

続いて service 状態、設定、directory entry を再検査し、同じ停止済み登録だけを削除する。
service handle を閉じた後、SCM から登録不存在を確認する。
その後に保持 handle を解放し、列挙済み file と空になった二つの directory を非再帰 API で削除する。
wildcard、任意 path 引数、広範囲の process 探索、再帰削除、自動 rollback は使わない。

私的証跡は、capture 後 24 時間以内に人間が確認、取得、削除する計画とする。
証跡削除は明示的な承認内の人間の操作であり、自動 timer や controller の追加 phase は設けない。
削除前に、固定証跡 directory の Administrators owner、protected な SYSTEM/Administrators 専用 DACL、非 reparse、`manifest.json`、`owner.json`、`subject.json`、`exit.json` の四 file だけという inventory を確認する。
manifest/owner binding と `exit.json` の report hash を照合し、承認済みの私的取得を完了した後、その四 file と空 directory だけを非再帰操作で削除する。
予期しない entry、照合失敗、取得結果不明では削除を停止してレビューへ戻す。
wildcard、公開 upload、任意宛先、失敗時削除は許可しない。

保護済み named mutex は、この controller の各 phase の変更期間全体を排他する。
同名 object があれば待機や変更をせず、その呼出しを拒否する。
これによって同じ script の Run/Cleanup 競合を防ぐが、script 外から SCM を操作する administrator への防御とは扱わない。

## 失敗時に残す資源と復旧境界

失敗した呼出しはその場で停止し、再試行や ACL/設定の自動修復を行わない。
次の操作を検討する前に、失敗箇所、承認 packet の hash、確認できた service 状態、残った固定 path を記録する。

| 失敗箇所 | 残る可能性のある object | 次の対応 |
| --- | --- | --- |
| service 作成前 | 所有 service/root はなし。同名の既存 object はあり得る | 衝突 object を引き取ったり削除したりせず、別途レビューする |
| service 作成後、Prepare 未完了 | 固定停止 service と、部分的な root/image/report/manifest。`owner.json` がない場合もある | 保持する。通常 Cleanup は receipt 不足や不整合を拒否する |
| `consumed.json` 作成後、起動失敗、controller 中断、終了未確認 | service、準備済み file、consumed marker、部分 report。有効な終了証拠はなし | 再実行しない。PID 再検索や SCM 停止だけで終了を認定せず、世代と native 終了証拠を別途レビューする |
| 証跡作成/copy 失敗 | 元資源と部分的な固定証跡 directory | 再試行を通すために証跡を上書き/削除せず、名前を固定した file を確認して保全する |
| service 削除が保留または結果不明 | 私的証跡、元 root、削除待ち登録の可能性 | 先に SCM の状態を読む。結果不明の削除を繰り返さず、file 削除へ進まない |
| service 不存在確認後、file 回収が中断 | 私的証跡と root の残存 entry | 通常 Cleanup は service を開けず拒否するため、別途レビュー済みの復旧を行う |

この文書や以前の `-Approved` は、例外状態の手動復旧を自動的に許可しない。
具体的な復旧を承認する前に、残った固定名 service の account/type/start/image/SID/owner/DACL/recovery 設定をレビューする。
残存 file の hash/owner/DACL と全 directory entry を承認 packet と照合し、link、reparse、予期しない entry を拒否し、process 世代の終了を確認して、上限内の私的証跡をレビュー済み固定宛先へ保全する。
その後、削除する所有 service、個別 file、空 directory の明示リストを承認する。
同名という理由だけで衝突 object を削除せず、global policy の変更や未拘束 PID の強制終了で復旧を進めない。

script は user/account、password、credential、logon right、privilege assignment、firewall、ESET、global ACL、既存 service の recovery policy を変更しない。
新しい service の既定設定は検査し、期待値へ書き換えて検査を通す処理は行わない。
SYSTEM probe service、認証、guest 実行、SSH、listener、network path は作成しない。

## 検証済み範囲と残る制限

macOS での確認は source review と差分衛生の範囲に限る。
local に PowerShell 5.1 parser と C# compiler はなく、追加 install もしていない。
Windows controller、Win32 ACL、SCM 登録/起動/停止/削除、file system race、report 永続化、timeout、回収の実挙動は未実行である。
別の Windows CI で script を実行せず、`ProtectionProbe.Native` の instance も作らずに parse と `Add-Type` compile を行えば構文と型を検査できるが、それも runtime や保護成立の証明にはならない。

自己レビューでは、作成時 ACL の空白期間、継承と ACE flag、共有 LocalService write 権限、read/hash/copy の競合、部分 Prepare の所有証拠、単回 marker、Run/Cleanup 競合、PID 再利用、STOP と終了の区別、起動失敗時の消費、削除保留、証跡衝突、予期しない entry、広範囲削除を点検した。
runtime 検査は未対応 descriptor/設定や不完全な所有証拠を拒否する。
Administrator による変更、電源断時の永続性、filter driver の挙動、OS/RPC hang、alternate stream の挙動、初期 process/token/thread 保護は、成立済みの証拠に含めない。
将来の Windows 実行承認でも、この未確認範囲を維持する。

API の正本は [CreateFileW](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew)、[CreateDirectoryW](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createdirectoryw)、[SetServiceObjectSecurity](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/nf-winsvc-setserviceobjectsecurity)、[service configuration](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/ns-winsvc-query_service_configw)、[StartServiceW](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/nf-winsvc-startservicew)、[FlushFileBuffers](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-flushfilebuffers) を参照する。
