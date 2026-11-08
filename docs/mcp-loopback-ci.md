# MCP の実 loopback 接続試験

`.github/workflows/mcp-loopback.yml` は標準 `macos-26` arm64 runner で
`ci_mcp_loopback` build tag を明示し、`TestLoopbackTLSMCPConnection` を
通常と race の各1回実行します。通常の Go offline lane には追加しません。
0件・skip・失敗・package未完了を成功に読み替えません。

## 確認する範囲

テスト用 HTTP client → `127.0.0.1` の実 TCP → 検証あり TLS →
製品 gateway の JWT 検証 → HTTP MCP → 既存の memory SSH/session → fake runtime。

- initialize、initialized、4 tools の一覧、start、status、output、cancel、session削除。
- 未信頼証明書、bearerなし／不正署名、別principalによるsession利用の拒否。
- 稼働中の合成作業に対する期限切れbearerでのinitialize、一覧、start、status、cancel、DELETEをHTTP 401で拒否。正しいbearerのsessionと元作業は維持する。
- trusted ownerの`Session.Revoke`によるhost OFF後、新規startと旧sessionのHTTP 404拒否、fresh discoveryからの実行拒否、元作業の停止を分けて確認する。
- fake runtimeのRelease失敗を注入し、Host終了を回収成功と扱わず`cleanup=unknown`を維持する。owner限定の再試行後だけ`confirmed`となり、権限や作業を復活させない。

OFF・期限切れbearerの共通シナリオは、通常Go laneの
`TestHTTPHostOffAndExpiredCredential`でも直接HTTP handler経由で実行します。
既存実装への後付け回帰であり、新仕様のTDD red/greenではありません。
transport認証期限とgrant期限は別です。ここで期限切れにするのは合成bearerで、
既存のgrant hard deadline回帰を置換しません。
- 自己所有 listener／HTTP server の終了と実行結果。timeout時は失敗。

TLS証明書・秘密鍵は試験内だけで生成します。信頼点はそのclientのcert poolだけで、
証明書検証を無効化せず、OSのtrustや実credentialを変更しません。
宛先を自己所有のloopback高位portに限定し、proxy・redirectを使いません。
公開endpoint、tunnel、外部サービス、恒久アクセスは作りません。

## 実証しない範囲

JWTとgrantは合成fixtureです。SSH側は既存memory接続、作業はfake runtimeのため、
実Go producer、native command、Tauri GUIからのON/OFF、通常Quit、全native資源の回収、
実dot／OAuth provider／tunnelとの互換性は別の受入です。
Swiftの読取専用GUI試験やa5のbrowser fixtureとも別の証拠として記録します。
CI成功は製品ゲート解除や実機接続の完了を意味しません。

## ローカル実行

既存のGoとmodule cacheがある環境で、`backend`から明示実行します。
既存の認証情報やユーザーのブラウザは使いません。

```sh
go test -mod=readonly -tags=ci_mcp_loopback -run '^TestLoopbackTLSMCPConnection$' -count=1 -timeout=60s ./internal/agentgateway
go test -mod=readonly -race -tags=ci_mcp_loopback -run '^TestLoopbackTLSMCPConnection$' -count=1 -timeout=60s ./internal/agentgateway
```

hosted CIの実行結果と固定headはPR／#189の進捗記録で確認してください。
