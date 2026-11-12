# 通常gatewayの固定常駐MCP組成

[CLI構成](agent-cli.md) / [常駐GUI](resident-gui-wiring.md) / [移行境界](legacy-permission-migration.md)

通常の`cmd/rescue-gateway`で、保護文書から既存の固定常駐controlとMCP sessionを組み合わせる入口です。
`ResidentPrincipals`と`ConfigureResident`を待受前に接続します。
これまではproducer CIの明示組成だけにあり、通常entryでは未構成だった部分を対象にします。

## 明示する設定

既存4 memberに、任意の`resident`配列を追加できます。以下は文書全体ではなく追加memberのplaceholderです。
実設定の作成・鍵の配置・起動を自動実施する手順ではありません。

```json
{
  "resident": [
    {
      "host_label": "<enrolled-host-label>",
      "principal_label": "<configured-principal-label>",
      "operation": "tether-fixed-command-v1"
    }
  ]
}
```

- 省略時は以前の構成と同じで、常駐subprotocolを受理しません。既存のhost登録・principal鍵だけから常駐利用を推定しません。
- 明示時は1〜4件。null、空配列、余剰member、重複member、同じprincipalの重複、未知host/principalを拒否します。
- principalは既存`hosts`全体でちょうど1つのhostに対応し、そのlabelが指定hostと一致する必要があります。複数hostのうち1つを暗黙選択しません。
- 公開鍵はそのhost登録から取得し、signerは既存の保護loaderで読んだ同じprincipal鍵だけです。ここへ別の鍵・signer・tokenは指定できません。
- operationは`tether-fixed-command-v1`だけです。任意command・timeout・grant・承認flag・共有policyは追加できません。固定commandと15秒の上限は既存実装が所有します。

全検査を鍵の読取り前に完了します。部分的に正常な配列でも一部だけ起動せず、設定不正は終了値2です。
文書・鍵の保護検査、fingerprint、HTTP認証、登録時の相互署名、exact path、既定loopback/private profileは変更しません。

## 実行時の境界

1. 文書の検証、既存全principal鍵の読取り、gateway生成の順序を維持します。
2. 選択したprincipalだけをregistration handlerの常駐署名者に指定します。
3. 同じhandlerをcontrolとして、固定host公開鍵とprincipalを`ConfigureResident`へ一回設定します。失敗はhandlerとgatewayの回収へ進みます。
4. その後で初めて待受します。MCP session生成後の設定追加やhot reloadの入口はありません。
5. 接続先hostが相互認証済みのcontrolを持つ場合だけ、新規initializeがそのprincipal/host/generationへ固定されます。接続前のdiscovery sessionや別principalは後から昇格しません。

control認証はnative workのgrantではありません。実Macの保護policy・固定enrollment・各workの承認経路と同一owner回収が別途必要です。
既存の一時grant登録は従来の別経路として残ります。常駐設定がそのgrantを更新・延長・移管することはありません。
新しいpermissionmodelのUserID/ClientID/HostID・共有revisionへ旧labelを変換しません。

取消要求、command終端、command cleanup、host-owner cleanup、gateway cleanupは従来どおり別々です。
署名済みの取得前拒否以外を「仕事なし」と推定せず、unknownをconfirmedへ変換しません。
結果保持・再送抑止・generation rotation・既存の有限shutdown順序も変更しません。

## 検証と未達

- `resident_config_test.go`は同じ通常`parseLocalConfig` / `execute`境界の先行red試験と実装後greenを対応させます。
- `resident_entry_test.go`は通常`execute`を通し、実gateway/registration handlerを`net.Pipe`で接続します。合成bearer/Access認証、相互署名、固定MCP辞書、固定intent送信、署名済み取得前拒否のMCP結果を確認します。
- 既存文書での常駐拒否、別principal/session、未認証、接続前sessionの非昇格、commandの上書き拒否も検査します。登録済み実鍵やnative processは使いません。
- production fileは既存`main.go` / `config.go`内だけで、GoCIの許可inventoryは増やしません。

これらはsourceと隔離CIの証拠です。通常配布物から実Macへ接続した証明ではありません。
実credential・鍵・運用保護文書の作成、gatewayの配置と起動、公開TLS/tunnel/Access route、dotsのMCP接続、実Macの許可・取消・回収は別途未検証です。
製品gateや既存44条件の未達を解除せず、運用上の永続アクセスをこの変更で作成しません。

## 常駐専用構成のoffline catalog

`resident_only: true` の明示構成で、認証済みprincipalに固定resident enrollmentがある場合だけ、Mac controlが未接続でもinitializeと固定7ツールの一覧を提供します。通常の4ツールdiscoveryとは別です。登録のないprincipalは従来どおり拒否します。各要求の認証、subject対応、SID所有、期限・上限は維持します。

このsessionにはbackend、control、signer、host ownerを渡しません。全tools/callをJSON-RPC errorで拒否し、status、cancel、list、read、releaseも実行しません。空の実行一覧、停止済み、回収済みや実行権限があるという結果は作りません。initializeのinstructionsにcatalog-onlyであることと、新しいinitializeが必要であることを明示します。

後からMacがONになっても既存SIDは昇格しません。認証済みstandbyの後に新しいMCP sessionを作る必要があります。初期化中にcontrolが接続した場合もoffline sessionはofflineのままです。公開URLを維持すること、MacのON/OFF、MCP session、作業grantは別です。実ChatGPTの再初期化・更新や5分のcontrol世代変更への追随は別途受入が必要です。

共有4枠、30分のidle期限、DELETEと結果保持、公開edgeの5分とGo側再接続の違いは[常駐MCP sessionの上限と世代変更](mcp-session-lifecycle.md)を参照してください。

試験は合成認証・net.Pipeの通常CLI入口とadapterの回帰です。実Cloudflare配置、鍵、enrollment、権限設定は変更しません。
