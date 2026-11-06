# 明示的なresident IPCのsource接続

このsourceは未統合#200/#201/#202のstackに依存します。
#200 Windows session-create阻害とstacked CI未実行を維持し、main適用済みとは扱いません。

## 二つの構成を区別する

通常の`commands()`/`run()`、標準`tauri.conf.json`、`shell-local` capabilityの選択は変えていません。
通常shellはresident/binding commandを許可せず、認証や操作権限を有効化しません。

明示的な`commands_with_ports`は既存の具体的`ResidentPort`を受けるprivate builderです。
通常callerはなく、bind/launch/policy command、任意path/argv/fd/digest/credential入力、source差し替え口はありません。
将来のsealed trusted bootstrapが同じownerのportを確立して使う接続先です。
このbuilderを選ぶだけでportがboundになることはなく、unboundならStatus/Receiptもunavailableです。
旧shell statusはこの構成でもnot_connected/操作不可/終了未確認のままです。

command manifestと限定`allow-resident-port` permissionはsourceとして定義しますが、通常shell-localには付与しません。
`tests/resident-context/tauri.conf.json`だけがmain/localの限定permissionを選び、MockRuntimeで実handler/ACLを検査します。
このtest contextを通常runへ選ぶ環境変数・CLI flag・GUI導線は追加せず、実GUI起動にも使用していません。
Tauriの当該版はconfigの親directoryから標準名を読むため、test configを別directoryに置いて選択自体もassertします。

## 受付と観測

`get_resident_status`はstrict request versionを検査し、同じportの観測だけを返します。
`request_resident_intent`は既存admissionへ一度だけsubmitし、既存Tauri async runtimeのblocking workerでReceipt receiverを待ちます。
UI thread上で待たず、送信者切断はunknown、task失敗は固定unavailable errorにします。
自動retry・再bind・観測の楽観更新・receiptからのON/OFF確定はありません。
後続のmatching transition、freshness、cleanupとreceiptは別に扱います。
viewer/app破棄だけを元ownerのClose/回収証明へ変えません。

source試験は正規の要求/厳密な拒否、別window/remote/opposite-platform origin拒否、受付とmatching観測の分離を検査します。
同じ自己所有fixtureの実stdin/stdout、Close/EOF、exact Child Wait、単一IO/supervisor workerとouter monitorの実join、temp削除を維持します。
fixtureはregistration=disconnectedであり、実producer/auth/GUIの実証ではありません。

## trusted launcherをまだ作らない理由

公開repoのTauri bundleはactive=falseで、resident producerのbuild/同梱/信頼した配置元の対応がありません。
この IPC 準備時点では Go source 閉包は未整備でした。後続の [公開 producer 組成](../../docs/mac-resident-producer-composition.md)が residentpipe / residentmac / residentwork と policy の source を準備しますが、実配布・配置・署名の信頼確立は別条件です。
既存Mac agent評価archiveは未署名で、source/binary対応やpublisher identityを保証しません。
Windows connected-package planも整合時にblockedを返す準備宣言で、Mac launcherの信頼根ではありません。

private評価の端末固有producer pinやpolicy path、利用者入力の期待digestで代替しません。
既存loaderのfinite context→保護されたReadTrustConfig→strict schema/fixed scope→既存key loader→immutable factoryを尊重し、Rustに第二のpolicy承認器を作りません。
実policy/鍵は読み取らず、新しいenrollment/署名/OS trust/運用設定も行っていません。

後続はproducer/policyの公開source閉包、固定build/package provenanceと信頼した配置契約、sealed launch、その後の通常GUI/native Quit結合と実機検証です。
現在のGUI/producer/認証/ON/OFF/配布物は未実証のままです。
