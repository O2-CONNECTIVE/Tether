# 同じ native owner の outer monitor join

[NativeIoSupervisor](mac-native-io-supervisor.md) を既存 lifecycle の外側 monitor が所有し、その monitor を実 join した後にだけ NativeOuterJoined を返す source-only 接続です。既存 ExitGate / CloseHandle と同じ join guard を使い、新しい generic port / callback factory / launcher は作りません。

```text
NativeOuterOwner
  outer monitor owns original NativeIoSupervisor
    original TLS IO worker / NativePreparation / Process
    active Pump → original Wait / EOF / close → actual IO worker join
  actual outer monitor JoinHandle.join → NativeOuterJoined
  ExitGate outcome = Unknown → Quit denied
```

NativeIoSupervisor / NativeJoined の参照範囲だけを crate 内へ広げ、既存の具体的 owner を渡します。private fields、元 native handle、thread confinement、C ABI は維持します。通常の publisher nil 拒否と source-unavailable、startup / IPC は変更しません。

元 IO worker の join、wire Close、元 Wait / IO state、outer monitor の join を別の証拠にします。IO evidence の保存、is_finished、取出した handle、Mutex 競合、monitor 自身からの join 要求だけでは outer proof を返しません。同じ既存 join guard が実 join を完了した後にだけ private field の NativeOuterJoined を作ります。既に得た同じ component は再読できますが、再 launch / 再 join ではありません。

native record は join 後も Unknown で禁止置換のままです。すべての native outcome を既存 ExitGate の Unknown に固定し、正常終了・confirmed wire Close・全 visible join が揃っても request_exit / finish_requested は false です。full cleanup / signed launch / application Quit 許可を新設しません。std Supervisor / StandbyOwner の safe 判定も変更しません。

Drop は同じ control を閉じて元 monitor を join し、pending worker を detach しません。monitor panic は Unknown と証拠なしを保持し、Drop も親を保持します。monitor の成功 join 後の Drop 復帰は、この component の寿命終了だけで、アプリの退出許可ではありません。元 native record は保持されます。unbounded producer drain / pending TLS exit 後の全回収・時間上限は未証明です。

## 合成 fixture の範囲

最初の実 consumer は missing outer module の compile red → 同じ fixed self-child の IO / Wait / outer join green10。追加回帰は normal / 未起動退役、confirmed / unknown / missing / malformed Close、不正 startup、exit2、自己 SIGTERM15、lost receipt、IO unwind、Drop、IO 完了後 outer hold、outer panic です。

```sh
python3 scripts/check_mac_native_outer.py
```

253 条件、normal 2＋fixture 14 modes。13 original children の Wait、52 active＋4 unlaunched physical closes、13 actual IO worker joins、12 successful active outer joins /1 failed outer join、3 preparation outer joinsを検査します。native Unknown 13、Quit permits0です。outer panic は元 child の Wait / IO join 後だけに注入し、失敗した親を fixture 内で保持します。生存 child を放棄した成功や実 producer の試験ではありません。

held fixture は実 monitor handle 登録後に同じ monitor の自己 join 拒否を確認し、IO evidence が保存されても outer join 前は proof 不在と Quit 拒否を要求します。別の自己所有 thread による Mutex 競合 join も false を要求します。これらの同期と panic は fixture cfg に限定し、通常の信頼入口へ流用しません。

先に同じ supervisor の IO join を取得済みの場合も、保存済み NativeJoined を元 owner の証拠として再利用します。未起動・active 両方の prejoined 回帰を実行し、外側 monitor の実 join は別途要求します。再 Wait や別 owner への置換は行いません。

normal / fixture は locked / offline、既存11 dependencies、clippy 両構成。従来の177条件・app 両構成・native source 全検査を維持し、Intel は同じ source の compile / link のみで foreign 実行はしません。

実 GUI / producer / host、実鍵 / protected policy、外部 / Tunnel / OS trust、署名・配置・persistent access は未実行です。a4 / a5 は不変で解除連絡待ち。full native cleanup / Quit、signed launch / verify-to-exec / atomic inheritance、製品認証・実機受入は残件です。既知 Windows timeout は未解明のまま保持し、この source 成功で解消したとは扱いません。
