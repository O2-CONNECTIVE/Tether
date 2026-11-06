# 同じ native owner と既存 Pump の source 接続

[#224 の借用 byte IO](mac-native-byte-io.md)を、既存 Pump の同じ framing / Shared / wire 処理へ接続する source-only 段階です。
通常 startup / IPC / GUI の binding は追加せず、publisher nil の通常 prepare は拒否します。native supervisor や新しい終了成功型も作りません。

## 具体的な2経路

`Pump<I>` の共通実装は既存の `Read` / `Write` だけを使い、partial frame、期限、厳密なwrite結果、drain / discard、raw EOFを扱います。構築関数は private で、任意IOを注入するpublic factoryや新しいgeneric traitはありません。

| 経路 | 生成と所有 |
| --- | --- |
| 既存 ChildIo | 同じ exact Child の stdin / stdoutだけを取得し、元のFIFO / access / CLOEXEC / nonblocking検査を保持。half-closeとDropはその所有Fileへ作用 |
| PreparedIo | 同じNativePreparationのmutable borrowだけを受け、fd所有はSwiftに残す。half-close成功後だけClosureへ記録し、不明時はfault、元native unknownを保持 |

clockのpre-read / post-read sampling、partial line期限、strict wire / request / receipt、writeの全量確認は変更しません。部分write / pendingを成功へ変換せず、EOFだけをClose確認にしません。
既存supervisorのdriveはdefault型 `Pump<ChildIo>` と exact Childを受けます。`Pump<PreparedIo>` はその型へ渡せず、std ChildのWaitをnative終了証拠に代用しません。
supervisor / owner proof / startup / Closure / Tracking / Sharedの判定コードは不変です。

## 明示的source構成

`resident_native_source` compile cfg はprivateなnative moduleとPump接続を認識するためのもので、通常buildでは設定しません。build scriptはcheck-cfgを宣言するだけでenableしません。Cargo featureや運用設定・信頼根overrideではありません。
fixture peerの送受信は別の `resident_launch_fixture` compile cfgだけに存在します。通常のnative export6件は維持し、追加peerを含むfixture16件と区別します。
fixture crateのserde / serde_jsonと推移依存11件は、既存アプリlockの同じversion / checksumだけです。独立lockでoffline / locked buildし、通常アプリのCargo.lockを変更しません。

```sh
python3 scripts/check_mac_native_pump.py
python3 scripts/check_mac_rust_owner.py
python3 scripts/check_mac_preparation_ffi.py
python3 scripts/check_mac_launch_requirement.py
```

既存のRust / Cargo / clippy / Swift / SDKを選んだDarwin環境で明示実行します。新checkerはnormalとsource fixtureの両構成をbuild / clippyし、自己所有の一時libraryとfixtureだけを実行します。GUIや実producerを起動せず、全compiler / fixtureの同じ子の終端後に一時領域を解放します。
fixtureは製品側と同じRust edition2021で、実際のPump / ChildIo / Shared / wire / clock sourceを取り込みます。判定ロジックの別実装やmock FFIではありません。

## 検証と読み方

通常拒否1条件＋source8 caseの95条件、計96条件を要求します。pending、分割frame、合成statusからのintent書込みとreceipt、malformed frame、shutdown discard、raw EOF、half-close、unknownを検査します。
合成status / receiptは私有protocol core内だけの観測です。書込みでreceiptを捏造せず、合成receiptを受けてもstandbyをONに変えません。authenticated context、実運用status、grant、実作業、GUI bindingではありません。
startup rejection＋EOFのparser確認も、exact Process exit2 / Wait / joinが無いため親解放証明にはしません。raw EOF / half-closeだけのcaseではClosure確認を拒み、ownerは未起動のままです。
前段sourceではtake_prepared constructor不在のcompile negativeを保持し、同じfixtureが新sourceでは実native IO経由で成功します。

通常アプリRust回帰はREADMEどおりfrontend buildを準備してから実行します。dist欠落によるmacro失敗をsource試験の成功へ読み替えず、placeholderで代用しません。
Rust Intel stdは選択した既存toolchainに無く未実行、foreign実行とold OS loadも未実証です。toolchain導入・既定変更・製品minimum OS引上げはしません。

## 残る境界

native PumpのDropは借用を終えるだけで、NativePreparationの所有を移したりfdを閉じたりしません。native driverには同じownerの終了・回収手順が別途必要です。
同じFoundation Processのsealed実行、起動後のchild側端点解放、SIGPIPE / peer喪失 / atomic inheritance、実producerのauthoritative Close / EOF / exact Wait、IO / supervisor / outer joinsは未接続・未実証です。
実publisher / 承認release / protected policy / 署名・配置とOS enforcementも未確立。通常CLI・認証・権限・persistent accessを有効化せず、#200 blocker / 未統合stack / CI未実行を維持します。
