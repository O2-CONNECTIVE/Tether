# resident transport と protected policy の source 閉包

#207 以前の未統合 stack に依存する準備 source です。
通常 CLI・resident 起動・IPC・認証・権限を有効化せず、#200 の Windows 阻害や stack CI 未実行を解除しません。

## 今回まとめる具体的な依存

identity/control transport の両端、exclusive source lease と opaque receiver、同じ世代の固定 work 登録・回収 receipt、gateway/MCP の固定操作、strict policy/source/worker を一緒に扱います。
receiver だけを型や callback に置き換えると、その認証・世代・回収の実依存が欠けるためです。
既存4 package に17 implementation fileを限定追加し、既存の typed glue を更新します。
新しい generic port や production fixture、任意 Command/path/hash/CA/trust override は追加しません。

- control は、既存の enrolled host key と principal key、exact endpoint/labels、両側の fresh nonce、generation に束縛した相互署名を検証します。identity の接続・再認証であり、grant 発行・更新やwork再開ではありません。
- `ResidentReceiver` は完全な handshake の後にだけ内部生成します。zero value・struct copy・自己申告flag・caller実装のmetadataでは認証済みsourceを作れません。
- 固定intentは署名・sequence・generation・request replay・operation・timeout・期限を検査し、receipt時のmonotonic lifetimeと元contextを保持します。後でwall clockから期限を作り直しません。
- `ResidentSourceLease` は元のreaderをexclusiveに渡し、consumerの `Finish` を待ちます。取消だけでconsumer回収を推定せず、unknownでは元lease・transportを保持し、再接続・置換で成功扱いにしません。
- work登録は同じintentとcanonical dispatchに署名を結び、現在のhost/principal/generationと元期限を確認します。signed pendingは受付だけで、実行や回収の成功ではありません。
- gateway/MCPは固定操作だけを公開する内部構成です。元のcommand状態、host-owner receipt、gatewayの元cleanupを別に保持し、確認済みの結果payloadだけを明示releaseできます。tombstoneはreplay用に残ります。MCP response取消やDELETEをwork停止・回収と混同しません。

通常のHandler構成はresident設定が空のままで、新しいprotocolを受け付けません。
新しいCLI flag・設定parser・既存cmdへのresident呼出しは追加しません。

## strict policy と native owner の境界

公開するloaderは固定scopeの `LoadStandbyResident` に限ります。
未使用の汎用 `LoadResident` 入口は移さず、standbyでは既存の固定operation・command・15秒timeoutの一組をkey取得前に確認します。
pathの選定はtrusted native compositionの責任で、通常GUI・env・remote入力からのbindingは未構成です。

既存 `agentkeys.ReadTrustConfig` のparent/owner/ACL/same-handle/close検査、strict JSON schema、policy digest、key fingerprint、期限を保ちます。
policy再読取りのdigest変化やcleanup不明では元factoryをblockし、同じsource/workerを保持します。
新しいfactoryでunknownから回復したことにしません。
protected fileの受理はpublisher・配置元や実行artifactの信頼証明ではありません。
既存native file I/Oは同期的で、context期限はsyscall内部の停止を保証しません。

`OpenWireSource` / `NewWireRequest` は実際のopaque receiver、endpoint、両key fingerprint、元generation/context/deadlineへ接続します。
output側のclient factoryは認証済み入力を生成するportではありません。
各requestは元issuer/Approved/clientと部分取得を同じ `ResidentWorker` に保持し、元の固定request IDへだけ渡します。
Run/Close・元cleanup・unknownを別々に観測し、source終了やreceiptだけをnative回収と呼びません。

## 検証した境界と残る実証

通常の追加unitはmemory HTTP/WebSocket/SSH、固定seedのkey vector、fake runtime/kernelで、実鍵・実policy・実producerを使いません。
実際の相互proofで生成したreceiverからstrict policyへつなぐ正の試験も追加します。
このDarwin/cgo試験だけは、checkout内に自分で作った合成key/policy fileを、変更していないnative保護readerで読みます。
運用配置・実key・既存policy・CA・ACL設定は変更せず、fixtureを最後に削除します。

この正の試験は `NewWireRequest` までです。
`Approve` / `Run` は呼ばず、grant/session/command/work transportを作りません。
元context保持、receiverのcopy拒否、scope release後の復活拒否、policy drift拒否、同じcontrolのFinish/Close/Run、memory streamとhandler終了を確認します。
`export_test.go` 相当のhelperは試験variantだけの観測IO注入で、production buildや認証constructorには含みません。

source/workerの署名付き承認・部分取得・Run/Closeは別のmemory unitで確認しますが、これらを全authenticated consume/native実行の一つの証拠へ合算しません。
私有のtagged integration exportや評価artifactは取り込みません。
実EOF・exact Child Wait・native IO/supervisor/outer monitorの全join、authoritative closed、GUIや退出許可は未実証です。

後続は具体的Standby、codec/mapper/owned pipe/startup/producer entry、sealed launcher、publisher・source/artifact対応・保護配置・verify-to-execの信頼です。
[全体の段階計画](mac-resident-source-plan.md)を参照してください。
