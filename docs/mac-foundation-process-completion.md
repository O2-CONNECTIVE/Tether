# 同じ Foundation Process の終了観測

既存の `ResidentLaunchPreparation` が保持する同じ `Process` に、終了理由と status の観測を結びます。通常 startup / IPC、C ABI、Rust の `End` や退出許可へは接続しません。production publisher は未設定のまま拒否します。

```text
同じ Process の生成 → unlaunched（終了証拠なし）
固定 test child の run 成功 → running（終了証拠なし）
その Process が停止 → その Process.waitUntilExit()
  → reason / status を一度だけ保存 → 同じ観測を返す
不明な状態 → 同じ owner を保持、別 Process / PID で置換しない
```

`processExit()` は未起動や実行中に結果を返しません。`isRunning == false` だけを未起動 Process の成功にせず、実際の `run()` 成功を経た同じ owner の phase を要求します。結果は普通の exit と signal を区別し、exit 2 や signal 終了を exit 0 に変えません。終了結果の保持後も二度目の実行は拒否します。

application の `terminationHandler` は追加しません。[Apple の契約](https://developer.apple.com/documentation/foundation/process/terminationhandler)では `waitUntilExit()` の復帰と handler の完了は別です。この source は handler join を捏造せず、予期しない handler があれば unknown を保持します。Wait 未確認の active / unknown owner は Drop でも解放しません。これは回収の時間上限ではありません。

失敗した run、unknown / Drop 時の保持は source の静的確認であり、下記34条件の動的実証には含めません。production の running 遷移はまだ存在せず、今回の保証は exact-Wait 源の実装を固定 child で試験した範囲です。

## 固定 fixture と確認範囲

`RESIDENT_LAUNCH_FIXTURE` だけに、署名・認証を主張しない専用の bare Process を持つ constructor があります。既存の requirement 付き準備 owner を変更・弱体化しません。その owner から試験用の実行を要求しても、executable や arguments を設定する前に拒否します。

実行できるのは、新しく compile した自分自身の fixture executable の固定 child mode だけです。任意 path / argv / digest / publisher / trust root を受ける製品入口はありません。child は exit 0、exit 2、自己 SIGTERM、または自己所有 stdin pipe の EOF 待ちを行います。producer・GUI・policy・鍵・外部接続を使いません。

[`check_mac_process_completion.py`](../scripts/check_mac_process_completion.py) は normal / fixture の Swift を2 architecture に compile し、host architecture だけで34条件を実行します。4 child の実 Wait を確認し、未起動拒否、pending、異なる終了理由、観測の固定、二度目の起動拒否、通常準備 owner の拒否を検査します。部分件数・失敗・未回収・producer 起動・余剰出力は成功にしません。最初の actual owner fixture は未実装 member による compile red から実 child の Wait / exit 0 の green を確認し、残りは追加の回帰です。

EOF 待ちには `Pipe` 自体を `standardInput` へ渡します。[Apple の仕様](https://developer.apple.com/documentation/foundation/process/standardinput)に従い、親側 child read end の終了を Foundation に任せ、同じ親 writer を明示的に閉じてから同じ child を回収します。これは通常 owner の `OwnedPreparationIO` の active transfer や producer EOF の実証ではありません。

## 次に必要な接続

これは Swift 内の同じ Process の終了観測までです。[後続の active IO source](mac-foundation-active-io.md)は同じ owner の child 側端点解放、SIGPIPE、親 IO / EOF と回収を固定 child で確認します。製品の sealed launch、atomic inheritance、authoritative Close、C ABI / 同じ Rust owner の completion、IO worker / supervisor / outer monitor の実 join を次に閉じる必要があります。未起動 worker join や今回の fixed child Wait を、実 producer の active joins や cleanup confirmed、GUI Quit の許可へ合算しません。

sealed release の publisher / exact image、保護配置と policy、署名・配置・OS enforcement は未確立です。通常の producer 実行 API は追加しません。製品最低 OS は変更せず、fixture の compile target と実配布 artifact の deployment target を区別します。実 GUI / producer・実鍵 / policy・署名 / 配置 / OS trust は未実行、凍結 a4 / a5 は無変更です。

## 後続の active component bridge

[同じ C ABI / Rust owner の接点](mac-native-active-bridge.md)は、この Swift leaf の終了観測と active IO を同じ handle / TLS worker へ渡します。IO 回収後も native record を保持し、replacement / full joins / Quit の許可にはしません。通常起動は未接続です。既存 Pump / supervisor の active 配線、signed launch と全 joins は引き続き残件です。上記の未接続説明は各 leaf 単独の保証範囲を表します。
