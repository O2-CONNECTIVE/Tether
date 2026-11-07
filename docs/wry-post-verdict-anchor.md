# 判定後の配送観測（実験専用）

本変更は新しい Host 診断ソースであり、起動タイムアウトの修正ではない。
本番利用、追加試行、timeout・pump・SDK の変更を許可しない。
通常の旧診断 JSON と、独立した `wry-sdk-post-verdict-1` companion を出力する。

## 証明できる範囲

専用の信頼済み CI job 内で、元の Node の session-create timeout が確定し、
その exact bytes のコピーと失敗 receipt が Flush(true)・close・読戻しを終えた後だけ signal を発行する。
既存 collector が signal を認め、配送 Probe 内で anchor を公開する。
既存の同期 event5 がその anchor を sample の予約前に一度取得し、
同じ controller の元の send が成功した event6 を観測した場合だけ、
`controller-pre-send-observation-after-verdict` とする。

callback 関数自体が判定後に入ったこと、inner Result の成功、HTTP 復旧、
native 成功、根本原因、修正方法は証明しない。
event5 の取得が0なら、後で anchor と send が記録されても unknown のままである。
Node・Host・配送・driver の clock は結合しない。欠測は永久停止を意味しない。

## 新しい sequence と互換投影

companion が anchor を含む最大7位置の causal sequence と捕捉値を保持する。
旧 `wry-sdk-delivery-1` の sample seq は、予約済み anchor の位置を除いた最大6の ordinal に投影する。
**これは新ソースからの明示的な投影であり、過去の配送 bytes・資格試験と同一ではない。**
sample の意味、時刻、loss、inFlight、scope の判定は維持する。
companion reader は raw seq と anchor から全 sample の逆対応を検証し、
元の snapshot JSON をそのまま内包する。二回目の seal は行わない。
投影 ordinal だけから判定後という推論を行ってはならない。
未公開予約、CAS loss、時刻逆転、途中 seal、重複、壊れた投影は positive を与えない。

## 信頼境界と有限終了

固定名の2048 byte以内の canonical signal を一度だけ読む。
manifest は起動前に束縛し、source/tree/control/specimen と fresh run/attempt/trial/nonce を含む。
Host は構造・identity を検査して manifest/failure digest を echo する。
独立 publisher が元コピー、receipt、signal、manifest の exact hash と producer の順序を検証する。
同一ユーザーの悪意ある改ざんへの認証ではない。nonce、path、PID は公開しない。
Windows reparse・link・exclusive open の実動作は Windows fixture で別に検証する。

追加処理は admission 前に開始する一つの10秒 monotonic budget を共有する。
copy、hash、flush、signal、grace は予算をリセットしない。
同期 filesystem・Python subprocess が停止した場合の hard deadline ではない。
正常・非対象・不正入力に grace を与えない。元の15秒 AbortSignal と native1/0・exit1 を変更しない。
追加の grace は通常の失敗 catch 内だけで開始し、取消の unwind/finally 内から開始しない。
finish helper と診断例外からも元の cleanup が呼ばれる nested finally とする。
新しい kill 権限や全子孫回収の保証はない。

PowerShell fixture の child cancel-file は、その協調経路だけを検証する。
`post-verdict-stop.test.ps1` は所有する child pwsh 内の実 engine Stop と元の catch/finally を検査する。
起動前停止、grace 中停止、予算超過、診断・finish・cleanup 例外の6条件を別 receipt にする。
所有 child の終了と全条件を確認後だけ、source/tree/control に束縛した閉じた summary を create-new で保存する。
control はその厳密な型・ケース集合・hash と公開 receipt を Python qualification と別に検査する。
実行 engine は厳密に7.6.9に限定し、相違時に近い版へ fallback しない。
[PowerShell の確認対象 source](https://github.com/PowerShell/PowerShell/tree/bf5fc434120666e5f1a8d1dfd6ecac7efc17a4be) と同じ版を要求する。
これは runner から child-pwsh までの graceful 伝播を保証せず、hard kill 時の finally も保証しない。
Windows preflight と最終 review は未実行のため native launch は HOLD とする。
別 watchdog や console framework は追加しない。

## 入力 identity と検証対象

`wry_probe_ci.source_identity` は固定 HEAD/tree の全追跡ファイルを検査する。
Cargo.lock の唯一の例外は既存の厳密な vendor 投影で、overlay と config も再生成照合する。
新規入力は `post_verdict_signal.rs`、`wry_post_verdict_probe.rs`、
`wry_post_verdict_record.py`、`post_verdict_failure.py`、`post-verdict.ps1`、
collector・delivery recorder・supervisor の限定変更と各 fixture である。
control は別 commit の `post_verdict_anchor.py` と `post_verdict_publish.py` を束縛する。
旧12 sectionと companion の public record は別で、旧 reader を拡張しない。
Wry/SDK overlay、protocol ABI、元の send/Result、pump、receiver、8段階 recorder、依存 lock は変更しない。

Python qualification は実際に実行した test ID の固定集合と件数を記録する。
Rust の実 recorder・materialized protocol/closure、signal→collector、元失敗コピー→publisher を試験する。
source-only/local 成功は Windows native 起動・取消・Flush の成功ではない。
将来 launch を認める場合も独立 final-head review、Windows preflight、cap-one・35分を必要とし、
healthy、failure、positive、unknown のどの結果も追加実行せず終了する。
