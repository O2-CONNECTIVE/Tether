# agent鍵loaderのLinux native kernel（source only）

[agent鍵loader（共通契約）](agent-key-loader.md) / [遠隔MCP gateway](agent-gateway.md) / [安全要件](security.md)

`backend/internal/agentkeys/native_linux.go`は、既存のLinux container内で動く最小のgateway（4人・gateway 1つ）が、`LoadCurrentUser`と`ReadTrustConfig`をnativeに呼べるようにするkernelです。
これまでLinuxは`native_other.go`の`ErrUnsupported`でした。共通のpath検証、`readKey`の順序・再検査・期限・close、`checkNode`の方針、鍵形式の検査は変更していません。Linux固有なのは、主体の証拠の取得、開き方、fstat・filesystem・POSIX ACLの事実を共有の`posixState`へ渡すmappingだけです。

状態：source/fake検証段階。Linux/amd64 CGO=0のgatewayとagentkeys test binaryをcross compileし、Linux向けvetも成功。既存WSLの非root userで明示TestLinuxとidentity cleanup回帰の16 top/90 subが成功しました。Darwinの共通16package normal/raceは各346 top/877 sub、vet、Darwin/Windows amd64/arm64 buildも成功。独立reviewのLow cleanup分類不整合は共通境界とLinux exitで修正し、先行失敗と最終成功を分けて保存しました。実kernel・procfs・filesystem・container保護は未実証で、fake成功とは別です。

## build範囲

| GOOS | file | 結果 |
| --- | --- | --- |
| linux（androidを除く） | `native_linux.go` | native kernel |
| android（`linux` build tagも成立する） | `native_other.go` | `ErrUnsupported` |
| darwin（cgo）/ windows | 既存のnative file | 変更なし |
| その他、cgoなしのdarwin | `native_other.go` | `ErrUnsupported` |

cgoは使いません。使うのは標準libraryと、固定済みの`golang.org/x/sys v0.49.0`の`unix`だけで、依存は追加していません。

## 呼出し契約（Linux）

- `Path`は正規の絶対POSIX path（例：`/home/<name>/.rescue/<file>`）。各要素をlinkを辿らずに開くため、symlinkを含まない実pathにします。root直下は`ErrConfig`です（共通）。
- 呼ぶprocessは、非rootの現在利用者として動くgateway自身です。値は保護設定文書と起動したtrusted compositionが決めます。環境変数、遠隔入力、home探索からは取りません。主体の判定にも環境変数（`USER`、`HOME`等）を使いません。
- `ctx`の期限、1回だけ読むこと、error種別（`ErrConfig`/`ErrUnsupported`/`ErrDenied`/`ErrCleanup`）は共通契約と同じです。

## 主体の証拠

固定したOS threadで、次の値を取ります。どれかが取れない・一致しない場合は`ErrDenied`で、そのthreadは破棄されます（共通）。

| 証拠 | API | 要求 |
| --- | --- | --- |
| thread ID・process ID | `unix.Gettid`、`unix.Getpid` | 正の値。入口・出口・各操作で同じ |
| real/effective/saved UID・GID | `unix.Getresuid`、`unix.Getresgid`（呼んだthreadの資格） | 3つが一致。UIDは0でなく、overflowuidとも違う |
| file-system UID・GID | `/proc/<tgid>/task/<tid>/status`の`Uid:`・`Gid:`の4番目 | real/effective/savedと一致 |
| capability | `unix.Capget`（version 3、pid 0＝呼んだthread）のeffective・permitted、statusの`CapPrm:`・`CapEff:`・`CapAmb:` | すべて0 |
| procfsの束縛 | statusの`Tgid:`・`Pid:` | getpid・gettidと一致 |
| 未対応付けownerの表示値 | `/proc/sys/kernel/overflowuid` | 正の値で、現在のUIDと異なる |

- file-system IDを照会だけで読むsystem callはありません。`setfsuid(2)`/`setfsgid(2)`は照会目的でも資格を変更するcallなので呼びません。代わりにthread自身のprocfs statusを読みます。
- capabilityを持たず、real/effective/savedが一致する非root processは、自分の資格を変更できません。multi-threadのprocessは`unshare(CLONE_NEWUSER)`・user namespaceへの`setns`ができません。Goのprocessは常にmulti-threadです。それでも同じprocessの別goroutineが資格を変える経路（全threadへのsetuid系の反映）に備えて、保持fdに対する各`open`の前、各`inspect`・`resolve`・`Read`の前後で、getresuid・getresgid・capget・gettidの値が入口と同じかを確認します。closeの後は、出口で全証拠（procfsを含む）を読み直して比較します。
- statusの解析：`Tgid`、`Pid`、`Uid`、`Gid`、`CapPrm`、`CapEff`、`CapAmb`がそれぞれ1回だけ、kernelの形式（`Key:\t値`、tab区切りの正規10進、小文字16桁の16進）で現れることを要求します。どれかが欠ける・重複する・形式が違う、NULを含む、末尾が改行でない、16KiBを超える場合は拒否します。kernelは`Name:`の改行をescapeするため、process名から行を作れません。
- UIDが2^31以上の場合：`Getresuid`の戻り値がC intのため負になり、拒否されます（fail closed、未確認）。

### procfsの開き方（path置換なし）

1. `openat(AT_FDCWD, "/proc", O_RDONLY|O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC|O_NOCTTY)`。process root直下の1要素なので、置換できるのは`/proc`自身だけです。`fstatfs`の種別が`PROC_SUPER_MAGIC`（0x9fa0）、`fstat`がdirectoryでinode番号が`PROC_ROOT_INO`（1）であることを要求します。
2. 保持した`/proc`のfdで`readlinkat(fd, "thread-self")`を読み（辿らない）、`<getpid>/task/<gettid>`と完全一致することを要求します。これは、このprocfsが呼出し側のpid namespaceのものであり、その名前が自分のthreadを指すことの証拠です。自分のthreadは実行中なのでIDは再利用されません。
3. `<tgid>`、`task`、`<tid>`、`status`を、保持した親fdから`openat(..., O_NOFOLLOW|O_CLOEXEC)`で1要素ずつ開き、それぞれprocfsの種別・`/proc`と同じ`st_dev`・期待した種別（directory/通常file）を要求します。
4. `sys`、`kernel`、`overflowuid`を同様に開きます。`/proc/sys`はcontainerで別のread-only bind mountになることが多いため、ここはprocfsの種別だけを要求します（overflowuidはnamespaceに依存しない全体の値）。
5. 開いたfdはすべて逆順に1回だけcloseします。close失敗は共通の`enter`/`exit`境界でも`ErrCleanup`を維持し、threadを破棄します。signerは返りません。

`/proc`が無い、`hidepid`等で読めない、別のpid namespaceのprocfsである場合は、すべて拒否です。

## 開き方・同一性・束縛

| 項目 | Linux |
| --- | --- |
| root | `openat(AT_FDCWD, "/", O_RDONLY\|O_DIRECTORY\|O_NOFOLLOW\|O_CLOEXEC\|O_NOCTTY)` |
| 下位要素 | 保持した親fdからの`openat(親fd, 名前, 同じflag)`。directoryは`O_DIRECTORY`、鍵は`O_NONBLOCK`（FIFOでblockしない。通常file以外は後で拒否）。名前に`/`は含まれない |
| 状態 | 同じfdの`fstat`、`fstatfs`、`fgetxattr`（path lookupは使わない） |
| 同一性 | `(st_dev, st_ino)` |
| 束縛 | `fstatat(親fd, 名前, AT_SYMLINK_NOFOLLOW\|AT_NO_AUTOMOUNT)`の`(st_dev, st_ino)`が保持fdと一致 |
| 安定性 | 共通と同じ。鍵は同一性・種別・uid・mode由来のgrant・ACLの有無・nlink・size・mtime・ctimeの完全一致。directoryはmtime・ctime・size・nlinkを除く。Linuxには`st_flags`がないためattrsは常に0 |

名前の各要素は`/`を含まないため、`O_NOFOLLOW`で最終要素のsymlinkを拒否すれば途中のsymlinkは存在しません。`openat2`の`RESOLVE_*`は使っていません。

## filesystemの契約

`fstatfs`の`f_type`が次のmagicのとき**だけ**`local`とします。ownerとmode bit、POSIX ACLを、このmount namespaceのkernelが手元で強制するfilesystemです。

| filesystem | magic（`linux/magic.h`） | 受理理由・前提 |
| --- | --- | --- |
| ext4（ext2/ext3もext4 driverで同じmagic） | `EXT4_SUPER_MAGIC` 0xef53 | local block filesystem。owner・mode・POSIX ACLをkernelが強制 |
| tmpfs | `TMPFS_MAGIC` 0x01021994 | memory上のlocal filesystem。ACLはkernelが`CONFIG_TMPFS_POSIX_ACL`付きでbuildされている場合だけ読め、無い場合は`ENOTSUP`で拒否 |
| overlayfs | `OVERLAYFS_SUPER_MAGIC` 0x794c7630 | 一般的なcontainer root。検査するのは現在のUIDから見たmerged viewのowner・mode・ACLだけ |

overlayfsについて：下層・上層のbacking storage、host側の権限、image layerの内容は検証しません。container engine、host、backing storageは信頼する運用者です。overlayの受理は、backingの保護を検証した主張ではありません。backingがnetwork filesystemでも、overlayの種別だけが見えます。

拒否（allowlistに無いため）：NFS、CIFS/SMB/SMB2、9p（WSL2のDrvFSを含む）、FUSE（virtiofs、Docker Desktopのbind mount、rootless Podmanのfuse-overlayfsを含む）、autofs、ramfs、procfs・sysfs等の擬似filesystem、btrfs、xfs、f2fs、不明な種別。btrfs・xfsはlocalでACLも強制しますが、この変更の検証範囲外のため受理しません。追加には別のreviewが要ります。hostのxfsをbind mountしたdirectoryに鍵を置くと拒否されます。

Docker・hostのsocket、資格情報のmountは、この変更では使わず、前提にもしません。鍵や設定の位置としては、通常fileでないもの（socket等）は拒否され、group/otherに読取りを許すsecret mount（例：mode 0444）も拒否されます。

### user namespace・idmapped mountの見え方

- ownerの比較は、現在のuser namespaceで`fstat`が返すUIDだけを使います。trustするのは現在のUIDと、そのnamespaceのUID 0（root）です。rootless containerのUID 0は、そのcontainerをmapした運用者です。
- このnamespaceへmapされていないownerは`overflowuid`（既定65534）に見えます。現在のUIDが`overflowuid`のとき、または`overflowuid`が0のときは主体の検査で拒否します。未mapのownerは、現在のUIDともrootとも一致しないため不合格です。
- idmapped mountでは、`fstat`は（kernelのpermission判定と同じ）mount側のmapping後のUIDを返す想定です（未確認）。

## POSIX ACLの契約

- 各fd（root・祖先・親・鍵）について、同じfdに`fgetxattr(fd, "system.posix_acl_access")`を行い、directoryでは`system.posix_acl_default`も読みます。pathからは読みません。
- `ENODATA`だけを「ACLなし」と扱います。`ENOTSUP`/`EOPNOTSUPP`（ACL未対応・`noacl` mount）、`ERANGE`（128 entry超）、`EINTR`、`EACCES`などは、mode bitへfallbackせず拒否します。mode bitからACLなしを推定しません。
- 読めたACLは厳格に解析します（version 2のheader、little-endianの`{tag u16, perm u16, id u32}`、`USER_OBJ`/`USER`/`GROUP_OBJ`/`GROUP`/`MASK`/`OTHER`だけ、perm 0〜7、1〜128 entry）。解析できない場合は拒否します。
- LinuxのPOSIX ACLにはdeny entryがありません。保存されたACLは全entryをallowとして数え、共通の`posixState`が信頼しない主体への全権のgrantに変換します。その結果、access ACLまたはdefault ACLを持つnodeは、mode相当の最小ACL（3 entry）であっても拒否します。Darwinのdeny-only ACLの受理に相当する形は、Linuxでは発生しません。
- NFSv4 ACL（`system.nfs4_acl`）等の他方式は、filesystemのallowlistで除外されます。

## 保護の判定

共通の`checkNode`と同じです。ownerは現在のUIDかUID 0だけ。groupは（gid 0を含めて）信頼しません。鍵は通常file・nlink 1・1〜16KiB・setuid/setgid/stickyなし・group/otherへの権利なし・ACLなし。親directoryは信頼しない主体への書込み（作成・削除・rename）を許しません。それより上は削除・rename・変更・owner/ACL変更を許しません（POSIXのdirectory書込み権は、stickyでも作成＋削除・renameとして扱うため、`/tmp`のような1777の祖先は拒否）。

## 回収・期限

- 保持fdは成功・失敗とも逆順に1回だけcloseします（共通）。`close(2)`の失敗（`EINTR`を含む）は再試行しません。Linuxはどの結果でもfd番号を解放し、番号は再利用され得るためです。`ErrCleanup`を返し、signerは返しません。
- `openat`と`read`の`EINTR`は各8回まで再試行し、それを超えたら拒否します。`fstat`・`fstatfs`・`fgetxattr`・`readlinkat`の`EINTR`は再試行せず拒否します。
- system callは同期で、期限は各段階の間でだけ確認します（共通）。

## 試験（fake、Linux system call・実OSは未実行）

- `native_linux_test.go`（GOOSがlinuxの場合だけ実行）：実際の`linuxKernel`/`linuxFile`の処理を、memory上の`linuxSys`表で動かします。実file、実procfs、実process、実account、networkは使いません。
  - 正常：overlay rootとtmpfsの鍵directoryで構成した合成container layoutで、鍵の読込みと`ReadTrustConfig`が成功。開く順序（procfs→`/`…鍵→出口のprocfs）、全fdの1回close、全openがread-only・`O_NOFOLLOW`・`O_CLOEXEC`で、`O_CREAT`/`O_TRUNC`/`O_APPEND`/`O_PATH`なし、鍵は`O_NONBLOCK`、directoryは`O_DIRECTORY`。
  - 拒否（読取り前）：最小・名前付きuserのaccess ACL、default ACL、不正なACL、ACLの`ENOTSUP`・`ERANGE`・`EINTR`・`EACCES`、FUSE・NFS・9p・CIFS・SMB2・btrfs・xfs・ramfs・procfs・不明なfilesystem、symlinkの鍵・親、FIFO、group読取り、setuid、hard link、0 byte、16KiB超、他のowner、overflowuid所有、world writable、stickyの1777祖先。
  - 主体（`/`を開く前に拒否）：root、overflowuidで実行、overflowuidが0・不正形式、real/saved UID・saved GID・file-system UID/GIDの不一致、負のUID、effective/permitted/ambient capability、procfsなし・tmpfs・root inodeでない、`thread-self`が別pid namespace・linkでない、task要素のsymlink、statusの別device・procfsでない・`CapAmb`/`Uid`の欠落、procfs fdのclose失敗、capgetの失敗。
  - 各操作の前後：読取り中のeffective UID変更、open後のcapability取得・thread変更、出口でだけ見えるfile-system UID変更。
  - 同一handleの再検査：openとinspectの間の鍵差替え、読取り中の鍵ctime変更・親の置換・親へのACL追加・祖先のfilesystem変更を拒否し、親のmtime/ctimeだけの変化は受理。
  - 中断と回収：`openat`の`EINTR`2回は再試行して成功、8回は拒否、`read`の`EINTR`2回は成功・`EIO`は拒否、鍵closeの`EINTR`は再試行せず`ErrCleanup`、root・親のclose失敗は`ErrCleanup`。
  - 純関数：statusの解析、主体判定、ACLのencoding、filesystemのallowlist、ACL付きdirectoryの共通policyでの拒否。
  - `nativeKernel()`がx/sys/unixの関数に束縛されていること（関数の同一性だけを確認し、呼びません）。
- `linuxfacts_test.go`（全GOOSで実行、Darwinを含む）：Linuxのmapping結果に相当する事実（allowlist外は`local=false`、ACLはentry数をallowとして`aclPresent`）を共通のfake kernelへ与え、container layoutの受理と、ACL・filesystem・sticky祖先・overflowuid所有・group権限の拒否を確認します。system callからの事実への変換は検証しません（それはLinuxだけの試験です）。

## GoCI inventoryの変更（統合済み）

`internal/agentkeys`のLinux向けnative fileが変わります。共通7fileは変わりません。

- linux：`native_other.go`から`native_linux.go`へ（`scripts/go_ci.py`と`scripts/tests/test_go_ci.py`の対応を更新済み）。
- darwin：`native_darwin.go`（変更なし）、windows：`native_windows.go`（変更なし）。
- `native_other.go`は対象3 GOOSのどれのproduct inventoryにも入らなくなります（android等でだけbuild）。

`docs/agent-key-loader.md`と`docs/go-ci.md`もLinux実装候補と実機未検証を分けて更新済みです。

## 実機で未確認の事実（compile・fake試験で代替できないもの）

1. `unix.Getresuid`/`unix.Getresgid`/`unix.Capget`/`unix.Fgetxattr`/`unix.Readlinkat`と`LINUX_CAPABILITY_VERSION_3`が、固定版x/sysの全Linux GOARCHで想定のsignatureで存在すること（amd64はCGO=0 compileで確認済み。他GOARCHと実syscall挙動は別検証。module cacheは読んでいません）。getresuid・getresgid・capgetがthread単位の値を返すこと。
2. 対象kernelで、ACLの無いfileとdirectoryに対する`fgetxattr`のaccess・default ACLが`ENODATA`を返すこと（ext4・tmpfs・overlayfs）。対象containerのtmpfs・overlayでACLが`ENOTSUP`の場合、全鍵を拒否します（fail closed）。
3. overlayfsの`fstat`と`fstatat`が同じ`(st_dev, st_ino)`を返し、無関係な変更でそれが変わらないこと。copy-up（信頼主体による変更でだけ起きる）の後に`st_ino`が変わる構成では拒否されます。
4. containerの`/proc`がそのpid namespaceのprocfsで、`/proc/thread-self`・`/proc/<pid>/task/<tid>/status`・`/proc/sys/kernel/overflowuid`がmaskされず読めること。procfsのroot inodeが1であること。statusの形式（`CapAmb`はLinux 4.3以降）。
5. container runtimeが非rootの実行userに、effective・permitted・ambient capabilityを与えないこと（与えれば拒否）。`no-new-privileges`は要求していません。
6. 鍵の配置先（例：image内のhome、tmpfs）が上記のallowlistに入り、各要素のownerが現在のUIDかUID 0で、ACLが無いこと。
7. 無関係な子の作成・削除でdirectoryに変わる項目が、比較から外したmtime・ctime・size・nlinkだけであること（共通）。
8. 保持中の祖先fdはrenameを妨げません。置換は再検査でだけ検出します（共通）。

## 配置前に必要な承認

- 独立したsecurity review（このsource・試験・本書）。
- 実container上での上記未確認事実の確認（read-onlyの照会でも、実account・実filesystemへの操作です）、実鍵の作成・配置・fingerprintの別経路での記録。これらは未承認で、本変更では行っていません。
- container orchestration、Docker・hostのsocket、資格情報のmount、権限の修復、鍵の生成は範囲外です。4人・gateway 1つの範囲を維持します。
- source・fake試験・compileの成功は、実relay・遠隔接続・配置全体の成功を示しません。製品gateは閉鎖したままです。

## 限定した実container検証（2026-10-31）

既存Linux Docker内で、sourceからCGO=0 buildしたgatewayのscratch imageを単発起動しました。UID/GID 10001、read-only rootfs、全capability drop、no-new-privileges、network none、host mountなし、64MiB/0.5CPU/pids32、自動再起動なしです。

公開fixtureだけをimage内に置き、親directory 0755・root所有、leafはUID10001所有としました。実鍵はありません。

- leaf 0400：実 `ReadTrustConfig` の主体/procfs・filesystem・owner/mode/ACL・同一handle再検査・closeが通過し、後段のschema検査が固定診断で拒否、exit2。
- leaf 0444：共通のprivate-file保護契約に反するため、native読取り段階で拒否、exit2。
- どちらもexited、OOMなし。鍵取得・listener・Tunnel・Mac commandへ進んでいません。

当初のfixtureはCOPYのchmodが新規親directoryにも作用してtraverse不可となり、両caseとも拒否しました。固定stageだけの一時診断buildと当該公開fixture metadataで原因を特定し、親を先にWORKDIRで作るfixture配置へ修正しました。製品の保護条件と製品binaryは変更していません。先行失敗と最終成功は別保存です。

この証拠は当該containerのoverlayfs/procfs条件だけです。tmpfs/ext4の別mount、実secret bind mountのhost側保護、実鍵/signature、全Linux構成や実接続の証明には広げません。試験用image/containerは停止して保存しており、既存CI workloadを変更していません。
