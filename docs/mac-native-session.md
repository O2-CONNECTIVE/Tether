# 同じ native owner と typed port の寿命

**#240 の未統合 head に依存する source-only の組成です。** #240 は PR CI 15 成功 / Windows release startup 1 失敗で draft 保留中です。main の前段 #239 も、candidate 16/16 に対して実 postmerge は 15 成功 / 通常 Windows native 1 失敗でした。両方の失敗と根因不明を保持し、この差分で rerun や Windows 修正を行いません。依存を無視した merge はできません。

既存 [NativeOuterOwner](mac-native-outer-monitor.md) と ResidentPort の間を、具体的な NativeSession が保持します。通常 startup / IPC / 認証は有効化しません。

```text
NativeSession::prepare (引数なし、sealed native preparation のみ)
  NativeIoSupervisor::prepare → 同じ producer / source の組
  NativeOuterOwner::start(producer) → 元 owner を先に保持
  ResidentPort::bound(source) → 元 source の first observation を確認
  private port + private outer owner → 借用した view のみ返す
```

attach は private で、外部から source と owner を別々に渡す入口を作りません。任意 path / argv / fd / digest / trust override はありません。active child の constructor は fixture cfg に限定し、通常 variant には存在しません。通常 prepare は publisher nil 拒否により unbound / source-unavailable のままです。`Ok(NativeSession)` は寿命管理の組が存在するだけで、接続成功・認証済み・起動許可の意味ではありません。

source attach / 初回観測に失敗しても session は同じ outer owner を保持し、同じ control へ close を要求します。port は unavailable を返します。元 owner を失ったり、別 owner で cleanup を代替しません。thread spawn 失敗は既存の captured-owner Drop guard を通ります。

port と owner の fields は private です。caller が得るのは借用 view だけで、view が使われている間の session Drop は Rust の E0505 で拒否されます。viewer の非表示・借用終了は control close / OFF 要求になりません。session 自体の Drop は port の後で元 outer owner の既存 guard を実行し、元 IO / Wait / outer join を回収します。receipt は受付の記録だけで、同じ observation が更新されない限り ON / OFF を捏造しません。

## 検証

```sh
python3 scripts/check_mac_native_session.py
```

251 条件、normal 1 / fixture 11 modes。固定自己 child 11 件の元 Wait、44 endpoint closes、明示的 outer joins 10 件と session Drop guard join 1 件、Unknown record 11 件の保持、実 native slot の replacement 拒否 11 件、Quit permits 0 を検査します。normal nil 拒否、confirmed / unknown / missing / malformed Close、attach 前の lost-close receipt、不正 startup、startup rejection、IO unwind、session Drop を含みます。

close 後の既存回帰も、Receiver が作れたことだけでなく、unbound は unavailable、bound は rejected という実 receipt を要求します。追加した idle-close と pending-close は、元 NativeSession / control / Process の実経路を通ります。pending child は request 5 を読み、revision 2 の pending 観測を返した後、元 control EOF まで accepted receipt を保留します。close はその待機中 receiver を Unknown として完了し、後着 accepted を重ねず、新規 intent を拒否し、status の ON / OFF 要求能力を無効にすることを検査します。

これは製品動作を変えない後付け回帰です。local copy にだけ入れた「discard 時 Unknown を accepted にする」「close admission seal を外す」という意図的な誤りを、各 3/3 検出しました。negative の判定は元 owner の Wait / join / close 後に行い、各回の Wait 1 / open 0 を確認しました。元 source を SHA 一致で復元後、両ケースが各 3/3 成功しました。この mutation は公開差分に含めず、現行製品の不具合修正や Windows timeout の解決とは扱いません。

同じ実 ResidentPort core を Tauri command module から振る舞いを変えずに分離し、既存の locked/offline fixture crate に import します。mock port や認証済み marker は使いません。normal / fixture の build / clippy と、実借用 view に対する E0505 compile refusal を要求します。期待どおりの borrow refusal 以外の compile error を成功にしません。既存 app 両構成・port / IPC 回帰と outer / supervisor / Pump の aggregate checker を維持します。

## 後続の実配線

次の caller は、信頼済み producer を構成する sealed bootstrap と、session 全体を保持する通常 GUI / IPC composition です。port だけを独立した Tauri State へ移動させて owner を落とす接続はしません。実接続には publisher / exact release / protected placement / verify-to-exec / atomic inheritance の根拠が別途必要です。合成入力はその根拠になりません。

native record の全解放・full cleanup / application Quit はこの session では許可しません。現在の worker Drop / TLS exit は Unknown を保持し、outer join が揃っても解除できません。次の終了実装には、元 worker 上で quiescence を準備し、実 IO / outer joins と結び付ける coherent な手順が必要です。raw handle と bool、PID 再取得、他 thread の Process 操作、既存 Unknown の clear で代用しません。unbounded drain / pending TLS exit の liveness も未証明です。

会社 Mac の実 GUI / producer / host、実鍵 / policy、署名・配置・OS trust・外部接続・persistent access は未実行です。a4 / a5 / task-7 は不変で、解除連絡待ちです。旧実機証拠を新 pin に引き継ぎません。
