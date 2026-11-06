# fixed intentのwire／runtime引継ぎ

#206以前の未統合stackに依存するsource準備です。
#200のWindows session-create阻害、stack CI未実行、通常resident起動・IPCの未接続を解除しません。

## 同じSessionに渡す要求

既存wire Serverはclient IDに自分で生成したprefixを加えて、同じSessionのidempotencyを分離します。
従来のFixedStartConstraintはraw IDだけを照合するため、正しい固定要求もwire経由では拒否されました。
Sessionの単回adapter claimに、その正規Serverがローカル生成したprefixを固定します。
client／MCP／WebView／remote fieldがprefixを選ぶ入口は追加しません。

固定要求はraw ID、または当該adapterのexact prefix＋固定IDに限り、command bytesとtimeoutは元の値と完全一致が必要です。
full prefixed IDをreplay keyとして保持し、direct ownerのraw IDと混同しません。
どちらかが一度実行を取得した後、別namespaceは実行中ならbusy、終了後ならexhaustedとなり、元の要求だけが同じ実行をreplayできます。
新しいowner／grant／commandや再接続・権限を作りません。
既存binding、mode/profile、署名peer、失効、単回claim、固定内容の制限は維持します。

## 有限runtime deadlineと取消の所有者

StartShellへ渡すcontextに、要求timeout・正規grant hard deadline・New時点の不変elapsed capのうち最短のdeadline情報を示します。
期限情報を伝えるwrapperであり、新しいtimerや独立cancel ownerを追加しません。
既存Session timerがgrant失効を反映してからcancelする順序を維持します。
metadataだけで現在の認証／grantを作ったり、現在の失効判定やOS停止中の期限執行を証明したりしません。
元source寿命とnative実行直前の再検査は、後続の具体的protected policy／Session runtimeが引き続き必要です。

## 検証境界

fake clock/timer／memory runtimeで、期限情報、元timer数、grant revokeとcancelの順序、elapsed cap、single claim、direct／wire競合とreplayを検査します。
Mac current-userの正規Session→Host→memory SSH→wire Client→fake runtimeでも固定要求と拒否・回収を確認します。
実shell／producer／鍵／policy／hostや外部通信、GUI、OS trustは使いません。
memoryキーは固定seedの既存test vectorだけで、認証済みnative起動や運用trustの証拠ではありません。

同じHost/Session、stream、Runと元cleanupを観測して終了させます。
このsource試験の完了を実EOF、exact Child Wait、native IO/supervisor/outer monitorの全joinやauthoritative closed、退出許可へ広げません。
unknownの元owner保持は既存契約のままです。
通常CLI／認証／権限を有効化せず、privateの無関係なWindows resident profileや別admissionも含めません。

後続は[resident source段階計画](mac-resident-source-plan.md)の具体的transport／policy／Standby・配置信頼です。
