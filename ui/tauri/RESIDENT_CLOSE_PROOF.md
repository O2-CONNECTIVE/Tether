# Resident wireのClose証跡前提

標準の`run()`は`commands_with_state`を使い、`get_resident_status`と`request_resident_intent`を登録します。
標準buildのUnbound statusはmacOSで`source-unavailable`、Windowsなど非対応OSで`unsupported`です。intent receiptは`unavailable`で、producerへは送信されません。
標準buildは`resident_native_source` cfgを有効にせず、`ResidentState`のsourceは`Unbound`でproducerを起動しません。macOS標準buildの`request_exit()`はtrueとなり、`ExitRequested`を保留しません。macOSで`#[cfg(all(target_os = "macos", resident_native_source))]`を明示した署名候補では、`NativeSession`が`NativeOuterOwner`を保持し、登録済みproducerを固定bundleから起動するsource経路があります。`run()`の`ExitRequested`はouter ownerの判定がfalseの間`prevent_exit()`で退出を保留します。この署名候補の実機起動・回収は未検証です。
この文書は内部`resident_port`のwire復号・状態整合・post-Close証跡部分を扱います。
[具体duplex owner](RESIDENT_DUPLEX_OWNER.md)はportとStatus/Receiptの内部IOへ接続しています。標準buildは`Unbound`のままで、署名候補のproducer起動経路とは構成を区別します。
ON/OFFのwire dataだけでは、本人同意や実行許可を成立させません。

## 復号と証跡

`duplex/wire.rs`は既に区切られた1行のpayloadを最大4095 byteとして検査します。
version、未知/重複field、必須nullable field、safe integer、状態とtransitionの整合を拒否します。
完全なJSONL framing、partial frameの時間制限、実EOFの観測は内部の具体IO ownerが担います。
復号できた値やsource名は、producerの認証や実行許可の証明ではありません。

`duplex/closure.rs`は同じlifetimeで次のwire条件を保持します。

- Close要求、stdin半閉鎖、freshな単回`closed: confirmed`、EOFがそろう。
- terminalのsource時刻はClose要求以後で、native/source時計が後退せず、未来や3000ms以上前ではない。
- 通常statusやaccepted OFF receiptはClose証跡にならない。古い通常statusの表示期限をshutdown全体のtimeoutへ転用しない。
- unknown、fault、不正値、duplicate、terminal後の追加frameは失敗を保持する。EOF後の入力でも復活しない。

EOF後event拒否は、従来PumpがEOFで止まる順序をこの内部部品でも強制する追加guardです。
単体の`Closure::confirmed`はwire条件の一部分だけを表し、アプリ終了や資源解放を許可しません。
このwire部品自体は実pipe、子process、threadを生成しません。

## 具体的な後続接続

既存の`Pump::step/consume`は同じChildから取得したstdin/stdoutを所有し、実半閉鎖・bounded framing・実EOFをClosureへ伝えます。
既存の`Supervisor::record`は、同じproducerからのauthoritative Close、実EOF、exact Child Wait、reader/writerを含むIOとsupervisor joinを区別して検査します。
明示構成の`NativeOuterOwner/ExitGate`はouter monitorの実joinを待ち、unknownを保持します。
Pump/Supervisorとstartup rejectionの別証跡は内部実装へ接続しています。明示的な`resident_native_source` cfgのmacOS構成ではouter monitor、guarded Quit、署名候補の登録済みproducer起動経路もソースにあります。実機での起動・回収は未検証です。
単純なprocess exitやreap gateを、resident全体の回収証明へ昇格させません。

## 確認範囲

このwire部分の試験はmemory内のpayloadとClosureを扱い、実IO ownerのfixtureとは結果を分けます。
正負のschema、時刻、OFF/status/receipt混同拒否、terminalとEOFの順序、unknown/fault保持を検査します。
署名候補のproducer起動経路は実機未検証です。認証、OS保護、実GUIのON/OFF/Quitとouter monitorも未実証です。自己所有fixtureのIO/supervisor joinを製品全体の終了証明にしません。
