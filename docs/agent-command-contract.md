# agent command coreの契約と未接続境界

[安全要件](security.md) / [脅威モデル](threat-model.md) / [単発版の配置](ephemeral-deployment-architecture.md) / [一時guest](temporary-guest-account.md) / [ADR-0003](adr/0003-owned-guest-workload.md) / [ADR-0004](adr/0004-localservice-auth-helper.md)

2026-10-31の方針変更で、AI agentによる操作が主目的になりました。目標は次の2つです。

1. agentが遠隔hostでcommandを実行できる（goal1）。
2. 人が開発PCから遠隔hostへ操作agentをすぐに送り出せる（goal2）。

`backend/internal/agentexec`は、goal1で共通に使うcommand coreです。
memory/fake試験と、全OS共通のsource登録だけを持ちます。
この文書は共通command契約です。Macの実装・固定旧snapshotの限定native4case、HTTP discovery、GUI合成試験、Windows保留、配布未完を区別し、現在の状態は[検証状態](tether-pilot-status.md)を正本とします。
fake/sourceの合格は、native guestの起動、遠隔接続、GUI、SSH、配置の成功を意味しません。製品gateは閉鎖したままです。
既存のSSH内側作業はこの契約から分離しており、コードも複製していません。

## 最小APIと呼出し経路

```text
保護owner: agent policyの承認 → ephemeral.Issuer.Prepare(PolicyDigest=FullPolicyDigest()) → Launch → *supervisor.Authority
  → agentexec.New(Config{Issuer, Authority, Runtime, Clock, Limits, CleanupTimeout})
      policy照合 → Issuer.Lookupでpointer一致 → Authority.ClaimConsumer（単回） → Authority.Done監視
  → 認証済みtransportから届いたrequestを、同じownerがSessionへ渡す
      Start / Status / Output / Cancel / Cleanup（公開形は前版と同じ）
  → Session.mu下でgrantとcommand停止を再確認 → GuestRuntime.StartShell(ctx, Binding, stdin) → GuestProcess
      Next（順序付き出力） / Wait / Terminate（Job全体） / Release（回収確認）
  → Issuer.Revoke/Close、Owner.Revoke、期限timer、Revoke、TransportLost
      → 認可失効を先に確定 → guest workloadの取消
```

- 認可の根拠は、`Issuer.Lookup`が返す現在Authorityと、保護ownerが渡した`*supervisor.Authority`のpointer一致です。
  同じ値を持つ別Authority、別issuer、request内のBinding・owner ID・SID・booleanは認可に変換しません。
- requestの`Binding`はgrantへのroutingと整合の検査にだけ使い、資格情報ではありません。
  送信元の認証はtransport側（端末側の正規認証）の責任で、relayは認可しません。coreは認証を行いません。
- 新しい認可源、SYSTEMのSeTcb発行者、resume/reset、期限延長はありません。

### supervisorへの追加（最小）

`supervisor/authority.go`へ、次の3つを追加しました。現在利用者の境界のための`CurrentUserNoIsolation`、`NewCurrentUser`、`ephemeral.NewCurrentUser`は、後述の「現在利用者の境界」に記載します。

- `Done()`: 同じAuthorityが失効した時点で一度だけ閉じます。明示Revoke、issuerのRevoke/Close（`Owner.Revoke`経由を含む）、Lookup等で観測した期限が対象です。
  timerではないため、未観測の期限だけでは閉じません。期限はSessionのtimerが執行します。
  Launch callbackのcontextは呼出し側の寿命に従い、起動完了とは別に取り消され得るため、借用しません。
- `ClaimConsumer(binding)`: 有効なgrantを1つのconsumerが単回だけ専有します。解放や再専有はなく、Closeや失効後も戻りません。
  状態はAuthority自身が持つため、保持pointerのglobal mapを作りません。
- `Grant()`: 不変のgrant値を、時刻を観測せずに返します。
  別goroutineで読んだ時刻が逆順に届き、時計の巻戻りと誤観測されるのを避けるためです。状態の判定には使いません。

## 承認されたmodeとpolicy

- modeを決めるのはgrantの`PolicyDigest`だけです。`Config`にmodeや期待digestの入力はありません。
- `FullPolicyDigest()`は、固定文書`rescue-host.agent-command-policy v1`のSHA-256です。
  文書には、mode=full、一時非管理者guest、固定guest shellのstdinだけを受けること、argvはruntimeが固定、任意target pathなし、特権shellなし、restricted helperは未定義で拒否、1承認1 commandを記載しています。
  既存のSSH/Python policy digestとは別の契約で、既存のdigestは変更していません。
- このdigest以外のgrant（評価用の`exit 0`、SSH、未知のdigest、ゼロ値）では、`New`が`ErrPolicy`を返します。そのため既定の未設定状態では拒否のままです。
- requestの`ModeFull`は、承認されたmodeと一致した場合にだけ受け付けます。`ModeRestricted`は常に`ErrRestrictedUndefined`です。

### 実行profile（2026-10-31追加）

| profile | policy digest | runtime | 意味 |
| --- | --- | --- | --- |
| `ProfileWindowsGuest` | `FullPolicyDigest()`（意味は変更なし） | profileを宣言しない`GuestRuntime`（従来どおり） | Windowsの一時非管理者guest。native runtimeは未実装 |
| `ProfileMacCurrentUser` | `MacCurrentUserPolicyDigest()`（別domain・別版の固定文書） | `ProfiledRuntime`として`ProfileMacCurrentUser`を宣言するruntime（`macagent.Runtime`） | ログイン中のMacユーザー。隔離なし。詳細は[Mac runtime](mac-agent-runtime.md) |

- `New`は、grantのdigestが示すprofile、runtimeが宣言するprofile、grantの`GuestBoundary`、issuerの種別の4つの一致を要求します。不一致は`ErrProfile`で、Authorityの専有は消費しません。
- profileはrequest、MCP、`Config`の値からは選べません。`Session.Profile()`は表示用で、認可の入力ではありません。

### 現在利用者の境界（2026-10-31追加）

Macの現在利用者profileは、guest分離を偽って表明しないよう、別の境界値と明示的な入口だけで発行します。

| 境界 | 発行できる入口 | 受け付けるprofile |
| --- | --- | --- |
| `supervisor.SeparatedNonAdminGuest`（従来） | `ephemeral.New()` → `supervisor.New` | `ProfileWindowsGuest` |
| `supervisor.CurrentUserNoIsolation` | `ephemeral.NewCurrentUser()` → `supervisor.NewCurrentUser` | `ProfileMacCurrentUser` |

- 既定の`supervisor.New`と`ephemeral.New()`のLaunchは、現在利用者の境界を従来どおり拒否します。既存のWindows consumer（bootstrap、control、servicehost等）は`SeparatedNonAdminGuest`だけを検査しており、変更していません。既定issuerの`Prepare`の挙動も変えていません。
- 現在利用者issuerは、guest境界のpolicyを`Prepare`でgeneration消費前に拒否します。issuerの種別は生成時に固定され、切り替える入口はありません。
- 期待digestやallow flagを外部から渡す入力はありません。policy・runtime・境界・issuerの束縛は`agentexec.New`が行います。
- epoch、単回Launch、Lookup、Revoke、期限の規則は、guest境界と同じです。
- **Macの同意producer**: `maclaunch` と `rescue-mac-agent` が、現在ユーザー・隔離なしの範囲を提示して今回の明示同意を受けるsourceを実装しています。`NewCurrentUser`自体は同意を代用しません。[CLI契約](agent-cli.md)と[固定snapshotの限定実証](tether-pilot-status.md)を分けて確認してください。
- Mac policy文書は、隔離がないこと、cwdがsandboxではないこと、回収が自分のprocess groupとI/Oだけを対象とすることを、承認範囲として明記しています。
- **Windowsで必要なproducer（未完）**: 保護された承認経路（servicehost/serviceprovisionの承認provider）が、operatorへfull agentの範囲を提示します。
  正規の承認を得たときにだけ、`ApprovedPolicy.PolicyDigest = agentexec.FullPolicyDigest()`で`Issuer.Prepare`を呼ぶ責任を持ちます。
  restrictedを選べる承認や、別digestの定義は、このproducerを作る時点で別に決めます。
  今回、実native操作を有効化する設定やfake producerは追加していません。

## requestの契約

| 操作 | 必要条件 | 結果 |
| --- | --- | --- |
| Start | `Version=1`、同じBinding、live grant、承認modeと一致する`ModeFull`、1〜64文字の印字ASCII request ID、1byte以上`MaxCommandBytes`以下のcommand、`TimeoutMillis`が1以上`MaxTimeout`以下 | opaqueな32桁hexのcommand ID |
| Status | 同じBinding | 外部失効を先に反映し、phase / exit / stop理由 / cleanupを別々に返す |
| Output | 同じBinding、live grant、`MaxBytes>0` | Cursor以降の有限なchunk。Cursor以前は確認済みとして解放 |
| Cancel | 同じBinding | 外部失効を反映したうえで、そのcommandだけを不可逆に停止。grantは失効させない。冪等 |
| Cleanup | 同じBinding、deadline付きcontext | 保持中の同じprocessに対し、未確認の段階だけを再試行 |

- **request IDは1 grant（1 Session）内だけで有効です。** 同じIDと同じ正規payloadは同じ実行を返し、結果不明の開始後や失効後も副作用を繰り返しません。
  新しい承認のSessionは旧IDを知らないため、旧Startを再送すると新しい実行になります。
  adapterは結果不明のStartを新しいgrantへ自動で再送せず、「不明」として利用者へ示します。
- 正規payloadは、version、Binding全体、mode、timeout、request ID、command bytesを長さ付きでSHA-256にしたものです。同じrequest IDで値が異なれば`ErrRequestMismatch`です。
- command IDは128bitの乱数です。取得に失敗した場合と、Session内の既存IDと衝突した場合は、`ErrIdentifier`でruntimeを呼ばずに拒否します。
- full modeのcommand bytesは`GuestRuntime.StartShell`のstdinだけへ渡します。`GuestRuntime`にはargv、path、user、SID、tokenの入力がありません。

## 期限の単一owner（2026-10-31更新）

- hard deadlineの判定は、Sessionの`checkDeadline`だけが行います。timerは常に1本です。
- 待ち時間は次の最小値です。
  - `DeadlineRecheck`（1秒）
  - 壁時計の残り
  - 固定の経過予算（New時点の壁時計の残りを単調時計の上限として固定したもの。後の壁時計の変化で短くはなっても、延びはしない）
- callbackは、issuerの`Lookup`（issuerのlock内で時計を読み、期限・巻戻り・取得不能を判定して失効させる）で決めます。失効の理由は時刻を新たに観測しない`Authority.Status()`で読みます。そのため、別goroutineが先に読んだ時刻による誤った巻戻り判定は起きません。
- 期限前に呼ばれたcallbackは失効させず、同じownerとして再予約します。巻戻りや取得不能の時計は失効させ、期限を延ばすことはありません。
- 失効の順序は従来どおりです。認可を先に失効させてから、commandを取り消して回収します。
- sleepや壁時計の前進の後でも、要求なしに1秒以内に`StopHardDeadline`で終わります。commandのtimeout（単調時計の長さ）は`StopCommandTimeout`のままです。grantの期限で頭打ちになったcommand timerは、同じowner（`checkDeadline`）へ判定を渡します。
- `Session.ObserveDeadline()`は、同じ判定を今すぐ行います。transportが期限で終わるとき、`TransportLost`より先に呼ぶことで、停止理由を期限として記録します。
- 前提（compileでは確認できない事実）：macOSのsleep中にGoの単調時計が進むか止まるかに依存せず、壁時計の再観測で上限を守る設計です。OSの時計の実挙動は検証していません。

### transport向けの読取り専用API

- `Session.Grant()`：正規Authorityの不変grantの写しで、時刻を観測しません。
- `Session.Done()`：正規Authorityの失効signalです。
- `Session.ClaimTransport()`：認証channel 1本だけの単回専有で、wireの`Claim()`とは別です。どちらも解放しません。

## 期限、失効、取消

- hard deadlineはgrantから取得し、Sessionのtimer（公開`New`では`time.AfterFunc`）と、`Issuer.Lookup`による観測の両方で執行します。commandのtimeoutはgrantの期限で頭打ちにします。
- 頭打ちになったcommand timer（timeoutがgrantの期限以降のもの）は、grantの期限そのものとして扱います。
  発火すると、Sessionのtimerより先でも認可を先に失効させ、`StopHardDeadline`を記録します。`StopCommandTimeout`は、grantの期限より前に満了したcommand timeoutだけに使います。
  以前は2つの実timerがほぼ同時に発火し、grantの期限による停止が`StopCommandTimeout`と記録されることがありました。
- 外部失効（`Issuer.Revoke`/`Close`、`Owner.Revoke`）は`Authority.Done`の監視goroutineがすぐに受け取ります。requestや出力を待たずにworkerを止めます。
  Start/Status/Output/Cancel/Cleanupも、処理の前に`Lookup`で失効を反映します。
- どの経路でも、`Issuer.Revoke(Binding)`と保持Authorityの`Revoke`を先に実行します。その後でworker contextを取り消し、`Terminate`を呼びます。
  試験は、`Terminate`の時点でAuthorityが`revoked`であることを確認しています。
- workerは`StartShell`の直前に、`Session.mu`の下でgrantのliveとcommandの停止を再確認します。停止済みならruntimeを呼ばず、`PhaseCanceled`/`CleanupNotRequired`にします。
  これはローカルの受付順序を決めるだけで、OS上の開始と失効を原子化しません。
  確認後の失効はctxの取消で伝わり、遅れて返ったprocessは同じcommandが回収します。native側にも、開始直前の再検査とctxへの追従が必要です。
- transport喪失は一時停止ではありません。同じSessionは再開せず、次のcommandには新しい承認、新しいAuthority、新しいSessionが必要です。隠れた再接続はありません。
- Cancelとcommand timeoutは、そのcommandだけを止め、grantは維持します。
- 1つのAuthorityから作れるSessionは1つだけです（`ErrSessionClaimed`）。同時の`New`でも同じです。Closeしても専有は戻らず、同じgrantで新しいSessionは作れません。

## 出力

- stdout/stderrを1本の順序付き列として保持し、各byteにoffsetを振ります。
  順序は`GuestProcess.Next`が返した順で、guestが2つのpipeへ書いた実時刻順は保証しません。
- 未確認の保持量は`MaxRetained`（recordごとに64byteの管理分を加算）が上限です。上限に達するとworkerは読取りを止め、guest側pipeに背圧をかけます。
  背圧の待機はcommand contextで打ち切られるため、取消、timeout、失効、期限のどれでもworkerは止まります。
- `MaxTotalOutput`を超えた出力はoffsetを振らずに破棄し、`Truncated`と`Dropped`で報告します。黙って欠落させません。
- Cursorより前は確認済みとして解放します。確認済みの範囲や、次のStartで解放された前commandの範囲を再要求した場合は、`Gap`で示します。
- 1回の応答は`MaxReply`以下です。`Complete`は、workerが終了し、全出力を返し終えた状態です。

## 状態と回収

- `Phase`（Starting/Running/Exited/Canceled/Failed/StartUnknown）、`Exit`、`Cleanup`（Pending/Confirmed/Unknown/NotRequired）は独立に返します。終了コードが0でも、回収が未確認なら`CleanupUnknown`です。
- `Exit.Known`は、停止要求より前に観測した自然終了だけを示します。強制停止の終了コードはcommandの結果として返さず、`Stop`の理由で示します。
  自然終了を観測した後のCancelや失効で、`Exited`を`Canceled`へ書き換えることもありません。
- workerは、自然終了の場合も含め、常に`Terminate`（Job全体の停止）の後に`Release`（実終了と所有handle回収の確認）を呼びます。
- `StartShell`が`(process, error)`を返した部分取得や、取消後に遅れて返ったprocessも、同じcommandが保持して回収します。
- `(nil, ErrNotStarted)`だけを未開始の証明として、`CleanupNotRequired`にします。それ以外の`(nil, error)`は`StartUnknown`と`CleanupUnknown`のままにし、再試行も再起動もしません。
- 回収未確認のcommandが残る間、新しいStartは`ErrCleanupUnknown`または`ErrBusy`で拒否します。
  Cleanupは、失敗した段階（Terminate、またはReleaseのみ）を同じprocessで再試行します。grantは不要です。
- `Close`はgrantを失効させ、全workerの終了と回収確認を待ちます。未確認なら`ErrCleanupUnknown`を返します。
- Cancel・失効・transport喪失で行う早期`Terminate`は、同じcommandが最大1本だけ所有します。
  停止要求の呼出しとは分離し、`CleanupTimeout`はgoroutineへ渡す前に固定します。workerはその完了をjoinしてから最終`Terminate`と`Release`へ進みます。
  `Cancel`の成功は停止受付だけで、早期停止の完了や回収成功ではありません。`Close`の呼出側期限が先に切れた場合はunknownを返し、未終了の処理とprocess参照を同じcommandに保持します。
- 早期停止の待機中は新規StartとCleanup再試行を拒否し、繰返しの停止で別の早期`Terminate`を作りません。
  元処理の完了・panic回収・context期限後も最終停止と回収を別に判定します。自然終了を先に観測したworkerの停止所有と結果は、後からのCancelやCloseで置き換えません。

## 制限

- 1承認につき、同時に動かせる非終端commandは1つだけです。並列command、対話shell、PTY、stdinの追加送信、長期sessionは未実装で、multi-command sessionとしての完成とは扱いません。
- Session内のcommand記録は`MaxCommands`件までです。新しいStartは、前commandの未確認出力を解放します。
- goroutineは、Sessionごとの失効監視1本、commandごとのworker 1本と最大1本の早期停止、timer callbackです。
  監視goroutineは失効かCloseで終わります。workerは、`GuestProcess`がcontextと`Terminate`に従う前提で動き、nativeがそれに反すると回収未確認のまま残ります。
  呼出側の有限待機は、応答しないnative処理の強制終了や所有processの退出許可ではありません。後着の完了を待つ同じownerを保持し、別Sessionへの置換・grant復活・回収成功へ読み替えません。

## 次に必要なnative adapterとcaller

2026-10-31の指示で、非管理者guestでのfull agent commandをsourceとして実装することは認可されました。
ADR-0003の固定`exit 0`は評価用の範囲で、このsourceを禁止するものではありません。
ただし、実OSでの実行は未承認です。次の項目は、まだsource化していません。

1. `agentexec.GuestRuntime`を実装する`servicehost`のadapter（新規file）。
   既存の方式C経路（`runtime_guest*`、`childjob.Prepared`の`Inspect`→`CommitInspected`、`guestaccount`のBATCH logon）で、同じ承認・同じguest tokenを保持するownerが`StartShell`を実装します。
2. 固定imageと固定argv（`childjob.NewFixedCommand`。例：保護layoutのPowerShellをstdin読取りで起動）を使います。
   `childstdio`の所有pipeでstdinへcommand bytesを書いて閉じ、stdout/stderrを`Next`へ渡します。開始直前には同じtargetを再検査します。
3. `Terminate`は同じPreparedのJob全体の停止に、`Release`は`Wait`/`Close`によるleader退出・Job active=0・handle回収の確認に対応させます。
4. callerは、`Issuer.Launch`が返したAuthorityと同じ`serviceauthowner`ownerの下で`agentexec.New`を作ります。
   Serviceの停止（`Owner.Revoke`）は`Authority.Done`を通じてSessionへ届きます。
5. 上記「必要なproducer」に書いた、agent policy digestを設定する承認provider。

## MCPとrelayの境界

- MCP serverは薄い外部adapterとして、tool呼出しをStart/Status/Output/Cancelへ1対1で写すだけです。grantの発行、期限延長、Binding生成、mode・profileの選択、restricted helperの定義、再接続は行いません。実装と範囲は[MCP adapterとagent wire](agent-mcp-adapter.md)を正本とします。
- 再送keyは、agentが明示するtool引数の`request_id`です。JSON-RPCのIDは仕様上再利用できないため使いません。wire Serverは、streamごとに48 bitの乱数接頭辞を付けてStartのrequest IDにします。同じgrant内の再送は同じ実行へ集約し、新しいgrantへの自動再送はしません。
- wire Serverは`Session.Claim()`で、Sessionの唯一のadapterを単回だけ専有します。相手鍵は、承認鍵集合の`runbackend.KeySetDigest`がgrantの`KeySetDigest`（`Session.KeySetDigest()`）と一致する場合に限り、その集合の要素であることを要求します。鍵の設定はlocal compositionだけが行い、実channelの認証はしません。
- relayは認証済みmessageを配送するだけで、作業を認可しません。relayの再接続や再登録で旧Sessionを復元しません。
- 現在のMac経路は[agent channel](agent-channel.md)・[WSS transport](agent-transport.md)・[gateway](agent-gateway.md)に実装契約を分けています。新しい端末の鍵・登録・本人同意はsource取得だけでは成立しません。

## goal2：開発PCからの送出し

Macの最小派遣経路は受領者の前景CLI起動とfresh同意です。Windowsの一時非管理者guest・一時監督serviceは別方式であり、Macの必須依存でも同等の隔離保証でもありません。
最初の到達には、既に承認済みの管理接続があるか、受け手が自分でpackageを起動することが必要です。遠隔からの無承認install経路は作りません。

| 段階 | manifestに記載する内容 | lifetimeと所有 |
| --- | --- | --- |
| package | 固定artifactと版、hash、署名（未決）、同梱する監督service・認証helper・agent runtimeのimage、固定guest shellのpathとargv、agent policy文書の版 | 配布元が固定する。開発PC側の設定で中身を変えない |
| launch | 受け手の起動、または既存の承認済み管理接続による起動。operator承認画面に期限と操作範囲（full agent）を表示する | 承認ごとにfresh issuer epoch、Authority、Sessionを作る。再起動後は再承認が必要 |
| registration | 一時監督service、認証helper、一時guest、agent用の端末側endpoint。relayには不透明な配送IDだけを渡す | 承認の期限までに限る。relay側の再登録でgrantを延長・復元しない |
| teardown | 失効→guest workloadの停止→回収確認→guestの無効化/削除→serviceの撤去、の各結果 | 各段階を別々に確認し、未確認を成功と表示しない |

開発PCが持つのは、遠隔hostへ送るcommandと、受け取ったoutput/statusだけです。遠隔hostのgrantを発行・延長する権限は持ちません。

## 進捗

| 項目 | goal1：commandの遠隔実行 | goal2：開発PCからの送出し |
| --- | --- | --- |
| command契約・冪等・期限・失効順・出力・回収 | source/fake試験あり（本package） | 共通coreとして再利用する予定 |
| policy束縛・失効通知・単回専有 | source/fake試験あり（agentexecとsupervisor） | 同左 |
| Windows agent policyの承認producer | 未完（Mac CLIとは別） | 同左 |
| Mac現在ユーザーruntime（`macagent`） | source/fake試験と固定旧snapshotの限定native4case。新buildへの実証転用不可 | 同左 |
| Mac現在利用者の境界表明 | source/fake試験あり（`CurrentUserNoIsolation`と明示入口、agentexecでの束縛） | 同左 |
| Macの利用者同意producer | maclaunch/CLIに実装。本人同意必須 | 同左 |
| Windows guest shell runtime | 停止中（source化は認可済み、実OSは未承認） | 同左 |
| Service/caller配線 | 未実装 | 未実装 |
| 認証済みagent channelと合成（agentchannel・agentlink） | source/memory試験あり（MCP→wire→暗号化channel→同じSession→fake Macの全経路） | 同左 |
| relayの登録・配送、受理済み`net.Conn`の供給 | WSS transport/CLIにsourceあり。実環境と登録は別 | 同左 |
| MCP adapterとagent wire | source/memory試験あり（鍵集合照合・単回専有・生存signalの受口を含む）。実channelの現行契約はagent-channel/agent-transportを参照 | 同左 |
| package/launch/registration/teardown | 該当なし | manifestとlifetimeの文書のみ |
| 実証・GUI・SSH・配布 | 固定旧snapshotのMac native4caseのみ限定実証。実agent+GUI・SSH互換・一般配布は未検証/未完 | 一般受領者の派遣受入は未完 |

## 検証の範囲

- coreの試験は`backend/internal/agentexec`と`backend/supervisor`（`ephemeral`を含む）の`_test.go`です。現在利用者の境界について、明示入口だけでの受理、profile・境界・issuerの不一致の拒否と専有の非消費、既定入口での拒否、Revoke・期限の規則を確認します。adapterの試験は[MCP adapter](agent-mcp-adapter.md#検証の範囲)を参照します。memory上のfake runtimeと、手動発火のtimerまたは数百msの実timerを使います。shell、process、network、OS APIは使いません。
- 対象は次のとおりです。
  - 認可済みStart→Output/Status→Cancel→同owner回収
  - 他grant・版・restricted・policy不一致・期限切れ・失効
  - 公開`New`の実期限と、2つ目のSessionの拒否（同時呼出しとClose後を含む）
  - 同ID再送と不一致、grant単位のID
  - 開始前のCancel/外部失効でruntimeを呼ばないこと、開始中のCancelと遅延process、部分取得
  - 背圧と上限、終端後のStatus
  - issuer/Ownerの外部失効を、requestなしで停止すること
  - transport喪失、回収未確認とClose・再試行、command IDの衝突
- 早期停止の追加試験は、Mac current-user / Windows guestの両profileで同じ公開Session APIを使います。
  メモリ内のcontext協調的な`Terminate`を保持し、短いClose期限、受付と回収の分離、停止の単回所有、join順、後着完了・期限・panic、同じprocessでの最終停止再試行を`testing/synctest`で照合します。自然終了後の停止も別に検査します。
  仮想時間のcore試験であり、実OSの停止時間、native全回収、実dot・GUI・常駐/単発版の実接続受入の証明ではありません。
- これらの合格は、native guestの起動、実Windowsの保護、遠隔接続、MCP/relayの動作、配置の成功を示しません。
