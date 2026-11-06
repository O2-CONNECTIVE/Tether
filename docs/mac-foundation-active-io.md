# 同じ Foundation Process の起動後 IO 所有

[終了観測](mac-foundation-process-completion.md)の同じ `Process` と `OwnedPreparationIO` を、起動後の端点移管・回収へつなぐ Swift 内部の source leaf です。通常起動は未接続で、publisher 未設定を拒否します。製品の起動許可や終了許可、C ABI / Rust の active completion は追加しません。

```text
元の owner が4端点を取得・設定 → 同じ Process.run 成功
  → 元 FileHandle.close() で親に残る child 側2端点を一度だけ閉じる
  → 親 control write / half-close と observation read / 実 EOF
  → 同じ Process の exact Wait / reason / status
  → 親側2端点を一度だけ閉じ、receipt と EOF と IO fault を判定
不明なら同じ Process + IO を保持し、別 owner の成功で置換しない
```

起動前の `retire` と起動後の `retireActive` は別です。[Apple の standardInput 契約](https://developer.apple.com/documentation/foundation/process/standardinput)では起動後の setter は無効であり、FileHandle を渡した場合の親側 close は Pipe の自動処理とは異なります。active 経路は `standardInput` / `standardOutput` を書き換えず、元 wrapper 自体を閉じます。閉鎖済み wrapper の背後で fd を raw close し、再利用番号を二度閉じる処理はしません。unknown の close は再試行しません。

親 writer は起動前に macOS の `F_SETNOSIGPIPE` を設定・確認し、EPIPE を unknown として保持します。これは open file description に付く設定であり、process 全体の SIGPIPE handler を変更しません（[Apple XNU 実装](https://github.com/apple-oss-distributions/xnu/blob/main/bsd/kern/kern_descrip.c)）。CLOEXEC / nonblocking も元の端点へ設定します。`pipe()` と `fcntl()` の間を atomic にした保証はありません。製品の concurrent-spawn / atomic inheritance 境界は未解決です。

Wait 成功や全 fd の物理 close だけでは IO confirmed にしません。実 EOF が未観測、IO fault、close receipt 喪失のいずれかがあれば unknown を保持します。実行中の retire、二度目の移管は拒否します。active IO が未確認なら元 owner を Drop でも解放せず、これは終了時間の上限ではありません。

## 合成 fixture と保証範囲

`RESIDENT_LAUNCH_FIXTURE` だけの bare owner が、新しく compile した自分自身の固定 child mode を実行します。requirement を持つ準備 owner はこの入口を拒否し、任意 path / argv / hash / fd を受ける製品 API はありません。実 producer / GUI / 鍵 / policy / 外部接続を使いません。

[`check_mac_active_io.py`](../scripts/check_mac_active_io.py) は Swift normal / fixture を arm64 と Intel に compile し、host architecture だけで129条件を実行します。echo、reader 喪失の実 EPIPE、実 close 後の合成 receipt 喪失、Wait 済みだが EOF 未観測、通常 owner の拒否を含みます。4 child は元の Process で実 Wait し、全16端点の物理 close を確認します。unknown 3件は元 owner の metadata を保持したまま、自己所有 fixture program の終了まで残します。これは kernel close エラーの注入試験や製品 cleanup confirmed ではありません。

元 wrapper の同一性、起動後 setter 不使用、二重 half-close / retire、閉鎖済み番号を再利用した自己所有 sentinel の生存も検査します。child は自身の fd table と元 pipe inode を確認し、親側端点の余分な継承を検出します。この観測は自己所有 child の一点の確認であり、同時 spawn 全般の atomicity 証明ではありません。最初の echo seam は未実装 member の compile red から green、他のケースと report 拒否は追加回帰です。

## 残る接続

製品の sealed launch と publisher / exact release、保護配置・policy・署名・verify-to-exec、C ABI / 同じ Rust owner の終了・IO 接続、authoritative Close、IO worker / supervisor / outer monitor の実 join は未接続・未実証です。固定 child の EOF / Wait と未起動 worker の join を、実 producer の回収完了や GUI Quit 許可に合算しません。

実 GUI / producer 起動、実鍵 / policy、署名・配置・OS trust は未実行です。凍結 a4 / a5 は変更せず、旧実機証拠をこの source pin へ引き継ぎません。既存 Windows timeout の根因を解消したとは扱いません。

## 後続の active component bridge

[同じ C ABI / Rust owner の接点](mac-native-active-bridge.md)は、この Swift leaf の終了観測と active IO を同じ handle / TLS worker へ渡します。IO 回収後も native record を保持し、replacement / full joins / Quit の許可にはしません。通常起動は未接続です。既存 Pump / supervisor の active 配線、signed launch と全 joins は引き続き残件です。上記の未接続説明は各 leaf 単独の保証範囲を表します。
