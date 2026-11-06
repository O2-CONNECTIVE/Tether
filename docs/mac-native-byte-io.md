# 同じ native owner の借用 byte IO

[#222 の Rust owner](mac-native-rust-owner.md)に、既存 `std::io::Read` / `Write` へ結ぶ具体的な借用 view を追加します。
fd 所有は Swift に残し、`PreparedIo` は `&mut NativePreparation` を借ります。raw fd の受渡し、別 owner、新しい generic port / factory はありません。
通常 Cargo / IPC / GUI は未接続で、publisher 未設定の通常 prepare は引き続き拒否します。

## Byte と lifetime の契約

native の read / write / control half-close は同じ handle と元 pthread TLS を確認し、元 owner が保持する parent 端点だけを扱います。
read / write は最大4096 bytesの一回の nonblocking syscallです。buffer pointer を保持しません。null・0・過大な count は syscall 前に拒否します。C caller は非 null の有効な buffer を渡す責任があり、safe Rust adapter は slice の長さと lifetime でこれを満たします。
結果は UInt64 の high32 tag / low32 count。bytes、pending、raw EOF、halfClosed、unknown、rejected を区別し、Rust は count と tag の不正な組合せを成功にしません。

| 観測 | Rust の結果と所有状態 |
| --- | --- |
| 実際に読書きした bytes | 実 count だけを返す。部分進捗は全量成功にしない |
| EAGAIN / EINTR | WouldBlock。retry や deadline 延長は native 内で行わない |
| 非空 read の実0 | raw EOF。owner は OwnedUnlaunched のまま |
| 空 slice の read / write | syscall なしで0。EOF の証拠ではない |
| control half-close | parent writer を一度 close。再要求も再 close しない |
| half-close 後の write | BrokenPipe。output read は引き続き可能 |
| IO / close の不明結果 | 同じ owner の残端点を一度 cleanup し元 record を UNKNOWN 保持。置換は禁止 |
| flush | user buffer がないため追加操作なし。receipt・delivery・Close の証明ではない |

view を使用中は safe Rust で owner の retire / drop ができません。view 自体の drop は借用を終えるだけで端点を閉じません。
同じ owner の元の retire / Drop が最後の回収を担い、reader / writer を別の所有者へ分けません。
準備段階の pipe bytes を認証済み context / status / receipt に変換せず、raw EOF / half-close から authoritative Close / OFF / GUI Quit 許可を生成しません。

## 合成試験

```sh
python3 scripts/check_mac_rust_owner.py
python3 scripts/check_mac_preparation_ffi.py
python3 scripts/check_mac_launch_requirement.py
```

同じ未起動 Process と自己所有匿名 pipe の固定 peer を使います。Process.run、GUI、producer、鍵、policy、認証を使いません。
Rust → 実 C ABI → 同じ owned端点の byte、pending、raw EOF、half-close、invalid buffer拒否、確認喪失後の unknown を検査します。
close receipt fault は実 close 後の合成確認喪失です。実 kernel read/write errno 全種の注入や、起動後の SIGPIPE・peer 喪失を実証したものではありません。
新 checker は前段53条件を維持して92条件を要求します。両構成の Send / Sync、借用中 retire / drop、通常 fixture API 不在の9 compile-fail は、所定の code と理由を確認します。
前段 source では `io` 自体が無い E0599 となるため、これを正しい借用拒否として合格にしません。実装後は E0499 / E0505 を要求します。
#224 時点の通常native6 / fixture14 exportに対し、[後続Pump fixture](mac-native-pump.md)ではfixture peer送受信だけを追加し、通常6 / fixture16 exportを厳密に区別します。既存 C 74条件を維持し、peer反復の16条件を追加した90条件、requirement35条件も維持します。
新しい fixture helper の close後再呼出しは、初回に自分の新しい pipe の flag を変更する negative を検出しました。必要な元端点の attempted 状態を fcntl 前に確認する最小修正で、閉じた番号の再利用へ触れません。既存 process の fd 検索や操作は使いません。
Swift の両 architecture・両構成、Rust host の両構成を compile します。Rust Intel std は既存 toolchain に無く未実行、foreign 実行・旧 OS load も未確認です。toolchain 導入や製品 minimum OS 変更はしません。

## 後続との境界

既存 `resident_port/duplex/Pump` は Read / Write と control half-close を使います。後続の[source-only Pump接続](mac-native-pump.md)で同じframing coreへ結びますが、通常startup / native supervisorには登録しません。
sealed 実行、同じ Foundation Process の終了結果と exact Wait、起動後の child側端点解放、SIGPIPE / peer喪失と atomic inheritance、authoritative Close、IO / supervisor / outer joins の所有証拠を閉じる必要があります。
`std::process::Child` の別 owner で native Wait を代用せず、未起動 owner を active owner に変更しません。
実 publisher / 承認 release / protected policy / 署名・配置 / OS enforcement は未確立です。未設定なら fail closed、通常起動は未接続。#200 blocker / 未統合 stack / CI 未実行は継続します。
