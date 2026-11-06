# 同じ active native owner と既存 Pump

これは source-only の接続です。通常起動は publisher 未設定拒否 / source-unavailable のままで、実 producer / GUI / host / 認証 / policy / 署名・配置は有効にしません。[active bridge](mac-native-active-bridge.md)の具体的な元 owner を、既存の Pump / shared IO driver へ接続します。

```text
original TLS worker
  NativePreparation (same private handle / Foundation Process)
    Pump::take_active (&mut owner, State::Active required)
      Pump.step / Shared / wire / Closure
      owner::drive_io (also used by existing supervisor)
    original poll_exit / retire_active_io
caller
  same actual IO JoinHandle.join
```

`take_prepared` は従来どおり未起動限定です。active adapter は raw fd / path / argv / hash / public callback を受け取りません。mutable borrow と thread-confined guard を保持し、既存の Read / Write / control half-close、bounded frame parser、monotonic freshness、intent receipt、Close / EOF の検証をそのまま使います。C ABI と通常 export は変更しません。

`drive_active_io` の成功は取り付けと IO loop の復帰だけです。不正 wire / EOF / fault による復帰もあり、戻り値を Close、元 Process の Wait、IO 回収、全 joins の証明に使えません。wire の `confirmed Close`、実 EOF、元 Process の exact Wait、各端点の close receipt、同じ IO worker の実 join を別々に確認します。intent の accepted receipt は実際の ON 状態を意味しません。

native record は既存通り Unknown と禁止置換を保持します。IO component が ActiveIoRetired でも global record の解放 / Quit は許可しません。既存 Supervisor の `End::Reaped(std::process::ExitStatus)` は std Child 専用なので変更せず、native の結果を代入しません。native Supervisor / controller handoff / outer monitor と全 joins は後続です。

## 合成 fixture

`resident_native_source` と `resident_launch_fixture` の両 cfg に限定した実 source consumer が、新しく compile した自分自身の固定 child だけを元 worker 内で起動します。実 producer や signed launch を使いません。既存 fixture constructor を再利用し、新しい信頼入口や C ABI を増やしません。

最初の consumer の未実装 `drive_active_io` compile red → 同じ child の wire / EOF / Wait / IO 回収 / join green を対応づけます。他の negative は追加回帰です。

9 modes は confirmed Close、unknown Close、Close 欠落、Close 後の不正 frame、起動時の不正 frame、startup rejection + exit2、自己 SIGTERM15、close receipt 喪失、worker の実 unwind です。9 original children を元 worker で Wait し、36 physical endpoints を一度ずつ閉じ、9 actual IO worker joins と Unknown 保持 / replacement 拒否を確認します。normal の拒否1条件と active127条件を strict report で検査します。既存 prepared Pump133条件は別 checker に保持します。

```sh
python3 scripts/check_mac_active_pump.py
```

locked / offline の既存11 dependency packages を使い、normal / fixture の両構成で build / clippy します。通常 native-source CI に追加し、既存 Intel checker は同じ Pump crate の両構成を compile / link するだけで外国 architecture の実行はしません。helper の report 回帰は件数不足・未回収・unknown 消失を拒否します。

failure / unwind の固定 child は出力を匿名 pipe 容量未満に制限し、元 worker が生きている間に元 guard / retained record を使って half-close / Wait / cleanup します。Wait polling と join には hard upper bound を主張しません。pending TLS exit 後の全回収や unbounded producer の drain をこの fixture から保証しません。ここが未証明なので、本物の native Supervisor を有効化しません。

signed launch、verify-to-exec、atomic descriptor inheritance、実 producer の終了と active 全 joins、実 GUI は未実証です。a4 / a5 と task-7 は無変更で解除連絡待ちです。旧 Windows session timeout の根因解消とも扱いません。
