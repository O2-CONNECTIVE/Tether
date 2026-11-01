# agent channel

既存のraw Ed25519/SSH暗号を、渡し済みの`net.Conn`1本の上でagent専用の1 channelとして使うsourceです。
設計、所有、上限、未接続の統合は[agent channel](../../../docs/agent-channel.md)を正本とします。

- dial・listen・endpoint登録・再接続・再試行・再開はありません。人向けSSH互換、内側sshd、既存sshdやloopbackへのfallbackもありません。
- serverはgrant・鍵集合digest・期限・失効signal・profileを、正規の`agentexec.Session`（そのAuthority）だけから読み、相手の値から導きません。別のAuthority入力はなく、Sessionの単回transport専有を取ります。
- 失効は理由の記録の後、connのcloseの前に行います。`Done`とRead/Writeのerrorは失効の後に返ります。compositionは`backend/internal/agentlink`です。
- 1接続につき固定名`rescue-agent-command-v1@rescue-host`の1 channelだけを受け、それ以外のchannel・global request・channel requestは拒否して接続を閉じます。
- 終了は不可逆で、serverでは同じSessionの承認を失効させてからconnを閉じます。

試験は`_test.go`内のbuffer付きmemory connと、合成の決定的Ed25519 seed fixtureだけを使います。socket、process、OS signal、実鍵、relayは使わず、試験の合格は実通信・実relay・登録・配置の成功を示しません。
