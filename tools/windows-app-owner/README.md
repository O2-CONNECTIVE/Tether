# 固定 app owner の有限 fixture（未採用）

[所有契約案](../../docs/windows-lineage-ownership-plan.md)の次の source 単位です。
現在の smoke、workflow、CIM 拒否、通常 close 判定は変更しません。
`AppOwner` は internal な固定 fixture 専用で、任意 executable の起動 API や
所有権 receipt を提供しません。現在ユーザーの token を使うため隔離境界でもありません。

## 同一 owner の境界

一つの `AppOwner` instance が unnamed private Job と `CreateProcessW` の元 root
process/thread handles を作成から bounded wait、cleanup まで保持します。
root は固定 self-exe で suspended 作成し、Job assignment と実 handle の所属照会を
終えてから resume します。Job は kill-on-close のみで breakaway flags は設定しません。
PID による reopen、CIM、名前、時刻、任意 callback/bool による所有判定はありません。

固定 root は同 exe の child を suspended 作成し、Job 継承を実 handle で確認します。
child が ready event を通知し release event を待つ間に root は終了します。
外側 owner は元 root handle の終了・終了値0と Job active=1 を同時に確認してから
release を送り、done event と Job active=0 を期限内に確認します。
root の終了だけでは正常終了にしません。child の元 handles は root が自身の終了まで
保持し、外側 owner の Job はその後も開いたままです。done は固定 helper の進行通知で、
Job 所属や終了の receipt の代替ではありません。

継承 handle は `PROC_THREAD_ATTRIBUTE_HANDLE_LIST` で query-only Job duplicate と
3つの unnamed events に限定します。Job の assign/terminate/set 権限は継承しません。
元の Job handle と owner 専用 event handles は継承不可です。helper の release は待機権限のみ、
done は通知権限のみとし、owner は自分の元 event handles で release 通知と done 待機を行います。
ready は root が待機し child へ通知権限を継承するため、待機・通知の両権限を持ちます。
root/child の生成は固定 CreateProcess 経路に限定し、
環境は空、作業 directory は system directory とします。event の数値は helper 起動時の
私的引数にのみ用い、公開出力へ出しません。

## 固定4ケース

- late-child: root 終了後の非空を確認し、明示 release 後の空を cleanup 前に確認。
- forced-cleanup: 非空のまま cleanup。強制終了は `ForcedCleanup` のみで正常成功に変換しない。
- cancel-before-resume: assignment 後に取消し、suspended root を回収。resume しない。
- assignment-denied: query-only Job handle で実 API の access denied を確認。
  suspended root は元 process handle で回収し、resume しない。

`Run` の正常成功は cleanup 前の正常観測と、cleanup で強制操作が一度もない場合だけです。
想定された強制・取消し・拒否ケースの試験合格は app の正常終了成功とは別分類です。
cleanup は root と Job の両方を試行し、期限内に終了を確認できない場合は失敗します。
終了不明の handles は明示 close せず disposable host の終了まで保持します。
これは回収保証ではありません。子が query Job handle を保持すると host 終了だけで Job の
最後の handle が閉じるとは限らず、停止に失敗した未割当 suspended root も残り得ます。
この不明状態は常に失敗のままで、host 終了による正常終了・回収成功へ読み替えません。
close 自体の失敗も合格にしません。全ケースと filesystem cleanup の完了後だけ固定合格を出力します。

## 実行と未確認

Windows x64 と既存 .NET Framework compiler、PowerShell 7 が必要です。導入は行いません。

```powershell
pwsh -NoProfile -File tools/windows-app-owner/app-owner.test.ps1
```

`windows-owner-probe/Native.cs` と `DescendantNative.cs` の宣言を compile 時に共有し、
既存 probe の entry point やファイルは変更しません。各 event wait は5秒（child の安全期限は
20秒）、Job 空の query polling は5秒で打切ります。sleep で子の生成順を推測しません。

この追加時の Linux 環境には C# compiler と PowerShell がなく、compile と Windows 実行は
未実行です。TDD red/green の実証、実 app/WebView2、external attach、driver 生存確認、
endpoint と app handle の対応、異常生成経路、runner nested Job 互換性、breakaway 攻撃、
期限直前の観測競合、OS API 自体の hang は未検証です。実 API failure の全組合せも網羅しません。
将来の driver は app Job 外に置く設計ですが、この fixture は driver を起動しません。
`#45` の解消、既存 native failure の根因解消、製品 gate の変更とは扱いません。

一次資料: [Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects)、
[CreateProcessW](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-createprocessw)、
[AssignProcessToJobObject](https://learn.microsoft.com/en-us/windows/win32/api/jobapi2/nf-jobapi2-assignprocesstojobobject)、
[UpdateProcThreadAttribute](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute)。
