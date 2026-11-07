# 単回の判定後配送診断: control

このcontrolはPR251の既存workflowを別実験として置換する。以前の実行予算を再使用しない。
新sourceは `cad02a5b6fefd3ac11a4b8b45758feb438bf024b`、treeは `3bb45eb8c459761898bfff160a4931ed895e4463`。
実験は元の15秒要求とnative失敗を維持し、別の閉じたcausal recordだけを付加する。
古い12-section recordのdeliveryは、source文書に記載したanchor位置の明示的ordinal projectionであり、旧sourceの未変更記録とは呼ばない。

## 固定対象と検証

source全tracked treeをHEADと照合する既存source_identityを維持する。Cargo.lockだけは既存の厳密な二vendor projectionを適用する。
新しい読取対象は次の通り。部分allowlistでこれ以外の変更を許す意味ではない。

- `ui/tauri/src-tauri/src/{startup_diagnostics,wry_delivery_probe,post_verdict_signal}.rs`
- `ui/tauri/native-smoke/{supervise,post-verdict,post-verdict-contract.test,post-verdict-stop.test}.ps1`
- `scripts/{post_verdict_failure,wry_post_verdict_record}.py`
- `scripts/tests/{test_post_verdict_failure,test_post_verdict_signal,test_wry_post_verdict}.py`

controlは `scripts/post_verdict_anchor.py`、`scripts/post_verdict_publish.py`、`scripts/post_verdict_engine.py`、対応testsとworkflowを含むcommit全体へ束縛する。
48個の固定test IDとそのdigestを閉じたqualification receiptへ記録する。実際の開始・成功IDが完全一致し、skip等がゼロの場合だけ受理する。
出力はInvoke-ProbeStepのredirect外で公開し、公開成功後のreceiptをnative admissionで照合する。
既存prior-red helperの試験はtracked `scripts/tests/fixtures/prior-red-workflow.yml`を使う。過去git objectを持たないdepth1 checkoutでも同じ試験ができる。

## 公開結果と停止

publisherは現在のGITHUB_RUN_IDとattempt1、prelaunch admission receiptのmanifest digestを再照合する。
publisherは元のnative/timingとFlush(true)済みcopyのbyte一致、closed failure receipt、signal、manifest、source/control/specimen、companionとdeliveryの一致を独立検証する。
nonceやpathを公開しない。digestは専用の信頼するCI job内のbindingであり、同一ユーザー攻撃者への認証ではない。
成功send-after-verdictの片側証拠だけを表し、callback関数entryの時刻、根本原因、修正、HTTP回復、無期限stallを推定しない。
元の失敗exit1をlate completionで成功へ変更しない。欠測・不一致は有限のunknown。

workflowはWindowsの協調取消fixtureと、既存PowerShell7.6.9固定のowned child engine-stop fixtureをnative前に実行する。
この閉じた試験はrunner→shell→childのCtrl-C伝播やhard-kill時のfinally実行を証明しない。
固定engineの6case receiptをstrict型/schema/version/順序/時間上限/source/controlで再検証し、redirect外で公開する。
公開後のdigest receiptとPython qualificationの両方がない場合、native admissionは拒否する。
この実Windows preflightはMacでは未実行。ローカル実装はpushやnative実行の許可ではなく、固定head reviewは別gateとして維持する。
予算は新規native最大1回、job35分、失敗後の新規処理は単一10秒協調budget。healthy/unknownを含め全結果で停止し、自動再試行しない。
