# PR213 assertion のfixture-only観測

実行対象の固定sourceは `43f56b87574f57cc87c1d0347b609499d896c222` / tree `e78c82786c440f76414ab55939efb5fa7efbbe50`。
PR213 base `5503d59a0b71c65c00b31d155a6e7c0f3582bc64` からの完全patch33818bytes/SHA256 `f692839091fec12a079041213edcc1ad9e6de0b7dc3f23557c26a31d0ca7a5c5` を再確認した。
元assertion sourceの独立reviewは指摘0、39Python/13Node/actionlint/diff成功。実PowerShell failure原因は未証明である。

## 事前宣言する一回の範囲

新draft/DO NOT MERGE PRのbaseは未変更PR213 branch `diagnostic/pr200-predicate-once-20261106` / `5503d59a0b71c65c00b31d155a6e7c0f3582bc64`。
headは `diagnostic/pr213-fixture-only-20261106`。opened-only・同repo・exact head/base branch/base SHA・draft・attempt1をgateにする。
旧diagnostic/main向けworkflowとそのchecksは変更せず、このeventで起動しない。
標準fresh windows-2022 runner一つ・job一つで、固定sourceを別checkoutする。
parseを一回、ledger/detail fixtureを各最大一回、元のfail-closed順で呼ぶ。早いstage失敗なら後続を実行しない。
失敗後の修正push/rerun/dispatch/別attemptや自動再試行は行わない。

workflowのstepはcontrol checkout、固定fixture source checkout、固定Python setup、recorderの四つだけ。
app/frontend build、Rust/toolchain setup、native-driver準備、app launch、native/supervisor trial、配布物作成を含めない。成功後にもこれらへ進む経路は無い。
既存standardrunnerのPowerShellを使い、job shellとPATHのpwshが同じapplicationと確認してversionを渡す。新しいPowerShell等を導入しない。
recorderPythonだけを公式setup-python SHAで3.13.17へ固定する。

## 固定sourceと証拠

recorderはcontrol head/tree/clean、固定source head/tree/cleanを照合し、八つの固定helperのbytesがGit blobそのもの又は全LF→CRLFのexact variantであることを確認してhashを記録する。
その固定checkoutからpowershell_preflight/reportのdefinitionsだけをfresh Python processへimportし、固定preflightを一回呼ぶ。他のapp/native dispatcherをimport・呼出ししない。
parse対象五file、ledger/detail合成fixtureの条件・marker/throwはレビュー済みsourceのまま。
個々のPowerShell process60秒上限とPython subprocess.runの所有child kill/wait、元停止順を維持する。
sourceを実行後に再照合し、変更・未知ならpassと扱わない。partial preflightが既に書かれていれば再実行せずschema検証して保存する。

完全recordは固定schemaのidentity.jsonと（実行され検証できた場合の）powershell-preflight.jsonを含め、各file/canonicalrecordのSHA256をcontrolled log/job summaryへ保存する。
record上限30000bytes、表示60000bytes、一行2000字。raw log/path/PID/command/stack/exception/response/arbitrarykeyは保存出力せず、fixed enum、hash、数字版だけを許す。runtime Python/PowerShell版も記録する。
原fixtureのknown assertion enumを保持し、unknown/missing/conflicting/truncatedはそのまま失敗とする。guard未確認時もboundary-rejected等の固定outcomeとnull/falseを残す。passを補完しない。

public repositoryを実行前に再確認し、標準publicrunnerとlog/summaryだけの範囲を使う（https://docs.github.com/en/billing/concepts/product-billing/github-actions）。
contents:read、checkout credentials非保持、公式action固定SHA、user/repo secrets無し。artifact/cache/binary転送、new spending、credential/persistentaccess/security/auth/settings/billing変更なし。

元run42037151371/attempt1、PR208/209/211/213 evidenceとPR200blockedを維持する。
PR213の実record2files/canonical1551/hash `d219204d88fc45cfcb2053a60051ebcd582770dfb7aceaf9c8c9d8f2ffc0d152` へ新factsを埋め戻さない。
このfixture-only結果は元timeoutの再現、profile同等性、通常close/owned cleanupの成功を示さない。15000ms/profileMatch/cleanup/native条件も編集しない。
