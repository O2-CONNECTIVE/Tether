# Mac resident producerの公開source段階

未統合#200/#201/#202/#203に依存する準備sourceです。
#200のWindows session-create阻害、stacked CI未実行、通常未接続を解除しません。

## 実際の閉包と今回の切り出し

固定producer sourceを全件manifest照合し、Go1.27.3のproduction file選択でDarwin arm64/cgoの依存を追いました。
開始時の#203に対する比較は24package/91 production fileで、同一45・新規31・変更15でした。
test、他OS、外部moduleや配布物の閉包はこの数字と別です。
13fileの直前private deltaだけを公開差分だと扱わず、既存public APIの変更も含めて調べます。

今回は[`internal/residentwork`](../backend/internal/residentwork/README.md)だけを完全なpackageとして切り出します。
policy・client・serverが共有する具体的なv1 transcriptで、標準library以外の依存がありません。
署名対象byte形式とfield制約を保持しますが、署名検証・現在の期限・認証・承認・実行許可を判定しません。
通常caller/CLI、鍵/設定/通信/常駐起動、第二のpolicy承認器を追加しません。
GoCIは当該1 production fileを厳密に列挙し、未知file/fixture/native factoryの混入を拒否します。

## 後続の具体的な依存

1. resident wire/mapperとread-only owner observation。strict framing・revision/freshness・receipt/transition・unknownを保持し、架空のproduction ownerで代替しません。
2. concrete worker/standby ownerとtransport/admissionのsource閉包。同じ所有者の停止、物理回収、unknown保持・禁止置換を、既存public security/authorizationへ対応付けます。
3. resident policy composition。既存`agentkeys.ReadTrustConfig`の保護read、strict schema/fixed scope、既存key loader、immutable factoryへ接続します。署名transcriptを得たことだけで認証/実行許可を作りません。
4. 明示的なproducer組成。以上のdirect/transitive APIが揃い、試験とレビューを閉じるまで通常CLIへ取り込みません。元ownerのClose・実EOF・exact Child・IO/supervisor/outer joinを別々に確認します。

これらを一括でsnapshot移植せず、型と実callerの閉包ごとに差分を固定します。
source/fake/memory試験をnative保護・認証・live producer・GUI・配布の成功へ広げません。

## build/packageと実行信頼は別の証拠

公開Tauriはbundle.active=falseでresident producerを同梱しません。
既存のMac agent unsigned評価archiveやWindows connected-package planは、resident producerの信頼根拠ではありません。

sourceとartifactの対応には、固定commit/tree、選択production fileとlock、toolchain/CPU/build flags、実出力bytesのdigestと同梱相対名を一つのbuild記録へ結ぶ必要があります。
この記録が示すのは同一bytesへの対応で、publisher identityや過去の配置保護を単独では証明しません。
manifestと期待digestを同じ入力元が差し替えられる状態や、利用者入力path/hashをtrustedとして渡す入口を作りません。

実行の許可には、別に承認された配布/配置元の根拠、保護された配置と同じ実行imageの対応、検証からexecまでの置換境界が必要です。
署名済み配布や新しいtrust rootが既に存在するとは宣言しません。署名/配置方式と同UIDに対する保証範囲も未確立です。
policyは既存のtrusted native compositionから選択したalready-enrolled保護入力だけを既存loaderへ渡し、WebView/env/remote入力で代替しません。

この二つの証拠とpolicy供給が閉じて初めてsealed launcher→同じChild/pipes→first観測→既存ResidentPort/明示IPCへ接続できます。
通常GUI/native Quitとの結合と実機再検証はその後です。
今回、実producer/鍵/policy、署名・OS trust・運用設定、persistent accessを作る操作は行いません。
