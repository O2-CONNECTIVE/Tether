# MCP adapter

MCP `2025-06-18`のstdio subsetで、`rescue_start`/`rescue_status`/`rescue_output`/`rescue_cancel`の4 toolだけを`agentwire.Client`へ1対1で写します。
仕様の範囲、error区別、上限は[MCP adapterとagent wire](../../../docs/agent-mcp-adapter.md)を正本とします。

- grantの発行・延長、Binding・mode・profile・hostの選択、結果不明のStartの再送、再接続は行いません。回収の再試行はowner専用で、toolにはしません。
- MCPの取消はそのrequestを取り消すだけです。commandを止めるのは`rescue_cancel`だけです。
- `Serve`はstdinを所有してclose/joinし、channelが終わると`ErrChannelClosed`で止まります。stdoutにはJSON-RPC messageだけを書き、tool結果は符号化後の長さで制限します。

試験はmemory上のchainだけで、遠隔実行・GUI・SSH・配布の成功を示しません。
