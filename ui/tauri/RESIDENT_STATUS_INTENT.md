# 同じownerの状態観測と意図受付

この文書は#202のcore範囲です。後続の[明示IPC source](RESIDENT_IPC.md)と通常shellの閉鎖を区別します。

このsourceは未統合の#200、#201に依存します。
#200のWindows session-create失敗、#201のstacked CI未実行は未解決のままで、mainへ適用したとは扱いません。

## 閉じた具体的接続

既存private `ResidentPort`/`Status`のcoreを、同じ`BoundDuplex`の`current`と`request`へ接続します。
callbackや任意のsnapshotを注入する通常constructorはありません。
`bound`は同じtyped duplexを受けるprivate methodで、最初の実観測を得るまでportを公開せず、後から別sourceへ差し替えません。
通常callerはなく、起動・IPC登録・capability・GUI経路は追加しません。
通常portはMacでsource-unavailable、他OSでunsupported、意図受付はunavailableです。

`current`は同じownerからの観測を投影します。単なるwire decodeや所有pipeをproducer認証へ昇格させません。
通常の構築経路を開く前に、sealed launcherがproducer identityとprotected policyの信頼境界を確立する必要があります。
fixtureのregistrationはdisconnectedであり、実認証済みの接続表示を作っていません。

`request`は既存TrackingのadmissionとReceipt receiverを渡します。
acceptedは受付であり、appliedではありません。portはrevision・ON/OFF・work・cleanup・transitionを楽観更新しません。
状態は後続の実観測だけを反映し、要求の解決は同じrequestId/intentに対応するtransitionを別に確認します。
未解決要求中の追加受付やClose後の要求は、同じownerの既存規則で拒否します。
GUI/IPCを接続する際はこのreceiverをUI thread外で待ち、receiptと観測・freshnessを別に扱う必要があります。

終了後も同じportから古い値を新しい観測に見せず、validForMs=0と操作不可を保持します。
最後のsnapshotがoffでも、freshnessやauthorityの確認なしに現在のOFF/回収完了へ昇格させません。
物理終了、wire Close、EOF、Child Wait、IO/supervisor worker join、outer monitor joinは引き続き別の証拠です。

## 試験と残件

通常unbound/unsupported、厳密なrequest schema、通常shellでのresident/binding command非登録を検査します。
自己所有の無害なpipe fixtureで、ON/OFFの受付後とmatching source transition後を別の段階に固定します。
生成した自己所有temp領域のtest-only release fileは通知順序の制御だけで、認証や回収結果を注入しません。
全releaseを解放し、元Child/IO/supervisor/outer monitorを実joinし、temp領域を削除してからassertします。
これは移植sourceに対する回帰・統合検査であり、実認証/GUIや製品producerの実証ではありません。

残件はsealed trusted launcher/identity/protected policy、通常Status/intent IPC、native Quit/GUI結合、実機検証です。
private identityの端末固有producer path/hashと評価policyは移植せず、任意path/argv/fd/digest入力や信頼根overrideで代替しません。
凍結privateや#199/#201の静的caller所見は変更しておらず、新sourceをprivate実行候補へ適用済みとは扱いません。
