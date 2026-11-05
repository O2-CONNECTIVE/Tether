# 記録層の分類改善・ローカル限定

baseはPR209 head `cdc349b7129fdd273159f01e3a43e3901a60f5b6` / tree `8bf83fade2fa47f250a1c8462a04f72a97d621ad`。
本変更の実行許可はsource/fixtureだけで、push、PR更新、workflow dispatch/rerun、Windows起動を含まない。
新しい実測、timeout修正、profile同等性、blocker解除を主張しない。

## 分類できる情報と残らない情報

既存のprofile-capability/profile-observationのschemaと値は保持する。
別sidecar `profile-capability-shape.json` には、既知のenvelope/value/capabilitiesと固定2箇所の候補についてJSON型、候補の欠落/重複/非文字列/path拒否/有効flat・nestedを固定語彙で記録する。
任意keyの一覧や値、session ID、原responseは保存しない。
既存捕捉の失敗も固定6 siteで区別する。追加sidecarのwrite失敗は元captureFailedを変更せず、欠測またはstickyなshape-record-writeとして新しい証拠gateを閉じる。

`observer-failure-sites.json` はcatch時点の固定siteを記録する。
siteは「snapshot-query」「lineage-query」「process-handle-dispose」等の観測箇所であり、Win32 error、例外文、根因の分類ではない。
同じsiteの重複をまとめ、site最大16、catch回数最大4096とsaturation/truncation flagに制限する。
以前のqueryFailedは維持し、正常queryが後で得られても失敗を取り消さない。
型・語彙・上限の不正、矛盾、未知、sidecar欠測はcontinueを許可しない。
正常な旧証拠だけがそろっても、新しい分類証拠が欠ければdecision境界ではclassification-evidence-incompleteで続行を拒否する。
実collectorでmissing fileがrejectedへ入る場合は、既存wrapperがより広いevidence-observer-or-byte-integrity-failureを最終理由に保存する。どちらもclassificationComplete=false / continue=falseのままである。
この追加の停止条件と、decisionとcollectorの理由の粒度を区別する。

保持済みの実測fixtureにはこのsidecarが無いため、capabilityClassとobserverSitesはnot-retainedのままである。
欠落、重複、非文字列のどれだったか、どのqueryが失敗したかは依然不明。
六つのchild分類行と元native集合の同一性も未実証であり、childをrootとして受理しない。

## 保持するruntime境界

元fetchの引数/AbortSignal/response、元JSON読取りの値/例外を保持する。
fetchとjsonの実呼出しは各一回とし、追加request、payload読取り、retry、timeout変更を行わない。
sidecarには追加のローカル書込みがあり、fixtureでその順序と回数を明示する。timingへの無影響は保証しない。
固定PR200 source/supervisor/15000ms request deadline、profileMatch、所有判定とcleanupの手順は変更しない。
rowのdirectory照会は元の順序のまま、各呼出し直前のsiteラベルだけを付ける。
handle.Disposeの例外も元と同じように上位へ伝え、siteだけを分ける。
helper source読込み/Add-Typeの前段失敗やsidecar自体の書込み不能では、新記録が作れない場合がある。その場合も推測せず欠測として停止する。

## ローカル検証と未実行

Nodeの追加shape fixtureを旧installCapture境界で先に実行し、欠落/重複/非文字列の区別が無い赤（6件中1失敗）を確認した。
これは合成responseに対する記録層の赤→緑で、Windows timeoutの再現ではない。
実保持record/hash/各file hashの検証、元Native成功とprofile停止の維持は別のregression fixtureで確認する。

```sh
python3 -B -m unittest discover -s diagnostics/pr200-fresh -p 'test_*.py' -v
node --test diagnostics/pr200-fresh/capture-capability.test.mjs
actionlint .github/workflows/pr200-fresh-diagnostic.yml .github/workflows/pr200-fresh-single.yml
git diff --check
```

`observer-reasons.test.ps1` は同じsourceからledger関数のASTだけを取り出す合成fixtureで、CIM/Add-Type/supervisorを呼ばない。
このMacにはpwshがないため、PowerShell fixtureと新observerの実runtimeは未実行。Pythonのsite vocabulary確認は静的検査だけである。
新sidecarのWindows実収集、exceptionの実site、collector overhead、Node固定CI版での新処理は未実証。
元のWindows session-create timeoutの根因を調べるには、承認された別工程で実site/type証拠と再現可能なactual loopが必要である。

## hosted実行を起こさない受渡し

workflow、permissions、filters、gate、testsのskip設定は変更しない。
既存Go/Python/Mac workflowはmain向けpull_requestをpaths制限なしで購読する。
Japanese commit-message workflowはpull_request editedも購読するため、PR本文の完了追記だけでもhosted jobが起動し得る。
診断jobをbranch guardでskipできても、これらの無関係なjobの実行は防げない。

検査した全workflowのpush定義はmainまたは固定wip branch等に制限され、今回のlocal branch名へのbare pushに一致する定義は見つかっていない。
bare pushが必ず実行を起こすとは主張しない。一方、未検査のremote automationまで含む無実行保証は確立しておらず、このturnではremote publicationを許可されていない。
skip directive、workflow無効化、gate緩和で回避せず、review済みlocal commitとpatchで渡す。
PR209/208/200の本文・branch・既存runは更新しない。
