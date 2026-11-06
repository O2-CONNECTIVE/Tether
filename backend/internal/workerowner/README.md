# workerownerのsource境界

既存resident producerが使用する、同じworkerの取得・Run・Closeと要求source寿命の所有処理です。
標準libraryだけに依存し、通常caller・CLI・通信・鍵・認証・権限・常駐起動を追加しません。
後続の具体的callerはmaclaunchのresident worker adapterとresidentmac.Standbyです。
それらのpolicy/transport/Session実装は未接続です。

## このownerが保持するもの

RequestとWorkerは単回・不変で、取得中の参照、返された部分worker、RunとCloseの未終了を同じOwnerに保持します。
RunOnceは絶対期限・取消・既に始まったcleanupの元予算を延長しません。
Close成功だけでRun未終了を回収済みにせず、未知の取得、Closeエラー、未終了Run/Closeではslotを保持します。
Stopはhost admissionを不可逆に閉じ、StopRequestは同じbackend-owned Requestだけを停止します。
古いwaiterや切断は後続の別要求を停止・unknown化しません。
失敗したCloseを製品コードから再試行しません。
元からあるowner_test.goのretryCleanupはfixtureだけの補助手続きです。

WaitSourceは一つの認証済みregistrationに対応する呼出側のsource寿命を扱います。
source切断/EOFはそのsourceの元要求だけを停止し、host取消はownerを閉じます。
正常回収後の別要求は毎回別に承認される必要があり、Session再利用・再接続・grant更新ではありません。

## 認証と終了証明の残件

Request、Approve callback、context、deadline、Snapshotは認証・署名・grant authorityを証明しません。
信頼したnative callerが元の有限sourceとcanonical Request対応を保持し、既存protected policyで実際の承認を行う必要があります。
任意のUI入力からRequestを作る入口、任意Command/path/hash、trust root overrideは追加しません。

このpackageの完了判定は、Session.Closeの具体的契約とRun完了に依存します。
実EOF、exact Child Wait、IO/supervisor/outer monitor join、authoritative closedを実装・観測したことにはなりません。
unknown後も呼出側は同じOwnerとnative資源を保持し、別Ownerへの置換やprocess退出を成功として許可してはいけません。
このpackageだけでは所有process退出のguardやOS上の回収証明を提供しません。

## 検証範囲

5つの元試験fileと追加回帰はメモリ内Session・channel・goroutineだけのsource試験です。
取得・Run・Closeを意図的に止める負例でも、同じfixtureを解放して元cleanupを観測します。
資源なしのunknown記録とClose失敗の記録は保持されることを検査し、偽のnative成功へ読み替えません。
実producer、鍵/policy、host、GUI、配布やpublisher identityは未検証です。
全体の残る依存は[段階計画](../../../docs/mac-resident-source-plan.md)に記載します。

追加回帰はsource切断/EOFのcleanup待機中にhost取消を重ねます。
host取消を待機後にも再確認し、元cleanup contextの残り予算で同じOwnerのStopへ移ります。
予算を延長せず、確認済みとunknownの両方でadmissionを閉じ、元Closeを重複実行しないことを検査します。
