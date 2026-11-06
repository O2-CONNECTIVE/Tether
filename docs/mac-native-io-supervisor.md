# 同じ native IO worker の supervisor

これは source-only の所有接続です。[active Pump](mac-native-active-pump.md)と同じ NativePreparation / TLS worker / Foundation Process を使い、具体的な NativeIoSupervisor が worker の実 join までを所有します。通常起動は publisher nil 拒否 / source-unavailable のままです。

```text
NativeIoSupervisor.prepare (no arguments)
  original Rust IO worker
    NativePreparation.prepare → publisher nil: Refused
    active source variant / fixed synthetic fixture only:
      Pump.take_active → existing Shared / Closure / drive_io
      original control half-close → original poll_exit / exact Wait
      original retire_active_io
  actual JoinHandle.join → NativeJoined
```

引数なしの通常準備は既存の閉じた NativePreparation.prepare を元 worker 上で呼びます。未起動準備が得られても retire し、active の終了証拠へ読み替えません。通常の active trusted launcher はまだありません。fixture 専用の固定 self-child enum と unwind 選択は別 cfg で、任意 path / argv / fd / hash / callback factory を入力にしません。

既存 BoundDuplex / CloseHandle を再利用し、viewer の寿命と process 終了要求を分けます。親は worker を先に保持してから attachment を待ちます。attachment の受取人が失われれば同じ CloseHandle を要求します。元 owner は worker 内の attachment / Pump unwind の外側に残り、元 Process の終了が観測されるまで保持します。Drop も Close を要求して同じ worker を join し、pending worker を detach しません。実時間の終了上限は主張しません。

## component の証拠と全回収の境界

NativeJoined の private field は実際の join 後にだけ作ります。Refused、未起動退役、active の元 Wait / IO state / unwind、worker unknown を別結果として保持します。wire Close の判定も別 component です。loop return、actual Wait、raw EOF、個別 close receipt、IO worker join を一つの楽観的成功へ圧縮しません。accepted intent は applied ON ではありません。

この IO worker は Read / Write を扱う一つの worker であり、別々の reader / writer thread の join ではありません。既存 std Child 専用 Supervisor / End、StandbyOwner、outer monitor、cleanup_confirmed / can_release / Quit API は変更・接続しません。NativeJoined に全回収の許可はありません。join 後も元 native record は Unknown と禁止置換を保持します。

Unknown active guard は通常の Read / Write / half-close を引き続き拒否します。新しい request_active_close は、既存 C ABI の close-once abandon を同じ private handle へ要求して guard を残すだけです。成功 receipt、EOF、Wait、join を返さず、Unknown を解除しません。未起動では no-op。mutable borrow により、借用 IO が生きている間の close 要求は compiler が拒否します。C ABI / normal exports は増やしません。

不正 startup / attachment failure / unwind では、元 control を閉じて元 Process を待ち、Unknown を保持します。EOF が未観測なら IO 回収を成功にしません。unknown な終了観測も元 worker / guard を保持して待ち、別 owner / signal / retry による成功へ置き換えません。unbounded producer の drain liveness と pending TLS exit 後の全回収は未検証です。この不足を隠して native full Supervisor や Quit を有効化しません。

## 合成試験と実行境界

最初の実 consumer は未実装 module の compile red → 同じ固定 self-child の wire / EOF / original Wait / IO 回収 / actual join green です。追加 negative / Drop / 未起動側は実装後回帰です。

```sh
python3 scripts/check_mac_native_supervisor.py
```

通常準備7条件、fixture11 modes は170条件、計177条件を strict report で確認します。10 active children の元 Wait、40 active endpoints、10 actual IO worker joins、未起動4 endpoints と2 preparation worker joinsを確認します。confirmed / unknown / missing / malformed Close、invalid startup、exit2 startup rejection、自己 SIGTERM15、lost receipt、実 unwind、親 Drop を含みます。固定 child は容量未満の有限出力で、元 worker 上の終了と cleanup を保持できる範囲だけを使います。

normal / fixture の build / clippy は locked / offline、既存11 dependency packagesのみ。既存 active bridge は未起動 close no-op の1条件を加え240条件、借用 close 拒否を加え compile refusal7。Intel は同じ source の owner / active / Pump の normal / fixture compile/link、refusal16を検査し、foreign image を実行しません。prepared Pump133、active Pump129と従来 owner / C ABI / Swift の契約は弱めません。

実 GUI / producer / host、実鍵 / protected policy、外部 / Tunnel / OS trust、署名・配置・persistent access は未実行。signed launch / verify-to-exec / atomic inheritance、native full Supervisor / outer joins / Quit と実機は残件です。a4 / a5 / task-7 は無変更、解除連絡待ちです。base の Windows debug session timeout は未解明のまま保持し、この source 試験で解消したとは扱いません。driver 起点の drop 時刻と forward 所要時間、別 Instant の Host 時刻も混同しません。
