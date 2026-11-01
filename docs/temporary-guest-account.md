# 単発版の一時guestアカウント

単発のレスキュー版は、セッションごとに別の一時非管理者Windowsアカウントを使う方針である。
将来の常駐版は専用アカウントを保持し、接続権限を毎回失効させる別modeとする。
一時アカウントの作成・削除に失敗した場合、常駐アカウントやホスト操作者の権限へfallbackしない。
製品gateは閉鎖している。

## 役割と今回の範囲

GUIから許可・停止するoperator、遠隔作業のguest、監督serviceと認証helperの実行主体を区別する。
一時guestの方針は、OperatorSIDやAuthenticationIdの認証方式の採用ではない。
接続が切れた場合の再承認、許可鍵、固定宛先、不変grant、hard deadline、短lease、不可逆revokeの要件を変えない。

`backend/internal/guestaccount`は、作成前登録から無効状態の準備と清掃までの純粋な順序・状態管理を準備する。
native factoryは未設定拒否とし、実アカウントAPIや資格情報生成へ接続しない。
通常unit/raceはprivate seamの合成記録・合成account・回収receiptだけを使う。
memory fixtureの保存成功は、OS保護された永続化や実Windowsの非管理者性の証明ではない。
SSHへの有効化、実token発行、既存guest主体への置換は今回の実装範囲外である。
後続の[Windows照会とjournal境界](temporary-guest-native-boundary.md)では、実API呼出sourceとmock試験を分けて準備する。
実機操作に必要な入力・権限・変更対象・復元は別途承認する計画（過去の参照資料・本文非同梱）へ分離する。
[作成入口と検査拒否](temporary-guest-create-boundary.md)では、作成返却SIDを同じcoreへ接続し、未構成の完全検査を成功にしない。

## 作成と有効化の境界

```text
正規のfresh承認と同じセッションの所有scope
  → protected journalへ作成意図を排他的に保存
  → 作成attemptを保存してから、一度だけdisabled accountを新規作成
  → 作成結果の実SIDを同じintent/machine/session/nameへ結合して保存
  → 無効状態・実効group/privilege/logon rights・保護対象への権限を確認
  → prepared-disabled
  → 実SIDを同じ不変grant/鍵/ACL/Jobへ結合する後続の保護配線
  → 有効化直前の再検査後にだけSSHを許可（未接続）
```

意図の識別子と内部生成名は相関値であり、認証秘密やアカウント所有の証明ではない。
UIが任意の既存ユーザー名、SID、path、資格情報を渡す入口を設けない。
同名が存在した場合は失敗し、そのアカウントを採用・変更・削除しない。
戻り値とerrorが同時に存在する部分取得も、同じ所有記録へ保持する。
SID記録のcommitが未確認なら有効化へ進まず、メモリに残る確定参照も捨てない。

新規作成のsource候補は、返却objectから作成SIDを得る`New-LocalUser -Disabled -NoPassword`である。
`NetUserAdd`後の名前lookupだけでは作成来歴を確定しない。`NetUserGetInfo`は読取診断に限定し、今回実APIを実行しない。
native実装ではdomain controllerを拒否し、実際のlocal account-domain SIDを照合する。SIDの形やNULL server指定だけでlocal accountと認定しない。
`USER_PRIV_USER`や非管理者の直接groupだけを検査しても十分ではない。
間接membership、実効privilege、logon rights、監督/設定/鍵/記録への書込・停止・複製権限を含め、必要範囲を確認する。
不明なgroupや未取得の検査結果を空の安全な結果に置き換えない。
logonの許可範囲、資格情報の生成・保管・破棄、必要なnative管理主体は未設定である。
SSH以外の対話ログオンや他資源への権限を暗黙に追加しない。

## 停止・削除・記録完了は別の結果

1. 新規admissionを閉じ、同じauthorityの許可を失効させる。
2. 所有が確定したアカウントの無効化を封じ込めとして行う。workload回収timeoutでも無効化の試行を恒久的に待たせない。
3. 同じsession/epoch/intent/SIDに結びついたnetwork/process/Job回収を確認する。
4. 削除意図を保存し、現在のaccount同一性を再検査する。
5. 正確な所有対象だけを削除し、不在を確認してから清掃記録を完了する。

accountのdisable/deleteを、既存token、SSH/TCP、ログオン、process終了の証明にしない。
回収receiptには当該guestの内側SSH sessionと作業processも含める必要があり、外側Goや認証helperのJobが空になっただけでは代用できない。
準備中のaccount callが未returnの場合、coreは停止要求でadmissionを閉じて実returnまで所有を保持し、Closeのbusyを清掃成功にしない。
Serviceのauthority失効はaccount workerのgate待ちと独立に先行させる必要があり、この未配線coreのCloseだけへ即時失効を任せない。
失効や回収のreceiptを、別owner・別epoch・別accountから流用しない。
cleanupが未確認なら、新規セッション・再接続・許可復元を拒否し、要清掃状態と所有参照を保持する。
削除の失敗は「接続を許可したまま成功」と表示せず、失効と無効化の結果、削除未完を分ける。

`NetUserDel`は名前指定で、SIDを条件としたatomic deleteではない。
SID照合から削除までのrename・同名再作成競合を排除するnative境界は未実装である。
private seamのexact-binding delete成功を、このOS保証の実証へ読み替えない。
一時名のprefixやcommentだけで削除対象を選ばない。UserCommentは本人が変更できる場合がある。
削除後の記録保存が失敗しても、その後の同名別SIDのアカウントを削除しない。
結果不明のcreate/enable/delete/commitを、別名や新ownerで盲目的に再実行しない。
削除済みaccountの不在を確認済みで終端保存だけが未確認の場合は、同じbindingと同じ終端recordへの冪等・条件付きcommitだけを再試行する。
その前提を満たすjournalは、単調stage、SID確定以外のbinding不変、他intent上書き禁止、terminalからの回帰禁止を要求する。

## 異常終了と再起動後

| 保護記録 | 回復時の扱い |
| --- | --- |
| 作成attemptの前で、OS副作用が未開始と確定 | 予約済みならSIDなし中止完了を保存し、receipt確認後だけ清掃完了。保存未確認では要清掃を維持し、既存accountを採用しない |
| 作成中でSID記録なし、またはcommit結果不明 | 名前/prefix/commentから所有認定せず、quarantineと手動照合の対象にする。有効化・自動削除・新sessionを拒否 |
| 実SIDの所有記録あり | 旧epochの許可を拒否し、旧sessionの回収証拠を確認して同じ所有対象を清掃。無条件resumeはしない |
| 削除の結果不明／完了記録未確認 | 既知SIDの不在とpending操作の実returnを照合。別SID・不明な所有では削除を再実行しない |

起動時は保護されたinstallation scopeの未清掃記録を対象にする。
OSの全ユーザーをprefix検索して一括削除しない。
再起動時のgrant全失効だけでは、OSアカウントの残留や旧process回収を証明できない。
特にService再起動では旧canonical Owner pointerを復元できず、新しく作った空Ownerの回収成功を旧sessionに使わない。
旧session/epoch/intent/machine/SIDに結びついた復旧回収sourceがなければ要清掃を維持する。

## 既存moduleとの接続条件

現在の`protectedkey.Profile`等は固定GuestSIDを必須とし、ACLやhelper profileへ含めている。
一時accountの作成結果SIDを、同じsessionの保護されたreceiptから解決する構成が別途必要になる。
架空SIDで初期検査を通したり、UIのSIDを採用したり、既に公開したgrantを後から書き換えたりしない。
ownerは固定service/operator policyと動的guest bindingを分け、保護provisioning、profile/digest、grant、ACL、helperへ渡す順序を確定する。
作成時間を理由に、承認済みhard deadlineを後ろへresetしない。

accountとjournalの同時commitはOS APIだけで得られない。
native実配線には、保護journalの排他・atomic commit・flush・crash recovery、真正な作成/回収receipt、SID条件付きmutationと同等の同一性保証が必要である。
永続grantを使わない初期版でも、OS accountの清掃記録は別途必要である。
memory journalをdurable backendとして製品へ接続しない。

account削除とprofile/home/作業データ/鍵/追加ACLの清掃は別工程である。
それらを作る場合は別の資源所有記録と同一性確認を追加し、任意pathやユーザー名からの再帰削除をしない。
このcoreのaccount清掃完了だけで、未記録の資源もすべて消えたと報告しない。

## 実機操作の前提

一時accountを作成・削除できる主体の権限は未確定である。
通常のLocalServiceにその能力があると仮定せず、LocalSystemや別資格情報へ自動昇格しない。
最小の管理broker、権限とIPC制限、保護配置、資格情報処理を具体化した後、対象端末・作成対象・設定変更・失敗時対応・復元手順を示して別の実行承認を求める。
今回アカウント作成、資格情報生成、ACL/FW/service設定、鍵、SSH、拒否済み専用試験は実行しない。

## 一次資料

- [NetUserAdd](https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netuseradd) / [USER_INFO_1](https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/ns-lmaccess-user_info_1)
- [USER_INFO_23](https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/ns-lmaccess-user_info_23) / [NetUserDel](https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netuserdel)
- [NetUserGetLocalGroups](https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netusergetlocalgroups): 間接membership。
- [SAMR Common User Fields](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-samr/899d68a9-8145-4334-a52f-2fbd92bec94f): UserCommentとAdminCommentの区別。
- [Access tokens](https://learn.microsoft.com/en-us/windows/win32/secauthz/access-tokens) / [LSA logon sessions](https://learn.microsoft.com/en-us/windows/win32/secauthn/lsa-logon-sessions)
- [DeleteProfile](https://learn.microsoft.com/en-us/windows/win32/api/userenv/nf-userenv-deleteprofilea)
