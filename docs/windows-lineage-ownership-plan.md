# Windows native lineage の証拠と次の所有契約

関連: [追跡 #45](https://github.com/O2-CONNECTIVE/Tether/issues/45)。これは未採用の設計で、現在の所有判定・cleanup・製品 gate を変更しません。

## 固定した失敗証拠

PR #295 の merge `089e75f3540fc0f559f2a525f7110d204d256c8b` に対する [startup diagnostic run 42569029454](https://github.com/O2-CONNECTIVE/Tether/actions/runs/42569029454) は debug job `129001879443` が失敗、release job `129001879025` が成功しました。release 成功を debug の原因解消とは扱いません。

[debug artifact 13652990118](https://github.com/O2-CONNECTIVE/Tether/actions/runs/42569029454/artifacts/13652990118) の `webview-result.json` は `actualRustIpc:true`、checks=3、keyboard cycles=3/checks=22 を記録しています。一方、`native-result.json` は次の結果です。

- nativeTestsRun=1 / nativeTestsPassed=0、failurePhase=ownership-confirmation、normalCloseVerified=false。
- 初回 driver 探索の depth4 で、親の名前分類は webview-name、子は other。相対生成時刻は -182344.376ms、reason=child-older。
- 現 snapshot 内の親・子の同一性分類はそれぞれ same。driver 起動前の snapshot では親は absent、子は same。
- cleanupFailures=0、driverExitObservedAfterCleanup=true。これは通常 close や未確定な子の終了を証明しません。

`absent` は起動前 snapshot にその PID の行が無いという分類であり、親の現在の同一性欠測ではありません。CreationDate は wall clock で、snapshot は作成時の親世代の証明ではありません。PID 再利用、取得の時間差、時計の変動などを、この記録だけで根因と確定できません。

## source から確定する失敗経路

`supervise.ps1` は Node の WebView/IPC 検査後、通常 close 前に `Get-OwnedApp` を呼びます。`processes.ps1` の `Get-Descendants` は ParentProcessId が一致する全候補をたどり、子の CreationDate が親より古い場合に `Stop-UnconfirmedLineage` で停止します。今回の匿名記録はこの境界と一致します。

既存 `lineage-baseline.test.ps1` は、同じ子が起動前に存在し親が absent の場合も拒否を維持する論理を検査します。別の時間差だけを追加した合成試験は根因の証拠を増やしません。今回、新しい production 修正や重複 fixture は追加しません。

## 不採用の近道

名前・profile 引数・app path・起動前から存在したという理由だけで子を除外しません。探索深さの切詰め、時刻逆順の無視、待機延長、同条件 rerun、未確定候補への kill は行いません。現在の `NATIVE_LINEAGE_OWNER_UNCONFIRMED` は保持します。

## 次に必要な producer 契約（未実装）

次の実装単位は、CIM の事後的な親 PID グラフから独立した、作成時の所有証拠を提供する disposable CI harness owner です。まず source と有限の fault model を別 PR で評価し、現在の caller へ接続しません。

1. **作成と束縛**: 元 owner が対象 root の実 process handle を作成時から保持し、子が走り始める前の所属束縛を証明すること。候補は suspended launch と専用 Job Object ですが、まだ採用しません。開始済み PID への後付け assignment は同等ではありません。
2. **閉じた所属**: 子孫の所属・breakaway・既存 runner job との nested-job 互換性・権限失敗を定義すること。root PID や生成順だけで membership を発行しません。assignment 完了前の resume、観測欠落、矛盾は unknown のままです。
3. **同一 owner の寿命**: root、job、観測対象の元 handle と outstanding 操作を一つの owner が保持すること。コピーした PID リストや任意 callback/bool では所有権を移転しません。取消・期限・部分失敗・終了中の新しい子の扱いを定義します。
4. **通常 close と cleanup の分離**: app と必要な子の通常終了を期限内に確認した場合だけ合格とすること。job 全体の強制終了、owner の破棄、後から空になった job を通常 close 成功へ変換しません。不明状態でも資源の元 owner を失わない設計が必要です。
5. **公開証跡**: source/binary と invocation に結びついた固定分類だけを出力し、PID、絶対時刻、path、command line や生エラーを公開しません。CIM 診断は原因比較用に残し、新しい所有証拠の代替にはしません。

## 一次資料から追加で必要な境界

2026-11-09 に確認した Microsoft の [Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects) は、通常の子継承にも生成方式の例外があり、一般の completion-port 通知の配達は保証されないと説明しています。通知の件数や未着だけで全子の捕捉・終了を確定する設計にはしません。tauri-driver / Edge / WebView の実際の生成経路を確認し、未対応の生成経路は fail closed にする必要があります。

[AssignProcessToJobObject](https://learn.microsoft.com/en-us/windows/win32/api/jobapi2/nf-jobapi2-assignprocesstojobobject) と [Nested Jobs](https://learn.microsoft.com/en-us/windows/win32/procthread/nested-jobs) は、既所属 process の階層条件・UI 制限・同じ session・必要 handle 権利を定めています。Windows の版が新しいというだけで runner と互換とは判断しません。既存 job を解除する fallback は設けません。

root を tauri-driver とする job には driver と Edge も含まれます。現行の合格条件は両 driver が app/子の通常 close 中も同一性を保って生存することなので、その段階で job 全体が空になることは要求できません。後続設計では、元 handle で特定した両 driver と app の役割、その他の所属 process の完了集合を分ける必要があります。PID/name の除外だけでこの集合を作ったり、driver を先に終了して空の job を合格にしたりしません。これは現行コードと API 仕様から導く設計上の制約で、実装や実機確認ではありません。

## 次の単位の受入と停止条件

最初の source-only 単位では、create → assign → resume の順序、assign 失敗、resume 前取消、子の追加と終了通知の競合、同一 handle の保持、部分失敗後の回収、通常終了と強制 cleanup の非同一性を有限モデルで検査します。モデル成功だけで native 所有証明が完成したとは扱いません。

native caller へ接続する前に、上記 API 制約に対して具体的な生成経路・runner job の適合を確認し、独立した所有権レビューを通します。その後の hosted Windows 検証は新しい証拠を得る一度の固定実験として設計し、既存の予算・close 条件を維持します。失敗したらその証拠で停止し、同条件の成功拾いはしません。

現時点では実 API producer、nested-job 互換性、breakaway 拒否、native 故障注入、今回の根因と通常 close は未確認です。#45 は open のまま維持します。Mac の実 GUI 成功、実 dot 接続、製品配布の受入とは別です。
