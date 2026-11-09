# Windows作成時所有の独立probe

追跡 [#45](https://github.com/O2-CONNECTIVE/Tether/issues/45)、設計 [#300](https://github.com/O2-CONNECTIVE/Tether/pull/300)。これはdisposable hosted runner上の互換性実験で、製品APIやnative smokeの所有producerではありません。

Windows x64 / PowerShell 7で `./tools/windows-owner-probe/probe.test.ps1` を実行します。外部依存の導入やネットワークは不要です。Macでは未実行であり、初回Windows CIの成功も以下の固定3ケースだけを意味します。

- system directoryのcmd.exeを固定の `/d /c exit 0`、空environment、handle継承なし、suspendedで作成。専用のunnamed/non-inheritable Jobへ割当て、同じprocess handleのmembershipを確認してからresumeします。exit code 0とjob active=0をcleanup前に確認します。
- null Jobへの実API assignment拒否後はresumeせず、元process handleで回収します。
- assignment成功後のresume前取消しを模擬し、同じprocess/job handleで強制回収します。取消しケースを通常終了成功とは扱いません。

Jobはkill-on-last-handleだけを設定し、breakaway flagやrunnerの既存Jobを変更しません。現在ユーザーでの通常CreateProcess以外にtoken取得・生成、privilege有効化、ACL/サービス/OS設定変更を行いません。既存runner Jobと共存できない場合は失敗し、別APIや権限へのfallbackはありません。

process/thread/jobの元handleを保持し、PIDから開き直しません。終了未確認ならhandleをstatic参照に保持して失敗し、このdisposable PowerShell hostの終了まで手放しません。handle close失敗も失敗です。cleanupは通常終了判定の後で、強制停止後の空Jobから通常終了を作りません。各ケースは一度だけ実行し、失敗時の再実行は組み込みません。

## 既存3ケースが証明しないこと

上記3ケースのcmdは子を生成しません。tauri-driver/Edge/WebViewの生成経路、子の継承・breakaway・新しい子の競合、Job通知の網羅性、app役割の識別、driver生存中のapp通常close、製品の非admin隔離、#45の根因・解消は証明しません。null Jobは限定故障注入であり、任意のassignment失敗やOS hangの再現ではありません。

通常ケースのjob active=0はこの固定fixtureの終了だけに使用します。driverを生存させる実ハーネスへ同じ判定を持ち込めません。既存 `backend/internal/childjob` の承認済token・保護artifact契約を変更せず、新しい汎用launcherやbool/receiptによる所有権発行APIは追加しません。

次の実装には、実driverの生成箇所で保持するhandleと役割、閉じた所属の検証、nested-job階層と生成経路の適合、通常終了集合とcleanup集合を分けた独立レビューが必要です。現在のCIM fail-closedと製品gateはそのままです。

## 子継承fixture（Windows未実行）

`probe.test.ps1` は上記3ケースの後に `descendant.test.ps1` を実行します。
Windowsに既存の .NET Framework x64 compilerで、このdirectoryの `Native.cs`、
`DescendantNative.cs`、`DescendantProbe.cs` だけを一時directoryへcompileします。
compilerが無ければ固定分類で失敗し、download/installや別toolchainへのfallbackはしません。
生成した固定exeを同じ実行で使い、終了後にその一時directoryを削除します。
compile診断は公開出力へ流さず、成功分類・固定失敗分類だけを出します。

新しいexeには固定2ケースがあり、既存3ケースの成功を引き継ぎません。

- **子の通常終了**: 外側ownerが専用Jobを作り、同じexeのparent modeをsuspendedで作成し、
  Jobへ割当ててからresumeします。parentは固定cmdをsuspendedで生成します。
  子へ後付けassignmentせず、作成時process handleで同じJobへの実membershipを確認し、
  同じthread handleでresumeしてexit code 0を待ちます。parentは子の終了確認まで生存します。
- **子のresume前取消し**: 同じ生成とmembership確認の後、子をresumeせず、元process handleを
  TerminateProcessしてexit code 77を確認します。parentは専用exit code 42を返します。
  外側ownerは通常ケースの0と取消しケースの42を区別し、強制停止を通常終了成功へ変換しません。

外側ownerはJob handleを `DuplicateHandle` でquery権限だけに制限したinheritable handleへ複製し、
`STARTUPINFOEX` のhandle listでその一個だけをparentへ継承します。Jobのassign/terminate権限は
parentへ渡しません。childはhandle継承なしです。全CreateProcessに空environmentを渡します。
Jobはunnamed、breakawayなしです。生のhandle値は継承済handleを指定するprivate invocationだけに
用い、PIDからhandleを取得し直すAPIや、外部callerへ所有権を発行するAPIは追加しません。
この固定helperは信頼済fixtureの一部であり、任意の実行者を認証するIPC境界ではありません。

外側ownerはparentの元process/thread/job handle、parentはchildの元process/thread handleを保持します。
判定は固定sourceの実API検査と終了codeに限定し、終了code自体を汎用の所有receiptとは扱いません。
親や子の終了待ちは有限で、失敗時は元handle/専用Jobだけを回収し、終了未確認ならhandleを
disposable host終了まで保持して失敗します。通常caseの判定前にcleanupで空Jobを作りません。

**意図的な未実装**: parentが先に退出してchildだけが残るcase、child handleのowner間transfer、
走り始めたchildの取消し、child追加と終了の競合、任意のbreakaway、実driver/appの役割は未検証です。
parent退出caseには生成元から外側ownerへの実process/thread handleのtransferと、その途中失敗・
acknowledgment・回収契約が必要です。PIDや通知・boolを渡す方式では代替しません。
今回の範囲は「生存する固定parentが子の元handleを保持した実Job継承」に限定します。
MacではC# compile、PowerShell実行、Windows API実行はいずれも未実行です。
Windows CIでの初回評価・固定headの独立レビューは別途必要で、#319のchild-older根因は不明のままです。
