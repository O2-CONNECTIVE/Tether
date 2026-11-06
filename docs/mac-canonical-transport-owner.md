# 正規transport ownerのsource閉包

未統合#200〜#205に依存するsource準備です。
#200のWindows session-create阻害、stack CI未実行、通常resident未接続を解除しません。

## 実際に閉じる依存

residentのprotected factoryはagenttransportを使う必要がありますが、従来のagenttransportはmaclaunch.Dispatchに依存していました。
既存Dispatchの同じ3fieldをagentchannelへ集約し、maclaunchとagentgatewayはaliasで互換性を保ちます。
agenttransport.Ownerはこの共通型を受け取り、production sourceのmaclaunch依存をなくします。
新しいfactoryやgeneric port、認証経路を追加せず、既存の具体的Approved・Host・Clientをそのまま使う依存整理です。

Server.DispatchとHost.ChannelDispatchは同じ正規Session/channelのbinding・元deadline・固定host公開鍵から読み取ります。
返す鍵bytesはcopyで、取消後も履歴metadataとして残ります。
これをlive grant、認証、OS owner proof、接続成功、cleanup成功として扱いません。
Hostはissuer/native準備の所有者ではなく、platform全体のtransport.Ownerを満たす代用品にはしません。

## 部分取得とcleanup観測の違い

- ApprovedはSession取得後のHost構成失敗・取消でも、同じissuer/Session/Host参照を持つ非nil ownerとerrorを返します。失敗ownerはcleanup専用で、Dispatch/Serve/LocalSummaryを公開しません。完了した部分取得cleanupは再試行せず同じ結果を返します。成功ownerの既存の明示的な同一owner Close動作は維持します。
- Approved.CleanupResultは最後に完了した明示cleanupの結果だけを読み、Close・Revoke・待機を行いません。未観測は成功ではありません。
- Host.CleanupResultは元のServeに伴うcleanup結果を保持し、同じServeのfinished通知前は返しません。単独Closeや後の明示Closeはこの元履歴を上書きしません。
- Client.CleanupResultは最後に完了したowner/transport cleanup結果だけを読みます。Runを始めた場合はfinished通知を必要とし、cleanup処理中も未観測とします。別途明示したCloseで最後の結果は更新され得ますが、getter自体は再試行しません。

これらは各componentのsource上の完了観測です。
実EOF、exact Child Wait、native IO/supervisor/outer monitorの全joinやauthoritative closed、process退出許可へ拡張しません。
unknownは同じ元ownerと参照を保持し、別ownerへの置換や正常成功で解消しません。

## 通常経路と検証

通常CLI・引数・TTY承認・鍵loader・通信protocol・権限は有効化／追加しません。
通常CLIには既に非nil部分ownerを処理する停止／terminal-unknown分岐があり、今回の実戻り値もその分岐で扱います。
protected policy、resident transport/opaque receiver、source/worker、Standby、trusted launcherは未接続です。
privateのWindows resident profile・別admissionやresident protocol分岐を無関係に移植しません。

元のfake-terminal／memory runtimeとHTTP/SSH memory streamを使い、部分取得、metadata不変、観測時の非取消・非再試行、unknown履歴と元Serve/Run終了通知を検査します。
固定seedの鍵はmemory test vectorだけで、実鍵／policyを読み書きせず、CAや運用trustを作りません。
CLI回帰の既存status-pipe子fixtureは同じtest実行体だけを起動し、execute/main/nativeDepsを呼ばず、pipeを閉じてexact Child Waitとcleanup markerを検査します。
この自己所有試験の終了は製品producerの回収証拠ではありません。

後続は[resident source段階計画](mac-resident-source-plan.md)に記載します。
