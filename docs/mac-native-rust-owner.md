# 同じ未起動 native owner を Rust から保持する

[#221 の FFI / IO 準備](mac-prelaunch-ffi-io.md)に依存する source-only の具体的 adapter です。
[`rust/owner.rs`](../ui/tauri/native/resident-launch/rust/owner.rs)は実 C ABI の create / state / retire を呼び、同じ native handle と、その先の未起動 Process / owned pipe を保持します。
別 owner、mock FFI、汎用 trait / factory は追加しません。通常 Cargo / Tauri / IPC に module・link 設定を追加せず、publisher 未設定の通常 prepare は拒否します。

## Rust と native の所有契約

Rust owner の constructor は native create だけです。handle / fd / path / argv / identity / hash を caller が指定する入口や、Clone / Copy はありません。
`PhantomData<Rc<()>>` により Send / Sync を実装せず、safe Rust で元 thread から owner や共有参照を移せません。実 Rc を割り当てる仕組みではありません。
native 側も元 pthread TLS identity を確認するため、unsafe な foreign caller を型だけで信頼しません。
[Rust Rc の thread 制約](https://doc.rust-lang.org/std/rc/)

| 操作 | 結果 |
| --- | --- |
| prepare | Refused / Owned / Unknown。非zero handle が UNKNOWN なら Owned にしない |
| state | 元 native state を確認。未知値・invalid・拒否は Unknown、成功へ推定しない |
| retire | native の RETIRED_UNLAUNCHED だけを成功とし、以後は同じ結果を保持 |
| Drop | まだ Owned の場合だけ元 native retire を一度呼ぶ。成功通知を作らない |
| unknown の再 retire / Drop | native retire を繰り返さない。native の元 Process record が残り、置換を拒否 |

Rust guard の消失は元 native record の解放を意味しません。
`mem::forget` や abort では Rust Drop を保証できません。元 thread が終了した場合の native TLS destructor は元 IO を回収し、元 Process を unknown として保持します。
thread 自体が残ったまま guard を忘れる場合は、native slot / IO が残る fail-closed な保留であり、回収完了とは扱いません。

## 実 library の境界

Swift の `@c @implementation` は C header と signature を compiler で照合しますが、internal visibility のままでは今回の dylib に C symbol を export しませんでした。
初回の実 Rust link が3 symbol未解決で失敗し、nm は正しい名を non-external と表示しました。ABI function の public visibility を明示する最小修正で link を成立させています。
#222 時点では通常3つの preparation export を検査しました。[後続の byte IO](mac-native-byte-io.md)は通常6 / fixture14、[Pump source fixture](mac-native-pump.md)時点は通常6 / fixture16 export を要求します。[後続 active bridge](mac-native-active-bridge.md)を含む現在は通常9 / fixture23 を要求し、欠落・混入を拒否します。
[Swift C export](https://www.swift.org/blog/swift-6.3-released/)

これは配布用の signed library / release identity / install ではありません。各 binary は自己所有の一時 fixture で、信頼済み配置や起動許可に使いません。
compiler が付け得る ad-hoc signature も publisher や配布承認の根拠にしません。

## 検証入口

既存 Rust / Swift / SDK を PATH で選び、repo root から明示的に実行します。導入や既定 toolchain の変更はしません。

```sh
python3 scripts/check_mac_rust_owner.py
python3 scripts/check_mac_preparation_ffi.py
python3 scripts/check_mac_launch_requirement.py
```

新 checker は同じ Swift source の通常 / fixture library を実 Rust caller に link して検証します。
通常構成の拒否、明示 retire、Drop、unwind、匿名 IO / EOF、部分取得中の receipt 喪失、unknown の禁止置換、元 thread 終了を自己所有 fixture で検査します。
部分取得の unknown は実 close 後の合成確認喪失で、実 kernel close errno の注入ではありません。
Send / Sync の両構成 compile-fail は E0277 と該当理由を要求します。通常 constructor への fixture 混入も compile-fail で検査します。単なる compiler 失敗・標準 library 不在・成功・別のエラーは合格ではありません。
compile-fail を含む全 compiler / fixture subprocess は同じ子の終了まで待ち、割込み時は成功にせず、終了前に一時 source / library を片付けません。

Swift は arm64 / x86_64 の両構成を compile し、Rust は checker の host architecture で両構成を compile / link / 実行します。foreign Rust compile と foreign 実行はこの checker の検証外です。
fixture の deployment target 11.0 は製品最低 OS の変更ではありません。14.4 未満の native 準備拒否、旧 OS 実 load 未確認を維持します。

## 残る具体接続

次の接続先は `resident_port/duplex` の同じ所有 worker です。現行 Pump の std Child をこの Foundation owner の exact Wait に代用せず、native owner の IO transfer と active lifecycle を結ぶ必要があります。
今回の retire は未起動 pipe の解放だけで、authoritative Close / 実 producer EOF / exact Process Wait / active IO・supervisor・outer monitor joins / GUI Quit 許可ではありません。
sealed 実行、atomic inheritance 境界、実 publisher / release / protected policy、署名・配置と OS enforcement は未確立です。read/write / launch / Wait の製品入口を追加していません。
#200 の Windows blocker、長い未統合 stack と CI 未実行を維持し、draft のみです。GUI / producer 実起動、実鍵・policy・署名・配置・OS 設定・persistent access は扱いません。
