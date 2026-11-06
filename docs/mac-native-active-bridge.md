# 同じ native owner の終了観測と active IO の接点

[#235 の completion](mac-foundation-process-completion.md)と[#236 の owned IO](mac-foundation-active-io.md)を、既存 C ABI / Rust owner に結ぶ source-only の段階です。通常 publisher は nil のまま拒否し、startup / IPC は source-unavailable を維持します。起動 API、任意 path / argv / fd / hash / trust root は追加しません。

```text
同じ native record / TLS worker / private handle
  → completion poll: pending / exit(status) / signal(status) / unknown
  → borrowed IO: write / half-close / read / 実 EOF
  → 元 Process の実 Wait 済みでだけ active IO retire
     → 全 close receipt + EOF + fault なし: ActiveIoRetired
     → 不明: 元 record の Unknown
いずれも元 native record を保持 → new create を拒否
```

## 独立した観測と保持

`rh_resident_preparation_exit` は、同じ Process の実 Wait と reason / status を返します。未起動、pending、unknown、wrong worker は exit 0 ではありません。packed status の不正な tag / status / 範囲も Rust が Unknown にし、正しい終了観測で既存 IO / owner の Unknown を解除しません。

`rh_resident_preparation_retire_active_io` は、元 owner に保存された実 Wait を要求します。caller の bool や status は受け取りません。Wait 未観測では端点を閉じず、元の state を返します。Wait 成功だけでは IO confirmed にせず、実 EOF、4端点それぞれの receipt、IO fault を確認します。prelaunch retire を active の証明に流用しません。

`ActiveIoRetired` は IO component の観測です。元 Process / record は破棄せず、新 owner の作成を拒否します。authoritative Close、IO worker / supervisor / outer monitor joins、GUI Quit や replacement の許可ではありません。nonzero exit / signal もそのまま返し、IO の物理回収を exit 0 に読み替えません。

Rust の NativePreparation は !Send / !Sync を維持します。PreparedIo は同じ guard の exclusive borrow であり、借用中の retire / poll / Drop は compile で拒否されます。C ABI は unknown 時も元 TLS identity を確認してから Process / IO に触れ、他 thread の同じ数値 handle では操作しません。normal9 / fixture23 exports を厳密に要求し、normal library への fixture 混入や欠落を拒否します。

## Drop / incomplete worker

active guard の Drop / unwind は元の control writer を一度 half-close し、元 Process が既に停止していればその Process の Wait と既知端点の回収を進めます。kill、PID reconstruction、別 owner、close retry は使いません。controller を失った元 record は必ず Unknown に保持します。pending child は pending のまま保持し、Drop の復帰を回収完了や時間上限にしません。

元 worker の TLS destructor も同じ規則です。worker が消えた後、別 thread へ completion / collection を代行させません。pending TLS exit の liveness / 全回収は未実証です。動的 worker-exit fixture は元 child の Wait / EOF の後に実行し、元 destructor の close と Unknown 保持を確認します。pending Drop / unwind fixture は元 worker が生きている間に、元 slot の固定 child を最後まで Wait / close します。この test-only collection は Unknown を解除せず、通常 library には export しません。

## 合成 fixture と試験境界

fixture 専用 constructor は bare owner が新しく compile した自分自身の固定 child だけを起動します。requirement-bearing owner や通常起動への流用はありません。echo、early exit、合成 close receipt 喪失、exit 2、自己 SIGTERM の固定動作を使い、実 producer / 認証 / policy / 鍵 / GUI / 外部接続は使いません。

最初の実 Rust / C ABI / Swift echo seam は未実装 API の compile red → IO / EOF / exact Wait / close green。他の negative / Drop / unwind / worker exit / ABI decode と strict report は追加回帰です。fixture 失敗時も同じ worker が残る場合は同じ retained record の child を回収し、部分件数や未回収を成功にしません。

既存 requirement35 / completion34 / active IO129 / C ABI90 / Rust owner92＋compile refusal9 / Pump133 を保持します。新 bridge は normal / fixture の239条件、9 child の元 Wait、36 original 端点の回収と、追加 borrow / normal-fixture isolation の compile refusal5 を検査します。Intel は compile/link だけで、Intel native runtime は未実行です。実 signed image の launch enforcement、atomic inheritance、通常起動への配線、既存 Pump / supervisor への active 接続と全 joins は残件です。

実 GUI / host / producer / 実鍵 / protected policy / OS trust / 実 release 署名・配置は未実行です。a4 / a5 と task-7 は無変更、解除連絡待ちです。既往 Windows timeout の根因解消とは扱いません。

## 未接続 consumer の compile 回帰

新しい型を `pub use` だけで再 export すると、active 接続をまだ使わない既存 private module の埋め込みで unused-import error になります。公開 type alias により同じ型・API を保ち、警告抑制は追加しません。実際の owner source を埋め込む metadata-only fixture を追加し、2021 /2024 edition × normal /fixture の4構成で red → green を確認します。既存 app の明示 native cfg と Pump build / clippy も別に検査します。
