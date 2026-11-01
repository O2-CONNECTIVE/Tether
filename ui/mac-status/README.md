# Tether — Mac の状態確認画面

SwiftUI の読み取り専用アプリです。macOS 13 以上を宣言し、SwiftPM の外部依存・plugin はありません。既存 Windows UI や agent runtime を置き換えません。

## 境界

- 入力は起動時に継承した **専用 FIFO pipe の stdin** だけ。ファイル選択、任意 path/URL、network、鍵・設定読み取り、agent 起動・承認・取消はありません。
- producer/pipe の結び付けは呼び出し側の責任です。同一 Mac の trusted composition を前提にし、JSON 自体を認証証明とはしません。新しい製品 launcher や GUI 同意フローは含みません。
- 一起動に一 pipe／一 reader。再 open・再接続なし。O_NONBLOCK＋100ms poll、2KiB/frame、64frames/秒上限、latest snapshot 一件だけ保持。生 JSON・未知文字列・コマンド内容を表示・保存しません。
- 最後の window 閉鎖／Cmd+Q は reader 取消→実 join→owned fd close。join 失敗時は終了を取り消し、join 前に fd を閉じません。agent に signal を送りません。
- 現在ユーザー権限・非 sandbox の警告を常時表示。画面を閉じても agent 停止ではありません。

## JSONL v1

正確な12メンバーのみ受理します。未知・重複キー、escape による別名、型違い、未知 enum、負数・小数・範囲外、途中 EOF は拒否します。

| キー | 契約 |
| --- | --- |
| version | 1 |
| seq | 1..9007199254740991、厳密増加（gap 可） |
| source_continuous_ms | 同じ Mac の mach_continuous_time 換算 ms、0..9007199254740991 |
| phase | checking_config / loading_key / preparing / awaiting_approval / connecting / connected / stopping / closed / failed |
| reason | none / revoked / deadline / transport_lost / runtime_fault / terminal_cleanup_unknown / canceled / command_timeout / config_error / key_error / preparation_error / connection_error / approval_not_completed |
| grant_remaining_ms | null または 0..86400000 |
| starting_workers / running_workers / stopping_workers | 各0..1、合計最大1 |
| cleanup | not_started / pending / confirmed / unknown |
| terminal_cleanup_unknown / terminal | boolean |

terminal は closed/failed と一致。terminal_cleanup_unknown は failed＋unknown＋対応reason＋terminal=true が必須で、成功に上書きしません。terminal後の追加frameは拒否します。

生成時刻の未来・後退、受領clock後退、source age>3秒を拒否。鮮度は受領時刻でなく **source continuous time** 基準で、sleep中も進みます。残時間は source+remaining の既存上限とmin。nullを挟んでも上限を解除しません。壁時計変更で延長しません。

stale/異常時は接続中の表示・残時間・件数を現在値として出しません。EOFだけでは停止完了と推定しません。明示的terminal報告の後のEOFは、配信終了と最終報告の履歴を区別して残します。0秒は停止完了ではなく停止確認中。件数は管理中の操作数で、OS全体の残存プロセス数ではありません。

## ビルドと検証

このディレクトリで：

```sh
swift build
swift run StatusCoreChecks
```

installed command-line SDK に XCTest がなかったため、追加インストールせず同じ Swift による dependency-free check executable を使用します。初回 `swift test --disable-sandbox` は XCTest module 欠落で **0件・環境失敗**でした。sandbox拒否ではありません。その後は上記標準 build/run のみで検証しています。

checks は strict decoder、seq/clock/stale/terminal/残時間、owned pipe 取消join、Go production encoder から出した synthetic frames を検査します。test executable は本体 app から呼び出されません。fixtureファイルは合成情報のみです。

明示的な `--fixture` は「表示テスト・実接続なし」を常時表示し、stdinを読みません。`--render-fixtures` は固定synthetic状態の自分のSwiftUI contentだけを `.build/fixture-images/` に描画し終了します。desktop captureはしません。ImageRendererではScrollViewが白画像となったため、描画時のみ同じcontentからscroll wrapperを外しています。実window screenshotと同一とは主張しません。

## 表示・未検証

単一画面、接続状態、承認残時間、管理操作数、停止/回収、常時権限警告。文字サイズは表示メニュー／Cmd+・Cmd-で1～1.6倍。狭幅ではカードを縦配置し、スクロール可能。色だけでなく記号と日本語で状態を表します。

light/dark、stale narrow、terminal unknown、文字1.5倍をown-content renderで確認します。VoiceOver実操作、キーボード操作の実GUI検証、macOS13実機、real agentとGUIの接続、配布署名・notarization・installerは未検証／未実施です。source/build/合成試験の成功を本番配布済みと扱いません。


## Local evidence update

The installed Swift 6.3.3/macOS SDK built the application and standalone checks.
A separate StatusWindowChecks executable opened its own NSWindow using the same
StatusUI view, received synthetic pipe frames, observed staleness and invalid input,
then closed its own window, joined the reader and closed only the read descriptor.
It did not launch an agent or issue any grant/command, and left its synthetic
producer write descriptor untouched until fixture cleanup. This is programmatic
owned-window verification, not manual accessibility/keyboard testing.

EOF after a valid terminal frame retains that explicit historical end report.
Confirmed terminal frames require worker total0 and remaining0/null, matching the
production owner contract. Join timeout/terminateCancel keeps freshness updates
running; only a successful join stops the UI timer. No unconfirmed join forces fd close.

## Local evaluation .app

`python3 scripts/package_local.py` packages the existing standard SwiftPM arm64
debug executable into `.build/local-evaluation/Tether Status.app`. Run `swift build`
first. No compilation, network fetch, signing, installation, registration, agent
launch or credentials are performed by the packaging script. Existing output
bundle or manifest of any type stops the operation; there is no automatic deletion
or overwrite. Partial output on error is retained for inspection.

This is an **identity-unsigned local evaluation bundle**, not a release. The
existing Swift linker emits an embedded **ad-hoc** signature with no TeamIdentifier;
the script preserves those bytes and does not sign the bundle. No notarization,
Gatekeeper bypass or installer is provided. Dependency/license inventory and
release notices remain a distribution gate. Only the executable, fixed Info.plist
and LOCAL-EVALUATION notice are copied; no Go agent, tests, fixtures, keys or config.

The script checks the trusted local build workspace, regular bounded inputs, exact
metadata and the toolchain-produced arm64 Mach-O's macOS minimum against Info.plist
13.0. This is limited packaging validation, not a complete Mach-O validator or proof
that an executable is safe. macOS 13 execution remains untested on this development
Mac. Standard .build/debug may be a symlink; packaging uses the fixed actual arm64
build path instead and rejects symlink inputs/components below the package root.

For explicit synthetic lifecycle verification only, the bundled executable accepts
exactly `--check-own-window-no-input` (runner supplies /dev/null) or
`--check-own-window-fixture` (does not read stdin, always synthetic badge). Both
use their own window, check unavailable/stale state, then performClose through the
normal termination delegate and reader join. An eight-second watchdog requests
normal termination, **not** a force-kill guarantee: join refusal retains ownership
and must be reported/supervised. Output is fixed PASS/FAIL/TIMEOUT only.
These modes do not grant authority or change the default stdin-only viewer.

Ordinary bundle launch with absent/non-FIFO stdin displays unavailable, not a live
connection or confirmed cleanup. Fresh consent remains in the separate CLI and
manual dedicated pipe composition remains unchanged. Closing this app never stops
an agent. Bundle lifecycle tests do not prove Finder launch, Gatekeeper approval,
real-agent connectivity or manual accessibility behavior.

## GitHub-hosted CI source preparation

`.github/workflows/tether-mac-status.yml` is prepared for main pull requests and
pushes to main or the specific Tether WIP branch. The main cf9fcec baseline has now passed GitHub-hosted CI (run 41287973206); this does not certify later source changes.
It uses the standard arm64 `macos-26` image and requires the existing
`/Applications/Xcode_26.5.app/Contents/Developer`; a missing selection fails
rather than installing or falling back. The job records actual compiler/SDK/image
versions. Official image availability is not proof of Swift 6.3.3 patch parity.

A dependency-free `Tests/run_ci_checks.py` has only source/gui modes and refuses
execution unless its GitHub/macOS/arm64/selected-toolchain scope matches. The check
is an accidental-local-execution guard, not authentication or a sandbox.
Source checks build/core26/package10/local bundle. GUI checks require the real
synthetic owned-window9, both bundled own-window cases and the exact20 own-content
PNG outputs. Unavailable GUI session, missing PASS, nonzero exit, timeout or image
mismatch fails the job; there is no skipped-success or continue-on-error fallback.
The hosted WindowServer/GUI fixture capability was exercised successfully on that exact baseline run; other images and real-agent GUI operation remain separate checks.

Timeout records an unreaped owned child and stops subsequent checks without force
kill or success inference. This workflow is limited to ephemeral GitHub-hosted VMs,
not a persistent self-hosted runner or real agent process. The job timeout/VM
disposal is not an assertion that the child performed normal cleanup. Logs are
local to the job; the helper rejects logs above1MiB after child completion (not a
hard streaming disk quota). Only fixed JSON summaries and synthetic PNGs have
short-lived artifact upload; no .app, agent, key, config or credential is uploaded.

Public standard-runner compute is free under GitHub's published conditions;
private repositories, larger runners and artifact/cache storage have different
allowances. No budget, paid runner, signing, deployment, secrets or permission
change is part of this source preparation. Local fake helper tests do not certify
a hosted GUI run or a signed product distribution.

## 起動・接続時のフィードバック

保護設定の確認、登録済み鍵の確認、ローカル準備、接続確認の**新鮮な実phase**だけで回転表示します。割合や完了予定時刻は推測しません。本人の承認待ちは回転せず「起動したターミナルで承認してください」と案内します。GUIに承認・再試行操作はありません。接続完了、terminal、stale、EOF、未知状態では回転を止めます。動きを減らす設定と静的renderでは処理中を固定アイコンで表します。

設定／鍵／準備／接続構成の失敗はCLI実callerから固定reasonだけを送ります。approval_not_completedは拒否・時間切れ・中断・読取不能を本人の明示拒否と断定しません。既存通信断や回収不明をセットアップ失敗で隠しません。実際のエラー文字列、設定値、鍵、ユーザー名、commandは表示streamに加えません。設定や権限変更、鍵生成、自動再接続もありません。

JSONL v1は同じ12キーのままenumを拡張しています。旧viewerは未知enumを安全に拒否するため、producer/viewerは対応する版を組み合わせてください。旧viewerへのsilent fallbackはしません。失敗reasonはfailed/terminal、worker0、残時間0/null、cleanup not_startedまたはconfirmedとの組合せだけを受理します。terminal_cleanup_unknownは常に優先して保持します。

20状態のown-content画像はsynthetic fixtureであり、実agentの接続結果でもanimationの動作実証でもありません。既存の実機検証結果をこの変更の合格へ流用しません。

## Pilot-only finite viewer observation

Exact opt-in `--pilot-exit-on-stream-end` preserves ordinary visualization until
actual pipe EOF or reader rejection/read failure. A terminal frame alone does not
close the viewer. Default mode still stays open after EOF. This flag never stops
an agent, issues a grant, opens a port, or changes consent.

After a successful reader join, the viewer terminates its own application/windows
and writes exactly one bounded JSON line on stdout:
`{"version":1,"stream_end":"eof","result":"confirmed_terminal","fault":"eof","reader":"joined","exit_code":0}`.
`stream_end` is eof/rejected/read_error/canceled/unavailable; `result` is
confirmed_terminal/unknown; `fault` is unavailable/stale/malformed/clock/sequence/
eof/read/rate/stopped. No IDs, paths, raw frames or commands are emitted.
Exit 0 requires actual EOF and a validated closed terminal with confirmed cleanup,
zero workers/remaining, no unknown, and source age at EOF at most three seconds.
All other summaries exit 2. A partial final line is EOF with malformed fault,
never a confirmed result. Missing/invalid stdout must be treated as unknown by
an owner; stdout failure is nonzero. Join refusal cancels termination, retaining
reader ownership and freshness updates.

Confirmed terminal is **producer-reported history**, not independent OS cleanup
proof or agent exit evidence. Owners must capture viewer stdout separately from
status stdin and keep agent exit/observations as separate evidence. Manual early
viewer close is canceled/unknown, not successful completion.

`python3 Tests/check_pilot_lifecycle.py` explicitly launches only finite synthetic
viewer fixtures. It performs no agent launch or desktop capture. The internal
`--check-own-window-eof` fixture checks that normal EOF retains its own visible
window for 3.6 seconds, then closes it normally. No force-kill fallback is used;
a timeout reports an unreaped owned viewer and stops the suite.
