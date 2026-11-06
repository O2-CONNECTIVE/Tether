# 未起動 owner の FFI と匿名 IO

[#218 の typed requirement 準備](mac-launch-requirement-preparation.md)と同じ `Process` を C caller から保持し、起動前の匿名 pipe を所有させます。
通常 Tauri / Rust 起動には link しません。production publisher は未設定のまま、C の create は owner / pipe を作る前に 0 を返します。
macOS 14.4 未満も拒否します。fixture の compile target 11.0 は製品の最低 OS 宣言ではありません。

## 所有契約

この #221 段階の [`ResidentPreparation.h`](../ui/tauri/native/resident-launch/ResidentPreparation.h) は create / state / retire です。現在の source には後続の[借用 byte IO](mac-native-byte-io.md)もありますが、起動や回収完了の許可は追加していません。
外部へ raw object pointer を渡さず、path / argv / fd / hash / publisher も受け取りません。
UInt64 handle は process 内の所有確認用で、認証・起動許可ではありません。

| 結果 | 意味 |
| --- | --- |
| create の 0 | 拒否。返却する所有 slot はない |
| OWNED_UNLAUNCHED | 同じ元 thread が未起動 owner を保持。起動・接続・ON の証明ではない |
| RETIRED_UNLAUNCHED | その未起動 owner の pipe 解放を確認し slot を破棄した |
| UNKNOWN | 元 record を保持。置換・close 再試行は禁止 |
| WRONG_THREAD / INVALID | owner へ触れず拒否。元 slot は維持 |

単一 slot の全状態を lock で保護し、handle は単調増加・再利用なし、枯渇時は割当前に拒否します。
元 thread には非公開の pthread TLS object を保持し、操作時の同じ TLS object と照合します。
TLS の値は thread ごとに独立し、新しい thread には NULL で始まります。数値 pthread ID の再利用や `Thread.current` の同一 object という未確認契約には依存しません。
thread 終了時の TLS destructor は、その元 thread 上で未起動 IO の解放を試み、元 Process を UNKNOWN のまま保持します。fixture の pthread join 後も置換を認めません。
TLS key は process lifetime に一つだけ保持します。[POSIX TLS 契約](https://pubs.opengroup.org/onlinepubs/9699919799/functions/pthread_key_create.html)

## IO と終了の区別

2 本の新しい匿名 pipe の 4 端点だけを採用します。部分取得を先に owner へ記録し、その後に CLOEXEC と parent 端点の nonblocking flag を設定します。
同じ Process の stdin / stdout に child 側の `FileHandle(closeOnDealloc: false)` を保持させます。
retire は Process の wrapper 参照を外し、weak ref の消失を確認してから、各元 fd を一度だけ close します。
getter 観測による autorelease 参照も消える前には close しません。
close の errno を元 record に保持し、失敗は UNKNOWN。再 close や deinit での隠れた cleanup はありません。
[Apple FileHandle](https://developer.apple.com/documentation/foundation/filehandle/init%28filedescriptor%3Acloseondealloc%3A%29)、[Apple Process input](https://developer.apple.com/documentation/foundation/process/standardinput)、[POSIX close](https://pubs.opengroup.org/onlinepubs/9699919799.2018edition/functions/close.html)

`pipe` 後の `fcntl` は atomic CLOEXEC 作成ではありません。この未接続 source は launch-safe transport として使えません。
実行を接続する前には同時 exec の inheritance 境界も閉じる必要があります。
Foundation Process の別 owner を作ったり、既存 Rust `Child` の Wait で証明したことにはしません。

## 試験と未実証

```sh
python3 scripts/check_mac_preparation_ffi.py
python3 scripts/check_mac_launch_requirement.py
```

新 checker は installed Swift 6.3 の `@c @implementation` と同じ C header を compiler で照合します。
通常構成と fixture 構成を arm64 / x86_64 向けに compile し、host architecture の fixture のみ実行します。
Weak framework link を検査し、未対応・部分実行・0 件・失敗・余分な出力を成功にしません。
全 compiler / fixture subprocess の終了を待ってから専用一時領域を解放します。
[Swift C export](https://www.swift.org/blog/swift-6.3-released/)

C fixture は固定合成 identity の未起動 Process、自己所有 pipe、自己所有 pthread のみです。
6 modes で stale / busy / wrong-thread / overflow、部分取得回収、元 thread 終了後の UNKNOWN、close receipt 喪失後の禁止置換を検査します。
receipt fault は実際の close 成功後に確認だけを失わせ、fixture 自体の fd 残留を作りません。これは実 errno の kernel fault 注入ではありません。
IO case は固定 1 byte の双方向読書きと匿名 pipe の実 EOF を確認します。
最初の IO negative は、getter の autorelease 参照を内側 pool で解放する修正により解消します。close 条件や期待値は緩めません。

**未実証:** 実 producer の EOF、authoritative Close、exact Process Wait、reader / writer の実 join、supervisor / outer monitor join、起動後 FFI lifetime、実署名の enforcement、旧 OS load、architecture / Rosetta、配布・配置・protected policy。
Process の run / executableURL / argv / PID / signal / Wait、実鍵・実 policy・GUI・認証・persistent access は追加していません。
後続はこの同じ owner に sealed executable と owned IO transfer / active lifecycle を結び、既存 duplex / Standby owner の終了証拠へ接続する工程です。

#200 の Windows timeout 阻害と未統合 Mac stack は継続します。この draft のローカル試験は stack CI や main 統合の成功ではありません。
