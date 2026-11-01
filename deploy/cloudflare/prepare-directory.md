# Tether: `C:\Tether` 管理者準備script（source only・Windows fallback）

[配置準備へ戻る](README.md) / [Linux container配置候補（優先）](linux-container.md) / 実演準備・操作承認票 3a（過去の参照資料・本文非同梱） / [保護loader](../../docs/agent-key-loader.md)

**状態：sourceのみ。作成者はこのscriptを実行しておらず、Windows上のparse・`Add-Type` compile・native動作はいずれも未検証です。このscriptに対する自動試験・静的解析・parse検査はrepository内に存在せず、実施していません。** 現在の優先案は[Linux container](linux-container.md)で、このscriptはその代替として未実行のまま保持するfallbackです。sourceの作成は承認済みですが、**実行は承認されていません**。このファイルと[`prepare-directory.ps1`](prepare-directory.ps1)の存在は、実行・ACL作成・配置の承認ではありません。

## 1. このscriptがすること・しないこと

管理者が手動で、**新しいdirectory `C:\Tether` を1個だけ**、最初から保護されたsecurity descriptor付きで作成します（`CreateDirectoryW`＋`SECURITY_ATTRIBUTES`。作成後にACLを付け替える方式ではありません）。

| 項目 | 内容 |
| --- | --- |
| 対象 | `C:\Tether` 固定。引数・環境変数から変更できません |
| owner | `BUILTIN\Administrators`（`S-1-5-32-544`） |
| DACL | 保護（`C:\`から何も継承しない）。次の3件だけ、この順、各OI\|CI |
| 1 | SYSTEM（`S-1-5-18`）FullControl `0x001F01FF` |
| 2 | Administrators（`S-1-5-32-544`）FullControl `0x001F01FF` |
| 3 | 指定runtime SID ReadAndExecute `0x001200A9`（作成・書込み・削除・WRITE_DAC・WRITE_OWNERなし） |

しないこと：

- 既存物の採用・修復・ACL変更・削除。`C:\Tether`が何らかの形（file、directory、reparse point等）で存在すれば、開かずに停止します。事前確認後に出現した場合も`ERROR_ALREADY_EXISTS`で停止します。
- **削除全般。この実行が作成を要求したdirectoryも含め、自動rollbackはありません。** 失敗・中断後に`C:\Tether`が残り得る場合は、終了コード3で手動確認を求めます。削除用のaccess（`DELETE`）も要求しません。
- `C:\`・ProgramDataのowner/ACL変更、親directoryの自動作成、自己昇格、ExecutionPolicyの変更・回避、moduleのimport・install。
- 鍵・config・binary・service・Tunnel・credential・accountの作成や配置、process/serviceの操作（停止を含む）、fileの内容読取り、groupのmembership照会。

primary group（SDDLの`G:`）は指定しません。作成者tokenの既定値になり、アクセス権には使われず、scriptもloaderも検査しません。

## 2. 入力と主体の条件（入力は両方とも必須・既定値なし）

| 引数 | 内容 |
| --- | --- |
| `-RuntimeSid` | runtime用の通常account SID。`S-1-5-21-a-b-c-rid`の正規形で、RIDは1000以上 |
| `-ExpectedRuntimeAccount` | そのSIDが**完全一致（大文字小文字を含む）**で変換されるべき`<コンピューター名またはドメイン>\<ユーザー名>`。末尾が`$`の名前（computer account・gMSA）は拒否 |

- 値は非秘密ですが、**本人確認は別途**行い、非公開の実操作票から転記します。このscriptでaccountを探索・列挙しません。実SID・実account名を公開repository・Issue・PRへ書きません。
- scriptは次を確認し、どれかが不一致なら何も作らず停止します：SID文字列の正規形、`NT AUTHORITY`・`BUILTIN`・`NT SERVICE`でないこと、名前末尾が`$`でないこと、SID→名前とその逆変換の完全一致、実行中の主体自身のSIDでないこと、（本実行のみ）`LookupAccountSidW`が`SidTypeUser`を返すこと。
- **実行する主体（operator）**：script実行中のtokenのuser SIDが`S-1-5-21-…`形式のlocal/domain accountで、名前末尾が`$`でないことを確認します（PreviewとExecutionの両方）。SYSTEM・LocalService・NetworkService・service SID・computer account・gMSAでは停止します。Entra ID（`S-1-12-1-…`）のaccountも形式上拒否されます。**操作しているのが人間の管理者であることはscriptでは確認できず、operatorの宣言です。**
- **operatorが別途確認し、scriptは確認しないこと**（予定表示にも明記されます）：
  - runtime accountが通常のuserであり、gMSA以外のservice用domain accountでもないこと（`SidTypeUser`として見えるため区別できません）。
  - runtime accountが`Administrators`・`Backup Operators`の**直接・入れ子いずれのmemberでもない**こと、backup/restore/take-ownership等の特権を持たないこと。scriptは稼働中のaccount・groupを照会しません。

## 3. 実行前の確認（管理者が手動で行う）

1. **scriptの確認**：独立reviewを通過した版を、承認された経路で受け取ります。中身を読み、reviewerが別経路で記録したSHA-256と照合します。

   ```powershell
   Get-FileHash -Algorithm SHA256 -LiteralPath '<承認された保管場所>\prepare-directory.ps1'
   ```

   hashは配布されたbytesそのものに対する値です。scriptはASCIIのみ・LF改行で、repositoryでは`.gitattributes`の`-text`によりcheckout時の改行変換を止めています（ASCIIのみなので、BOMなしでもWindows PowerShell 5.1のcode page解釈に依存しません）。別経路での改行変換や再保存でhashは変わります。保管場所は管理者だけが書き込めるdirectoryにし、照合から実行までの間に差し替えられないようにします。
2. **昇格したWindows PowerShellを手動で開く**：スタートメニューの「Windows PowerShell」を右クリック→「管理者として実行」→UACを確認します。titleが「管理者:」であることを見ます。scriptは`#Requires -PSEdition Desktop`でWindows PowerShell 5.1（Desktop edition）以外では読み込まれません。ISE・PowerShell 7・リモートsession・SSH経由は想定外です（本実行は対話consoleでなければ停止します）。
3. **Previewと本実行は、それぞれ新しいwindowで行います。** 同じsessionで2回目の本実行をするとscriptが停止します。また、前のcommandの`$LASTEXITCODE`が残っていると、後述のとおり中断時に古い値を結果と誤認する危険があります。
4. **ExecutionPolicyで止まった場合は、そこで停止**して、承認済みの署名・配布方法を相談してください。`-ExecutionPolicy Bypass`、`Set-ExecutionPolicy`、`Unblock-File`、内容のcopy & pasteで回避しません。Constrained Language Mode、AppLocker/WDAC、AMSIで`Add-Type`が拒否された場合も同じです。
5. **入力したSID・account名はshell履歴に残り得ます**（PSReadLineの履歴file、transcriptやmodule/script block loggingが有効ならそのlog）。非秘密ですが個人・環境を識別し得る値なので、履歴・logを公開資料へ転記しません。履歴設定の変更やlogの削除はこの手順に含めません。

## 4. Preview（作成なし。ただしofflineではない）

```powershell
& '<承認された保管場所>\prepare-directory.ps1' -RuntimeSid '<実操作票のSID>' -ExpectedRuntimeAccount '<実操作票のaccount>' -Preview
```

- 先頭に`MODE: PREVIEW`、最後に`PREVIEW ONLY`と`Nothing was created.`を表示し、確認語を求めません。
- 行うこと：入力文字列の検査、operator accountの形式確認、SID⇔account名の変換、自sessionが昇格済みかの表示、予定SDDLの自己検査と表示。
- **SID⇔account名の変換はOSのlocal security authority（LSA）への照会で、domain accountではdomain controllerへの通信が起こり得ます。Previewはofflineの操作ではありません。**
- 行わないこと：`Add-Type`によるhelperのcompile（compile時に`%TEMP%`へ一時fileを書くため）、`C:\`・`C:\Tether`を開くこと、対象の存在確認、`SidTypeUser`の確認、group membershipの確認。**Previewの成功は、本実行の成功や`C:\Tether`の不存在を意味しません。**
- `-WhatIf`は受け付けません（引数エラーで何も実行されません）。PowerShell標準のWhatIfと混同しないための設計です。

表示された対象path・owner・3件のentry・SID・account名・SDDL・operatorの宣言事項を、実操作票と1つずつ照合してください。

## 5. 本実行（別途承認された後だけ）

Previewの内容を含む実操作票が**別途承認されてから**、hashを再照合し、**新しい**昇格windowで`-Preview`を外して実行します。

```powershell
& '<承認された保管場所>\prepare-directory.ps1' -RuntimeSid '<実操作票のSID>' -ExpectedRuntimeAccount '<実操作票のaccount>'
$LASTEXITCODE
```

1. 入力・operator・変換・昇格・対話consoleを確認し、予定を表示します。非昇格なら停止します。
2. `CREATE C:\Tether`の**完全一致入力**を求めます。それ以外（空Enterを含む）は停止します。確認を省略する引数はありません。
3. ここで初めて`Add-Type`で小さなC#（Win32 P/Invoke）をcompileします。
4. `C:\`をno-followで開いて保持し、directory・非reparse・final path `\\?\C:\`・volume GUID path・NTFS/ReFS・fixed driveを、保持handleから確認します。`C:\`のowner/DACLは検査しません（§7）。
5. `GetFileAttributesW`で不存在（`ERROR_FILE_NOT_FOUND`）を予備確認します。これは予備確認にすぎず、最終判断は次の`CreateDirectoryW`です。
6. `CreateDirectoryW`の**呼出し前に作成状態を「不明」にし**、戻り値を記録してから「作成」または「未作成」にします。呼出しと記録の間で中断されても「未作成」とは報告しません。失敗（`ERROR_ALREADY_EXISTS`を含む）なら既存物に触れず停止します。
7. 作成直後、同じpathをno-follow・`FILE_READ_ATTRIBUTES|READ_CONTROL`（`DELETE`なし）・share-deleteなしで開いて保持します。
8. 同じ保持handleで、次を**すべて**確認します：directory・非reparse・final path `\\?\C:\Tether`の完全一致・`C:\`保持handleと同じvolume（serialとGUID path）・作成時刻がこの実行の時間枠内、owner が`BUILTIN\Administrators`、DACLが保護され予定の3件（型・flag・mask・SID・順序）と完全一致、最後にfile ID等が変化していないこと。
9. 各handleを1回だけcloseします（失敗しても再試行しません）。

| 終了コード | 表示 | 意味 |
| --- | --- | --- |
| 0 | `RESULT: SUCCESS` | `CreateDirectoryW`が成功し、保持handleで§5-8の全検査に合格し、全handleのcloseに成功。volume serialとfile IDはlocal記録用で、公開しません |
| 1 | `RESULT: STOPPED` | `CreateDirectoryW`を呼ぶ前に停止したか、呼出しが失敗を返した。この実行は`C:\Tether`を作成しておらず、owner/ACLも変更していない（Previewの入力エラーも1） |
| 3 | `RESULT: MANUAL INSPECTION REQUIRED` | 作成状態が不明、作成後の検査不合格、close失敗のいずれか。**何も削除していません。** `C:\Tether`は残り得て、置換・改変された物の可能性もあります。削除・修復・owner取得・ACL変更・使用をせず報告してください |

終了コード2は使いません（以前の自動rollbackは削除しました）。

失敗時は`PRIMARY ERROR`（最初の原因）と`CLEANUP UNKNOWN`（close失敗）を分けて表示します。close失敗で最初の原因を上書きしません。closeに失敗した場合、検査が通っていても成功とは表示しません。

**`RESULT:`行が表示されなかった場合は、`$LASTEXITCODE`の値にかかわらず「結果不明」とし、終了コード3と同じく`C:\Tether`が存在し得るものとして扱います。** Ctrl+Cなどで停止がPowerShellに伝わると、`exit`に到達せず`$LASTEXITCODE`が前のcommandの値のまま残ることがあります（0の可能性もあります）。scriptはCtrl+Cを抑止しません。`RESULT:`と終了値が矛盾した場合も結果不明として扱います。

## 6. 失敗時の扱いと限界（自動rollbackなし）

- **自動削除はしません。** 作成後の検査に1つでも不合格なら、`C:\Tether`をそのまま残し、終了コード3で報告します。pathによる削除も、保持handleによる削除もしません。撤去が必要なら、別の承認票で対象・同一性・空であることを確認してから行います。
- **作成状態が不明な場合**（`CreateDirectoryW`の呼出しから結果記録までの中断、native呼出しの例外）は、作成された可能性があるものとして終了コード3にします。
- **置換された物を自分の物とみなさない**：保持handleのownerが`BUILTIN\Administrators`でない、またはDACLが予定と完全一致しない場合は、他者の物の可能性があるとして触れずに報告します。ただし、owner・DACLが一致しても**この実行が作成した物だとは証明されません**。path・作成時刻・volumeの一致も補助的な証拠にすぎません（作成時刻は書込み権を持つ主体が設定でき、下記のname tunnelingでも古い値になり得ます）。
- **信頼された主体による置換は防げません**：`CreateDirectoryW`の成功から保持handleを開くまでの間に、Administrators・SYSTEM・後述の特権保持者・TrustedInstaller・`C:\`のowner等は、同じowner・同じDACLのdirectoryへ置き換えられます。このscriptはそれらの主体に対して何も保証しません（§7）。
- **name tunneling**：同じ名前の`C:\Tether`が直前に削除・改名されていた場合、OSのname tunnelingにより新しいdirectoryへ古い作成時刻が引き継がれ、作成時刻の検査で不合格になり得ます。その場合も保守的に拒否し、終了コード3（残存あり・削除なし）になります。直後に再実行しても、残った`C:\Tether`のため停止します。
- **保持handleはrename/deleteを遮断しません**：属性とsecurity descriptorだけのaccessではshare-deleteの排他を保証できません。削除権を持つ主体による変更は前後のpath/identity検査で観測できた範囲だけ検出します。**scriptも手順も、processを停止しません。**
- **成功は後の空状態を保証しません**：終了コード0は、検査時点で上記条件を満たしたことだけを示します。close後は、書込み権を持つ信頼された主体が中身を追加・変更し得ます。後続の配置前に、別工程で同一性・owner/DACL・空であることを改めて確認します。
- 電源断・process強制終了ではcloseの報告も`RESULT:`行も出ません。Ctrl+Cでは`finally`内でcloseと報告を行う想定ですが未検証で、報告自体が中断されることもあります（§5の「結果不明」）。
- 成功後の`C:\Tether`を取り消す機能はありません。

## 7. 信頼の前提と脅威境界

- review済みのscript bytes（hash照合）、Windows PowerShell 5.1・.NET Framework・C# compiler・OS自体は改ざんされていない。
- **次の主体は信頼境界内で、何をされても防げません**：昇格した管理者、SYSTEM、TrustedInstaller、`C:\`のowner、backup・restore・take-ownership等の特権（`SeBackupPrivilege`・`SeRestorePrivilege`・`SeTakeOwnershipPrivilege`など）を持つ主体、`Backup Operators`のmember。runtime accountがこれらに当たらないことは§2のoperator確認に依存します。
- **`C:\`のsecurity descriptorへの信頼は、このscriptでは確立していません。** scriptは`C:\`のowner/DACLを読まず、誰が`C:\Tether`を削除・改名・置換し得るかを検査しません。pathと作成時刻の一致はこの信頼の根拠になりません。
- SIDとaccount名の対応は、OSのLSA（domain accountではdomain controller）の応答を信頼する。入力値そのものは別途の本人確認で正しいとする。
- 表示されるSID・account名・file IDは非秘密だが個人・環境を識別し得るため、公開資料へ転記しない。

## 8. 未検証で、実機確認が必要な事項

このscriptはparse・compile・実行のいずれも行っていません。repository内に自動試験はなく、coordinatorのsource検査や独立reviewでも次は代替できません。承認されたWindows実機で初めて確認できるものです。いずれかが想定と違えば、scriptは停止または終了コード3になる設計です（fail closed。ただし設計どおりに動くこと自体も未検証です）。

1. Windows PowerShell 5.1でのparse、`#Requires -PSEdition Desktop`の扱い、`Add-Type`のcompile（警告がerror扱いになる環境を含む）。
2. 昇格tokenで、`O:BA`を指定した`CreateDirectoryW`が成功し、DACLが3件・flag `OI|CI`のみ・maskそのままで保存されること（OSによるACEの分割・追加がないこと）。違えば残存した`C:\Tether`が終了コード3で報告されます。
3. `GetKernelObjectSecurity`・`FileIdInfo`・`GetFinalPathNameByHandleW`（DOS/GUIDの両形式）・`GetVolumeInformationByHandleW`・`GetDriveTypeW`（volume GUID path指定）が、`FILE_READ_ATTRIBUTES`等の要求権限で期待どおり動くこと。final pathが`\\?\C:\Tether`と完全一致すること。
4. `WindowsPrincipal.IsInRole(Administrator)`が非昇格の管理者tokenでfalseになること、`WindowsIdentity.Name`・user SIDが想定どおりの形式であること、`LookupAccountSidW`がlocal/domainの通常accountで`SidTypeUser`を返すこと。
5. Ctrl+C時に`finally`内のclose・報告がどこまで行われるか、`$LASTEXITCODE`がどうなるか。
6. 対象hostのExecutionPolicy・言語mode・AppLocker/WDAC・AMSIの下で、回避なしに実行できるか。

作成後の`C:\Tether`が保護loaderに受理されるかは、`C:\`を含む全祖先の条件（[loader](../../docs/agent-key-loader.md)のOS volume root例外など）に依存し、別途確認が必要です。

## 9. 後続の工程（すべて別承認）

- 鍵・config・binaryの`C:\Tether`配下への配置は、管理者による別の配置工程です。runtime accountには読取り/実行しか与えていないため、runtimeからは書き込めません。配置前に同一性・owner/DACL・空であることを改めて確認します。
- 配置後、**非昇格のruntime account**で保護loaderの受理と同一性・closeを確認します。拒否された場合に、検査の緩和・権限の追加・別credential・管理者でのruntime起動へ切り替えません。
- service化、Tunnel設定、firewall、account作成はこの準備にも後続の通常配置承認にも含まれません。
