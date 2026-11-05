# PR211 後の分類細分化（ローカルsource-only）

対象baseは `bec16caeb28fade25b2ae3035a3ee7e6e6424d2e` / tree `0a5a829b527194adb60e5c68110dbc8b19119b89`。
この変更は診断evidenceとfixtureだけを扱う。workflow、product、固定PR200 source、native supervisor、Mac laneを変更しない。
push、PR編集、hosted/native/Windows実行、tool導入はこの工程に含まれない。実行済みの各runの判定は変更しない。

## 分類の境界

`path-validation.mjs` はPR211のnormalizeから同じ順序のpredicateと同じ正規化結果を取り出した。
最初の拒否だけを `non-string` / `drive-prefix` / `forbidden-character` / `extra-colon` / `empty-component` / `dot-component` / `trailing-dot-or-space` で分類する。
capability candidateの非stringは既存shapeで分類し、pathPredicateはnot-applicableとする。
acceptedは既存validatorが受理した意味だけで、OS pathの妥当性やprofile同等性を意味しない。
shape schema2にpathPredicateを追加し、legacy profile-capabilityの判定・書込み回数とfetch/response/json/error/AbortSignalを維持する。

observerのmetadata複合guardを同じ短絡順の二つのifへ分け、creation checkの例外/拒否、CommandLine readの例外/拒否、identity-keyとtracking checkの例外を区別する。
どちらのguardも同じPROFILE_IDENTITY_UNKNOWNをthrowし、creation checkで止まればCommandLineを読まない。
元Get-OwnedAppを同じ引数（LineageEvidence=nullを含む）で呼び、native helperを編集・再実装・wrapper置換しない。

lineage例外はScriptStackTraceの先頭二frameまでを、hash一致の固定processes.ps1の既知function/行だけへ対応づける。
sourceのSHA256はLF bytesの `a54b23fea4ed7bfc951701f2c2bee50bd3d414ca850915d5b01626670f086303` と、全LFをCRLFへ置換したbytesの `8667b375c8ba70aa06b36031888449ea796092cea2868bda16bf8709369bd72f` の二つだけを信頼する。
二つのhashと行table不変性はsourceから合成的に計算して検証する。これはPR211でprocesses.ps1の改行bytesを実測した意味ではない。任意の改行/BOM等を正規化して許すfallbackは設けない。
raw hashがこの二つと不一致、欠測、別source、16384字超/32行超はunclassified-detailのまま閉じる。
Get-OwnedApp/Get-Descendantsの直接statement分類は既知の先頭frameだけを必要とし、後続callerは分類に用いない。Stop-UnconfirmedLineage/Require-Live経由の分類は先頭と直後のcallerの両frameを必要とする。それぞれ必要なframe/function/行が未知ならunknownのまま閉じる。
これらのcodeはsourceのguard/call/check statementの位置であり、例外typeやOS原因を示さない。同じ行内の演算のどれかまでは分類しない。
path・stack・行番号・commandline・PID・exception text/typeは出力しない。公開可能なledgerはschema2の固定site/code pairのみで、count4096、sites/details各16、飽和/省略flagを維持する。
既存catchでqueryFailedをstickyにする動作、process handleのopen/dispose/finally、finish/cleanup順は維持する。

旧schema1は保持記録の読取り用にだけ受理する。新continuationにはschema2が必要で、missing/unknown/矛盾/飽和/省略はfail-closed。
新predicate/detailが揃っても元native/profile/cleanup gateは必要で、EBWebView childをroot成功へ変更せず、集合対応も推定しない。
15000ms deadlineとPR200 blockedを維持する。

## fixture と実行した境界

`fixtures/pr211-trial1-record.json` は実保持15-file recordそのもので、canonical28488bytes / SHA256 `e922423db73c2743e04a5a0ebc32b095b62392f4019d3a4346e3e8929d319dd3`。
全15 file hashを再検証し、当時のclassificationComplete=trueを含むtrial.jsonは変更しない。
新readerでは未保持predicate/detailをnot-retainedと読み、当時のcandidate-invalid-pathやsite8件から復元しない。PR209の実保持fixtureも不変。

新Node fixtureは合成入力15972個をPR211のverbatim normalize bodyと比較し、正規化値・受理/拒否が一致することと、最初の拒否順・非string非coercionを確認する。
実capture seamではpredicateと元request/signal/response/value/error、1 fetch/1 JSON readを確認する。これは破棄済みresponseの再構成やWindows timeout再現ではない。
Python fixtureは実record不変性、旧schema/未知/矛盾による拒否、detail pairの語彙/相関/サイズ、固定source hash/行tableを確認する。
observer非evidence部分は、追加分類コードを除き二つのguardを元表現に戻した静的projectionでPR211 sourceと一致した。これはruntime証明ではない。

```sh
node --test diagnostics/pr200-fresh/*.test.mjs
python3 -B -m unittest discover -s diagnostics/pr200-fresh -v
```

ローカル結果はNode v26.13.0で13 tests成功、既存Python3.13.2で29 tests成功。CI固定Node24.22.1/Python3.13.17との差を残す。
初回の新Node fixtureは未実装moduleで非0になり、実装後greenとなった。分類境界のfeedback loopであり、元timeoutのred/greenではない。
既定python3=3.9.6での初回成功は正本の検証に用いず、既存3.13.2で再実行した。toolは導入していない。
既存actionlintとdiff checkも実行する。workflow変更やCI実行はない。

PowerShell runtime/parserはMacに無く、旧ledgerの変更版と新 `observer-detail.test.ps1` は**未実行**。
新fixtureは固定合成stackの位置map、未知/不信source拒否、ledgerの重複/漏洩/上限、actual metadata guard textの合成getterでの短絡・error動作を確認する設計で、OS/CIM/native helperを呼ばない。
PR211で旧ledger5 checksが実行成功した事実は、この変更版fixtureの成功へ引き継がない。
このfixtureを既存preflightへ自動追加せず、hosted実行が許可されたときにそのpreflight/workflowも改めてレビューする。

## 残る証拠と次の判断

PR211の実値がどのpredicateで拒否されたか、8 observer失敗の具体的statement/metadata guard、raw例外原因は依然不明。
このsource-only工程から元timeoutの根因・修正・profile同等性・blocker解除は成立しない。

追加runtimeを別途判断する場合の最小案はfresh標準Windows runner一つ、先に変更版PowerShell fixturesを実行し、通過後だけ元supervisor/native最大一回で新enumを取得するcap1。
これによりvalidator拒否predicateと観測失敗statementを区別できる可能性がある。stack形式が対応しなければunknownのまま停止する。
その実行には新しい明示的判断と最終workflow/source reviewが必要で、ここから自動実行・公開しない。
