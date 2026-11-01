# Tether deployment components

このディレクトリは実装済みの配置部品・合成設定・利用上の制約を説明します。常時接続サービスや配置済み環境の提供ではありません。現在の実証範囲と残件は[検証状態](../../docs/tether-pilot-status.md)を参照してください。

- [gateway CLI](../../docs/agent-cli.md): 保護設定・前景起動・終了の契約
- [HTTP認証](../../docs/agent-gateway.md) / [主体照合](../../docs/tether-access-identity.md): 検証済み主体と登録の対応
- [Linux container](linux-container.md): scratch image、専用network、保護mountの条件
- [commandless probe](probe/README.md): HTTP/WSS到達を確認する限定echo。command実行とは別
- [native Worker](native/README.md): 認証済みMCP/登録経路の薄い配送
- [Windows directory準備tool](prepare-directory.md): Windows fallback専用。Mac targetの必須依存ではない

samplesは合成・未設定で、そのまま実接続できません。秘密・実identity・鍵をimageや公開repositoryへ含めません。
gatewayは本人同意を代行せず、grantを発行・延長しません。公開route、enrollment、鍵配置、OS権限変更はこのsourceを取得・buildするだけでは行われません。
新しい実操作には対象・影響・停止と回収の方法を別に確認してください。失敗を広い権限や別経路へのfallbackで回避しません。
