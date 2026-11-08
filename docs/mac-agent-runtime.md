# Macの現在ユーザーで動くagent runtime

> **現在地の正本（2026-11-01）:** 本文には初期source実装時点の「未実装」「未接続」「実機未実行」が残っています。API・安全契約を維持しつつ、現在のCLI/transport接続と限定実証は[文書索引](README.md)・[検証状態](tether-pilot-status.md)を優先してください。nativeの固定4case、Claude HTTP/OAuthのdiscovery、GUI合成試験は別の証拠で、実AI command・全利用者・新build全体の成功へ広げません。

> **2026-11-01 更新:** 現在の限定実機検証・OAuth結果・一時派遣の残件は [Tether pilot 状態](tether-pilot-status.md) を正本とします。以下の「source only」「未実行」「producer 未実装」は初期実装時点の記録であり、現在の全体状態ではありません。権限・設定・回収の制約は維持します。更新された契約は各文書末尾の追補を優先してください。

[agent command契約](agent-command-contract.md) / [安全要件](security.md) / [脅威モデル](threat-model.md)

2026-10-31の方針により、dotsがMCP経由でMac hostを操作する経路を優先します。
`backend/internal/macagent`はそのためのagentexec用runtimeです。既にログインしている現在ユーザーとして、固定のshellをcommandごとに1回起動します。
現状はsourceとfake試験だけで、OSのcommand、signal、descriptor操作はこのrepositoryの試験で一度も実行していません。
本人同意のproducer、channel/relay、package、MCP、実Macでの動作は、別担当の作業か未実装・未検証です。
Windowsの一時guest経路には依存していません。

## これは隔離ではない

- commandは**現在ユーザーの全権限**で動きます。読めるfile、keychainへの要求、network、同じユーザーの他processへの操作に届きます。
- cwdは作業を始める位置にすぎず、**filesystemのsandboxではありません**。
- 新しいsession/process groupは敵対的な封じ込めではありません。commandは`setsid`などでgroupの外へ出られます。
  **groupを出た子孫の終了は保証しません。**
- PTYはなく、対話shellとの互換性も約束しません。operatorの制御端末には接続しません（setsidで制御端末を持たないsessionになります）。
- sudo、昇格、UIDの変更、service、daemon、launchd登録、TCC、フルディスクアクセス、アクセシビリティの要求や変更はしません。既存のOS保護もそのままです。

この範囲は、Mac専用policy文書（`MacCurrentUserPolicyDigest()`）の`principal=logged-in-current-user`、`isolation=none-current-user-resources-reachable`、`cwd=fixed-by-local-owner-not-a-sandbox`、`cleanup=owned-process-group-and-io-only`として承認の対象になります。

## 呼出し経路と信頼する入力

```text
受け手が自分で起動したlocal binary（構成owner）
  → macagent.New(Config{WorkDir, Home, TermGrace, KillWait, IOWait})
      // 起動はしない。SIGCHLDが無視されていればErrHostState。darwin以外はErrUnsupported
  → 本人同意producer（未実装・別担当）: ephemeral.NewCurrentUser()
      → Prepare(PolicyDigest=agentexec.MacCurrentUserPolicyDigest(), GuestBoundary=CurrentUserNoIsolation) → Launch
  → agentexec.New(Config{Issuer, Authority, Runtime: macRuntime, ...})
      // policy、boundary、issuerの種別、runtimeのprofileが一致する場合だけSessionを作る
  → Start/Status/Output/Cancel → Runtime.StartShell
      → 読取り専用のhost検査 → 固定shellの起動 → kqueueによる終了観測
```

- `WorkDir`と`Home`はlocal構成ownerだけが決め、遠隔request、MCP、承認messageからは受け取りません。
  受け付けるのは、`/`ではないclean absoluteなslash pathだけです（NULと改行は拒否）。
- profileは、grantのdigest・boundary・issuerと、runtime自身が宣言する`AgentProfile()`から決まります。requestやMCPからは選べません。
  Mac runtimeはWindows guestのgrantを扱えず、profileを宣言しないruntimeはMacのgrantを扱えません。
- 承認を自分で作る経路や、未構成で有効になる既定値はありません。

## 固定の実行条件

| 項目 | 値 |
| --- | --- |
| shell | `/bin/sh` |
| argv | `["/bin/sh", "-s"]`。commandはstdinからだけ読む |
| 環境変数 | `HOME=<構成のHome>`、`PATH=/usr/bin:/bin:/usr/sbin:/sbin`、`SHELL=/bin/sh`、`LANG=C`だけ。親からは何も継承しない |
| cwd | 構成の`WorkDir` |
| process | `os.StartProcess`と`Setsid`で起動する。新しいsessionとprocess groupになり（pgid == pid）、制御端末はない |
| descriptor | stdin/stdout/stderrの3本のpipeだけを0〜2へ渡す。pipeはclose-on-execで作る |
| 入力 | command bytesをstdinへ書いて閉じる。書込みが詰まった場合は、停止時にstdinを閉じて書込みgoroutineを合流させる |
| 出力 | stdout/stderrを32KiB単位で読み、最大4件をqueueに入れる。満杯なら次のreadを始めず、pipeに背圧をかける。順序は観測順 |

## 終了観測と回収（2026-10-31修正）

以前は、shellの自然終了を`Wait`がその場で回収（reap）していました。そのため、stdioを閉じて背景に残った同じgroupのmemberを回収前に止められませんでした。
現在は、終了の観測と回収を分けています。

- **終了の観測**: 起動直後に、kqueueの`EVFILT_PROC`へ`NOTE_EXIT|NOTE_EXITSTATUS`を登録します。kqueueは`ForkLock`の下で作り、すぐにclose-on-execにします。
  この通知はleaderを回収せずに、終了とwait statusを返します。
  - 登録時の `ESRCH` も observer の失敗として扱い、終了済みとは推定しません。同じ部分起動を保持して停止・回収し、自然終了ではなく失敗として報告します。
  - kqueueの作成や登録が失敗した場合は、同じprocessを部分起動として返し、ownerが停止・回収します。
  - NOTE_EXIT を含まない one-shot event や観測失敗では、終了状態を推測しません。`Wait`はerrorを返し、自然終了として報告しません。
- **回収は1か所だけ**: `stopAndReap`だけが回収します。下記の限定 snapshot 経路以外は、次の順に進みます。
  1. groupへTERMを送る
  2. 構成済みの `TermGrace` を待つ。ただし元の caller context の取消・期限で待機を打ち切る。stdio の早い EOF では短縮しない
  3. 同じgroupへKILLを送る（ここまで回収前）
  4. `wait4(WNOHANG)`で回収する

  限定 snapshot 経路を使わない自然終了でもこの順序を通り、stdio を閉じた同じ group にも回収前に KILL を送ります。各 member の終了そのものを証明する保証ではありません。
  caller context が先に終了した場合は、従来どおり既存の nonblocking snapshot / KILL 判定へ進みます。期限を延長したり、新しい cleanup 予算を作ったりしません。
- **自然終了のstatus**: 停止signalより前に観測した終了だけを、自然な結果として`Wait`が返します。
  有効な NOTE_EXIT 通知に status がない場合は、groupをsweepした後の回収で得たstatusを返します。終了後に送ったKILLでは、そのstatusは変わりません。
  停止signalの後に観測した終了は、自然な結果として報告しません。
- **signalの安全**: signalと回収は同じmutexで順序付けます。leaderが未回収で、所有を失っていない間だけ`kill(-pgid)`を送ります。
  回収後や、`wait4`の異常（ECHILDなど）で所有を失った後は、signalを一切送りません。PIDだけを指定したsignalや、名前でのkillはありません。
  group signalの成功はgroupへの送信を示すだけで、各memberの終了の証明ではありません。

### CleanupConfirmedの意味

次のすべてが成立した場合だけ、`CleanupConfirmed`になります。

- 自分が起動したshell leaderを、自分が1回だけ回収した。
- その回収の前に、同じ process group へ TERM/KILL を送り、どちらも失敗しなかった。または、新しい自然 known exit について signal 前に、未回収の同じ leader だけが存在する完全な owned-group snapshot を取得した。
- stdin を閉じ、stdout/stderr が正常 EOF となり、読書き goroutine が合流した。stdin の正常送信と Terminate による予定した local close は区別し、異常 read/write は確認済みにしない。
- 所有するdescriptor（3本のpipeとkqueue）の閉鎖を確認した。

groupやsessionを離れた子孫の終了は含みません。また、group signalの成功を、各memberの終了の証明として扱いません。

次の場合は`CleanupUnknown`のまま参照を保持し、Sessionは次のcommandを拒否します。自動でリセットはしません。

- KILLの後もleaderを回収できない（再試行では、未回収の間だけ再びsweepする）。
- `wait4`の異常で所有を失った（以後のsignalは止める）。
- groupへのsignalが失敗した（この失敗は記録し続け、再試行でも確認済みにしない）。
- stdioがEOFにならない（groupを出たprocessがpipeを保持している）。
- descriptorの閉鎖が不確実。

### descriptor閉鎖の試行規則

所有するdescriptorは`ownedCloser`で閉じます。

- closerが`errCloseRetry`を返した場合だけ、「閉鎖を試みておらず、まだ所有している」と扱い、同じownerが後で再試行します。
- それ以外のerrorは結果を不確実として固定し、二度とcloseを呼びません。番号が再利用されたdescriptorを閉じないためです。
- Goの`os.File`は、最初のClose後にdescriptor番号を手放します。そのためnativeのpipeでは、errorは常に不確実として扱います。
- kqueueのraw descriptorも、1回だけcloseを試みます。

## 信頼する構成の不変条件

次の条件は、OS上で原子的に強制できません。このruntimeを組み込む専用binary（構成owner）が守る前提で、守られない場合の保証はありません。

1. **継承されるdescriptor**: spawnの直前に`/dev/fd`を列挙し、descriptor 3以上に`FD_CLOEXEC`がないものがあれば起動を拒否します（`F_GETFD`の読取りだけを使い、hostのdescriptorを閉じたり変更したりしません）。
   検査からforkまでは原子的ではありません。そのため、同じprocess内のすべてのcodeは、descriptorをclose-on-execで作る必要があります。
   Go標準libraryはそう作ります。cgoや外部libraryで、raw fdをclose-on-execなしに作るcodeが並行して走らないことも必要です。
   host signer、鍵、token、control pipe、socketのdescriptorがcommandへ渡らないことは、この条件と検査に依存します。
2. **他の回収者がいない**: wait-any（`wait4(-1)`など）や、このPIDへの外部からの`wait`を行うcodeがあってはいけません。
   SIGCHLDのdispositionの変更や、`SA_NOCLDWAIT`も禁止です。
   構成時とspawn直前に、`signal.Ignored(SIGCHLD)`を読取り専用で確認し、無視されていれば拒否します（dispositionは変更しません）。
   外部codeが設定した`SA_NOCLDWAIT`は検出できません。
   所有の喪失を`wait4`で検出した場合は、signalを止め、`CleanupUnknown`にします。
3. **専用のprocess**: 同じprocessで、別のownerがこのpgidへsignalを送ったり、このPIDを扱ったりしないことが前提です。

## 未接続の境界

1. **本人同意producer**: Mac上でoperatorに「現在ユーザーで任意のcommandを実行でき、隔離はない」範囲を示し、明示的な同意を得たときだけ現在ユーザーissuerでgrantを発行します。
   統合担当が`ephemeral.NewCurrentUser`と`supervisor.CurrentUserNoIsolation`を追加済みですが、同意を取るproducerは未実装です。
2. **channel/relay**: 別担当の`agentchannel`などです。今回はtransport、鍵、登録を変更していません。
3. **MCP**: 別担当です。
4. **最初の到達**: 受け手が自分でpackageを起動することを最小のモデルとします。認証なしの遠隔installはしません。
5. **実Macでの検証**: `native_darwin.go`はcompileの対象にすぎません。kqueue、`wait4`、`kill(-pgid)`、`/dev/fd`、`/bin/sh`の実際の挙動は未検証です。

## 試験の範囲

- `macagent`の試験は、fakeの`osProcess`、fakeのhost検査、`io.Pipe`、memory上のreaderだけを使います。shell、subprocess、signal、descriptor操作、socketは使いません。
  公開の`agentexec.New`と、実際の現在ユーザーissuerを通します。
- 対象は次のとおりです。
  - policy・profileの分離
  - 固定のpath・argv・cwd・環境変数と、継承の除外
  - 自然終了の後もsweep（TERM/KILL）してから回収すること
  - stdioを閉じた背景memberを回収前にKILLすること
  - 有効な終了通知に status がない場合の回収後の status
  - observerの作成失敗（部分起動）と、実行中の観測失敗（終了を推測しないこと）
  - Cancelの順序、詰まったstdin書込みの解放、pipeが欠けたprocessの所有と停止
  - 期限、外部失効、起動しない条件、host検査による拒否
  - 部分起動と遅延起動、groupを出たstdio保持者、KILLの無視、signalの失敗、回収状態の喪失
  - descriptor閉鎖の再試行と不確実の固定
  - `Wait`と`Terminate`の競合（毎回1回の回収、回収前のKILL、回収後はsignalなし）
  - 読取り先行の上限（readごとに条件を確認する決定的な試験）と、Sessionの背圧
- これらはfakeの境界に対する試験で、native adapterの実際の安全性の証明ではありません。

### 限定 natural-exit snapshot 経路（source/fake）

[owned-group 観測契約](mac-group-observer-diagnostic.md)に従い、自然終了の
既知 status、signal 未実行、所有喪失なし、signal error なしの場合だけ、
同 owner の mutex 内で anchor-only snapshot とその leader の回収を行います。
完全な一覧が未回収 leader 1件のときに限り TERM/KILL は不要とします。
extra member・照会拒否・不完全結果は sticky unknown とし、再照会や別APIで
救済しません。過去の permission error や unknown は消しません。
この確認は観測時点の group と、同世代の子回収・owned I/O 解放に限られます。
group を離れた子孫や将来の再加入の永久停止は保証しません。単独 OS probe
の成功と、本体 runtime の実証は別です。この本体変更は実機未実行です。


## Explicit terminal cleanup unknown (pilot option 2)

The foreground CLI now has a local-only `TerminalDisposition`. After cleanup
fails it revokes the local issuer and attempts bounded transport shutdown before
any optional child disposal. It emits exactly `TERMINAL_CLEANUP_UNKNOWN`, warns
that related processes may remain, and exits nonzero. This is not confirmation,
not a retry of group observation or signalling, and never reuses the grant.
The registry preserves original process/pipe references and error evidence until
foreground exit; terminal relinquishment is the explicit exception to indefinite
owner retention described above. Prior unknown results are never rewritten.

Only when revocation and transport sealing both succeeded, and the process owner
can acquire its existing ownership locks without waiting, may it issue one
nonblocking wait for its original, known-exited, unreaped child. A missing/lost
anchor, live/unknown child, busy owner, pending wait, or wait error remains unknown.
There is no EINTR retry, PID-only signal, alternate observer, or permission change.
An already admitted operation may finish; terminal state prevents later process
operations and prevents a release from committing clean success. New/late runtime
acquisition is terminalized too. Original uncertain pipe closes are not retried.

The normal confirmed path is unchanged: owned-group anchor-only snapshot under
same-generation ownership, child reap, owned I/O completion and successful resource
release. Escaped descendants and future group rejoin are not permanent-stop
promises. Terminal disposition is explicitly weaker and never reported as normal
completion. Source/fake tests and builds do not constitute a new native trial.

## Pre-KILL anchor snapshot for a fresh stop

A fresh stop whose TERM succeeded may avoid KILL only after independent owner
exit evidence and the same complete anchor-only group snapshot used by natural
cleanup. TERM returning success is NOT evidence that every group member stopped.
The owner retains `sweep` and its generation lock across non-reaping exit
observation, the one-shot group query, and the sole same-generation reap. KILL
attempt history excludes this fast path; previous signal errors are not erased.
A canceled command stays canceled, never becomes a successful natural exit.

Observation outcomes are deliberately separate:
- A normal not-yet-exited result does not perform a group query and retains the
  original authorized KILL path for a live child.
- Exit-observer errors/loss, or an exited child without a known status, are sticky
  unknown; they do not masquerade as a live-child fallback.
- Known exit permits at most one group query. Extra members, incomplete/denied
  queries or missing/changed anchors remain unknown with no further signals.
- Anchor-only permits same-generation reap, but EOF/join and Release must still
  succeed. I/O errors or close failures remain unknown.

Unknown-status handling is intentionally stricter than the previous sweep:
previously that ambiguity reached KILL/reap. A canceled context alone grants no
snapshot authority; genuine held exit evidence after TERM may satisfy the new
path even when the calling context has already been canceled. These are source
contract changes covered by fake tests, not a claim about a new native run.

`killAttempted` is an ever-attempted latch excluding the new fast path, not a
permanent ban on existing explicit cleanup retries. The sweep mutex serializes
concurrent stops. Existing same-owner retry after successful signals but an
unreaped live child is preserved; denied/partial group evidence stays sticky,
terminal disposition forbids reuse, and signal error evidence cannot be cleared
or rescued by the new snapshot path.

### TERM grace before the first group decision

The configured `TermGrace` now runs independently of stdio EOF, starting after
the original TERM attempt and ending when that duration or the original caller
context ends. It is not a second interval started after EOF. Early EOF no longer
advances the first pre-KILL snapshot or the existing live-child KILL fallback.
The owner does not hold its process mutex during this wait; `sweep` still
serializes the original stop. The known natural-exit snapshot path is unchanged.

This corrects decision timing, not the meaning of cleanup evidence. Neither
elapsed grace, context expiry, EOF nor successful TERM proves member absence.
A shorter caller budget may still reach a conservative unknown result. The same
independent exit evidence, one complete anchor-only snapshot, sole same-owner
reap, normal owned I/O completion and successful release remain necessary for
the snapshot path. An attempted negative snapshot remains sticky with no new
query, signal-based rescue or replacement owner.

Virtual-time regressions use only the existing fake OS and in-memory pipes.
They check already-closed and delayed EOF, original cancellation/deadline
including timer coincidence, live-child KILL fallback, immediate natural cleanup
and retained unknown ownership. They do not establish that an observed extra
member was a zombie or that waiting will make every native cleanup succeed.

## 起動前検査中に届いた停止の再確認

`StartShell` は、読取り専用の SIGCHLD・descriptor 検査後、spawn を呼ぶ直前に、
元の command context の取消と同じ local terminal disposition を再確認します。
検査中に届いた Cancel・Revoke・TransportLost・Close、または terminal unknown を
既に観測できる場合は、shell を起動せず `ErrNotStarted` を返します。
context の拒否は既存の固定診断 `context`、terminal disposition は
`ErrTerminalCleanupUnknown` を維持します。新しい timer や権限判定は追加しません。

これは取消と fork の原子化ではありません。直前の確認後に停止が届く場合や、
既に入った spawn が遅れて process を返す場合は残ります。
その process は従来どおり同じ owner が保持し、停止と回収を別々に確認します。
terminal unknown の後着取得も未知のまま保持し、成功へ戻しません。

回帰は公開 Session と実 Mac runtime の組成、および公開 `StartShell` を通し、
host 検査と spawn の OS 境界だけを memory fixture に置き換えます。
両検査中の4種類の停止と terminal disposition で spawn 0件を red→green で確認し、
既存の遅延起動・後着取得の所有回帰を維持します。実 OS の fork 競合、実dot、
GUI、常駐版・単発版の実接続受入を完了したとは扱いません。

