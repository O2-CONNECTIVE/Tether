# Windows制御client候補の確認範囲

[開発入口](README.md) / [serverの正本](../../docs/windows-local-control.md) / [共通契約](../../docs/adr/0001-control-contract-candidate.md) / [Issue #25](https://github.com/O2-CONNECTIVE/Tether/issues/25)

`src-tauri/src/windows_client` は、既存 `rescue-host-control/1` を使うRust内部の実WinAPI adapterです。
製品runtimeのbootstrapは未接続で、開始・停止ボタンと接続gateは閉鎖を維持します。
実pipe、service、account、ACL、credential、Go/SSH、新PCの操作は行っていません。

## 接続と相手の識別

pipe名は既存のgenerationと検査済みnonceから固定local namespaceへ生成します。
UIからpath、PID、実行引数、SIDを受け取るTauri commandはありません。
`CreateFileW` は個別access `0x00100183`、非継承、overlapped、Identification SQOSを使い、別名・generic rights・再接続へfallbackしません。
message pipe/client end/最大1 instanceを検査し、message-read modeを設定します。
これらの検査だけでACLやserver認可が成立したとは扱いません。

期待するserviceのPID・生成時刻・TokenUser SIDは、将来の正規owner bootstrapから独立に渡す前提です。
pipeの実PID、保持したquery-only process handle上のPID/生成時刻/生存/主TokenUserを前後照合します。
期待値を受信frameや実測値から作らず、service group、LocalSystem、debug privilege、管理者、DACL変更へのfallbackを持ちません。
一致はprocess/accountのbindingであり、承認artifact、SCM service、正規grantの証明ではありません。
別accountのtoken照会は権限制約があり、取得不能なら拒否します。実provisioningでの照会可否は未実証です。
[CreateFileW](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew)、[OpenProcessToken](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-openprocesstoken)。

## frame、取消、所有寿命

送信は1 frameにつき1回の `WriteFile` と厳密な転送数確認です。short writeを再送しません。
受信はconsumerの小bufferに合わせてNative Readを分割せず、内部16KiBで1 message全体を既存codecへ通してからbyteへ切り出します。
MORE_DATA、成功0 byte、LF欠落、複数frameは失敗です。Broken pipeのEOFとは区別し、残片の再解釈やresumeをしません。

read/writeは固有event・OVERLAPPED・bufferを持ち、共通I/O mutexで取消を塞ぎません。
開始前にraw pointerを取得し、pending中はRust参照を再生成せず、終端結果を確認した後だけ所有権を復元します。
`CancelIoEx` の成功やNOT_FOUNDを完了証拠にせず、取消後の通常完了も取消済み要求の成功へ変換しません。
取消完了待ちの予算は1秒で、完了照会不能や不明な結果ではpending資源を保持したままnativeの非復帰abortへ進みます。
fake APIの試験ではpanicを捕捉しますが、nativeのabortは実行していません。
[取消仕様](https://learn.microsoft.com/en-us/windows/win32/api/ioapiset/nf-ioapiset-cancelioex)、[Rustの参照契約](https://doc.rust-lang.org/std/cell/struct.UnsafeCell.html#aliasing-rules)。

reader/writerは同じpipeとserverのquery handleを共有保持し、writer先行Dropでもreaderの資源を閉じません。
reader event取得後・適用前に確定済みの固定faultを再確認し、queue済み応答や汎用の取消結果より先にcoordinatorを閉鎖します。
明示取消とI/O障害は同じatomic latchに最初に記録されたfaultを保持します。
外部cancel tokenへの書込み時刻やOS完了の実時間順を保証するものではありません。
[Rustのatomic ordering](https://doc.rust-lang.org/std/sync/atomic/enum.Ordering.html)に従い、fault記録後に取消をReleaseで公開し、観測側はAcquireで読み取ります。
readerの取消・実join・EOFと、peerのGo状態・stop receipt・Go Close・backend回収を分離します。
終了待ちの予算切れでは所有を残し、候補clientのDropでjoinを確認できないときは自身のprocessをabortします。
これは保護serviceやGoの終了証明ではなく、OS APIやschedulingに対するhard realtime保証でもありません。
GUIの既定状態は未接続・開始不可・停止不可・終了未確認のままです。

## 試験と残る実証

pure API試験は、short write、message全体検査、EOF/途中frame、取消競合と完了不明、主SID/PID/生成時刻とquery拒否、最後のhalfまでのhandle保持を扱います。
memory reader試験は共有cancel、faultとqueueの優先関係、予算切れ後の所有保持と実joinを扱います。
固定Windows Rustのcompile/lint/testと、既定未接続shellのWebView2試験は別の証跡です。
実pipeのI/O、token権限、cancel完了、service分離、grant、hard TTL、実SSHの成功へ読み替えません。

次の最小実API確認案は、標準Windows hosted runner内のtest-only一時local pipeを1 instance/1 connectionだけ作り、既知の有限frameを交換するものです。
同じ既知processの独立bootstrap値との照合、read/write取消、実joinと所有handle終了を確認し、予算・cleanup・失敗を固定codeで記録します。
account/サービス/鍵/既存ACLを変えず、ネットワーク接続やGo/SSHを含めません。
同じaccountでのmechanics確認は別service accountやguest拒否の証明になりません。
この実pipe確認案は未承認・未実行です。実施前に対象API・一時objectのACL・寿命・戻し方を統合ownerへ提示して別に確認します。
