# agent鍵の現在利用者loader（source only）

[遠隔MCP gateway](agent-gateway.md) / [agent専用channel](agent-channel.md) / [安全要件](security.md)

`backend/internal/agentkeys`は、運用者が事前に配置した既存のOpenSSH Ed25519秘密鍵1件を、現在のaccountで読み取り専用に開き、`ssh.Signer`として返すsourceです。
鍵の生成・書込み・移動・権限変更・探索はしません。key bytes、path、identityをlogへ出しません。
2026-10-31にdirectory再検査・`ReadTrustConfig`期限・上付き数字の予約名の修正後sourceで、fake試験normal/race各32件＋183subtest、vet、Darwin（cgo有効）/Windowsのamd64・arm64 test compileが成功しました。独立差分reviewはmaterialなsource欠陥なし。実鍵・実OSの動作は未検証で、compileを実保護の証明とは扱いません。
「OS volume rootのTrustedInstaller所有」（下記）の追加後も、keyloaderと両CLIの3 packageでnormal/race、Darwin arm64のvet、39 production packageのDarwin/Windows amd64・arm64 GoCI buildが成功しました。独立reviewは重大source欠陥なしで、証拠の説明を限定修正しています。最終snapshot・件数・追加test compile結果はローカル引継ぎを正本とします。
既存の`internal/protectedkey`（service SIDと固定volume profileが前提）は変更していません。

## 呼出し契約

```go
signer, err := agentkeys.LoadCurrentUser(ctx, agentkeys.Config{Path: path, Fingerprint: fingerprint})
```

- `Path`は既存の鍵fileの正規の絶対path（Linux `/home/<name>/...`、Darwin `/Users/<name>/...`、Windows `C:\Users\<name>\...`）。各要素をlinkを辿らずに開くため、symlink・junctionを含まない実pathにします。root直下の鍵は受け付けません。
- 大文字小文字：Windowsは`GetFinalPathNameByHandle`との完全一致を要求するため、disk上の表記と一致しないpathは拒否します。Darwinはdisk上の表記を検査しません（大文字小文字を区別しないvolumeでは表記違いのpathでも同じobjectを開き、受理します）。同一性は表記ではなく`(st_dev, st_ino)`と名前の束縛で確認します。
- Windowsの予約device名（`CON`、`PRN`、`AUX`、`NUL`、`CONIN$`、`CONOUT$`、`COM0`〜`COM9`・`LPT0`〜`LPT9`と上付きの`COM¹²³`・`LPT¹²³`、拡張子付きを含む）の要素は、何も開く前に`ErrConfig`です。
- `Fingerprint`は、鍵とは別の経路で記録した公開鍵の`SHA256:...`（`ssh.FingerprintSHA256`）です。file全体のhashではありません。
- 両値は、process所有者のローカルtrusted compositionが決めます。遠隔command、MCP tool引数、HTTP header、dispatch、環境変数、network経由の設定、home既定位置の探索から取りません。
- `ctx`は期限が必須です。native I/Oは同期のため、期限は各段階の間で確認するだけで、停止したsystem callは中断しません。呼出しは完了までblockし、内部goroutineを放置しません。
- 1つの鍵につき起動時に1回呼び、返った`ssh.Signer`をprocessの寿命の間保持します。要求ごとに再読込みしません。

| error | 意味 | 呼出し側の扱い |
| --- | --- | --- |
| `ErrConfig` | ctx・path・fingerprintが不正。何も開いていない | 構成を直す |
| `ErrUnsupported` | このbuildにnativeの実効ACL検査がない（cgoなしのDarwin、Android、その他未対応OS） | 起動しない |
| `ErrDenied` | 主体・保護・同一性・鍵形式のいずれかが不合格 | 起動しない。別path・別権限へfallbackしない |
| `ErrCleanup` | 保持handleのclose成功を確認できなかった。signerは返さない | 再試行の反復をせず、processを終了する |

### `maclaunch.Config.HostKey`（source caller接続済み）

受け手のMacで、受け手自身が起動したtrusted compositionが、配置済みhost鍵のpathとfingerprintを固定値として持ち、`maclaunch.Prepare`の前に`LoadCurrentUser`で読んで`HostKey`へ渡します。
`maclaunch`と同じく非rootの現在利用者だけが対象です（rootとset-id processは`ErrDenied`）。
cmd/rescue-mac-agentが保護設定文書の明示path/fingerprintを読み、同じsignerをPrepareとWSS Clientへ渡します。実鍵enrollment/配置は未承認です。

### `agentgateway.Principal.Signer`（source caller接続済み）

対応OS上のgateway compositionが、4人それぞれについて別の`Config`で1回ずつ呼び、結果を`Principal.Signer`へ入れます。
失敗したprincipalにはsignerを入れず（`Connect`が拒否します）、他principalの鍵へ代替しません。
cmd/rescue-gatewayの保護CLI envelopeがmemberごとのpath/fingerprintを供給します。秘密鍵bytesは設定に含めず、全memberの鍵検査が成功しなければ起動しません。

## 検査の順序

1. 固定したOS threadで、現在の主体を確認します（下表）。
2. rootから鍵まで順に開き、開いたhandleそのものを検査します。前要素の検査に合格するまで次を開きません（前要素のhandleは保持したままです）。Darwinは保持中の親fdからの相対`openat`です。Windowsは親handleからの相対openではなく、各要素を絶対pathで`CreateFile`し、開いたhandleのfinal pathとvolume・file IDを検査して束縛します。
3. 各要素の名前が、同じobjectへ解決されることを確認します（Darwinは親fdからの`fstatat`、Windowsは親の絶対pathに名前を連結した別handleのfile ID）。
4. 全handleを再検査して（同じ名前の束縛・下記の安定性）、鍵を最大16KiB+1 byteまで読み、検査時のsizeと一致させます。
5. 全handleを同じ条件で再検査し、逆順に1回ずつcloseし、主体を再確認します。
6. その後で鍵を解析し、fingerprintと署名の往復を確認してから`ssh.Signer`を返します。

| 検査 | Darwin（cgo） | Windows |
| --- | --- | --- |
| 主体 | `getuid==geteuid`、`getgid==getegid`、uid≠0 | threadにimpersonation tokenがない（`ERROR_NO_TOKEN`のみ受理）、process tokenが非昇格、userが`S-1-5-21-…`のaccount、Administratorsはdeny-onlyのみ |
| 開き方 | `open("/")`→`openat(親fd, 名前, O_NOFOLLOW\|O_CLOEXEC)`、dirは`O_DIRECTORY`、鍵は`O_NONBLOCK` | 各要素の絶対pathを`CreateFile(OPEN_REPARSE_POINT\|BACKUP_SEMANTICS)`（親handleからの相対openではない）、鍵は`FILE_READ_DATA`かつshare-readのみ |
| 同一性・束縛 | `(st_dev, st_ino)`、`fstatat(親fd, 名前, AT_SYMLINK_NOFOLLOW)`。disk上の大文字小文字は検査しない | volume serial + 128bit file ID、`GetFinalPathNameByHandle`の完全一致（大文字小文字を含む）、親の絶対path+名前で開いた別handleのfile ID、全要素が同じvolume |
| volume | `MNT_LOCAL`、`MNT_IGNORE_OWNERSHIP`なし、apfs/hfs | fixed drive、NTFS/ReFS |
| 所有者 | 現在のuidまたはroot | 現在のuser、SYSTEM、BUILTIN\Administrators。例外としてOS volume rootと実証したdrive rootだけ`NT SERVICE\TrustedInstaller`（下記） |
| 保護 | group/otherのmode bit、拡張ACL（`acl_get_fd_np`） | DACLの全ACE（NULL DACLは拒否） |
| 安定性（鍵file） | 全項目の完全一致：同一性・種別・uid・保護（mode由来のgrant）・拡張ACLの有無・nlink・size・flags・mtime・ctime | 全項目の完全一致：同一性・種別・属性・link数・size・作成/更新時刻・owner・DACL由来の実効allow grant・final path |
| 安定性（directory） | 鍵fileと同じ。ただしmtime・ctime・size・nlinkは比較しない | 鍵fileと同じ。ただし更新時刻・size・link数は比較しない（作成時刻は比較する） |

directoryのsize・link数・更新系の時刻は、無関係な子の作成・削除だけで変わるため、再検査の比較から外します（以前は全項目の一致を求め、安全な鍵の読込みも拒否していました）。directoryの同一性・名前の束縛・final path・owner・grant・ACLの有無・属性/flags・種別は引き続き完全一致を要求し、置換・owner変更（信頼主体同士の変更を含む）・実効allow grantに影響するACL変更は拒否します。DENYやinherit-only ACEの差分そのものは比較せず、安全性をDENYに依存させません。
DarwinのdirectoryはmodeそのものではなくPOSIX mode由来のgrantを比較するため、grantに影響しないmode bit（読取り・検索）の変化は検出しません。directoryのmode・ACLを変更できるのは所有者（信頼主体）かroot、または`rightControl`を持つ主体（既に拒否済み）だけです。

保護の判定（信頼しない主体への実効allowに対して）:

| 対象 | 拒否する権利 |
| --- | --- |
| 鍵file | 読取り・実行を含む全て。Darwinは拡張ACLの存在自体（deny-onlyでも）、setuid/setgid/sticky、nlink≠1、0 byte、16KiB超、通常file以外 |
| 鍵の親directory | 子の作成、変更、削除・rename、owner/ACL変更 |
| それより上のdirectory | 変更、削除・rename、owner/ACL変更（新しい名前の作成だけは許容。既存要素は置換できないため。既定の`C:\`の「Authenticated Users: フォルダー作成」を想定） |

- DENYで危険なALLOWを相殺して受理しません。inherit-onlyのACEはその要素では実効でないため無視します。`OWNER RIGHTS`は（検査済みの）ownerとして扱います。
- object/callback/条件付きACE、未知のflag・mask bit、`ACCESS_SYSTEM_SECURITY`、`MAXIMUM_ALLOWED`は分類不能として拒否します。

### OS volume rootのTrustedInstaller所有（Windows、source only）

既定installではOS driveのroot（例：`C:\`）のownerが`NT SERVICE\TrustedInstaller`（`S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464`）の場合があり、保護された専用directoryへ到達する前に拒否していました。
Microsoftの資料は、OS資源をTrustedInstallerが所有することを説明しています（[Security identifiers](https://learn.microsoft.com/en-us/windows-server/identity/ad-ds/manage/understand-security-identifiers)、[About Windows File Protection](https://learn.microsoft.com/en-us/windows/win32/wfp/about-windows-file-protection)）。
drive ownerの変更、任意祖先の緩和、service SID全般の信頼、`account.control`への追加はしません。次の全条件を満たすnodeだけ、このownerを受理します（`osRootOwner`）。

1. 構成pathのroot node（`X:\`、名前なし、ancestor）であること。親・鍵・root以外の祖先には適用しません。
2. ownerがこのSIDと文字列で完全一致すること（名前、似たSID、0詰め、`S-1-5-80-0`、他のservice SID、LocalServiceは不可）。
3. 保持中のroot handleがOS volumeのrootであるとnativeに実証できたこと（`state.osRoot`、`osVolumeRoot`）。証拠は次の通りで、どれかが取れない・一致しない場合は不受理です。
   - OSの位置：`GetSystemWindowsDirectory`の戻り値。呼出し側、環境変数、設定、pathの末尾`\`からは決めません。
   - そのdirectoryを別のno-follow・属性読取りだけのhandleで開き、directoryでreparse pointでないこと、DOS final path（`\\?\X:\<name>`）、volume GUID final path、FileIdInfoのvolume serialを読みます（handleは直ちにclose、失敗は`ErrCleanup`）。
   - 保持中のroot handle自身のDOS final pathが`\\?\X:\`、volume GUID final pathが`\\?\Volume{GUID}\`の形であること。
   - OS directoryのDOS final pathがroot handleの`X:\`+名前、GUID final pathがroot handleのGUID root+同じ名前、volume serialが同一、APIの戻り値が`X:\`+名前（大文字小文字を区別しない比較）であること。
4. この証拠はrootのownerがTrustedInstallerのときだけ集め、同じ保持handleの各inspect（初回・読取り前・読取り後の再検査）で毎回取り直します。`osRoot`は保持handleの同一性・final pathに結びつけて再評価し、その真偽・owner（TrustedInstaller→SYSTEMを含む）・root同一性の変化を拒否します。root側だけ又はOS directory側だけのGUID不一致も拒否します。同じ保持root/volumeを再証明できる両側一貫したGUID変更は受理し、GUID文字列自体の不変を保証しません。

受理するのは所有だけで、ACLの信頼は変えません。TrustedInstallerへのallow ACE（`OWNER RIGHTS`経由を含む）は引き続き信頼しない主体のgrantとして扱い、rootでも変更・削除/rename・owner/ACL変更の権利があれば拒否します。root以外のnodeへの継承的な信頼もありません。
ownerは暗黙に`READ_CONTROL`と`WRITE_DAC`を持つため、この受理はTrustedInstallerがroot DACLを書き換えうることを受け入れる意味です。TrustedInstallerはSYSTEMが実行するOS fileを所有・置換できる主体で、信頼済みのSYSTEM・Administratorsを超える能力ではないため、このOS rootに限って受け入れます。読込み中のDACL・owner変更は再検査で拒否します。他のWindows service、LocalService、任意のservice SID、他のuserは引き続き信頼しません。
reparse・remote・非NTFS/ReFS・非fixed driveの拒否、祖先・親・鍵のACL検査、名前の束縛、全handleの逆順closeは変更していません。
- POSIXのdirectory書込み権はstickyでも作成+削除・renameとして扱います（保守側）。groupはwheel・admin・staffを含めて信頼しません。
- Darwinのdirectoryは、全entryが`ACL_EXTENDED_DENY`の拡張ACL（既定のhomeにある「everyone deny delete」等）だけを許容します。allow entryは内容を解析せず、信頼しない主体への全権として拒否します。

## 信頼境界

- 信頼する主体：現在のaccountと、OSの制御主体（Linux/Darwinのroot、WindowsのSYSTEMとBUILTIN\Administrators）だけです。
- 信頼しない主体：他のuser、全group（Users、Authenticated Users、Everyone、staff、admin等）、LocalService/NetworkService、service SID、`NT SERVICE\TrustedInstaller`、CIの別account。TrustedInstallerは、実証したOS volume rootのownerとしてだけ受理し（上記）、grantの主体としては信頼しません。
- 守る対象：他の非管理者accountによる鍵の読取り、置換、保護変更。
- 守らない対象：同じaccountで動く別process（鍵を読めます）、root/Administrators/SYSTEM、kernel、backup・swap・core dump、検証前から存在したhandleや複製、Goのmemory上の複製。Goのmemory消去は最善努力で、全複製の消去を保証しません（下記「回収の保証」）。
- 現在のACLは過去のアクセス履歴の証明ではありません。

## 回収の保証（source上）

- 開いたhandleは成功・失敗のどちらでも逆順に1回だけcloseします。close失敗は再試行せず（Darwinではfd番号が再利用され得るため）、`ErrCleanup`を返します。
- Windowsの名前解決用handleと、OS directoryの証拠取得handleのclose失敗も`ErrCleanup`です（`inspect`の`ErrCleanup`は`ErrDenied`へ変換しません）。
- 鍵の消去は最善努力です。この package が確保・参照できるslice（読取りbuffer、復号したPEM block、envelope解析中の内部秘密鍵slice、検算用の導出鍵、不合格時の`ssh.ParseRawPrivateKey`の戻り値）だけを0で上書きします。読取りbufferは失敗時に、成功時は解析後に消去します。
- `ssh.ParseRawPrivateKey`は入力bytesを自分で再解析するため、上記とは別の、このpackageが管理しない秘密鍵の複製（PEM decodeの結果等）を作り得ます。これらは参照できず、消去しません（library内部はこの担当の確認範囲外で、source照合もしていません）。成功時に返す`ssh.Signer`の秘密鍵も、process寿命の間memoryに残ります。Go runtimeによるcopy・GC前の残存も消去の対象外です。
- 主体の確認に失敗したthreadは、lockしたままgoroutineを終了させ、runtimeに破棄させます（他の処理へ再利用させません）。

## 試験の範囲（fake と自己所有file fixture）

agentkeys packageのunit試験はmemory上のfake kernel（handleはobjectへ束縛、名前は別に差し替え可能）と、公開されたRFC 8032 TEST 1の合成test vectorだけを使います。
実file、実鍵、account列挙、native API、ACL変更、listener、networkは使いません。

- 正常：Darwin相当とWindows相当の既定layoutで同じaccountの鍵を読み、固定fingerprintと署名を確認。全handleの逆順close、buffer消去
- TrustedInstaller所有のOS volume root（`osroot_test.go`、source/fake）：既定layoutで鍵と`ReadTrustConfig`の両方を受理。OSがD:にある証拠ではD:を受理。拒否側は、OS証拠なし、似たSID・0詰め・余分なsub-authority・`S-1-5-80-0`・他のservice SID・LocalService・名前だけ・他user、OS directoryの別volume GUID・別serial・別drive・APIとfinal pathの不一致・UNC・reparse・file・root自身、root handleの別GUID・GUIDなし・root以外のGUID path・不正GUID、root以外のfinal path、reparse・removable、TrustedInstaller/`OWNER RIGHTS`のfull control ACE、他主体の削除・WRITE_DAC・WRITE_OWNER、同じownerの非root祖先・profile・親・鍵、OS volumeでないD:（C:の証拠のままを含む）。鍵と`ReadTrustConfig`の両方で読取り前に拒否。読取り前・後のowner変更・証拠消失・片側GUID不一致・root同一性・ACE追加を拒否し、更新時刻だけの変化と同じroot/volumeを再証明する両側GUID変更は受理。証拠取得handleのclose失敗は`ErrCleanup`
- 不正な所有者（他user、LocalService、OS root証拠のないTrustedInstaller所有のdrive root）と保護（group/other読取り、拡張ACL、allow ACL、setuid、symlink/reparse、FIFO、hard link、非local volume、親の作成権、祖先の削除・DACL変更、未知ACE・mask、NULL DACL、別volume、final pathの不一致）を、読取り前に拒否
- 親の置換・鍵の差替え（読取り前と後）、読取り中のctime・owner・link数・flags・ACL順序・sizeの変化
- 読取り中のdirectory変化：Darwin homeのowner・flags・deny-only ACLの消失、Windows `C:\`のowner（SYSTEM→Administrators）・作成時刻・属性・信頼主体ACEの追加を拒否。Darwin home・鍵の親のmtime/ctime・size・nlink、Windows `C:\`の更新時刻と鍵の親の更新時刻・link数だけの変化は受理
- `ReadTrustConfig`：mode 0644、world writable、Darwinの拡張ACL（deny-onlyを含む）、WindowsのUsers読取りACE・Everyone書込みACEを読取り前に拒否。読取り後・鍵のclose中・最後のclose後の期限切れで、nilを返しbufferを消去
- 主体の変化（入口・出口）、部分読取り・過大申告・無進捗・早期EOF・size変化、各handleのclose失敗、名前解決handleのclose失敗、途中のopen失敗
- 鍵形式：前後の余分なtext・改行・空白、複数PEM block、CRLF、PEM header、暗号化・KDF、2鍵、outer/inner公開鍵とseedの不一致、check値、padding、非Ed25519、fingerprint不一致、期限切れ
- path：両形式の正規化、`..`、空要素、制御文字、Windowsの末尾`.`/空白・ADS・予約device名（上付き`COM¹²³`・`LPT¹²³`を含み、kernelを作る前に拒否）

上記のagentkeys unit試験はDarwin/Windowsの実native APIを呼びません。共通mapping（`posixState`、`windowsState`、`windowsGrants`）をfake検証します。Linuxは`native_linux.go`をin-memory syscall tableで実行し、実procfs/file APIを呼ばずに実装経路を検証します。
Darwin・Windowsのcompile成功も、ACL・identityの実保護の証明ではありません。

## 実機で未確認の事実（compileやfake試験で代替できないもの）

1. Darwin：`acl_get_fd_np(fd, ACL_TYPE_EXTENDED)`がACLのないobjectで`NULL`と`errno==ENOENT`を返すこと。違えば全鍵を拒否します（fail closed）。
2. Darwin：`acl_get_entry`が成功で0、終端で-1と`EINVAL`を返すこと。違えば拒否します。
3. Darwin：既定の`/`、`/Users`、homeのACLがdeny-onlyか無いこと。allow entryがあれば拒否します。`pthread_setugid_np`によるthread単位の資格が`getuid`へ反映されること。
4. Windows：drive rootのownerが`NT SERVICE\TrustedInstaller`の場合、OS volume rootの証拠（上記）が揃ったときだけ受理します。実機で未確認：`GetSystemWindowsDirectory`が環境変数・呼出し側に影響されずboot時のOS directoryを返すこと（containerやterminal serverを含む）、root directory handleに`GetFinalPathNameByHandle(VOLUME_NAME_GUID)`が`\\?\Volume{GUID}\`を返すこと、OS directoryのDOS final pathの名前と戻り値の大文字小文字以外の一致、FileIdInfoのvolume serialが同じvolume内で一致すること。どれかが違えば拒否します（fail closed）。既定rootのDACLにTrustedInstallerや`OWNER RIGHTS`の変更系ACEがあれば、この変更後も拒否します。`windows.GetSystemWindowsDirectory`の宣言はWindows compileで確認します。
5. Windows：CI accountが昇格済み、またはAdministratorsがdeny-onlyでない場合は拒否します。現在のCI accountの状態は確認していません。
6. Windows：祖先directoryのhandleはdata accessを持たないためrenameを妨げず、置換は再検査でのみ検出します。Darwinも同様です。鍵のhandleはWindowsでのみ書込み・削除の同時実行を妨げます。
7. Darwin・Windowsで、無関係な子の作成・削除が変えるdirectoryの項目が、比較から外したもの（Darwinのmtime/ctime/size/nlink、Windowsの更新時刻/size/link数）だけであること。属性・flagsなど他の項目も変わる場合は、安全な鍵も拒否します（fail closed）。
8. cgoなしのDarwin build、Android、その他未対応OSは`ErrUnsupported`です。Linuxはpure Go native候補を実装しましたが、[固有の検査条件と未実証事項](agent-key-loader-linux.md)を満たす実環境の確認は未実施です。Darwinのcgo buildはlibSystemのheaderとC compilerを要します（既存CIは`CGO_ENABLED=1`）。

## source統合と残る検証

GoCIには共通7fileとOS別native1fileを登録済み。両CLIと保護設定文書の具体callerへ接続しました。Darwin共通fake試験、Linuxの明示TestLinux fake試験、Darwin/Windows buildとLinux/amd64 CGO=0 compileはそれぞれ保存したsnapshotで成功、後続変更は再検証が必要です。実配置・ACL・enrollment・native適合は別承認と検証が必要です。

## 配置前に必要な承認

- 実鍵の作成・配置と、その記録経路（fingerprintの独立記録を含む）。
- 実Macと実Windows CI host上での、上記未確認事実の確認（読取り専用の検査に限っても、実account・実ACLへの操作です）。
- OS volume rootに限るTrustedInstaller所有のsource受理は独立review済みですが、実native loader適合と配置場所の決定は別です。各環境の実機read-only owner/ACL照会にも、その対象と範囲の別承認が必要です。sourceの検証・reviewは実機操作の包括承認ではなく、実owner/ACL変更は未承認です。
- これらの成功は、実relay、遠隔接続、Macでの実command実行、配置全体の成功を示しません。製品gateは閉鎖したままです。


## 公開 trust 設定の保護読み取り

`ReadTrustConfig(ctx, path)` は同じ owner/親ディレクトリ/ACL/同一 handle/close 確認で、明示したローカル設定を最大 16 KiB 読みます。issuer/sub/host pin は公開値でも改変されると認可が変わるため、秘密鍵と同じ保守的な private-file 保護を要求します（group/other への読取り・書込み、Darwin の拡張 ACL、信頼しない主体への Windows の allow ACE はすべて拒否）。全 handle の close と主体の再確認の後にも期限を確認し、期限切れなら buffer を消去して nil と `ErrDenied` を返します。JSON schema の厳格検証は各 CLI が行い、返却 buffer は使用後に消去します。これは公開設定の実内容を assistant に表示する許可ではありません。

Darwin API の source 照合（2026-10-31）: Apple の [acl_file.c](https://github.com/apple-oss-distributions/Libc/blob/main/posix1e/acl_file.c) と [filesec.c](https://github.com/apple-oss-distributions/Libc/blob/main/gen/filesec.c) では、ACL property がない場合に ENOENT を設定する経路を確認しました。[acl_get_entry(3)](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man3/acl_get_entry.3.html) は正常 0、列挙終端の EINVAL を記載しています。運用配置・実enrollment・異常ACLや変更競合を含む実Mac適合は未実行です。

## resident入力の限定file fixture

resident transport の Darwin/cgo 入力試験と、owned pipe の idle component 試験は、checkout 内に試験自身が作る合成 policy と固定 seed の key vector を、この native reader で読みます。
前者は実相互 proof から NewWireRequest まで、後者は接続・ON・work を行わない同じ idle Standby.Close と pipe EOF までの限定試験です。既存 key/policy や運用配置を読まず、ACL/CA/OS trust を変更しません。
全handleの検査・同一性・close条件を維持し、fixtureは削除します。
これらの自己所有 fixture の成功を、実 enrollment、全 ACL ケース、署名済み配布/配置元、実 native 実行の成功に広げません。
運用対象のowner/ACL照会と変更には、上記の対象・範囲を固定した別承認が必要です。
