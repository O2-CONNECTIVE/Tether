# Resident wireのClose証跡前提

通常Mac起動とIPCは未接続のままです。
内部`resident_port`は、既存の凍結実装からwireの復号・状態整合検査・post-Close証跡だけを切り出したsource前提です。
port、provider、Status factory、intent受付、launcher、任意Command/path/fd入口はありません。
ON/OFFのenumやreceiptは入力schemaを検証するdataであり、操作を実行しません。

## 復号と証跡

`duplex/wire.rs`は既に区切られた1行のpayloadを最大4095 byteとして検査します。
version、未知/重複field、必須nullable field、safe integer、状態とtransitionの整合を拒否します。
完全なJSONL framing、partial frameの時間制限、実EOFの観測は後続のIO ownerが担います。
復号できた値やsource名は、producerの認証や実行許可の証明ではありません。

`duplex/closure.rs`は同じlifetimeで次のwire条件を保持します。

- Close要求、stdin半閉鎖、freshな単回`closed: confirmed`、EOFがそろう。
- terminalのsource時刻はClose要求以後で、native/source時計が後退せず、未来や3000ms以上前ではない。
- 通常statusやaccepted OFF receiptはClose証跡にならない。古い通常statusの表示期限をshutdown全体のtimeoutへ転用しない。
- unknown、fault、不正値、duplicate、terminal後の追加frameは失敗を保持する。EOF後の入力でも復活しない。

EOF後event拒否は、従来PumpがEOFで止まる順序をこの内部部品でも強制する追加guardです。
単体の`Closure::confirmed`はwire条件の一部分だけを表し、アプリ終了や資源解放を許可しません。
このsliceは実pipe、子process、threadを生成しません。

## 具体的な後続接続

既存の`Pump::step/consume`は同じChildから取得したstdin/stdoutを所有し、実半閉鎖・bounded framing・実EOFをClosureへ伝えます。
既存の`Supervisor::record`は、同じproducerからのauthoritative Close、実EOF、exact Child Wait、reader/writerを含むIOとsupervisor joinを区別して検査します。
さらに`StandbyOwner/ExitGate`はouter monitorの実joinを待ち、unknownを保持します。
これらのcaller、startup rejectionの別証跡、trusted producer準備とprotected policy、guarded Quitは今回まだ接続していません。
単純なprocess exitやreap gateを、resident全体の回収証明へ昇格させません。

## 確認範囲

試験はmemory内のwire/frame payloadとClosureだけを扱います。
正負のschema、時刻、OFF/status/receipt混同拒否、terminalとEOFの順序、unknown/fault保持を検査します。
実producer、認証、OS保護、実GUIのON/OFF/Quit、writer/reader/supervisor/outer monitorの実join成功は未実証です。
