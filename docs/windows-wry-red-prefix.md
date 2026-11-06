# 失敗窓のWry prefixを得る次の限定実験案

このcontrolは診断専用・DO NOT MERGE。実行上限を前phaseの一回から変更するため、公開・実行前に新しい上限の確認と独立最終reviewを行う。現時点ではlocal準備で、次のWindows実行は開始していない。

## 未回答の一問

元のsession-create timeoutが実際に起きた同じ窓で、固定Wryのenvironment/controllerはどこまで進んだか。
[#240の元失敗](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/42277585224/job/128097840476)はHost build-startまでしか保持していない。
[#244の単回成功](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/42289436070/job/128138678449)はHTTP200/5435ms、全8markerを保持したが、失敗時のprefixを得ていない。
成功窓のcontroller api→sendは3887601us、send→wait-returnは78us。これを過去失敗の原因へ外挿しない。

## 変えないもの

- 診断source `d2643b7385b661e3a6b64cf0d63ef57cafa57a31` / tree `c910247c3937d7b92320a97133d49e43b5d3f2aa` と8marker probe。比較対象は元#240 `4ba768e910af595f26176eb69aaf2e21d663b726`。
- 元request/body/AbortSignal、15000ms、元supervisor/driver variant、所有/normal-close/cleanupとunknownの扱い。profileや返却capabilityの別診断へ戻らない。
- #244と同じ全preflight順序、Rust/Node/Pythonと公式action/tool pins、release/custom-protocol/startup-diagnostics、runner内ZIP展開後の検証実行。
- #240・main#239・#242を含む過去失敗、元記録、未mergeのMac stack。成功を修正・製品CI clearanceへ読み替えない。

## 明示した上限と停止

一つのopenedイベント、attempt1、最大3つのserial jobs。各jobはfresh standard public windows-2022一台、fresh owned profile、release specimenを一回build、native session最大1回。
`trial2 needs trial1` / `trial3 needs trial2` と、直前jobのresultがsuccessの場合だけ実行する条件を組み合わせる。
したがってmatching timeout、他の予期しない失敗、欠測・identity drift・元cleanup gate不確実のどれでも、後続jobは開始しない。
nativeのretry、manual rerun、matrix、dispatch、capの自動拡張はない。失敗の後に成功へ置換する仕組みではない。

各jobは同じ固定sourceから独立にbuildする。EXE/archive/driverの実hashを個別に保存し、binary一致を仮定しない。
job間のEXE、ZIP、cache、artifact転送は行わない。固定schemaの匿名JSONとhashのみをcontrolled logs/summaryへ保持する。
最大3回がすべて成功してもinconclusiveで停止する。failure rateを推定せず、blockerをclearせず、4回目を自動的に作らない。
fresh profilesだけの同一runner連続試行では、first startupと後続warm startupが別条件になるため、この問いの代用にしない。

GitHubの[hosted runner契約](https://docs.github.com/en/actions/concepts/runners/github-hosted-runners)は、single-CPU例外を除きjobごとに新VMと説明している。
標準windows-2022以外、self-hosted/larger runner、connected Windows device、追加credentials/security/billing変更は用いない。
[公式billing条件](https://docs.github.com/en/billing/concepts/product-billing/github-actions)に沿い、public標準computeとlog/summaryのみ。新しいartifact/cache保存を追加しない。

## 共有stepとtrigger境界

step listとenvをYAML anchor/aliasで同じ内容へ固定し、各jobに明示したneeds/guardを残す。merge keyやreusable workflow callは使わない。
[GitHub公式のanchor契約](https://docs.github.com/en/actions/reference/reusable-workflows-reference#yaml-anchors-and-aliases)に従い、actionlintと展開後YAMLの構造を検査する。
safe baseは `diagnostic/wry240-red-prefix-base-20261106` の固定 `03c6025f3c32a416902d809ada73d16eb467b3a1`。
headは `diagnostic/wry240-red-prefix-20261106`。draft/同repository/public/opened/attempt1/正確なbase/head guardを維持し、main向けと旧診断workflowを発火させない。

## 結果の読み方

- 元timeoutがredになり、同じsource/specimen/driver・元cleanupの証拠と完備Wry prefixが得られた場合だけ、最後の境界に対応する次の一変数を選ぶ。
- send markerがありwait-returnが欠けても、callback exitや永続deadlockと断定しない。prefix内時刻だけを比較し、Host/driver/Node時計を差し引かない。
- 既存after-cleanup projectionのnull/unknownは、元harness gate成功と別の欠測であり、独立の全残存0監査を主張しない。
- 原因を支持しないproduction依存patchやtimeout延長は行わない。診断controlはmergeしない。

## 別のkernel prototypeを先行させない理由

一次資料に基づくsent-message callbackの短いWin32 prototypeはsource-onlyで保留している。
固定webview2-comのコメントはposted callbackを前提にし、cross-thread senderにはqueueを起こす責任を説明している。
人工的なsent producerでpumpの制約を示せても、WebView2が元失敗でその配送を使ったとは証明できない。
そのpatternだけを製品timeoutの再現・根因・修正の根拠にせず、まず実際の失敗prefixを得る。
