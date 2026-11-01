# agent command core

1つの承認grantに対するcommandのstart/status/output/cancel/cleanupを扱う、memory-onlyのcoreです。
契約、未接続のnative/transport/MCP境界、goal別の進捗は[agent command契約](../../../docs/agent-command-contract.md)を正本とします。

- 認可は、同じ`ephemeral.Issuer`の現在Authorityと保護ownerが渡したAuthorityのpointer一致だけです。requestのBinding・ID・booleanから認可を作りません。
- modeとprofileは、grantの`PolicyDigest`だけで決まります。Windows一時guestは`FullPolicyDigest()`、Macの現在ユーザーは`MacCurrentUserPolicyDigest()`です。それ以外は`New`が拒否します。restricted modeは未定義として拒否します。
- runtimeが宣言するprofile（`ProfiledRuntime`。宣言がなければWindows guest）、grantの`GuestBoundary`、issuerの種別がpolicyのprofileと一致しない場合は、専有の前に`ErrProfile`になります。Macの現在利用者は`ephemeral.NewCurrentUser`と`supervisor.CurrentUserNoIsolation`の明示入口だけを受け付けます。Macの詳細は[Mac runtime](../../../docs/mac-agent-runtime.md)を参照してください。
- `Session.Claim()`はadapter（wire Server）1つだけの単回専有です。`Mode()`、`Profile()`、`KeySetDigest()`は読取り専用の写しで、認可の入力ではありません。
- hard deadlineは単一owner（`checkDeadline`、timer 1本）が、最大`DeadlineRecheck`（1秒）ごとに正規issuerの時計で再観測します。sleepや壁時計の前進の後でも、要求なしに終わります。New時の残りを経過予算として固定し、延長はしません。期限前のcallbackでは失効させません。頭打ちになったcommand timerは、このownerへ判定を渡します。
- transport向けに、読取り専用の`Grant()`・`Done()`、単回の`ClaimTransport()`、即時の再観測`ObserveDeadline()`があります。
- 1つのAuthorityには`ClaimConsumer`で1 Sessionだけを結び、Closeしても専有を戻しません。
- `Authority.Done`を監視し、issuerやOwnerの失効をrequestなしでworkerへ伝えます。失効は認可を先に確定してからworkerを取り消します。
- command bytesは`GuestRuntime.StartShell`のstdinだけに渡します。argv・path・token・SIDの入力はありません。
- 回収未確認の間は新しいStartを拒否し、同じprocessで同じ段階だけを再試行します。同時に実行できるcommandは承認ごとに1つです。

通常buildにはnative runtimeもfakeも含みません。試験はmemory上のfake runtimeと、手動発火または短い実timerだけを使い、shell・process・network・OS APIを使いません。
fake試験の合格は、guest shellのnative起動、実Windows保護、遠隔実行の成功を示しません。
