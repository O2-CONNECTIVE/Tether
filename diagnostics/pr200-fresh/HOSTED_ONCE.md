# 分類を実測する一回限定診断・DO NOT MERGE

直前のsource-only工程はhead22120c4dac08e0d97a0c19249f7f474da88298ab/tree4de926e17bf071087efdd5b9943fb229d71cb443で完了した。
patch SHA256 `98fe092d7b438ee7931b4a80e08e41ba1eff2077e5c23a6bfb374418d1f57ebd` と元treeを再照合した。
本工程は、その分類sourceを基に、別途委譲された一回限定のhosted診断を行う。旧source-only工程の記録や実測を変更しない。

## capとsource

- 新head branchは `diagnostic/pr200-classification-once-20261106`。
- 新workflowは `pr200-classification-once.yml` の1 jobだけ。matrix、workflow_dispatch、後続job、再試行はない。
- exact head branch、draft、attempt1、同一repository、base branchとbase SHAの全てを要求する。
- eventはopenedだけとする。source再push、reopen、本文editedから追加runを起動する入口を設けない。
- source under testはc6c5ae5342fcde6b1bf77eff744ad48ddce5b8da/treee278496a55b4accc6caafa7a7a99538e5c8ceeca。別checkoutでtree/cleanを照合する。
- 普通のrelease/custom-protocolだけを一回buildし、既存のlocked npm/Cargo/license/package/EXE/ZIP検証を維持する。新buildは別specimenで、元EXEのreplayではない。
- controllerとmetadataのcapも1とし、index2/3は起動前に拒否する。

## 旧workflowと無関係なartifactを起動しない比較先

新draft PRのbaseは既存診断branch `diagnostic/pr200-fresh-start-20261106`、固定headcdc349b7129fdd273159f01e3a43e3901a60f5b6とする。
そのbranch・PR209自体へ変更を加えない。
既存PR208/209診断workflowと無条件Go/Python/Mac/Japanese checksはmain向けpull_requestだけを購読するため、この別baseのeventには一致しない。
旧workflowのfilter、permissions、tests、required check設定は変更しない。
これはmerge候補の検査を省略して合格扱いする工程ではなく、DO NOT MERGEの隔離診断である。必要な固定source/package/native検査と新fixtureは専用job内で全て行う。
このworkflowもedited eventを購読しないため、結果の本文追記は追加診断を起動しない。

## native前のPowerShell runtime検証

Python/Node fixturesとPowerShell source parse後、disposable Windows上でobserver-reasons.test.ps1を一回実行する。
これはCIM/Nativeを呼ばない合成ledger fixtureのruntime検証である。
exit0と固定のchecks=5 marker、stderrなしを全て要求し、raw stdout/stderrはrunner private tempだけへ置く。
安全なpowershell-preflight.jsonにruntimeExecuted、checksVerified、exitCode、固定codeを保存する。
marker欠落・不一致・非0・timeout・起動不能ではそのrunを失敗として保存し、build/nativeへ進まない。
static parseをruntime検証へ読み替えない。preflightを繰り返さず、別runへ置換しない。

## native判定と診断分類

固定supervisor、元request/response/error/AbortSignal、15000ms deadline、所有・通常close・cleanup条件は維持する。
元profileMatch=trueの全所有WebView要求も維持する。child分類や別snapshotの件数一致で代用しない。
新しいcapability-shapeとobserver-failure-sitesは実測の型・観測箇所を示す追加記録であり、元timeoutの根因とはしない。
欠測・未知・失敗・profile不一致はfail-closed。native結果、profile証拠、observer完全性、cleanup、preflightを分けて報告する。
その結果が赤でも、追加push、rerun、dispatch、試行追加、gate緩和は行わない。

## pins・permissions・費用・保存

official checkout/setup-python/setup-nodeは既存の固定SHAのまま。tool source/patch/lock/hash/version/signatureも固定PR200の条件を保つ。
contents:read、credentials非保持、repository/user secretsなし、標準windows-2022の使い捨てrunnerだけを使う。
公開repoの標準computeは[GitHub billing仕様](https://docs.github.com/en/billing/concepts/product-billing/github-actions)の無料範囲。log/summaryはartifact storageに数えない仕様を確認した。
upload-artifact/cache、EXE/ZIP/encoded-binary転送、外部/livehost、larger/selfhosted runner、security/auth/credential/billing/persistent accessの変更は行わない。
既存main向けMac workflowのartifactも、このbaseのeventでは起動しない。
完全な匿名固定schema JSONとrecord/file hashをcontrolled log/job summaryだけへ保存する。新preflight recordを含めて既存の300000/600000bytesとline8000文字の上限を維持する。

最終combined headのsource/workflow/pins/permissions/profile/cleanup/preflight/保存境界を独立レビューしてから一度だけpublishする。
元run42037151371/attempt1のfailure、PR208/209のinconclusiveと未達、main/Mac stackは維持する。
