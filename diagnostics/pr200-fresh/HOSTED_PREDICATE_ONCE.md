# 拒否条件・観測箇所の一回限定診断

レビュー済みsource `3ed3a6b288d31517d4370cee1e7638933c76a418` / tree `27ac1b92b89965ccdfa2c21b58fc6b0ab7246558` を使用する。
patch SHA256 `c7ae352571c962e0716ac87aab3a18416bb4324c3a0fe1e0a3acb97caad693d5` を再確認した。
PREDICATES_ONLY.mdは直前のsource-only工程の記録であり、今回のhosted実行を示すものではない。

## 事前宣言した実行境界

新branch `diagnostic/pr200-predicate-once-20261106` のdraft/DO NOT MERGE PRだけを、未変更のPR211診断branch `diagnostic/pr200-classification-once-20261106` / base `bec16caeb28fade25b2ae3035a3ee7e6e6424d2e` に対して開く。
new workflowはopenedだけ、同repo・exact head/base branch/base SHA・draft・attempt1をgateにする。
旧PR208/209/211診断やmain用workflowをこのeventで再開しない。既存workflow/check/保護は編集しない。
一つの標準windows-2022 job、fresh runner一つ、native/supervisor最大一回。matrix、後続job、dispatch、retry、再pushによる追加観測は設けない。
失敗や未知のまま停止し、この工程中はpreflight修正後の再runも行わない。

固定app sourceは `c6c5ae5342fcde6b1bf77eff744ad48ddce5b8da` / tree `e278496a55b4accc6caafa7a7a99538e5c8ceeca`。
ordinary release/custom-protocolを既存locked条件で一回build/package/verifyし、source/tree/EXE/archive/tool identityを保存する。startup-diagnosticsは有効化しない。
新buildは元失敗binaryの再演ではなく、以前のspecimenとのbinary等価性も仮定しない。Python source-pin fixtureは既知のLF/CRLF両checkoutだけを検査し、CRLFを二重変換せず、runtime mapperの受理条件を変えない。

## 必須PowerShell preflight

MacではPowerShell parser/runtimeを実行していない。
Windows上でparse-predicate-sources.ps1がobserve-profile、observer-detail、両fixture、auditの5 sourcesをparseする。
次に変更版observer-reasons.test.ps1、observer-detail.test.ps1を各一回順番に実行する。ledgerの匿名化fixtureも新しいdetailsを十分なJSON depthで検査する。
各processは60秒上限で、Python subprocess.runのtimeout時の所有child kill/waitを使い、次へ進まない。
いずれもOS/CIM/native起動を行わない合成fixtureで、旧PR211のledger5成功を流用しない。
新preflight schema2はparseと二fixtureのruntimeExecuted/exitCode/pass/codeを固定schemaで記録する。parse5とfixture2の全成功だけがbuild/nativeへの必要条件となる。
stdout4096bytes以下のexact marker、stderr空、exit0が必要。private log/exception/pathは出力しない。未実行stageはnot-runを残す。
旧schema1は保持記録用に読むだけで、新trial/publish continuationには使えない。
preflight/pin/permission/costの未知または失敗ならnative0回で停止する。

## 元gateと保存

元supervisor/request/response/error/AbortSignal、15000ms deadline、owned identity、全owned WebView profileMatch=true要求とhandle disposal/cleanupを維持する。
新分類も閉じたenumだけで、LF/CRLF二つの既知source hash以外や未知stack/predicateをfail-closedとする。
EBWebView childをroot成功へ読み替えず、別observerと元集合の対応を推測しない。
validator拒否はOS path不正の証明ではなく、source位置はOS例外原因ではない。
通常closeとcleanupは別に報告する。PR200 blockerをこの一回で解除しない。

sanitized fixed-schema JSONとhashだけをbounded controlled log/job summaryへ保存する。raw path/PID/commandline/stack/response/exceptionやarbitrarykeyは出力しない。
upload-artifact/cache/EXE/ZIP/encodedbinaryの保存転送は設けない。failed candidateはdistributionにしない。
public repoは実行前に再確認し、standard public runner computeとlog/summaryが追加storage枠を消費しない範囲だけを使う（https://docs.github.com/en/billing/concepts/product-billing/github-actions）。
secret、新credential/persistentaccess、livehost/larger/selfhosted、security/auth/settings/billing変更や購入は行わない。
公式action SHAと既存tool/source pinsを固定し、contents:read/credentials非保持を維持する。

元run42037151371/attempt1、PR208/209/211の結果、PR21115-file record hash `e922423db73c2743e04a5a0ebc32b095b62392f4019d3a4346e3e8929d319dd3` はそのまま保持する。
新しい観測から破棄済みfactsを補完しない。main/Mac/product/fixed sourceを編集せず、approve/ready/mergeしない。
