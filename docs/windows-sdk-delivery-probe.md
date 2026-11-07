# 同じcreation／receiverに対応するSDK completion phaseの限定診断

診断専用・DO NOT MERGE。基点f8f322fの[一次資料とlocal model](windows-callback-wake-research.md)を引き継ぐ。元#240の失敗窓のprefixは未観測であり、#245の終了済み3成功を修正扱いにしない。

## 実行前の一問と予測

実SDK completion closureのentry／元send／guard-dropの時点で、対応するenvironment/controllerの**同じcreationと元Receiverのwait**はどこにいるか。

| 捕捉結果 | 限定した意味 | 次に許される解釈 |
| --- | --- | --- |
|同じcreationのSDK API bracket|callback receiptはAPI bracket内。元Receiverはその後同じcallsiteでwaitへmoveされる|inlineかAPI内部のpumpかは未分離|
|同じ元waitのGetMessage bracket|callbackはGetMessageの前後hook間でreceiptされた|内部nonqueued dispatchの候補。instruction pointerやFFI returnの証明ではない|
|同じ元waitのDispatchMessage bracket|取得messageのdispatch前後hook間|このinstanceのreceiver再検査経路。SDKの普遍的posted保証にはしない|
|outside／unknown／nested／thread不一致|上記へ強制分類しない|欠測や別invocationを時刻でjoinしない|

成功send後もGetMessage bracketが続く失敗窓ならwake仮説の次の識別材料になる。正常returnしたinstanceのGetMessage内callbackだけでは根因ではない。slow completion、未到達、controller後の初期化も残す。

## 最小のsource境界

Wry0.57.1の二つの実creation関数でprivate tokenを一度発行し、同じchannel producer closureへCopy tokenをcaptureする。元Receiverは同じscopeのwait wrapperから**元のwait_with_pump**へmoveする。二つの公開slotはenvironment/controllerだけで、token・OS thread ID・private memoryは出力しない。

固定webview2-com0.39.1へdiagnostic moduleを追加する。const thread-local Cellがscope／API／original-wait／GetMessage／Dispatchのbracketを保持し、callback guardがそれを読む。unwrapped／nested pump、重複scope/wait、token/thread mismatchはunknownへ倒す。元SDK call／channel send／Result／GetMessage／Translate／Dispatchを各一度だけ評価し、error変換を保持する。新しいwake、wait代替、timer、poll loop、強制順序は加えない。

scope entryでcurrent-threadのapartmentをread-only分類し、callbackの同scope-thread boolと区別する。callback時点の連続apartment保証にはしない。追加Win32 readはLastErrorを保存・復元する。critical phaseはTLSとatomicだけを使い、blocking lock、file I/O、stack/suspendを行わない。追加命令とqueryのperturbationは明示する。

Hostの別固定storageは2 scopes×3 samples（closure entry／send result／closure guard-drop）、capped countersとloss/ambiguityを保持する。guard-dropはFFI returnの証拠ではない。重複／overflow／書込中seal／missing／途中終了は完備扱いしない。既存workerで既存Host traceのfile closeより先に匿名JSONを保存し、元verdictと元8markerを置換しない。
新storageは固定サイズをprocess寿命だけ保持し、hook内で最後のArc解放やallocator呼出をしない。scope entryのapartment読取はcallback時点の連続保証ではない。sendSucceededはchannel送信の成否で、SDKのinner Resultではない。wait/scope guard-dropも成功returnではなく、元8marker／元Native verdictと区別する。
current-threadの[InSendMessageEx](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-insendmessageex)はsent-context／not-sent-context／unknownの固定分類だけを保持する。raw flags／messageやsenderは保持しない。0はposted配送の証明ではなく、nonzeroもSDK自身の配送APIを特定しない。unknownは分類完備を満たさず、元Native verdictは別に保持する。
各recorderは別の時計と順次sealを持つ。新recordのsealElapsedUs内だけを比較し、Node deadline時点のstackや連続停止を推定しない。後から届くcallbackと欠測の窓を明示する。

## 検証と一回の上限

公開archive/lock/vendor hashesとinverse projectionを固定する。実overlay module＋元pump body＋実Host recorderを通る純fixtureでAPI/Get/Dispatch/outside、nested/wrong token/other-thread、missing/duplicate/loss/seal/途中exit、error/send/receiver identity、元source復元とmutationを検査する。local simulated deliveryとWindows/SDK実行を分離する。

最終source/controlのStandards／Spec独立review、required preflight、public標準runnerとremote重複確認の後、一つのfresh windows-2022 job、attempt1、native最大1だけ。元request/15000ms／profile／ownership／normal-close／cleanupを保持する。preflight不合格はnative0で停止。欠測／未知／identity／cleanup不確実、または一回終了で停止し、rerun/cap拡張しない。

source/vendor/EXE/archive/driverの実hashと完全な固定schema記録のみをcontrolled log/summaryへ保持する。artifact/cache/app binary転送、raw paths/commands/messages/URLs/stack/memory、secret、追加credentials/security/billing、larger runnerなし。診断sourceは元失敗binaryと同一ではない。#240、main#239失敗、#240→#241→#243／Mac受入／a4/a5/task-7の境界は維持する。

## publication前のsource fetch gate

#246はreview済みsourceがcontrolのsiblingであり、source refをpushに含めなかったためcheckoutで停止した。nativeは0で、過去の失敗を置き換えない。source payloadは`bf33feb738688f72d8ea6e23ff9171d35a5bb564`／tree `29cd44b55f1390de3dd0b8bcb5f5af216a2f1ee6`のまま固定する。

新controlをpublishする前に`python3 -B scripts/check_wry_delivery_fetch.py`を実行する。guardはworkflowのsource SHA/tree/refと固定pinを照合し、空の一時repositoryから同じHTTPS remote・full SHA・depth1・no-tags・no-submodulesでfetch／checkoutする。既存object store／alternates／Git credential/configは使わない。匿名public readは同repositoryのcontents read範囲であり、消滅済みのhosted GITHUB_TOKENを再利用したとは主張しない。sourceを含むrefの存在やlocal cloneだけでは代用しない。省略sibling→fetch失敗、publish後→正確なcommit/tree成功を独立bare-remote回帰で検査する。

guard成功・重複runなし・最終control独立review後に、別のopened-only／attempt1 controlで新たなfresh runner一つ・native最大1だけを認める。元#246はrerunしない。source fetchの修正も実SDK配送やtimeout根因を証明しない。

fetch gateのsubprocessは空の一時HOME／USERPROFILEを使い、netrc・askpassの環境overrideも除く。Git設定とcredential helperだけでなくlibcurlの暗黙のhost認証入力を除外し、匿名public readの記録と一致させる。fixtureは合成netrcと継承環境を渡し、実fetchを呼ぶ境界で隔離を検査する。実個人の認証情報は読み取らない。

## 元の一回のreceiver pollを区別する追加観測

feedback-loop改善段階。元timeoutを高速・決定的にredへするloopは未達であり、本変更は修正ではない。元の`rx.try_recv()`一回が返すResultをidentity adapterへ渡し、同じResultをそのまま元`if let Ok`へ返す。Ready / Empty / Disconnectedの三分類だけを記録する。Disconnectedでも元どおりGetMessageへ進む。二回目のreceive、peek、wake、timer、retry、sender/receiverの追加所有はない。

既存token・元wait・thread照合がすべて証明できたenvironment/controllerだけを対応づける。nested／unbound／wrong-token／other-threadはunknown集計にする。unknown bucketは単一receiverやscriptを識別しない。2 scopes＋unknown ×3分類の固定atomic counterのみを持ち、0=未観測、1=一回、2=二回以上で飽和する。回数飽和はlossや他状態の証明ではない。concurrent writerを待たずlossへ倒し、seal途中のwriter、registration欠測、scope ambiguity、未完了のcollectionは元wait対応の確定に使わない。既存のLastError保存・復元の内側で記録する。

既存collectorが元file closeより前に別の有限record `wry-receiver-polls.json` を保存する。旧delivery schema／意味／元native verdictは変更しない。別storageのsealは別時点であり、SDK phase recordとのatomic snapshotではない。時間順や最後の状態を記録せず、observed Emptyからsenderがその後も生存したとは推定しない。GetMessage内へ入った後のsender-dropは次の元pollまで未観測であり、欠測はliveを意味しない。callback guard-dropはsender-drop証明でもない。

local fixtureは実std::mpsc、実patched pump、実recorderを使い、Win32だけを有限に代替する。Windows API／SDK未実行、元timeout未再現。新publication modeは`WRY_RECEIVER_ENABLED=1`で明示し、旧fixtureでは外す。公開前は最終source/control独立review、sourceをancestorに含むcontrol、空の匿名shallow source fetch、重複確認が必要。child-pwsh launch、元15000ms、profile、cleanup、identity gate、旧受入未達を保持する。
