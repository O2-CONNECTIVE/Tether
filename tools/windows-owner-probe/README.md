# Windows作成時所有の独立probe

追跡 [#45](https://github.com/O2-CONNECTIVE/Tether/issues/45)、設計 [#300](https://github.com/O2-CONNECTIVE/Tether/pull/300)。これはdisposable hosted runner上の互換性実験で、製品APIやnative smokeの所有producerではありません。

Windows x64 / PowerShell 7で `./tools/windows-owner-probe/probe.test.ps1` を実行します。外部依存の導入やネットワークは不要です。Macでは未実行であり、初回Windows CIの成功も以下の固定3ケースだけを意味します。

- system directoryのcmd.exeを固定の `/d /c exit 0`、空environment、handle継承なし、suspendedで作成。専用のunnamed/non-inheritable Jobへ割当て、同じprocess handleのmembershipを確認してからresumeします。exit code 0とjob active=0をcleanup前に確認します。
- null Jobへの実API assignment拒否後はresumeせず、元process handleで回収します。
- assignment成功後のresume前取消しを模擬し、同じprocess/job handleで強制回収します。取消しケースを通常終了成功とは扱いません。

Jobはkill-on-last-handleだけを設定し、breakaway flagやrunnerの既存Jobを変更しません。現在ユーザーでの通常CreateProcess以外にtoken取得・生成、privilege有効化、ACL/サービス/OS設定変更を行いません。既存runner Jobと共存できない場合は失敗し、別APIや権限へのfallbackはありません。

process/thread/jobの元handleを保持し、PIDから開き直しません。終了未確認ならhandleをstatic参照に保持して失敗し、このdisposable PowerShell hostの終了まで手放しません。handle close失敗も失敗です。cleanupは通常終了判定の後で、強制停止後の空Jobから通常終了を作りません。各ケースは一度だけ実行し、失敗時の再実行は組み込みません。

## 証明しないこと

このcmdは子を生成しません。tauri-driver/Edge/WebViewの生成経路、子の継承・breakaway・新しい子の競合、Job通知の網羅性、app役割の識別、driver生存中のapp通常close、製品の非admin隔離、#45の根因・解消は証明しません。null Jobは限定故障注入であり、任意のassignment失敗やOS hangの再現ではありません。

通常ケースのjob active=0はこの固定fixtureの終了だけに使用します。driverを生存させる実ハーネスへ同じ判定を持ち込めません。既存 `backend/internal/childjob` の承認済token・保護artifact契約を変更せず、新しい汎用launcherやbool/receiptによる所有権発行APIは追加しません。

次の実装には、実driverの生成箇所で保持するhandleと役割、閉じた所属の検証、nested-job階層と生成経路の適合、通常終了集合とcleanup集合を分けた独立レビューが必要です。現在のCIM fail-closedと製品gateはそのままです。
