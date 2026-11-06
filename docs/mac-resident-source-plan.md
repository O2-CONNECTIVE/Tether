# Mac resident producerの公開source段階

未統合 #200 / #201 / #202 / #203 / #204 / #205 / #206 / #207 / #210 に依存する準備 source です。#208 / #209 の Windows 診断は別系統です。
#200のWindows session-create阻害、stacked CI未実行、通常未接続を解除しません。

## 開始時の閉包と段階的な切り出し

固定producer sourceを全件manifest照合し、Go1.27.3のproduction file選択でDarwin arm64/cgoの依存を追いました。
開始時の#203に対する比較は24package/91 production fileで、同一45・新規31・変更15でした。
test、他OS、外部moduleや配布物の閉包はこの数字と別です。
13fileの直前private deltaだけを公開差分だと扱わず、既存public APIの変更も含めて調べます。

前段は[`internal/residentwork`](../backend/internal/residentwork/README.md)だけを完全なpackageとして切り出します。
policy・client・serverが共有する具体的なv1 transcriptで、標準library以外の依存がありません。
署名対象byte形式とfield制約を保持しますが、署名検証・現在の期限・認証・承認・実行許可を判定しません。
通常caller/CLI、鍵/設定/通信/常駐起動、第二のpolicy承認器を追加しません。
GoCIは当該1 production fileを厳密に列挙し、未知file/fixture/native factoryの混入を拒否します。

## 次段のworker owner閉包

続いて[`internal/workerowner`](../backend/internal/workerowner/README.md)をpackage全体で移します。
3 production fileと5つの元試験file・追加回帰で、取得・Run・Close、RunOnceの元cleanup予算、source切断とhost停止を同じownerへ結びます。
標準library以外の依存はなく、試験はメモリ内fixtureだけです。
source cleanup中のhost取消の再確認漏れを追加負例で再現し、元予算内の同じOwner.Stopへ移す最小差分を含みます。
wire/mapperは具体的Standby、policy/transportは既存public APIの追加差分を必要とするため、今回はその代わりの汎用portや架空ownerを作りません。

後続の具体的接続先はmaclaunchのresident worker adapterとresidentmac.Standbyです。
protected policy・transport・具体的Sessionの終了証拠を揃えるまで通常callerを追加しません。
この所有slotの完了と、実EOF/exact Child/IO/supervisor/outer joinの完了は別です。
unknownでは同じOwnerを保持し、別ownerで回復したことにしません。

## source 段階で扱う具体的な依存

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

## 正規transport ownerの前提

次段は[共通Dispatchと具体的owner cleanup](mac-canonical-transport-owner.md)を既存6 implementation fileで閉じます。
agenttransportからmaclaunchへのproduction型依存を取り除き、後続protected factoryからの逆向き依存が循環しないようにします。
通常のTTY承認入口は維持し、部分取得ownerと元のcleanup結果を失わず観測します。
resident transport/policyを有効化せず、opaque receiver、protected sourceとStandbyの実組成は後続です。

## fixed intentの実引継ぎ

[既存Session／wireの次段](mac-fixed-intent-handoff.md)では、正規adapterのnamespaceと固定内容、有限runtime deadlineを同じSessionへ渡します。
元のclaim・replay・timer／cancel ownerを維持し、新しい認証や通常resident入口は追加しません。
具体的resident transport／opaque receiver／work registration、protected policy／source／worker、Standby／wire／producer組成と配置信頼は未接続です。

## transport / opaque receiver / policy の結合

[今回の閉包](mac-resident-transport-policy.md)で、両側のcontrol、opaque receiver、同じwork登録・receipt、固定MCP操作、strict policy/source/workerを結びます。
既存4 packageに17 implementation fileを限定登録し、通常cmd・resident起動・IPCは未接続です。
正の接続試験は実相互proofと自己所有の合成fileを使って `NewWireRequest` までを確認し、`Approve` / `Run` や全native実行は未実証として分けます。
次の [Standby / owned pipe 組成](mac-resident-producer-composition.md) で具体的な source を接続し、sealed entrypoint と配布・起動信頼を残します。

## Standby / owned pipe の具体的組成

[今回の source 組成](mac-resident-producer-composition.md) は residentmac の4 fileとresidentpipeの5 fileです。
#205 の source cleanup中のhost停止、#206 の元owner、#207 のfixed handoff、#210 のreceiver単回claimとstrict APIを保持します。
run loopはpackage内部だけで、汎用loaderと任意protected-policy flagを受けるprivate commandは移しません。通常CLI / IPC / GUIや認証・権限は未接続です。
source上の残件はsealed entrypoint、trusted native policy選定と配布・配置・verify-to-execの根拠です。
sourceのcompileやcomponent回帰を実producer / GUI / authenticated work / native全回収の成功に読み替えません。

## library build record と残る起動根拠

[build record工具](mac-resident-library-build.md) は固定Git source / lock / recipeと、実Go compilerのresidentpipe library archiveを結びます。
新producer commandやlauncherではなく、recordは常にactivation / distribution blockedです。実artifactを起動せず、任意source/path/hash/argvを信頼入力にしません。
source選択とarchiveの対応はbuilderの観測であり、publisher / compiler / SDKの独立真正性、署名済み配布 / 保護配置、verify-to-exec、trusted policy選定は別の未確立条件です。
この契約がないままsealed entrypointを有効化せず、通常未接続・#200阻害・未統合stack / CI未実行を維持します。

## Native enrollment 選択と残る verified launch

[固定 enrollment の準備](mac-resident-trusted-preparation.md) は、現在 UID の OS account home から製品専用の一つの相対名を選び、既存 strict loader に内部接続します。
env / UI / CLI の path override、profile 探索、新しい権限や通常 caller は追加しません。固定位置を選んだことと enrollment・認証の証明は別です。
署名・配置と同じ image の起動は OS launch requirement と単一 owner の具体的な bridge を後続に残し、通常起動は未接続です。

## Typed requirement と起動前 native owner

[Apple API の準備 source](mac-launch-requirement-preparation.md) は typed な publisher / exact-release 条件と、一つの未起動 Foundation Process の保持までを独立 compile / 合成 fixture で確認します。
production の publisher は未設定で拒否し、Tauri / Rust / Go へ link・起動配線しません。14.4 未満は機能を拒否し、アプリの minimum OS は変更しません。
owned I/O / exact Process Wait / supervisor / outer joins と実物の launch enforcement は後続であり、準備 object を admission や回収完了の証明にしません。
