# agent専用の認証済みchannel

> **現在地の正本（2026-11-01）:** 本文には初期source実装時点の「未実装」「未接続」「実機未実行」が残っています。API・安全契約を維持しつつ、現在のCLI/transport接続と限定実証は[文書索引](README.md)・[検証状態](tether-pilot-status.md)を優先してください。nativeの固定4case、Claude HTTP/OAuthのdiscovery、GUI合成試験は別の証拠で、実AI command・全利用者・新build全体の成功へ広げません。

[MCP adapterとagent wire](agent-mcp-adapter.md) / [安全要件](security.md) / [脅威モデル](threat-model.md) / [Go基盤のCI](go-ci.md)

[MCP adapterの呼出し経路](agent-mcp-adapter.md#呼出し経路)で`[未実装]`だった、agentwireの下の認証済みchannelのsourceです（`backend/internal/agentchannel`）。
2026-10-31に統合担当へ引き継ぎました。channelの所有者が書いたsourceを、統合担当が今回の修正（失効の順序、正規Sessionからの権限導出、合成package）とあわせて更新しました。今回の版は、この担当では試験もcompileも実行していません。統合担当が実行し、結果を別に記録します。
実relay、endpoint登録、実鍵の登録、利用者同意の発行元、配置は実装も実行もしていません。製品gateは閉鎖したままです。

## 位置づけ

- AI agent（端末側）からMac hostへ、既存のraw Ed25519/SSH暗号をagent専用の転送として再利用します。relayは暗号化済みの不透明bytesを運ぶだけで、commandを認可しません。
- 人向けSSH互換ではありません。内側SSH server、既存sshd、loopbackへのfallbackはありません。
- 既存のgrant、Identity/epoch、固定hard deadline、失効、鍵集合digestの規則が正本です。このpackageはそれらを変更・延長・発行しません。
- Windows向けの追加は保留です。channelはprofileを区別せず、Macの経路にWindows固有の処理は不要です。

## API（呼出し元と所有）

| API | 呼出し元 | 所有 |
| --- | --- | --- |
| `NewServer(ServerConfig)` | host側の信頼済みlocal owner（通常は`agentlink.NewHost`） | 全入力を検査した後で、Sessionの単回transport専有（`Session.ClaimTransport`）を取ります。2つ目のServerは拒否されます |
| `CanonicalKeys([]ssh.PublicKey)` / `(*Server).ApprovedKeys()` | 同上 | 承認鍵を1度だけ正規のraw Ed25519複製へparseします。channelとagentwireの両方が、同じ複製をdigestと所属の照合に使います |
| `(*Server).Accept(ctx, net.Conn)` | 同じowner。connは既に受理済みのもの | non-nilのconnは呼出し時に1度だけ移ります。1 Serverにつき1回で、2回目は`ErrUsed`で渡されたconnだけを閉じます |
| `Open(ctx, ClientConfig, net.Conn)` | 端末側の信頼済みcomposition | 同上（Openごとにconn 1本） |
| `*Channel` | 上記の戻り値 | 渡されたconn、SSH接続、agent channel 1本とworkerを所有します。`Read`/`Write`/`Close`（`io.ReadWriteCloser`）、`Done`、`Reason`、`Peer`、`Binding` |

- `ServerConfig`：正規の`Session`、承認済みのraw Ed25519鍵集合`AllowedKeys`、既存のhost signer `HostKey`（生成しません）、`Budgets`。Authorityの入力はありません。Binding、鍵集合digest、境界、hard deadline、失効signal、profileは、Sessionがclaimした正規Authorityから`Session.Grant()`・`Session.Done()`・`Session.Profile()`で読みます。そのため、同じ値に見える別Authorityを渡す余地はありません。
- `ClientConfig`：明示のdispatchから来る`Binding`、pinしたraw Ed25519 host公開鍵`HostKey`、登録済みの`Signer`、端末側の`Deadline`（最大24時間、grantの期限以下にすること）、`Budgets`。
- 失敗時の戻り値が`(nil, err)`なら、承認の失効・connのclose・workerの合流を確認済みです。`(*Channel, err)`で`ErrCleanupUnknown`を含むときは、同じownerが未確認のまま返っています。その`Close`だけが同じconnの再closeと合流を再試行し、別の接続を閉じることはありません。
- `ctx`はchannelの寿命全体を区切ります。取消でteardownします。

### 認証と照合

- serverは既存の`internal/auth`（LaunchIDのuser、固定鍵集合、照会時と署名検証後の両方での有効性確認）を再利用し、署名検証後のcallbackで検証済み鍵を記録します。handshake後に、LaunchID、raw Ed25519、承認集合への完全一致、`runbackend.KeySetDigest`（変更なし）がgrantの`KeySetDigest`と同じことを独立に再検査します。
- SSHのuser名はLaunchIDの選択子で、認可の根拠ではありません。
- 鍵種別はraw `ssh-ed25519`だけです。証明書、password、keyboard-interactive、auth-none、agent forwarding、TOFU、known_hosts、insecureなhost key callbackはありません。clientのSSHライブラリは最初に`none`で方式一覧を照会しますが、serverは受理しません。
- 鍵交換・暗号・MACは固定一覧です（`curve25519-sha256`、`chacha20-poly1305@openssh.com`/`aes256-gcm@openssh.com`、`hmac-sha2-256-etm@openssh.com`）。
- host鍵とsignerの公開鍵は一度parseした複製を固定し、各署名をその複製で検証します。返す`Peer.PublicKey()`も複製です。
- channel openの付加dataは32 byteのcommitmentです。channel名、Generation、LaunchID、Epoch、PolicyDigest、KeySetDigest、profile、host公開鍵を長さ付きでSHA-256にかけた値で、両端がそれぞれ自分の信頼済み入力から計算します。serverは比較するだけで、受け取ったbytesからgrantを導きません。launch・epoch・鍵・grant・profileのどれが違っても拒否します。

### 相手の識別（provenance）

`Peer`のfieldは非公開で、handshakeの検証を終えたchannelだけが0以外の値を持ちます。`Peer.Key()`は、検証済み鍵の正規複製を新たにparseして返します。
旧`Caller`（digestと、任意の0以外の値）は削除しました。`agentlink.Host`は`Peer.Key()`を`agentwire.ServerConfig.Peer`へそのまま渡します。agentwireは、それが同じ承認集合の要素であり、その集合のdigestがgrantと一致することを再検査します。

## channelの許可範囲

- 1接続につき、固定名`rescue-agent-command-v1@rescue-host`のchannelを1本だけ受けます。版は名前に含み、交渉はありません。
- 拒否して接続を閉じるもの：`session`、`direct-tcpip`、`forwarded-tcpip`、`x11`、その他すべての別名channel、2本目以降のchannel、そのchannel上のすべてのchannel request（`exec`、`shell`、`pty-req`、`env`、`subsystem`など）、拡張data（stderr）、心拍以外のglobal request（`tcpip-forward`、`keepalive@openssh.com`など）、形式違いの心拍、上限超過の心拍。
- 宛先host/portを運ぶchannelはなく、routingもfallbackもありません。

## 終了と失効

- teardownは不可逆で、最初の理由を`Reason`として保持します（`closed`、`canceled`、`hard-deadline`、`revoked`、`handshake`、`rejected`、`violation`、`heartbeat`、`read-stall`、`write-stall`、`transport`）。
- 契機：ownerの`Close`、ctxの取消、hard deadline（serverはgrantの値、clientは`Deadline`）、`Authority`の失効（外部Revoke・Session側の期限）、心拍の失敗、読み書きの停滞、transportやchannelの終了、相手の違反。
- serverでは常にSessionを失効させてから、所有connを閉じます。失効では、まず`Session.ObserveDeadline`で期限を再観測し（期限後なら停止理由は`StopHardDeadline`）、その後`Session.TransportLost`を呼びます。SSHライブラリがhandshake失敗や読取り終了で自らconnを閉じる場合も、connのwrapperで同じ順序を守ります。
- 外から観測できる順序は次のとおりです。最初の`Reason`の記録 → （server）Sessionの失効 → `Done`のclose、`Read`/`Write`のerror → 所有connのclose。`Close`は、さらにconnのcloseと全workerの合流を確認します。相手はconnのcloseか、自分の心拍・停滞の上限で後から知るだけで、同期した通知はありません。
- admission後にSSHライブラリが自らconnを閉じた場合は、失効の前に`transport`を理由として記録します。そのため、自分の失効を外部の`revoked`と取り違えません。handshake中は、AcceptとOpenが分類した理由（`handshake`、`rejected`、`violation`など）を記録します。
- hard deadlineは単調時計のtimerに加え、`WallRecheck`（1秒）ごとに壁時計を読み直して判定します。sleepや壁時計の前進の後でも、要求なしに1秒以内に終わります。
- 再接続・再試行・再開はありません。次のcommandには新しい承認・Authority・Session・channelが必要です。
- Accept前の認証失敗でもgrantを失効させます（fail closed）。relayへ届いた未認証の接続1本で、そのgrantが使えなくなることは既知の制約です。

## 生存確認と停滞

- 製品固有の心拍`rescue-agent-heartbeat-v1@rescue-host`（応答必須、payload空）を両端が送り、正確に`true`と空payloadの応答だけを成功とします。拒否（既存sshdなど）は失敗で、fallbackはありません。
- 同時に送る心拍は各端1件だけで、応答を受けてから次を予約します。`SendRequest`が戻らないときは期限切れで同じ所有connを閉じて解除し、その送信workerも合流の対象です。
- 心拍は認可のleaseではありません。相手の心拍に応答してもhard deadlineは動かず、自分の生存確認にも数えません。終了中の相手の心拍には応答せず、closeで知らせます。
- agentのdataは見ません。出力のない長いcommandでも、心拍が通る限りchannelは続きます。
- 停滞：admission前は全読み書きをhandshake期限で、admission後は1回の読取りを`ReadStall`、1回の書込み（transportとchannelの両方）を`WriteStall`で区切ります。どれもhard deadlineを超えません。渡すconnは期限設定に対応している必要があり、設定できなければその呼出しを失敗にします。

### 上限（`Budgets`はすべて必須。0は既定値にせず拒否）

| 項目 | 値 |
| --- | --- |
| `Handshake`（handshake・認証・channel admission） | 0超〜30秒 |
| `HeartbeatInterval` | 10ミリ秒〜30秒 |
| `HeartbeatTimeout` | 0超〜30秒 |
| `ReadStall` | `HeartbeatInterval + HeartbeatTimeout`超〜120秒 |
| `WriteStall` | 0超〜60秒 |
| `Cleanup`（Close 1回あたりの失効・close・合流の待ち） | 0超〜10秒 |
| client `Deadline` | 現在より後、24時間以内 |
| 相手の心拍 | 1秒の窓で100件まで。超過は違反 |
| channel open付加data | 32 byte固定 |
| 同時の自分の心拍 | 1件 |

`Cleanup`はteardown後に走るため、hard deadlineの後に最大`Cleanup`だけ合流待ちが続きます。承認はその前に失効済みです。

### goroutineと合流

workerは、admission監視1、transport待ち1、global request 1、channel open 1、channel request 1、拡張data監視1、生存監視1、心拍送信最大1、shutdown 1です。すべて`Close`の合流で数えます。
数えないものは、`Write`の停滞timer（teardownを起動するだけで待たない）と、所有connの`Close`呼出し1件（同時に1件まで。結果は`Close`が確認する）です。SSHライブラリ内部のgoroutineはconnのcloseで終わることを前提にし、個別には数えません。

## 合成（`backend/internal/agentlink`）と残る境界

- **host側**：`agentlink.NewHost(HostConfig{Session, AllowedKeys, HostKey, Budgets})`は`agentchannel.NewServer`でtransport専有を取ります。`(*Host).Serve(ctx, conn)`の流れは次のとおりです。
  - `Accept`で認証済み`*Channel`を得る。
  - `agentwire.NewServer`を作る。入力は、handshakeで証明した`Peer.Key()`、同じ正規の承認鍵`ApprovedKeys()`、心拍喪失の`ch.Done()`（Liveness）で、ここでSessionのadapter専有を取る。
  - `Serve(ctx, ch)`で処理する。
  - 終了時は`TransportLost` → 同じchannelの`Close`（合流の確認）→ `Session.Close`（guestの回収待ち）の順で行い、未確認があればerrorに含めて返す。2回目のServe、再接続、再開はありません。
- **端末側**：`agentlink.Connector(ClientConfig, conn)`は単回の`connector`を返します。`agentchannel.Open`が返す`*Channel`を`cmd/rescue-mcp`が`agentwire.NewClient`→`mcpadapter.Serve`へ渡し、終了時はClientを閉じた後に同じstreamの`Close`で合流結果を確認します。
- どちらも、渡された`net.Conn`、登録済みsigner、pinしたhost公開鍵だけを使います。dial・listen・鍵の生成・登録・grantの発行はしません。`cmd/rescue-mcp`の既定のconnectorはnilのままです。
- **未実装**：
  - 利用者の明示同意からgrant（`MacCurrentUserPolicyDigest`、`CurrentUserNoIsolation`、`KeySetDigest`）を発行するlocal producer。試験の発行はfixtureで、自己承認の代用ではありません。
  - 端末鍵の実登録、受信者が明示起動するdispatchでpin値・Binding・期限を端末へ渡す経路。
  - relay endpointの登録と受理済み`net.Conn`の供給、dotsのMCP登録、package化。
- **native（Mac）**：`macagent`は独立レビュー中で凍結しています。合成は正規Sessionを通じてそのruntimeを使う前提ですが、実kernelの挙動（process group、signal、EOF）はこの試験で検証していません。

## 検証の範囲

- 試験は`backend/internal/agentchannel`の`_test.go`だけで、buffer付きmemory conn（期限・半開・読取り停止・close遅延/失敗を注入できる）、既存の`ephemeral.Issuer`と実`agentexec.Session`、起動しないMac profileのfake runtime、合成の決定的Ed25519 seed fixtureを使います。network bind、dial、process、shell、OS signal、実鍵は使いません。SSHのhandshakeは実ライブラリをmemory上で動かします。
- 対象：pin済みhostと登録済みclientでの双方向data、誤ったpin・未登録鍵・LaunchID・epoch・generation・policy・鍵集合digest・profileの拒否、設定の拒否、Acceptの単回性、非対応channel・2本目・global/channel request・拡張dataの拒否、取消・期限切れ・部分handshake、心拍が通る無出力期間の継続、半開relayと読取り停止での失効後close、hard deadline・外部失効・ctx取消、心拍の拒否と過多、close遅延時の未確認保持と後からの合流、close失敗の同じconnでの再試行。
- 合成の試験は`backend/internal/agentlink`の`_test.go`です。MCP stdio→実agentwire client→暗号化した実SSH agent channel（buffer付きmemory conn）→同じgrantのSession→fake Mac runtimeの全経路を通します。内容は、固定出力とrequest_idの冪等・不一致、明示cancel、所有channelの喪失（半開）、要求なしの短いhard deadline、誤pin・未登録鍵・epoch・generation・鍵集合・profile・policyの拒否、session・direct-tcpipへのfallbackがないことです。2本目のagent channelの拒否はagentchannelの試験で確認します。
- 試験は時間に依存する部分があり（10ミリ秒〜数秒）、遅い環境での揺れは統合担当の実行で確認が必要です。
- これらの合格は、実relay、実通信、native起動、実鍵登録、同意、配置の成功を示しません。

## Integration update (2026-10-31, local source only)

The independent channel/link review found no Critical/High/Medium defects.
Coordinator follow-up retains an unconfirmed channel in Host and exposes
Host.Close(ctx) for same-owner retries. A failed Connector returns a non-nil
stream when cleanup is unknown; callers must retain it. A second Host.Serve
refuses without revoking the first. Every end of the accepted channel is
classified as agentwire.ErrPeerLost by the composition; stop reasons observe
the canonical deadline before transport loss.

The recipient-local consent producer now exists as source in
backend/internal/maclaunch (see its README). It uses /dev/tty, fixed deadlines,
explicit current-user non-isolation disclosure and the existing issuer/Session/
Host chain. Its terminal/native entry has NOT been executed; the package launcher,
operational relay, dispatch delivery and dots remote MCP registration are still
absent. The default rescue-mcp connector remains nil.

The native macagent delta review is complete at source level; its real kernel
behavior remains unverified. None of this update enables the old product gate.
