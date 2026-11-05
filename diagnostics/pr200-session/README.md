# 診断専用・DO NOT MERGE

PR #200 の固定ソース `c6c5ae5342fcde6b1bf77eff744ad48ddce5b8da`、tree `e278496a55b4accc6caafa7a7a99538e5c8ceeca` に対する、一度の隔離診断である。
製品修正、#200 の更新、main への統合、配布許可を目的にしない。
元の [run 42037151371 / job 127278142941 / attempt 1](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/42037151371/job/127278142941) の失敗は保持する。
その評価 EXE と ZIP は保存されていないため、新しい build は別 specimen であり、元 binary の replay ではない。

main `420186c6bdbbd55f978e7f3237c7d937433c1b6a` からの独立 branch に、この directory と専用 workflow だけを追加する。
runner は control を PR の exact head、製品を上記固定 SHA で別々に checkout し、tree と tracked clean を確認する。
元の評価 workflow の Node 24.22.1 / Python 3.13.17 / Rust 1.99.1、npm ci、Cargo --locked、license 収集、unsigned 検査、package / archive 検証を保つ。
普通の release を一回だけ build し、feature は custom-protocol だけとする。
startup-diagnostics は有効にしない。
展開後 EXE と archive の hash、source / tree、両 lock、supervisor、tool pins、driver provenance、実 tool version を保存する。
build 後の失敗時も、取得済み EXE / archive の安全な hash を保存する。

各 trial は同じ検証済み EXE と driver bytes を再使用する。
trial ごとに新規 tools / profile / raw log / trace / finish marker / output directory を作り、driver の source、patch、lock、binary hash を起動前に再検証する。
固定ソース内の supervisor / Node / driver patch を変更しない。
一回の session request は既存の 15000 ms、Node は60秒、通常 close / 子待ちは各10秒、supervisor の外側の期限は180秒とする。
最大10回を直列に観測し、最初の一致する timeout、別の失敗、観測不備、cleanup 失敗・不明で停止する。
失敗 trial の差替え、無変更 rerun、成功するまでの retry、期限延長、assertion 緩和は行わない。
workflow は attempt 1 の、この名前の同一 repository draft branch にだけ実行を限定する。
再 push は別の起動になり得るため、実行後はこの branch に変更を push しない。

通常 close は native-result の元 verdict であり、cleanup 後の消失と区別する。
追加 audit は CIM / listener の読取りだけを行い、所有外プロセスを停止しない。
app、trial driver、profile に言及する process、同じ harness の Node、4444/4445 listener が全て不在で、query が完全であることを次の trial の条件にする。
通常 close 成功時は、保持した子 handle の退出と匿名 snapshot の全件回収も要求する。
未知・欠測は停止条件で、wrapper 側の失敗時に推測の kill / 後続 trial を行わない。

`safe/` の固定 vocabulary に適合する JSON だけを7日間 artifact 保存する。
未知 field / string、重複 key、過大 file、path、URL、request / response payload、原例外文を拒否する。
EXE、ZIP、raw logs、raw trace、profile、画面は upload しない。
環境は GitHub hosted Windows Server 2022 の使い捨て runner、contents:read、固定 SHA の公式 actions、既存の固定公式 tool source とする。
secret、外部 host / 実機接続、永続アクセス、OS trust / security / auth の変更は不要である。
inventory の preinstalled EdgeDriver は実際に使用する固定 driver と区別する。

session-create の NATIVE_DRIVER_TIMEOUT、document / title / origin 未観測、および complete trace の session-received → forward-start → request-dropped（native-headers / native-error なし）が一致条件である。
trace summary の dropped=0 は診断イベントの損失がない意味で、session request の drop がない意味ではない。
一致すれば新 specimen の赤として報告する。10回無再現でも inconclusive とし、#200 の blocker を解除しない。
同 head の startup 診断成功や source base の成功、過去 #74/#83/#91 は comparator / 履歴であり、共通根因や無関係の証拠にしない。
#193 の ownership-confirmation 失敗とも混同しない。
根因仮説や製品修正はこの診断範囲に含めない。

ローカル検証（Mac では source / fixture だけ。GUI / Windows software は起動しない）:

```sh
actionlint .github/workflows/pr200-session-diagnostic.yml
python3 -B -m unittest discover -s diagnostics/pr200-session -p 'test_*.py' -v
```

この fixture 検証を Windows 実測と数えない。
最初の実行可能 push の前に、final commit 固定の独立レビューで checkout、pins、permissions、回数、停止条件、sanitization を確認する。
この追加だけでは元の Tauri paths-filtered suite は起動しない。repository の無条件 PR checks は通常どおり起動する。
保護ルール・approval を変更しない。
