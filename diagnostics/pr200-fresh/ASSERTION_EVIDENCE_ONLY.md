# PR213 fixture のassertion証拠だけを補う

Baseは `5503d59a0b71c65c00b31d155a6e7c0f3582bc64` / tree `fe246931b909a865b119e6db400fcda6dcac12be`。
再開時はcheckoutがcleanで、このtask配下を引数に持つ別processが0件と確認した。既存PowerShell runtimeは見つからず、導入もしていない。
この工程はsource/fixtureと独立reviewだけで、push・PR編集・CI・native/Windows実行を行わない。workflow/product/fixed PR200 source/Mac laneは変更しない。

## 実際に分かること

PR213の実保持recordは2files/canonical1551bytes/SHA256 `d219204d88fc45cfcb2053a60051ebcd582770dfb7aceaf9c8c9d8f2ffc0d152`。
parse5/5とledgerは成功、detail fixtureはexit1、build/tool/nativeはskip・native0だった。具体的assertionや例外は保持されていない。
実recordをそのままfixture化して全file hashを再検証し、schema2を読み取っても新assertionを埋め戻さない。
PowerShellの実failure seamはローカル実行できていない。source inspectionやPythonの合成入力から、失敗したassertionや原因を特定したとは扱わない。
元timeout、PR200 blocked、PR208/209/211/213の結果も維持する。

## 狭い変更とfeedback loop

observer-detail.test.ps1の14個の既知assertionの直前で、そのassertion固有の固定literal enumだけをstdoutへ出す。判定条件と元throwは変えない。
出力失敗を無視して元throwへ進めるため、marker書込みエラーで元assertionを置き換えない。追加のliteral writeだけを除くと、正規化sourceのSHA256はPR213原本 `0deeb8e28cb31a3ba6e46ce20940aba09e8f9e3cffd90b5be88dbb2455f51a1f` と一致する。
この静的比較はPowerShellのruntime動作の証明ではない。

既存preflight collectorが固定failure markerを捨てる境界をmocked subprocess stdoutで駆動し、`detailAssertion`欠測でredを確認してから修正し、同じ試験をgreenにした。
これは実collector/公開JSON経路の証拠欠落のfeedback loopであり、実PowerShell failureの再現や修正ではない。

新schema3はstatus/codeだけを保持する。knownは14個の固定codeだけで、detail stageがfixture-rejected/exit1、stdoutが単一の完全なmarkerだけの場合に限る。
空出力はmissing、未知・不完全marker/不正UTF8/余分な任意出力はunknown、複数marker/passとの競合やexit0との矛盾はconflicting、4096byte上限超はtruncated、実行や読取り未確認はnot-observedとする。成功時だけnot-applicable。
不完全な文字列が実際に切断されたかは分からないので、上限超以外をtruncatedと推測しない。markerはstderr/例外から抽出しない。
新しい読取りも最大4097bytesに制限し、入力substring、任意stdout/stderr、exception text/type、stack/path/PID/commandline/response/arbitrarykeyをrecordへ返さない。
元の成功marker・stderr空・exit0・時間上限・process所有回収の条件はそのまま。既知assertionを得ても失敗は失敗のままで後続/nativeへ進まない。
旧schema1/2は保持記録用の読取りだけで、今のcontinuationはschema3で全stage成功・assertion not-applicableを必要とする。欠測や未知をpassへ変えない。
15000ms deadline、元profileMatch/所有/cleanup gate、request/response/error identityは変更しない。EBWebView childのroot扱いや集合対応の推測も行わない。

```sh
python3 -B -m unittest discover -s diagnostics/pr200-fresh -p test_detail_assertion_evidence.py -v
python3 -B -m unittest discover -s diagnostics/pr200-fresh -v
node --test diagnostics/pr200-fresh/*.test.mjs
```

実行Pythonは既存3.13.2、Nodeは26.13.0でCI固定版とは異なる。PowerShell parser/runtimeと変更版PSfixtureは未実行であり、合成stdio試験やactionlintをその代わりにしない。

## 次の証拠

必要なのは、実PowerShellでfixtureが失敗したときのknown assertion codeか、known markerまで到達しなかったという閉じたstatusである。現recordからは取得不能。
追加実行を別途判断するなら、最小はfresh標準Windows runner一つ、parse/ledger/detail preflightのみ各一回・native0/build0のcap1。今回の工程から公開・実行はしない。
known codeが得られても、そのassertionが失敗したruntime原因や元PR200 timeout根因が直ちに確定するわけではない。
