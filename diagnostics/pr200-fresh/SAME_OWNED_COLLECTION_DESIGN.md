# 同じowned collectionのprofile対応を保つ最小設計

**source-only設計。hook実装・native実行・acceptance変更は今回行わない。** PR220のnative成功/normalclose/cleanup、元timeout、exact-root、capability、all-lifecyclequeryを別の命題として扱う。すべての既存gate/失敗/15000ms/request/response/error/AbortSignalは維持し、PR200はblockedのまま。

## 現在の証拠が要求している強さ

| 命題 | 現状と限界 |
| --- | --- |
| 元session timeout | 元run42037151371/attempt1の赤は不変。PR220の5334ms成功はclearanceではない。元binaryなし。 |
| 通常close/ownedcleanup | PR220で成功。profile同等性やall-lifecycle観測完全性とは別。 |
| commandline exact-root | 元6WebViewprofileMatchfalse。root/EBWebViewの通常layoutでもこのgateは通さない。 |
| returnedcapability | nestedstring/drive-prefix。WebView2固有の非空drive絶対/root表記保証は確認できず、string subclass不明。 |
| all-lifecycleobserver | startup/teardownを含み得る失敗を一度でも観測すればstickyqueryfalse。PR220の発生phase/orderは未保存。 |
| same-owned-set対応 | 独立6rowsと元6rowsの件数は証明にならない。公開slotは生identityを含まず、observer tracked.Valuesはprivatekey/orderを落とす。retroactivejoin不可。 |

[公式EBWebViewlayout](https://github.com/MicrosoftDocs/edge-developer/blob/8f4be4dcd43226dff0ee1131be8b504a29bf8ef7/microsoft-edge/web-platform/devtools-mcp-server.md#L408-L414) と[返却capability](https://github.com/MicrosoftDocs/edge-developer/blob/8f4be4dcd43226dff0ee1131be8b504a29bf8ef7/microsoft-edge/webdriver/capabilities-edge-options.md#L85-L104) は別契約。COMactualUDF保証をEdgeDriverへ移さない。通常layoutと比べ現診断gateは意図的に強い。これをnormal behaviorに合わせて緩めることはevidence修理ではなく、別の明示policy決定を要する。

## 選択肢

1. **推奨: opt-in in-process read-only capture。** 元Get-OwnedAppが返したcollectionと元のhandlesを同じ呼出しで受け取る。rawidentityをmemory内だけで照合し直接分類、公開は同じslot関係だけ。第二ownedset推定もrawsidecarも不要。
2. **privateledgerを外部observerへ渡す。** 元collectionのslot+privateidentityをauthoritativeにemitすればjoin可能だが、元channel追加、別process/timegap、partialfile、寿命・回収の責任が増える。単なる独立snapshot、PID/name/count一致は不可。Aより小さくも強くもない。
3. **返却capability/独立pollのみ、またはCOMactualUDF追加。** 前二者はsamecollection証明を持たない。COM追加はproduct/WebView側の変更を要しfixedordinaryspecimenを変え得るので、この最小案から除外する。

Strictにruntimeinstrumentationを不変としたまま、既存publicoutputsからmissingjoinを作る方法はない。close-observation wrapperやcallback経由でもinstrumentation境界とpreclosetimingは変わる。将来実装には別の明示scope・最終source review・source/driver/specimenhashを要し、旧supervisorと同じbytesで呼べても「unchanged observation」とは表示しない。

## authorityとstablepoint

[固定supervise.ps1 L78–92](https://github.com/O2-CONNECTIVE/rescue-host/blob/c6c5ae5342fcde6b1bf77eff744ad48ddce5b8da/ui/tauri/native-smoke/supervise.ps1#L78-L92) のGet-OwnedAppだけを所有判定元とする。元snapshot/observation.Childrenをfreezeし、`$children`全体の順序を維持。WebView subsetをrenumberしない。元Open-ChildObservationHandlesのSlotも全childrenと同じindexで照合する。

capture候補点はL85のhandle取得後からL92のclose要求前まで。apphandleもborrowするなら元L89–90の取得/Refresh後を選ぶ。開始/終了の両方で元collectionの各borrowedhandleのidentityと生存を確認し、途中exit/照会不能/重複はexited/unknown。Get-OwnedAppの再実行で集合を選び直さず、newlydiscoveredchildを混ぜない。「この元記録集合のcapture区間」をstablepointと呼び、全未来processや全lifecycleが安定していたとは主張しない。sessiondeadlineは既にsettle済みでも、付加処理はpreclose timingを摂動するため、別の短い上限と超過unknownをレビューで固定する。

## identity lifetimeとread-only条件

[元handle取得 L6–27](https://github.com/O2-CONNECTIVE/rescue-host/blob/c6c5ae5342fcde6b1bf77eff744ad48ddce5b8da/ui/tauri/native-smoke/close-observation.ps1#L6-L27) はPID/path/creationを照合するがHasExitedを要求しない。追加分類はoriginalhandleがverifiedでもliveを推定せず、live-before/live-afterを別flagにする。[Windows handleはexit後もcloseまで有効](https://learn.microsoft.com/en-us/windows/win32/procthread/process-handles-and-identifiers)であり、validhandleだけでは生存証明にならない。

borrow中にoriginalprocessobject/handleをreopen、kill、dispose、並べ替えしない。originalownerのcleanupを変更しない。安全なmanagedhandle借用scopeをpairedreleaseで管理し、newprocesshandleを作らない。元CIMrowのPID・ExecutablePath・CreationDateと同じhandleのquery結果をprivateに照合し、creation精度差は既存変換契約で一致を確認できなければunknown。PIDだけでlookupし直さない。名前はroleラベルにのみ使い所有証明には使わない。

[GetProcessTimes](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-getprocesstimes) はhandleからcreation時刻を取得し、[query limited rights等](https://learn.microsoft.com/en-us/windows/win32/procthread/process-security-and-access-rights)を要する。既存borrowedhandleにread権が無ければunknownで停止し、権限grant/privilege/ACL変更やPROCESS_ALL_ACCESSへ拡大しない。driver/appの既存所有・liveness判定を置き換えず、edgeについて存在しないoriginalhandleを借用済みと主張しない。

元ownedrowに含まれるargvを一回だけprivateにparseし、同じslot/identityへ結ぶ。absent/null/duplicate/split/quotedambiguityは各closedstatus、原値やexceptiontextは出さない。別pollのargvや親profileを補完しない。alreadyexited/reusedなら対応unknownで、現在同じPIDのprocessへ転用しない。

## rootとruntimechild、directoryの保証範囲

lexical分類はrequested-root / exact-runtime-ebwebview-child / outside / unknownを別値のまま保存する。rootとchildをcanonicalに同じdirectoryと呼ばない。childnormalityはacceptanceではない。

現Non-Reparse-Directory/Resolve-Pathはsequentialpathchecksで、atomicnamespaceidentityproofではない。強いcaptureを主張するなら、所有temp祖先からroot/直接childまでread-onlydirectoryhandlesでmetadata/reparse/canonicalidentityをboundedに検証する別設計が必要。[GetFinalPathNameByHandle](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-getfinalpathnamebyhandlew) はhandleに対するpathを返し、[reparseの特別なopen条件](https://learn.microsoft.com/en-us/windows/win32/fileio/reparse-points-and-file-operations)も必要になる。

提案する保証は既存read権で開けたdirectoryに対する前後整合だけ。reparsepoint/alias/volume差/parentchain欠測/renameの検出不能をunknownにし、全namespaceの敵対的同時改変に対するatomicproofは主張しない。volume/fileidentity等のprivate比較を採る場合も公開は一致boolだけとし、実API/精度/parent-child束縛/threatmodelを別reviewする。path再読だけでracefreeと言わない。newdirectoryhandlesはcaptureのfinallyで全disposeし、cleanupへ持ち越さない。dispose不確実もunknownで閉じる。

## 最小公開schema案（設計のみ）

top-level: source/control/specimenhash、capturepoint enum、originalChildrenCount、rows上限32、omittedCount、captureComplete、sameCollectionBound、directoryGuarantee enum（sequential-only / handle-checked-at-boundaries / unknown）。

row: **元all-childslot**、元parentSlot、roleenum、originalHandleVerified、snapshotIdentityBound、liveBefore/liveAfter、argvStatus、lexicalMapping、root/childDirectoryChecksComplete、reparseFree、canonicalRelationConfirmed、errorCode（identity/creation/exit/argv/directory/limit/unknown）。boolもunknownが必要な項目はnullableにする。任意keys/strings/rawpath/command/PID/creationtime/stack/exception/識別子hashを出さない。schemaenum/size/rowboundsは固定し、32超のomitted、duplicate/shuffledslot、missinghandle、partialcaptureはcompletefalse。件数一致をsameCollectionBoundの根拠にしない。

元normalclose/native/profileMatch/cleanup verdictは一切書き換えない。新captureはparallelなevidenceであり既存continuegateへ代入しない。startup/teardownのsticky失敗ledgerも全保持する。あとでpolicyを変える場合は「same集合に対するroot-child対応をどの受入命題へ用いるか」を明示し、旧失敗・原timeout・未達の意味を保存する。

## 実装前に必要な検証と今回の境界

- originalset/slot保持、subsetrenumber/並替え/重複の拒否。
- PIDreuse、creation精度/照会失敗、verified-but-exited、capture途中exit、newchild混入、missing/duplicateargv。
- root/直接EBWebViewchild/sibling/nestedchild/outside、reparse/alias/rename/partial/omission、directoryhandle全回収。
- getter/read一回、元request/error/AbortSignal/15000ms/通常close/cleanup保持、hook失敗で元failureを上書きしないこと、付加遅延上限/unknown。

今回のpurepollfixtureはsnapshotguardとstickyfailureのsynthetic意味だけを検証する。realownership、directoryproof、実PR220chronology、samecollectionjoin、元timeoutcauseを証明しない。hookは未実装でありnativescopeを自動で開始しない。受入変更前に上記design/fixture/独立reviewと、明示許可された一回だけのbounded実証が別途必要。
