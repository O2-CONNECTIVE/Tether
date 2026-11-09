# Go制御契約adapterの純粋な接続

[採用候補ADR](adr/0001-control-contract-candidate.md) / [正本schema](../contracts/control-v1/schema.json) / [共通vector](../contracts/control-v1/vectors.json) / [Issue #25](https://github.com/O2-CONNECTIVE/Tether/issues/25)

固定正本は公開head `ef556a26a410d23cb383410e37e0db77cdf7c9e0` です。
正本6ファイルを変更せず、既存 `internal/control` にcodecとstdio.Service adapterを追加します。
共有定義の変更は正本ownerへ戻し、Go側だけのwire field・版・error codeは追加しません。

## 初期版の保証範囲（2026-10-28改訂）

初期版は、遠隔作業を行う非管理者guestから監督serviceと停止処理を守る範囲へ限定します。
ホスト操作ユーザーや管理者が既に侵害されている場合、および管理者guestへの対応は保証対象外です。
別保護service、guest SID、immutable grant、hard TTL、固定宛先と許可鍵、不可逆revokeの要件は維持します。
正規hostoperatorの承認操作、guestからのIPC拒否、ACLとpeer identityの確認は必要です。
毎回の別admin sign-inを初期版の必須条件として追加しません。
これは保証範囲の改訂記録であり、OS設定・認証・実機受入の成功や、既存全受入条件の達成を示しません。
受入定義の正本改訂と未検証の状態は別に管理し、P2専用再現やOS変更の実行承認へ読み替えません。

## 検証する境界

codecはLF込み16KiB、深さ8、UTF-8、単一object、復号後の重複key、exact field/kind/payloadと正規uint64文字列を検査します。
CRLFのCRはJSON空白として許容します。
Encodeもmarshal前の内部入力長と、生成後の同じwire制約を検査します。
共通49 wire vectorのbyte列はhexから直接復元します。
JSON文書のcheckout改行だけを正規化してschema/vectorのSHA-256を固定し、wire byte列は正規化しません。

Stateは保護所有者から事前に渡されたgeneration/Identity/hash/nonceとbootstrap・方向別連番・要求IDを照合します。
helloの値から期待contextや認可grantを生成しません。
hash/nonceの一致はchannel bindingであり、同一userへの認証や隔離の証明ではありません。
不正frameやcontext/順序不一致はechoせず閉鎖し、業務拒否は検証済み相関だけで固定codeを返します。
各1件の成功start/stop受付だけを保持し、同じ内容の再送へ新sequenceでreceiptを返します。
失敗応答やsnapshot結果は成功receiptとして保持しません。
新IDのstopは同じSessionへ別要求として渡し、保持済みの同ID・同じ復号済み論理内容だけを再送と扱います。
request IDやsequenceの最大値を消費してstop不能になる前にchannelを閉じ、所有loopのcleanupへ進めます。
相関台帳は通常15件/48KiBとstop1件/16KiBを上限にします。
これは未完了要求の台帳であり、実行queue・背圧中の停止受付・総メモリを実装した保証ではありません。

Adapterは既存coordinatorへsnapshot/stopを渡し、startは常に拒否します。
内部Reasonと生errorはwireへ流さず、正本の六codeへ変換します。
`goCloseConfirmed`は同じIdentityのClose成功後だけtrueにし、terminatedやOwned=0から推定しません。
stop receiptとGo Close確認は所有processの正常終了・回収と別です。
実EOF、reader完了、所有processのexit0と回収を含む正常終了の判定は提供しません。

Handle/Closeは有限deadlineを要求し、受付競合の待ちにも取消を適用します。
serviceのCloseでは新しい有限contextで同じSessionの停止確認を要求します。
不正入力時のwire閉鎖後も、呼出側は所有serviceのCloseを必ず実行する必要があります。
既存stdio.Runのdefer cleanupへ接続する型として適合しますが、製品CLIには配線していません。
長い処理の割込み、親死、OS停止、独立失効は保護監督の未実装範囲であり、このadapterでは成立しません。

## 試験と限界

最初のhelloはstubで拒否されるREDを観測し、codec実装後にGREENを確認しました。
共有49 wire vectorと6状態vectorの適合確認を別に記録します。
状態vectorの初回は文書内の整形改行をwireへ渡した試験準備エラーで、機能REDには数えません。
adapterのbootstrap→start拒否→snapshot→stop→snapshotと、deadlineなし受付拒否は実装欠落を確認してからGREENへ進めました。
その後の再送・未確認終了・資源上限・loop cleanupは純粋な回帰確認です。

in-memory Session seamとメモリ入出力だけを使い、実Session、鍵、listener、SSH接続は生成しません。
製品CLIの起動拒否、P2専用runtime未実証、拒否された専用/代替再現の停止、double-close NOT_MET、Windows背圧UNVERIFIEDを維持します。
保護所有者の認証、grantの保護受渡し、hard deadline、独立失効、実行queue、実process回収は未接続です。
pure codec/state、既存Session APIへの対応、CI成功を実SSH・実機受入・製品gate解除へ読み替えません。
