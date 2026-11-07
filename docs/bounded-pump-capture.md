# Windows timeout の有限失敗採取（CONTROL のみ、merge 禁止）

これは diagnosing-bugs の Phase 1 であり、原因の A/B 実験や製品修正ではありません。
PR248 は controller API return 後、Empty 2+ と callback 未観測の timeout を採取しました。
PR249 の追加 pump 観測は healthy だったため、失敗時の bracket は未確定です。

## 固定条件

- source: `cee2e9baa22200c929f015199aac68626925a486`
- source tree: `ce3c61a0e56b2bec1fdc2a5990813dff17afb4ec`
- isolated base: `b924553f114dc2dfbb37d6098af585a598d5e7d1`
- 対象: public same-repository draft PR の opened、run attempt 1 のみ。
- 専用 head/base branch は workflow に完全一致で固定します。
- 標準 windows-2022 の独立した job を最大3回、直列に実行します。
- 各 job は同じ source、release profile、child pwsh、15000ms の original session、owned cleanup を維持します。
- 各 job の original native command は一つだけです。artifact/cache/secrets/runner 間の実行物転送は使いません。
- 各 fresh build は独立です。EXE hash はその trial の build→launch→final 内だけで束縛し、trial 間の同一 binary は主張しません。
- source の probe、runtime、publisher と13 section は変更しません。CONTROL の資格記録・admission・workflow のみ変更します。

## 停止表

| 先行 trial の状態 | 次の trial |
| --- | --- |
| native 成功、全 original/diagnostic gate、資格記録、identity、cleanup が成功し job も success | 1→2、2→3 だけ許可 |
| 最初の timeout | 採取結果として停止。再試行しない |
| preflight/native/classification/identity/cleanup 失敗 | 停止 |
| unknown、欠測、資格記録の欠落/拒否、continuation の欠落/不正 | 停止 |
| cancellation、skipped job、非successの job | 停止 |
| 3回 healthy | 不明として停止。継続 token を発行しない |

job の依存は固定2本です。next job は先行 job の success と `healthy-1` / `healthy-2` の完全一致の両方を要求します。
手動dispatch、rerun、matrix、workflow API retry、rescheduling はありません。
失敗後の bounded partial record は、元の native 結果と独立して cleanup 後に公開します。
採取途中の job 強制終了で公開できない場合は欠測として停止し、成功へ読み替えません。

## Windows 資格証跡

PR249 では資格 JSON が disposable raw-python.log に隠れ、外から実行件数を検証できませんでした。
今回も raw compiler/test output は disposable log のみに置きます。
control qualifier は source の41件を実行し、必須 LastError adapter fixture の start と success をそれぞれ一回確認します。
ゼロ/欠落/重複/skip/failure/expected failure/unexpected success は拒否します。
Windows x64、rustc Windows MSVC target、実 GetLastError/SetLastError、TLS 前 clobber、元 error conversion、guard-removal negative control は既存資格条件のままです。

全 field が固定された閉じた qualification JSON を source/tree/control/trial ordinal と束縛し、canonical SHA256 と共に native 前に公開します。
41以外の件数、bool/int の混同、余分な field、非canonical JSON、重複 key、2048 byte 超過、symlink/hardlink/reparse point/nonregular file は拒否します。
公開済み receipt と preflight 全12 stage の exit 0 を native admission 直前に再検証します。
receipt は成功した公開処理の証拠であり、外部でログを受領・保存できたこととは別です。終端監査で実際の公開 block/hash を再確認します。
元 publisher の全 gate 合格だけでなく qualification/admission receipt も必要です。

## 採取結果の読み方と予測

| timeout 時の controller pump state | 絞れる範囲 | 結論にできないこと |
| --- | --- | --- |
| get-message-bracket | 最後の整合した公開観測が GetMessage 呼出境界内 | 純粋な queue wait、内部 sent-message 処理、API 前後の preemption の区別 |
| dispatch-message-bracket | 最後の公開観測が DispatchMessage 呼出境界内 | callback の不在理由、deadlock の断定 |
| between-calls | 公開された呼出間 | 現在 PC、未公開の遷移、継続停止 |
| wait-ended | 対象 wait 終了が公開済み | 他記録と同時刻だったこと、後続 timeout の原因 |
| unknown/missing/拒否 | probe ではその失敗を区別できない | 原因や修正の断定 |

これらは causal ranking ではなく採取前の識別条件です。
GetMessage は内部 sent-message 処理を含みます。record はそれぞれ独立に seal するため、cross-record duration や同時刻 join を計算しません。
最後の公開 prefix は現在 PC や唯一の active wait の証明ではありません。
callback 未観測の理由は依然未確定です。probe が失敗を区別できなければ不明として設計を再評価し、production を推測で変更しません。
healthy 3回でも timeout の原因/解決を証明しません。既存製品 gate は閉じたままです。

## ローカル検証と公開境界

`python -B -m unittest discover -s scripts/tests -p test_bounded_pump_capture.py -v` は control の有限 fixture です。
実 Windows ABI の実行や timeout 再現ではありません。既存 test_wry*.py の有限 fixture と actionlint を別判定します。
Mac で Host GUI、native session、新規 toolchain install は行いません。
公開前に最終 control SHA の独立レビューと実行承認を確認し、初回 timeout または3 healthyまでを一つの有限実験として終端監査します。
