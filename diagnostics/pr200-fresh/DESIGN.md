# 第2段階の設計・未実行・DO NOT MERGE

## 保存済みの第1段階

PR208 head `9e9ae376d78b16934181d834598118c18069f0ec`、run42077571267/job127421749039/attempt1、artifact13439642688をそのまま保持する。
10試行の無再現はinconclusiveで、profileMatch=falseを停止gateに含めなかった未達を維持する。
初回6121ms、後続712–802msは単一runnerの連続観測であり、この差の根因は未確定。
元PR200 run42037151371のfailureを解除しない。

## profileの確認済みsourceと未確認runtime

- 固定crate `tauri-driver2.1.0` のSHA256は `2f2e29bd6900fb718dfdc06f2e7a371d6087c085daaf83709847ef3e6b0c1953`。
  その `src/server.rs` のWindows `into_native_object` は `webview_options` を `ms:edgeOptions.webviewOptions` へ値のまま移す。
- 固定 `webdriver.ts` は `tauri:options.webviewOptions.userDataFolder=NATIVE_PROFILE` を送る。
- [Microsoft capability仕様](https://learn.microsoft.com/en-us/microsoft-edge/webdriver/capabilities-edge-options)はこのfieldをWebView2 UDFの指定と定義し、returned capability `msedge.userDataDir` も記載している。
- 固定 `close-observation.ps1` は、CIM CommandLineの単一 `--user-data-dir` 値と要求pathをslash/case/trailing separatorの正規化だけで完全一致比較する。
  実UDF API、returned capability、実directory containmentの確認は行わない。
- [Microsoft UDF資料](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/user-data-folder)ではUDF内のsubfolderへの保存も説明される。
  これらのsourceだけでは、第1段階のfalseがsuffix、別profile、別の理由のどれかを確定できない。
  元の匿名JSONから失われたpathを復元したり、profile再使用やtimeoutの根因を推定しない。

## fail-closedな観測案

`profile_contract.py` はportableな判定契約、`observe-profile.ps1` はWindowsのread-only収集候補である。Windows実証は未実行。
native supervisorと15000ms request期限は変更しない。
別のread-only observerが起動前に要求profileの不存在・親の自己所有と非reparseを確認する。
実行中は固定driver/app pathとPID/CreationDateのlineageで所有WebViewを同定し、Windows CommandLineToArgvWでargvを取得する。
regexでquoted suffixを受理せず、欠測・重複flag・空集合・切詰め・identity不明は停止する。

実 `--user-data-dir` は要求root、厳密なEBWebView child、それ以外に分類する。
この分類を別々に保存する。単なるprefixや任意subfolderを通過させない。
厳密なchildも診断専用の観測であり、continueを許可しない。元supervisorのfalseとobserverのchildを同じPID/CreationDate集合として相関できていないためである。
directory実在・全ancestor非reparse・OSのcanonical path一致も必須とし、lexical比較だけでは通さない。
続行には固定supervisorのchildCloseFacts自体にある全WebView行がhandleIdentityVerified=true / creationKnown=true / profileMatch=trueであることを必須にする。省略行や欠測は拒否し、同じnative集合内の行数を検査する。別observerの件数一致を集合一致へ読み替えない。独立observerも要求rootへの完全一致と実directoryを確認し、既存の通常close・handle・snapshot・cleanup・listener監査も通った場合だけcontinue=trueにする。
false/unknown/欠測なら、当該単発trial後のcleanup証跡を保存して後続jobを開始しない。
Windows native収集・coverageの実装をレビューできるまでlaunchしない。

## runnerと起動境界

新branch `diagnostic/pr200-fresh-start-20261106` はmain420186cから分ける。
新workflow `.github/workflows/pr200-fresh-diagnostic.yml` と `diagnostics/pr200-fresh/` だけをPR差分にする。
旧PR208のbranchやworkflowへpushしない。新branchには旧ten-trial workflowを持ち込まない。
pull_requestのopened/synchronize/reopenedのみ、同一repo・このexact branch・draft・attempt1をgateにする。
source c6c5ae5342fcde6b1bf77eff744ad48ddce5b8da/tree e278496a55b4accc6caafa7a7a99538e5c8ceecaとcontrol exact headを分離checkoutする。

事前上限は3 observations。
trial1（build+一試行） → trial2（build+一試行） → trial3（build+一試行） の明示needs chainとし、各trialは別の標準 `windows-2022` job、一回だけ起動する。
前jobのsuccessとcontinue=trueを両方要求する。timeout、profile不一致、通常close失敗、回収不明、観測不備で後続をskipする。
本phaseはfresh runnerの最初の起動を測る実験で、fresh-machine間の完全な同質性や根因を保証しない。
各runnerが固定sourceから普通のreleaseとdriverをそれぞれbuildする。全jobでsource/tree/lock/tool sourceは同じだが、EXE/archive/driver hashを個別specimenとして保存し、binaryの同一性は仮定しない。

## executable transfer・storageは使わない

第2段階の確定範囲は各runnerでのrebuildであり、EXE/ZIP/artifact/cache/log encoding/public fileによるbinary転送を行わない。
全3jobは既存のlocked build/package検査をそれぞれ一回行う。
公開sourceのspecimenであってもusable distributionとして案内・uploadしない。

[GitHub billing仕様](https://docs.github.com/en/billing/concepts/product-billing/github-actions)ではpublic repositoryの標準hosted runner computeはfree。
artifact storageの利用枠は仮定せず、upload-artifact/cacheを使わない。
完全な固定schema匿名JSONをjob summaryとcontrolled log blockへ保存する。
record全体のcanonical JSON SHA256と各fileのcanonical JSON SHA256を付け、取得後に完全性を検証する。
記録全体は300000bytes以内、表示は600000bytes以内、各lineは8000文字以内を必須とし、truncateして合格へ進めない。
summary/logの上限超過、欠測、未知field、未知string、秘密/pathの混入候補はpublicationを失敗にして後続jobをskipする。
原ログ・原commandline・secret・payloadはこのblockへ含めず、binaryをencodingして転送する用途にも使わない。
logs/job summariesはartifact storageの対象外であるというGitHub仕様の範囲を使い、購入・billing変更・larger runnerを行わない。

## 追加観測と元verdictの分離

`capture-capability.mjs` はsupervisorのNode childだけへNODE_OPTIONS --importで渡す。
既存NODE_OPTIONSがあれば上書きせず拒否する。
元のfetch引数/AbortSignal/response/parsed value/例外を保持し、追加request/timeout/retryを設けない。
request中のuserDataFolderと環境要求値、returned msedge.userDataDirの固定2表現（flatまたはnested、同時存在は不明）を匿名分類へ変換する。
実pathやsession ID、payloadは保存しない。
戻り値観測と並行CIM収集には観測コストがあり、元runとのtiming無影響は保証しない。
普通のEXEにstartup-diagnosticsを有効化せず、元supervisor/native deadline/source/verdictは変更しない。
元のprofileMatch=falseもnative-result内にそのまま残す。

profileの成功は、capabilityもprocessも要求rootへの完全一致、fresh rootの不存在→作成、canonical実directory、非reparse、固定supervisorの正確な所有集合における全WebViewのprofileMatch=trueの全てを要求する。
child、任意のdescendant/sibling/default profile、相互矛盾、欠測は拒否する。
observer内のPID/CreationDate unionの件数は元supervisorとの同一性証明に使わない。allObservedWebviewsCoveredは固定native集合自体のprofile証拠だけから決める。
元verdictが成功でも、この独立証拠が欠測/false/unknownならcontinueを出さない。
## launch前の残項目

1. final workflow/control headの独立レビュー。旧ten-trialを起動しないtrigger、個別specimen、profile/cleanup/permissions、summary/log完全性と上限、停止条件を全て対象にする。
2. レビューと必要なlocal検証がそろった時だけ、新しい診断専用draft PRでlaunchする。
3. 最初の失敗・不明で停止。無変更rerun、試行差替え、成功するまでのretryを行わない。3回無再現でもinconclusiveでPR200はblocked。
