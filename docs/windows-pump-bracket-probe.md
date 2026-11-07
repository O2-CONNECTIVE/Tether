# Windows pump bracket の追加診断

これは原因修正ではなく、独立して seal する有限の観測です。診断 PR は merge しません。
PR248 の controller は Empty 2+、Ready/Disconnected 0、callback/send/drop 未観測のまま timeout しました。
その事実と、ソースから導いた callback 配送の仮説は別です。

## 記録する境界

固定 webview2-com 0.39.1 の元の GetMessage 呼出前、scalar return、DispatchMessage 呼出前後だけを観測します。
元の receive は一回のままです。SDK、closure、send、所有権、結果、error、timeout、child pwsh 起動を変更しません。
GetMessage の -1/0/other を分類しても元の i32 はそのまま返します。
LastError は adapter の TLS lookup より前に保存し、元の Error::from_thread より前に復元します。

状態は missing/between-calls/get-message-bracket/dispatch-message-bracket/wait-ended/unknown だけです。
十種の履歴は各 2 で飽和し、独立した状態遷移で最後の bracket を保持します。飽和 count の差から状態を推定しません。
MSG、payload、window/thread/process ID、address、path、raw scalar は出力しません。

original-wait は**公開済みの観測の由来**です。最後に公開された整合した観測列を示し、seal 時の唯一の active wait、PC、継続停止、原因を保証しません。
既存 protocol の TLS 変更から FFI 通知までには未公開の区間があります。その区間で別の wait が始まっていないとは主張しません。
完了した pre-seal の nested/invalid/old-scope/foreign 通知は unknown にします。旧 delivery の seal 後も、新記録自身の seal まで association 通知を受け付けます。
GetMessage bracket は内部の sent-message 処理、API 前後の preemption、純粋な queue 待ちを区別しません。
旧 delivery と receiver と pump の seal は別々です。同時刻の join や cross-record duration を計算しません。

## 非待機 seal の根拠

入口の最初の観測操作は AtomicU64 の fetch_add です。上位 bit は CLOSED、下位 63 bit は生存する入口 guard の数です。
seal の fetch_or(CLOSED) は close と pre-close active count を一操作で取得します。
一人でも active なら、その後 writer が完了しても最初の snapshot は unknown のままです。
入口で CLOSED を見た呼出は validation/更新をせず、全経路で guard を減算します。

cap 32 は admission の仕事量を制限し、raw ingress count を飽和させるものではありません。
許可した Windows x64 同一 process の正常な呼出では、各 live guard に一つ以上の stack storage が必要です。
Windows x64 の user virtual address space は 2^63 byte 未満なので、2^63 個の同時 live activation は成立せず、CLOSED bit への count overflow は起こりません。
これは memory corruption、非局所 exit、任意の外部 FFI による不正な lifetime を保証する証明ではありません。
不正 count/cap/overflow を注入する fixture は fail-closed を検査しますが、破壊された process の安全保証には広げません。

writer は一度だけ busy CAS を試し、collision/cap は sticky loss を設定してから guard を落とします。
token/owner validation、状態更新はこの guard 内です。GetMessage/DispatchMessage 自体を guard 内に入れません。
spin/retry、blocking lock、書式化、I/O、allocation、apartment/sent-context query を新 pump hook に入れません。
旧 scope begin の既存 query は scope bookkeeping の入口に残ります。

二つの slot と metadata を固定長 atomic word で保存し、first snapshot を別の atomic word に一度だけ固定します。
並行 seal は同じ frozen word を返し、既に close 済みで frozen 未公開なら保守的に unknown にします。
遅い field store は frozen word を更新しません。OS scheduling を含む hard wall-time bound は主張しません。

## 検証と限界

実際の patch 済み pinned pump、protocol、recorder を finite fake Get/Dispatch と組み合わせます。
held Get/Dispatch、ready、disconnect、-1/0/positive/other negative、cap を超えた周回、entry/return omission/duplication の mutant を扱います。
recorder は validation/field write/drop の barrier、admission collision/cap、旧 seal 後の invalidation、並行/repeated snapshot を検査します。
reader/publisher は closed schema、privacy sentinel、追加/欠落 field、enum/count 異常と欠測を拒否します。
元の native verdict と診断の classification verdict は独立です。

Windows の実 GetLastError/SetLastError を使う fixture は、TLS 前 clobber、元 -1 conversion、および保存 guard 除去の negative control を含みます。
Mac の成功は Windows ABI の証明ではありません。Windows fixture を native 起動より先に実行し、失敗時は native を起動しません。
ソース fixture の成功も元の Windows timeout 再現/解決を示しません。
独立レビュー後の新しい固定 source/control による一回の実 Windows 観測だけを次の候補とします。
