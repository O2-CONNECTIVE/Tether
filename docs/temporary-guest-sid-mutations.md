# 一時GuestのSID指定清掃候補

一時Guestの既存coreに、SID指定の無効化・削除候補を接続するためのsource変更です。
公開の`guestaccount.NewNative`は未構成を返し、製品接続gateは閉鎖したままです。
実アカウント、PowerShell、資格情報、ACLの変更をこの試験では実行しません。

## 対象と失敗時の扱い

通常の管理者操作で同じ名前が別SIDへ置き換わっても、その別主体を変更しないことを要求します。
名前を再照会して`NetUserDel`へ渡す方式は、この競合を防げないため採用しません。
公開cmdletの[`Disable-LocalUser -SID`](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.localaccounts/disable-localuser?view=powershell-5.1)と[`Remove-LocalUser -SID`](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.localaccounts/remove-localuser?view=powershell-5.1)を固定処理から呼びます。
Goから非公開のsamlib ABIを直接呼び出しません。

`sidSource`は、作成来歴が既に確定した一つの完全bindingとSIDだけを保持します。
SID清掃source自身の作成処理は未構成を返し、元producerの完全権限検査を代用しません。
[後続の作成入口](temporary-guest-create-boundary.md)は、作成元が確定したSIDだけをこの清掃sourceへ接続します。
coreの失効→無効化→旧workload回収→再検査→削除意図保存→再検査→削除の順序を維持します。
不明な操作は単回で消費し、同じsourceで再dispatchしません。
正規の作成元と完全権限producerの実装は別の未完了境界です。

## 固定子の実行境界

新しいprocess/Job/pipe実装を増やさず、既存の`childjob`と`childstdio`を使います。
固定argvと依存artifactを`FixedCommand`へコピーし、起動時にもfreezeします。
既存の既定argv、bootstrap経路、suspended start→Job割当→resumeを維持します。
依存manifest/DLLはhash、同じ保持file、reparse拒否、通常file、link数1、write/delete共有拒否を確認し、process退出とJobが空になるまで保持します。
これは親ディレクトリの保護や依存解決全体の真正性を証明しません。

scriptは`NoProfile`・`NonInteractive`で起動し、module自動探索を止め、保護側の固定manifestを指定します。
入力は16KiB以内の単一canonical JSON frameです。
型、版、field順、未知・重複field、世代、SID構文を固定形で検査し、実EOFまでimportも変更も行いません。
親はchild向け複製handleを閉じた後に要求を書き、stdinを閉じます。
操作は常にSID引数で、`ErrorAction Stop`・`WhatIf:$false`・`Confirm:$false`を明示します。
例外では成功結果を出しません。

要求値は相関値であり、認証根ではありません。
成功には、固定子の結果が要求全体にbyte単位で一致することに加え、stdoutの実EOF、stderrの有限drain、exit 0、Jobが空であること、I/Oの実returnと回収確認が必要です。
取消やhandle閉鎖だけをEOF、終了、成功へ変換しません。
部分取得、panic、回収期限切れは同じownerに参照を残します。

## 不在記録

同じsourceで完全bindingの検査済みexact snapshotを得た後、その正確なSIDの削除成功まで確認した場合だけ、同lifetimeのtombstoneを保持します。
`complete`は既検査済み主体についての削除確定を示し、新たな権限検査を発明しません。
[WindowsのSIDは再利用されない](https://learn.microsoft.com/en-us/windows-server/identity/ad-ds/manage/understand-security-identifiers)ため、同名の新アカウントにこの記録を適用しません。
再起動、not-found、未知の削除結果からtombstoneを復元しません。
削除後のjournal commit前に結果を失えば、不明状態と手動照合を維持します。

## 受入と未実証

fake境界で、同名別SIDを変更しないこと、別binding・型・重複・余分な結果を拒否すること、不完全検査から削除や完全な不在証拠を作らないことを確認します。
runnerでは実EOFとworker終了、取消、途中取得、所有handle回収を合成I/Oで検証します。
Windows compileはAPIの型整合だけを確認します。

実配置では、固定PowerShell本体・manifest・assemblyと全依存の保護配置、実行主体とprimary tokenの正規供給、process/pipeのguestからの保護、固定artifactと実module動作の一致確認が必要です。
調査した[PowerShell v7.4の第一者実装](https://github.com/PowerShell/PowerShell/blob/v7.4.0/src/Microsoft.PowerShell.LocalAccounts/LocalAccounts/Sam.cs)と、対象端末に配備するbinaryの同じ動作は未実証です。
不明な値を観測値や自己申告値で埋めるproducerは作りません。
保護root供給、作成元証明、完全権限検査、外側Serviceの回収合流も未配線です。
実機承認の対象・変更・復元手順は既存の計画（過去の参照資料・本文非同梱）で別に扱います。
通常試験やJob成功を、実Windows隔離・停止・監督hang・OS sleep・実SSHの証拠へ広げません。
