# 同じ native owner の terminal component と退出判定

変更起点は main `9b8591e478dd65de310907a1c7e06e65d8c30c59` です。
これは既存の Swift / Rust 所有経路の接続であり、通常の producer 起動や署名済み release の実証ではありません。`ApprovedRelease.production` は未設定、通常 startup gate は閉じたままです。

## native component

`rh_resident_preparation_finish_active` は、同じ handle / 元 TLS worker で一度だけ受け付けます。同じ Process の終了観測、元の全4端点の確定 close、実 EOF、active IO の退役、Unknown 履歴なしを再確認し、元 record を terminal tombstone として保持します。別 owner の生成・置換を許可しません。

Rust の `NativeTerminal` は private field を持つ非 Copy の component です。これを返せた `NativePreparation` だけが active abandonment を解除できます。未完了・失敗・Unknown の Drop と TLS 終了は従来どおり元 record を Unknown として保持します。terminal 後の通常 IO と重複 finalization は拒否されます。

native component 単独は authoritative Close、worker join、退出許可ではありません。元 `NativeIoSupervisor` は clean EOF、unwind なし、同じ wire の確定証拠と exit 分類が揃う場合だけ finalization を呼びます。既存 parser fault、missing / unknown / malformed Close、失われた close receipt、遅い物理回収を成功へ変更しません。

## join と分類

1. 同じ IO worker の実 `JoinHandle.join` 後だけ、native component を `NativeJoined` の private evidence にします。
2. 元 outer monitor は証拠を保存しても成功を公開せず、Unknown を維持します。
3. `NativeOuterOwner.poll_join` が元 monitor の実 join に成功した後だけ、以下を分類します。held monitor、自己 join、競合、panic は退出成功になりません。

- Confirmed: authoritative confirmed Close、実 EOF、exit 0、native terminal component、元 IO worker と outer monitor の実 join。
- StartupRejected: 最初の正しい rejection frame、intent なし、実 EOF、exit 2、同じ native component と両 join。
- NotSpawned: 正に未起動かつ退役済みの元 preparation と両 join。
- その他は Unknown。create の 0 / Refused は既存 record による拒否も含むため NotSpawned と同義ではありません。

request_exit / finish_requested は未終了 monitor を待つ成功経路に入る前に nonblocking の join 準備を確認します。退出要求そのものと、終端の証拠は別条件です。

## 検証契約と今回の境界

既存 active bridge / supervisor / outer / session の有限 self-child を使います。direct terminal case は元 worker 内で EOF・exact Wait・IO retirement を順に確認し、前提不足、別 thread、重複呼出し、Drop / TLS 後の tombstone、再生成拒否を検査します。借用中 finalization と Copy の compile refusal を追加し、元 !Send / !Sync の検査を維持します。

既存の confirmed / rejection positive だけを許可側へ変更し、exit 2 だけで rejection proof がない child、Unknown Close、欠落・不正 frame、signal、lost receipt、unwind、outer panic と有限 backpressure の負例を保持します。outer held は内側の証拠が揃っても実 outer join 前の退出を拒否します。

今回の作業環境で実行したのは Python checker の offline unit test と静的検査だけです。Swift / Rust の compile、native self-child、GUI、実 producer、署名 enforcement は未実施です。native 検証は独立レビュー後、承認済み hosted macOS 27 CI の固定 head で別途判定します。既存 source pins の変更を検査成功の代わりにしません。

実鍵・protected policy・signing identity・credential・persistent access・ユーザー Mac・OS 設定には触れません。製品の sealed launch、実 release identity、配置と enrollment の承認・実機受入は後続です。
