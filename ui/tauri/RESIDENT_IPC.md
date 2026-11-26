# 明示的なresident IPCのsource接続

#200の実IOと同じChildの終了所有はmainに統合済みです（merge commit `32b6377`）。この文書はresident IPCのsource接続と内部contractを記録します。

## 二つの構成を区別する

標準の`run()`は`ResidentState::startup()`のstateを`commands_with_state`へ渡し、resident status/intent commandを登録します。標準buildのsourceは`Unbound`でsidecarを起動しません。
Unbound statusはmacOSで`source-unavailable`、Windowsなど非対応OSで`unsupported`です。intent receiptはどちらも`unavailable`です。
標準`tauri.conf.json`が選ぶ`shell-local` capabilityは`allow-resident-port`を許可します。
このpermissionはcommandの呼び出しだけを許し、source binding・認証・操作権限を有効にしません。

`commands_with_ports`は`#[cfg(any(test, resident_gui_fixture))]`のtest/GUI fixture限定private builderで、通常起動や将来のbootstrapには使いません。
通常起動は`commands_with_state`が管理するstateを使います。bind/launch/policy command、任意path/argv/fd/digest/credential入力、source差し替え口はありません。
標準buildは`Unbound`でproducerを起動しません。macOSで`#[cfg(all(target_os = "macos", resident_native_source))]`を有効にした署名候補には、登録済みproducerの固定bundle起動経路がありますが、実機起動は未検証です。
旧shell statusはこのIPC構成でもnot_connected/操作不可/終了未確認のままです。

command manifestと限定`allow-resident-port` permissionはsourceとして定義し、標準shell-localがmain/localへ付与します。
`tests/resident-context/tauri.conf.json`もmain/localの限定permissionを選び、MockRuntimeで実handler/ACLを検査します。
このtest contextを通常runへ選ぶ環境変数・CLI flag・GUI導線は追加せず、実GUI起動にも使用していません。
Tauriの当該版はconfigの親directoryから標準名を読むため、test configを別directoryに置いて選択自体もassertします。

## 受付と観測

`get_resident_status`はstrict request versionを検査し、同じportの観測を返します。macOSの署名候補で元のportに観測源を接続できなかったときは、同じnative sessionの起動結果を返します。
起動結果の応答は`protocolVersion: 1`、`source: "resident-owner"`、`state`の3キーです。
元のownerの回収を確認中は`state: "source-startup-checking"`を返し、同じ画面での再取得と応答待ちは合計3秒を上限にします。画面の再表示やメニュー経由の照会も、確認中は同じ期限を使います。
元のproducerと監視側を回収し、起動時の登録設定の拒否を確定できた場合は`state: "source-policy-rejected"`を返します。
resident policyが指すKeychainのホスト鍵を読めない場合は、`exit 3`と`startupRejected`の`code: "host-key-unavailable"`が一致し、policy拒否と同じ起動前・最初のframe・3秒未満・EOFまで追加frameなしの条件を満たしたときだけ`state: "source-host-key-unavailable"`を返します。
既存の`exit 2`と`code: "policy-rejected"`の組は変えず、組の不一致、ほかの起動不可、証拠不足と確認期限切れは未確認と表示します。いずれも待機操作を許可しません。
`request_resident_intent`は既存admissionへ一度だけsubmitし、既存Tauri async runtimeのblocking workerでReceipt receiverを待ちます。
Tauriへは`request`をJSON文字列で渡します。Rustは文書を直接デシリアライズして、重複キーや未知のキーを拒みます。
UI thread上で待たず、送信者切断はunknown、task失敗は固定unavailable errorにします。
自動retry・再bind・観測の楽観更新・receiptからのON/OFF確定はありません。
後続のmatching transition、freshness、cleanupとreceiptは別に扱います。
viewer/app破棄だけを元ownerのClose/回収証明へ変えません。

Mac のメニューバーの「待機を止める」は、既存の main WebView に値を持たない合図を届けます。
同じコントローラーが新しい状態を取得し、画面の操作と共通の要求 ID と受付を使って OFF を送ります。
隠れた画面は隠れたままで、受付後は同じ要求 ID の適用と回収の観測が必要です。

Mac の期限通知は画面の表示にかかわらず、元の常駐接続の新しい状態を読み取ります。
適用済み ON または受理済みの延長の要求 ID ごとに、待機中で期限がまだ先であることを確かめてから通知します。
延長の受付後は新しい状態を待ち、時計の補正で表示上の時刻が変わっても同じ要求の通知を重ねません。
初めて待機 ON を確認したとき、通知の許可が未設定なら macOS に許可を求めます。
通知の追加直前にも、同じ要求と有効な期限を照合します。
アプリの終了を確定した時点で、新しい通知と通知許可の要求を止めます。

## 画面との最小の境界

HostSnapshotはresident pipeのstatus frameが持つsnapshot、Intentはintent frame、Eventはreceipt・`closed`・`startupRejected`です。
Tauriの`get_resident_status`はstatusだけを返し、`closed` outcomeと`startupRejected`はRust内部で扱います。
期限切れ・EOF・異常応答は「未確認」とし、OFF・ON・待機中・成功へ丸めません。`cleanup: unknown`は「要清掃」と表示し、停止成功を示しません。
Close要求後からterminal前までのstatusは、Closureで形・不変条件・source時刻順序を検証します。freshnessは見ず、Trackingへの記録はrevisionが進むか、同じrevisionで`validForMs`以外が同一の場合だけ行う表示用のbest effortです。記録条件の不一致や記録失敗はstreamを止めません。Close証跡はClosureの検証と`closed`のoutcomeで決まり、この記録は証跡になりません。
全停止はGoの`hostlifecycle` coordinatorが判定し、`closed`のoutcomeはその判定から出ます。schemaの正本（#36 DEC-02）は未決です。

製品statusの`status.snapshot`には`standbyUntil`（Unixミリ秒または`null`）、
`standbyEndingSoon`、`standbyExpired`を必須で含めます。`standbyUntil`は`null`または
1〜2^53−1の整数で、`standbyEndingSoon: true`なら`null`にできません。未知の鍵、欠落、
型違い、範囲外の値は拒みます。期限の値はGoが出し、RustとTSは計算・補完しません。
Close後に検証・記録するstatusも同じ3鍵を使います。

製品statusの`status.snapshot`には`sleepPrevention`も必須です。
`active`はGoがmacOSのシステム idle sleep 防止を保持している状態、`inactive`は保持していない状態、
`unknown`は保持や解除の状態を確認できないことを表します。
RustとTSは値を検査して表示に渡します。待機や作業の状態から値を補完しません。
`previousCleanup`も必須で、前回の常駐 owner の回収が未確認なら`unknown`です。これはアプリの再起動では消えず、Macの再起動で解消します。
防止中も画面のスリープや蓋を閉じたときのスリープは防ぎません。
回収を確認できない場合の終端statusには、Goが最後に確定した期限切れの有無を載せます。

intentは`on`・`off`・`extend`です。`standbyHours`は`2`・`4`・`8`・`12`・`24`だけを
受け、`on`では省略時にGoの既定2時間、`extend`では必須、`off`では禁止です。
`allow-run` / `deny-run`は製品JSONでは受けません。receiptはrequestと同じ`requestId`・
`intent`で照合します。`extend`はGoでtransitionを作らずreceiptだけを返し、
`Transition.intent`は`on`・`off`のままです。

source試験は正規の要求/厳密な拒否、別window/remote/opposite-platform origin拒否、受付とmatching観測の分離を検査します。
同じ自己所有fixtureの実stdin/stdout、Close/EOF、exact Child Wait、単一IO/supervisor workerとouter monitorの実join、temp削除を維持します。
fixtureはregistration=disconnectedであり、実producer/auth/GUIの実証ではありません。

## 配布と信頼の成立は未検証

公開repoのTauri bundleはactive=falseで、resident producerのbuild/同梱/信頼した配置元の対応がありません。
この IPC 準備時点では Go source 閉包は未整備でした。後続の [公開 producer 組成](../../docs/mac-resident-producer-composition.md)が residentpipe / residentmac / residentwork と policy の source を準備しますが、実配布・配置・署名の信頼確立は別条件です。
既存Mac agent評価archiveは未署名で、source/binary対応やpublisher identityを保証しません。
Windows connected-package planも整合時にblockedを返す準備宣言で、Mac launcherの信頼根ではありません。

private評価の端末固有producer pinやpolicy path、利用者入力の期待digestで代替しません。
既存loaderのfinite context→保護されたReadTrustConfig→strict schema/fixed scope→既存key loader→immutable factoryを尊重し、Rustに第二のpolicy承認器を作りません。
実policy/鍵は読み取らず、新しいenrollment/署名/OS trust/運用設定も行っていません。

標準buildは引き続き`Unbound`です。署名候補の固定bundle起動経路があることは、producer/policyの公開source閉包、固定build/package provenance、信頼した配置契約や実機検証の成立を意味しません。
現在のGUI/producer/認証/ON/OFF/配布物は未実証のままです。
