# AI agent用MCP adapterとagent wire

> **現在地の正本（2026-11-01）:** 本文には初期source実装時点の「未実装」「未接続」「実機未実行」が残っています。API・安全契約を維持しつつ、現在のCLI/transport接続と限定実証は[文書索引](README.md)・[検証状態](tether-pilot-status.md)を優先してください。nativeの固定4case、Claude HTTP/OAuthのdiscovery、GUI合成試験は別の証拠で、実AI command・全利用者・新build全体の成功へ広げません。

[agent command契約](agent-command-contract.md) / [Mac runtime](mac-agent-runtime.md) / [安全要件](security.md) / [脅威モデル](threat-model.md) / [Go基盤のCI](go-ci.md)

goal1（agentが遠隔hostでcommandを実行する）のうち、MCP stdioからcommand coreまでをつなぐ薄いadapterです。
最優先の経路は、dotsからMacの現在利用者へのcommand実行です（認証済みの不透明relayを経由）。
2026-10-31時点のsourceは、memory上の試験だけで確認しています。
実transport、relay登録、channelの認証・handshake・heartbeat、利用者同意のproducer、鍵の生成・登録、MCP登録、配置は、実装も実行もしていません。
試験の合格は、遠隔接続、実shell起動、GUI、SSH、配布の成功を意味しません。製品gateは閉鎖したままです。

## 呼出し経路

```text
MCP client（stdio）
  → cmd/rescue-mcp（既定は未構成。stdin/stdoutに触れる前に終了値2）
  → mcpadapter.Serve（4 toolを1対1でClientへ写す。stdinを所有してclose/joinする）
  → agentwire.Client
  → agentlink.Connector → agentchannel.Open（pinしたhost鍵・登録済みsigner・渡されたnet.Connだけ）
  → 暗号化したagent専用SSH channel（[未実装] relayの登録・配送とnet.Connの供給。relayは不透明bytesだけを運ぶ）
  → agentlink.Host.Serve → agentchannel.Accept（正規Sessionからgrant・鍵集合・期限・失効を読む）
  → agentwire.Server（証明済みPeer.Key()、同じ正規の承認鍵、ch.Done()をLivenessとして）
  → agentexec.Session（同じissuerのAuthority。policy・profile・境界・issuerを束縛）
  → agentexec.GuestRuntime（Mac: macagent。凍結・独立レビュー中で実kernelは未検証。Windows guestは停止中）
```

- `backend/internal/agentlink`の試験は、この全経路をbuffer付きmemory connと合成の決定的な試験鍵で通します。runtimeはfakeです。adapter単体の試験は、channelを`net.Pipe`へ置き換えます。これらは実relay、利用者同意、実shell起動の代わりではありません。
- 既存のSSH内側作業（人向けSSH互換、二重SSH、inner shell）はこの経路に含めず、コードも複製していません。

## MCPの対象範囲

固定版は`2025-06-18`で、最新版への対応は主張しません。
根拠は、統合担当が確認した公式仕様の[transports](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports)、[lifecycle](https://modelcontextprotocol.io/specification/2025-06-18/basic/lifecycle)、[tools](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)です。この担当はページを再取得していません。

- 対象はstdioの改行区切りUTF-8 JSON-RPCだけです。HTTP listener、OAuth、resources、prompts、logging、sampling、server発のrequestはありません。stdoutにはJSON-RPC messageだけを書き、診断はCLIのstderrへ出します。
- `initialize`には常に`2025-06-18`を返します。別版を要求されても応答版は変えず、使えないclientは仕様どおり切断します。
- `initialize`の応答後、`notifications/initialized`を受けるまでは、`tools/list`と`tools/call`を`-32600`で拒否します。`ping`はいつでも応答します。2回目の`initialize`も拒否します。
- notificationと、こちらが送っていないresponseには応答しません。batch（JSON配列）は2025-06-18で廃止されているため、`-32600`で拒否します。
- `notifications/cancelled`は、そのMCP requestを取り消して応答を送らないだけです。**遠隔commandを止めるのは`rescue_cancel`だけ**で、開始済みのcommandはMCPの取消では止まりません。この区別はtool説明にも明記しています。
- request IDは文字列か整数だけで、受信したbytesのまま返します。nullと小数は拒否します。実行中のIDの重複は`-32600`で拒否し、そのmessageは実行しなかったこと、元の呼出しはまだ実行中で同じIDで応答され得ることを説明します。

### 入力の所有と終了

- `Serve(ctx, cfg, in io.ReadCloser, out)`は`in`を所有します。読取りgoroutineは1本だけで、終了時には`in`を閉じてからjoinします。そのため`in`は、Close時に保留中のReadを解除できる必要があります。`out`は呼出し側の所有で、閉じません。
- blocking modeのstdin fileは、Closeしても保留中のReadが戻らないことがあります。そのため`cmd/rescue-mcp`は、channelの取得後に1本のpump goroutineでstdinをio.Pipeへ写し、そのpipeを`Serve`へ渡します。`Serve`はpipeを閉じてjoinします。pumpはdescriptorのmodeを変えず、joinもしません（次の読取り結果、またはprocessの終了で終わる）。processが所有するstdinは閉じません。試験はio.Pipeで行い、実端末やdescriptorでは検証していません。
- stdinの終了とctxの終了では、実行中の呼出しを取り消し、それらの応答は送りません。
- agent channelが終わると、実行中の呼出しは通信errorを返し終えます。その後`Serve`は`ErrChannelClosed`で終わり、以後の呼出しに`transport_closed`を返し続けることはしません。CLIは終了値1とstderrの診断で終えます。

## 4つのtool

| tool | 引数（すべて必須で、他の要素は拒否） | core操作 |
| --- | --- | --- |
| `rescue_start` | `request_id`（1〜48の印字ASCII、空白なし）、`command`（1〜65536 byteの文字列）、`timeout_ms`（1〜4294967295。hostの上限（既定30分）を超える値は丸めずに`invalid`で拒否し、何も開始しない） | Start |
| `rescue_status` | `command_id`（32桁小文字hex） | Status |
| `rescue_output` | `command_id`、`cursor`（0〜2^53-1）、`max_bytes`（1〜49152） | Output |
| `rescue_cancel` | `command_id` | Cancel |

- Binding、epoch、host、path、token、mode、profile、issuer、期限、SYSTEM・argvの指定はありません。指定すると`-32602`です。
- 回収の再試行（`Session.Cleanup`）と`Close`は保護owner専用で、5つ目のtoolにはしていません。
- 結果は`structuredContent`と、その同じJSONを入れたtext contentで返します。
- 4本とも`outputSchema`を公開します。start/status/cancelは既存status、outputは既存chunk/cursor形状で、それぞれ既存の`error`/`message`形状との`oneOf`です。statusの`exit.known=false`にはcodeがなく、trueではint32のcodeが必須です。未知fieldは拒否する形状で、`terminal`と`cleanup`、`complete`と出力欠落を統合しません。
- schemaは既存の返却形状の説明であり、認可や回収保証を追加しません。出力counterの型は現行uint64を反映し、要求cursorの2^53−1上限を返却値へ誤って適用しません。byte上限、UTF-8/base64の実内容、状態遷移は既存codec/runtimeの責務であり、JSON Schemaの形状検査だけでは証明しません。

2026-11-07に固定版[tools仕様](https://modelcontextprotocol.io/specification/2025-06-18/server/tools#output-schema)のoutputSchemaとstructuredContentの契約を再確認しました。既存textの併記を維持し、実clientのschema解釈は別検証です。[#188](https://github.com/O2-CONNECTIVE/Tether/issues/188) / [#32](https://github.com/O2-CONNECTIVE/Tether/issues/32)の新catalog、host選択、context発行、共通application APIの設計採用を意味しません。静的discoveryにも同じ4本だけを返し、unbound sessionへ実行権限を後付けしません。

### profileと保証の表示

statusの結果には、遠隔Sessionの`profile`が読取り専用で入ります。tool説明はprofileに依存しない書き方にし、各profileの保証を明記しています。

| profile | 保証として表示する内容 |
| --- | --- |
| `windows_guest` | 一時的な非管理者guest account（native runtimeは停止中） |
| `mac_current_user` | ログイン中のmacOS利用者。昇格・sandbox・隔離はなく、その利用者の資源へ到達できる。回収の対象は、所有するshell・そのprocess group・I/Oだけで、groupを抜けた子孫は含まない |

restrictedのprofileや承認は作っていません。restricted helperは未定義のままcoreが拒否します。

### 引数の厳格さ

引数objectは次のものを拒否します。重複member（`\u`表記の同名も含む）、未知member、大文字小文字違いのmember、null、不正UTF-8、対にならないsurrogate、深さ32超。
`encoding/json`の構造体decodeは大文字小文字を同一視し、重複時は後勝ちになります。そのためmemberは完全一致のmapで取り出してから、型ごとにdecodeします。
整数は、小数・指数・範囲外を拒否します。messageは1行512KiBまでです。超えた分は改行まで読み捨て、`-32600`を返します。

### 状態と出力の表し方

- statusは`profile`、`phase`、`exit`（`known`と、判明時だけ`code`）、`stop`、`cleanup`、`terminal`を別々に返します。強制停止では`exit.known=false`で、停止理由は`stop`に入ります。`cleanup`が`confirmed`のときだけ回収確認済みです。終了コード0や`terminal`だけでは、回収完了を意味しません。
- outputの各chunkは`stream`、`offset`、`encoding`、`data`を持ちます。bytesが正しいUTF-8なら`utf-8`の文字列、そうでなければ`base64`です。
- 1つのtool結果は、JSON escapeとtext contentでの重複を含めた**実際の符号化後の長さ**で`MaxToolResultBytes`（160KiB）以下に抑えます。超える場合は、末尾のbytesをrune境界で落とします。`next_cursor`は落とした位置の手前で止まり、そのbytesは確認済みとして扱いません。次の呼出しで同じbytesを返し、`complete`は`false`にします。
- `gap_bytes`は既に解放されて取得できないbytes、`truncated`と`dropped_bytes`はhost側で破棄した出力です。黙って欠落させません。遠隔出力はdataとして扱い、指示ではありません。

### errorの区別

引数・method・lifecycleの不正は、JSON-RPCのprotocol errorです。遠隔での拒否や通信の不確定は、tool結果の`isError: true`で返します。

| tool error | 意味 |
| --- | --- |
| coreのsentinel（`denied`、`busy`、`cleanup_unknown`、`request_mismatch`、`not_found`、`policy`、`profile`、`identifier`、`session_claimed`、`cursor`、`exhausted`など） | coreのerrorをそのまま写したもの |
| `denied` | 操作ごとに文言を分けます。Startは「この呼出しでは何も開始していない」、OutputとStatusは「既存commandの停止・回収状態は`rescue_status`で確認する」とし、以前のcommandが動かなかったとは主張しません |
| `not_sent` / `transport_closed` | 送信前に取り消した / channel終了後で送信していない |
| `start_uncertain` | Startがchannelへ渡った後、応答がない・`internal`・読めない応答（未知のenumやprotocol違反）のいずれか。開始した可能性があるため拒否扱いにせず、自動再送もしない。channelが生きていれば、同じ`request_id`と引数の再呼出しで同じ実行を確認できる |
| `uncertain` | Start以外で応答を観測していない |
| `protocol_violation` / `remote_internal` | Start以外での遠隔wire違反（channelを閉じる） / 遠隔の内部error |

## agent wire

MCPから独立した、版付きの有限protocolです。

- frameは4 byte big-endianの長さ（1〜131072）と、JSON object 1つです。nullを含め、上記と同じ厳格なJSON検査を行います。
- requestは`v`（1）、`id`（相関用の非0整数。認可にもStartのrequest IDにも使わない）、`op`（`start`/`status`/`output`/`cancel`）と、opごとの固定memberだけです。`cleanup`のopはなく、mode・profile・Bindingのmemberもありません。
- Serverは、SessionのBindingとpolicy由来の`Session.Mode()`を注入し、statusへ`Session.Profile()`を読取り専用で入れます。
- 構造の不正、未知member、版違い、未知opはstreamを閉じます。

### 相手鍵と承認鍵集合

`ServerConfig{Session, Peer, ApprovedKeys, Liveness}`はlocal compositionだけが作ります。wireやMCPのmemberからは設定できません。

- `ApprovedKeys`は承認済みのraw Ed25519公開鍵です。既存の`runbackend.KeySetDigest`（domain prefix、sort・重複除去、件数と長さの区切り）で計算した値が、同じSessionのgrantの`KeySetDigest`（`Session.KeySetDigest()`）と一致しなければ`ErrPeer`です。
- `Peer`は、将来の認証済みchannelのownerが検証した相手の公開鍵です。正規化したmarshal bytesが、承認鍵のいずれかと一致しなければ`ErrPeer`です。
- 各鍵は1度だけ正規の複製へparseし、digestと所属の照合に同じ複製を使います。`agentlink.Host`は、channelの`CanonicalKeys`で得た同じ集合と、handshakeで証明した`Peer.Key()`を渡します。
- agentwire単体の照合は鍵集合への所属だけで、秘密鍵所持の証明はagentchannelのSSH handshakeが行います。
- `Serve`をnil ctxなどで早期に拒否した場合も、保持しているSessionを失効させてからconnを閉じます。2回目の`Serve`は自分のconnを閉じるだけです。`Serve`を一度も呼ばないownerは、Sessionを自分で閉じる必要があります。
- 試験の鍵は、固定bytesの公開鍵fixtureだけです。秘密鍵の生成・保持・登録はしません。

### 単回専有と所有

- `NewServer`は、すべての入力を検査してから`Session.Claim()`を取ります。拒否された構成は専有を消費しません。2つ目のServerは`ErrSessionClaimed`で、global mapやresetはありません。
- Serverは、48 bitの乱数接頭辞をcoreのrequest IDへ付けます。ownerが直接開始したcommandを、同じrequest IDのreplayで取り込むことはできません。Serverは自分のStart応答で得たcommand IDだけを所有し、それ以外は`not_found`です。

### 生存と終了

- `Liveness`は、channel ownerのheartbeat/leaseが相手の喪失（half-open）を判定したときに閉じるchannelです。必須の入力で、nilは`ErrUnconfigured`です。
- Serveは、streamの終了・ownerのctx取消・`Liveness`の喪失・違反のいずれでも、先に`Session.TransportLost`で承認を失効させ、その後でconnを閉じます。この順で、停止したReadとWriteも解除します。`Liveness`の喪失では`ErrPeerLost`を返します。
- agentwireは独自のidle timerを持たず、有効な長時間commandを停止しません。`agentlink.Host`は、agentchannelの心拍喪失・停滞・期限で閉じる`ch.Done()`を`Liveness`へ渡します（source/memory試験のみ。実relay上の挙動は未検証）。
- guestの回収確認は、保護ownerの`Session.Close`で行います。Serveは回収完了を主張しません。

### Clientの不確定と応答の検査

- 送信前の取消は`ErrNotSent`、送信後に応答がない場合は`ErrUncertain`です。Clientは再送しません。Startが`internal`や読めない応答を受けた場合も、`ErrUncertain`で包みます。
- 応答は、未応答のrequestか、放棄したrequestに1回だけ対応する必要があります。要求していないID、重複、応答済みIDはprotocol違反としてstreamを閉じます。
- 放棄したrequestの記録は最大`MaxAbandoned`（32）件です。超える放棄はstreamを閉じ、記録を増やしません。遅れて届いた放棄済みrequestへの応答は、消費して捨てます。
- Outputの応答は、要求したcursorから連続し、要求した`maxBytes`以下である必要があります。

## 上限

| 項目 | 値 |
| --- | --- |
| MCP 1行 / tool結果 | 512KiB / 符号化後160KiB |
| MCP同時tool実行 | 4（超過は`-32000`で即時拒否。readerは止めない） |
| wire frame | 128KiB |
| wire Serverの同時処理 / Clientの未応答request / 放棄記録 | 4 / 8 / 32 |
| command | 64KiB |
| 1回のwire output | 48KiB（小さいchunkが多い場合は、frameへ収まるよう末尾を次回へ回し、確認済みにしない） |
| 1 tool呼出しのwire往復 | 既定30秒（超過はuncertainとして返す） |

goroutineは、MCPのstdin reader 1本、メインloop、tool実行最大4本、Clientのreader/writer各1本、Serverのreader・監視各1本と処理最大4本です。

## CLI

`rescue-mcp`は、引数、環境変数の資格情報、endpoint設定を読みません。
信頼済みのchannel producerがまだないため、既定では診断をstderrへ出し、stdinを読まず閉じず、stdoutへ書かずに終了値2で終わります。
試験はpackage内の`execute`へmemoryのconnectorを渡し、stdin終了時にchannelを閉じること、channel終了時に終了値1で止まりstdinを閉じることを確認します。

## 未接続のproducerと次の作業

1. **利用者同意のproducer（Mac）**: ログイン中の利用者本人が明示的に承認したときだけ、`ephemeral.NewCurrentUser`の`Prepare`へMacのpolicyと承認鍵集合のdigestを渡します。自己承認やfakeで代用しません。
2. **channelと合成（source済み）**: `agentchannel`と`agentlink`で、handshake・心拍・合成のsourceとmemory試験があります。未実装なのは、relayの登録・配送、受理済み`net.Conn`の供給、端末鍵の実登録、host鍵とBindingをdispatchで渡す経路です。
3. **開発PC側の配線**: `cmd/rescue-mcp`の既定connectorはnilのままです。`agentlink.Connector`へ渡す実conn・登録済みsigner・pin値の供給元は未実装です。dotsのMCP登録もしていません。
4. **package境界（goal2）**: 受け手が明示的にpackageを起動する経路と、その固定manifestです。既存の管理接続がない場合の遠隔install経路は作りません。
5. **Windows guestのnative runtime**: 停止中です。既定のWindows securityは変更していません。

## 検証の範囲

- 試験は`backend/internal/agentwire`、`backend/internal/mcpadapter`、`backend/cmd/rescue-mcp`の`_test.go`だけです。`net.Pipe`、`io.Pipe`、memory fakeのruntime、手書きの不正wire peer、手動clockを使います。network bind、process、shell、OS APIは使いません。
- 対象は次のとおりです。
  - MCPのlifecycle、4 toolの厳密なschema、不正入力（重複、未知member、null、大文字小文字違い、範囲外、超過行）
  - MCP→wire→実agentexecのstart/output/status/cancel、profileの表示、回収未確認の表示とowner専用の再試行、同じrequest IDの再送で副作用がないこと
  - 他streamやownerのcommandが`not_found`になること、単回専有、相手鍵と鍵集合の照合
  - Startの不確定（応答の喪失、`internal`、未知enum）、応答の検査（要求外・重複・遅延・要求量超過・放棄の上限）
  - escape後の結果長の上限、生存喪失時に失効が先でcloseが後になること、channel終了での停止、stdinのclose/join、CLIの未構成拒否
- これらの合格は、認証済みtransport、relay、利用者同意、Macでの実command実行、実Windows保護、遠隔実行の成功を示しません。

## Output lifetime update (local source)

A stdout write is bounded by five seconds. On channel loss or cancellation the
same in-flight write has at most 100 ms to report completion, so a successfully
delivered uncertain/failed response is not discarded by a scheduling race.
If still blocked, ErrOutputUnconfirmed is returned; no further writes start.
One caller-owned Write may remain blocked, and its output must not be reused.
The CLI closes the agent channel and exits; it does not claim that this write
or its process-owned stdin pump joined. Generic embedded callers must honor
that stream/process lifetime contract. No descriptor mode or stdout ownership
is silently changed. This is still stdio only, not a remote HTTP/OAuth endpoint.

Writer failure also wakes the Serve loop when it occurred on a tool-reply
goroutine and stdin is quiet. ErrOutputUnconfirmed is joined with any existing
channel/cancellation/input error, so embedders never lose the pending Write
ownership warning behind another stop reason.
