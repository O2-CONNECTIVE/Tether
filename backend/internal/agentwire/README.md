# agent wire

1つの`agentexec.Session`へのstart/status/output/cancelを、認証済みのbyte streamで運ぶ版付きの有限protocolです。
設計、上限、未接続のproducerは[MCP adapterとagent wire](../../../docs/agent-mcp-adapter.md)を正本とします。

- wireにBinding・mode・profile・host・token・issuerはありません。ServerがSessionのBindingとpolicy由来のmodeを注入し、profileを読取り専用で返します。
- `NewServer`は、local compositionが渡す相手公開鍵が、`runbackend.KeySetDigest`でgrantと一致する承認鍵集合の要素であることを要求し、その後で`Session.Claim()`を単回取得します。鍵集合の照合は認証ではありません。
- streamの終了・違反・ctx取消・owner提供の`Liveness`喪失では、必ず`Session.TransportLost`で承認を失効させてからstreamを閉じます。再接続・再開・自動再送・独自のidle timerはありません。

socket、SSH channel、handshake、heartbeat、relay登録・認証、鍵の生成は含みません。試験は`net.Pipe`とmemory fake、公開鍵の固定fixtureだけで、認証済みtransportの成功を示しません。
