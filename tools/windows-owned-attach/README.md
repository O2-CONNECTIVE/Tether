# 実 app の owned attach 固定実験（未採用）

これは信頼済み disposable Windows x64 runner 専用の別 caller です。
既存 native smoke、CIM の拒否、待機予算、製品 gate を変更しません。
#341 の過去の失敗の根因を修正したとも、一般の WebView2 生成経路を証明したとも扱いません。

## 同一 owner と実験順序

外側 C# launcher が元 tauri process/thread handles と kill-on-close Job を保持します。
`CreateProcessW` の suspended 作成、外側 Job assignment、実 handle の所属確認後だけ resume。
二本の anonymous pipes は厳密な inherited handle list で tauri だけへ渡します。
Tauri は Edge の生成前に pipe の inherit flag を消します。stdout や HTTP は所有権 channel ではありません。

固定 2.1.0 crate の別 variant は、元 Edge `Child` と app の元 process/thread handles、
private app Job を一つの serialized owner thread に保持します。Tokio proxy は待機で block しません。
元の tauri 自己割当て Job を同じ kill-on-close/raw API で作り、具体的な handle を保持します。
app は suspended 作成後、breakaway 許可なし・非継承の専用 Job へ assign。
app が tauri Job と専用 Job の両方、Edge が tauri Job だけに所属することを確認して resume。
app に Job handle や owner channel を渡しません。新規の空 UDF を一度だけ使用します。

private channel の固定 sequence:

1. `1 prepare` → app 作成、全 endpoint 検査 → `1 ready`
2. Node が一度だけ `browserName=webview2` と `debuggerAddress=127.0.0.1:9222` で attach。
   `2 attached` → retained handles と listener の再検査 → `2 bound`
3. 別 Node caller が既存の実 Rust IPC 3 checks と keyboard 22 checks を同じ session で実行。
   `3 close` → endpoint 再検査、通常 WM_CLOSE、元 app handle の exit0（10秒）、Job active=0（10秒）→ `3 normal`

各観測で元 Edge が live、外側は元 tauri が live であることを確認します。
通常 close 自体は10+10秒のまま、失敗後の5秒 cleanup 分類を失わないよう private result の受信期限は28秒です。
外側 workflow は4分で、supervisor の180秒と bounded cleanup/reporting に余裕を持たせます。
正常 verdict は cleanup 前に固定し、外側が tauri/Job を回収してから最終成功を出します。
root 終了だけ、強制終了後の Job 空、driver の先行停止は通常成功になりません。
partial failure の cleanup は root と Job を独立に試行し、終了不明なら handles を保持。
明示 cleanup は一回だけで、成功した close の handle は消し、失敗分を再使用しません。

## Endpoint の限定された証拠

4444/4445/9222 の IPv4/IPv6 listeners を全件調べ、全て loopback であることを要求します。
4444 と4445 は元 tauri/Edge handles の role に対応します。debugger listener PID は発見のためだけに
query/synchronize handle を開き、live と exact app Job membership を確認して保持します。
初回 request 前、attach 直後、close 前に同じ listener 集合と retained live handle を再確認します。
候補 handle 捕捉前の endpoint 未出現だけを readiness 待機し、捕捉後の消失・変更は失敗を固定します。
通常終了後に observer handles の close 成功を確認してから正常 reply を送り、失敗した close は再試行しません。
PID/名前/親 PID で driver role を発行せず、CIM 候補を除外・kill しません。
これは hostile process の socket takeover/rebind、kernel API hang、proprietary runtime の例外的な
生成方式に対する普遍的保証ではありません。固定 runtime における実験結果としてのみ受け入れます。

## Source、binary、実行

`prepare.py` は既存 checker の固定 upstream archive SHA と exact patch parser を再利用します。
全 source tree を build 前後に照合し、archive/patch/全 source/binary SHA を保存します。
`run.ps1` は実行前に tauri/Edge hash を照合、app/launcher/Node hash と実 HEAD、固定 invocation を記録。
raw logs、profile、session ID は runner temp 内の private directory に留め、artifact に含めません。
公開 artifact は固定 phase、allowlist 内の owner failure/cleanup 分類と SHA の result/provenance のみです。
compile 失敗は固定 compiler code と許可された相対 source 名・行番号だけを別 result に残し、
rendered message、絶対 path、原ログは含めません。

`.github/workflows/windows-owned-attach-experiment.yml` はこの実験 branch の PR だけが対象です。
既存 pinned setup、既存の固定 toolchain、変えていない app build を使用し、先に
`windows-app-owner/app-owner.test.ps1` の4ケースを実行します。一つの source/input 条件で一回。
失敗後の同条件 rerun、成功拾い、長い close budget、別 launch fallback はありません。
初回実行で失敗した場合、証拠を調べて source/input の差分を独立レビューしてから次の実験を決めます。

## 現時点の検証境界

Linux 上の source/package 6 checks、固定 archive の exact patch 適用、差分の静的確認のみ。
C#/Rust compile、PowerShell、実 Windows API、実 app、external attach、nested Job、通常 close、
runtime の子孫 containment は未実行です。合成 red/green や source 検査を native 成功に読み替えません。
実験の全 API failure 組合せ・敵対的 endpoint・OS API hang・deadline 境界競合も網羅していません。

一次資料: [Microsoft WebView2 WebDriver external launch/attach](https://learn.microsoft.com/en-us/microsoft-edge/webview2/how-to/webdriver)、
[Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects)、
[Nested Jobs](https://learn.microsoft.com/en-us/windows/win32/procthread/nested-jobs)。
