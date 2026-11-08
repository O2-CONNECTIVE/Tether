# 選択folderの非公開Darwin読取候補

[操作辞書 #188](https://github.com/O2-CONNECTIVE/rescue-host/issues/188) / [目標5 #189](https://github.com/O2-CONNECTIVE/rescue-host/issues/189) / [有限読取コア](bounded-file-read-core.md)

## 段階と実行範囲

本人が選んだ一つのfolderを別の読取専用許可で扱う方向に対する、非公開・未配線の準備実装です。
`backend/internal/fileread` の型・関数は非exportedで、通常production callerもありません。
匿名の新規一時folderを使うDarwin試験だけがadapterを呼びます。
実folderの選択・取得、実permission provider、認証情報やcontextの発行、永続設定、MCP登録、GUI、検索はありません。
このPRを根拠に本人のfolderや既存の実データを開きません。
Linux/Windowsは供給されたroot handleをcloseして非対応を返し、shellや汎用path APIへfallbackしません。

## 独立した有限読取契約

`readPermission` は、別の信頼できる組成側が渡す**一要求に限定したallowlistと再確認port**です。
構造体の作成・非zero ID・root handleの所持・待機ON・既存command権限を読取許可とみなしません。
本物の権威を発行・確認するproviderは未実装で、試験だけが匿名の値とcallbackを供給します。

- principal / host / connection / selectionはprivateな不透明比較値で、永続grantや公開protocolの形式を決めるものではありません。信頼側が実主体・host・接続世代・本人のroot選択との対応を保証する責任を持ちます。
- exact relative path、offset、最大bytes、期待versionを同じ要求に固定します。未知path・別range・別bindingは拒否します。
- 有限のpermission期限、Context deadline、非nilのlive recheckが必須です。既存shell/control権限からの暗黙変換はありません。
- 子を照会・openする前に要求全体とroot identityをproviderへ渡します。root descriptorの実identityを照合してから辿り、対象取得後は観測identity/version/sizeとrangeを束縛して最初のbyte読取前、読取前後、close後に再確認します。
- 期限・取消は同期port呼出しの間で協調的に確認します。OS呼出しが止まらなければ時間上限内の復帰を保証せず、timeoutだけ返して裏で処理を残すgoroutineは作りません。

## 取得・所有・予算

呼出側は、別途承認・取得済みのroot directory `*os.File` を単独所有で渡します。
この候補はrootの絶対pathを受け取りません。
root取得時の本人選択、symlink拒否、ACL・mount確認は呼出側の未実装責任です。
root自体を既にsymlink経由で取得したかはdescriptorだけでは証明できません。
entry以降の呼出側による再利用・並行操作・dupした別名handleの利用は契約外です。

1. pathは最大1024 byte・16 component、componentは最大255 byteです。空、絶対path、dot/dot-dot、隠しcomponent、NUL、backslash、colon、非ASCIIや空白等は拒否し、clean/正規化で別pathへ変換しません。
2. 各componentを親descriptorから `fstatat(AT_SYMLINK_NOFOLLOW)` で事前照会し、`openat(O_RDONLY | O_NOFOLLOW | O_CLOEXEC | O_NONBLOCK)` で取得します。中間には `O_DIRECTORY` も要求します。取得前後のmetadataが不一致なら拒否します。
3. root/中間directoryは現在実効UIDと一致し、group/world-write modeを拒否します。leafは同じUIDの通常file、link count 1、非負sizeだけです。全componentのdeviceとfilesystem IDをrootと比較します。
4. rootからleafまでdescriptorを保持し、各checkpointで同じdescriptorのmetadataと親nameからのno-follow照会を照合します。root・祖先のmetadata変化も保守的に拒否し、無関係な兄弟file更新でも拒否する場合があります。
5. 既存 `readOwned` を使い、最大48KiB、最大64 read、overflowしないrange計算、途中失敗時の無payload、binary slice、scratchの可能な範囲のclearを維持します。
6. rootを含む取得済みhandleは失敗経路も逆順に一回closeを試みます。途中取得失敗でも先のhandleを残しません。一つのclose失敗で他のcloseを省略せず、cleanup unknownを独立errorとして保持してbytesを抑止します。自動retryやclose再試行はありません。

## 秘密情報と名前の限界

exact allowlist以外はdefault denyです。
さらに隠しcomponent、secret/credential/password/token/keychain等を含む名前、private-keyの代表名、`.key` / `.pem`等のsuffixを拒否します。
これは追加の保守的な名前filterであり、完全な秘密情報分類器ではありません。
普通の名前のfileにも秘密・資格情報・個人情報は入り得ます。大文字小文字を区別しないfilesystemや別名・Unicodeの扱いにも限界があります。
**名前filterやallowlistだけで「全秘密を除外した」とは主張しません。**
何を安全な内容として許可するか、分類・更新後の再承認・例外の責任は未決で、実providerと製品公開の前提です。
返却されたfile内容は未信頼データであり、権限を増やす命令には使いません。

## 製品公開を止める残余リスク

- **連続したroot内拘束は未成立:** retained descriptorは対象objectを指し続けますが、そのroot/ancestorが外へmoveされた瞬間や、変更して元へ戻した間を原子的に排除できません。持続するrename・name差替えの検出試験を、全symlink/rename raceの保証へ広げません。
- **hardlink/mount:** link count 1や同device/FSIDは観測時点だけです。hardlinkの作成・削除競合、同じfilesystemのmount別名、root取得前の由来を証明しません。実mount操作は試験していません。
- **ACL:** UIDとmode bitsはDarwin ACLによる書込み許可を排除しません。現在userの実行権限や安全に見えるmodeをsandbox・OS隔離と扱いません。
- **special fileのopen競合:** no-follow事前照会で特殊fileを拒否しても、直後に差し替えられたdevice等のopen handlerが走る可能性は残ります。`O_NONBLOCK` は副作用なしopenの保証ではありません。「特殊fileを決してopenしない」とは主張しません。
- **snapshotと版:** dev/inode/generation/birthtime/FSIDからidentity、size/mode/link count/UID/GID/flags/mtime/ctimeからversionをhash化します。atimeは読取自身が変え得るため除外します。これらは観測stampであり、content hash・credential・原子的snapshotではありません。同stamp書換え、混合bytes、観測間の変更・復元、最終確認後の変更を排除できません。
- **失効と返却:** 最後の認可確認とbytes返却の間の失効を原子的に順序づけません。実権威側との公開時点の契約は未決です。
- **資源と停止:** 同期syscall/closeの停止時間、全copyの消去、callerの所有契約違反への隔離、全requestの同時実行・総量・保持上限は未実証です。

これらが残るため、source/匿名fixture/CIが成功しても実file toolを登録しません。
#188のroot外拘束・秘密除外・版とbytesの結合、実client縦断、本人同意・実権威、配布受入を達成扱いにしません。

## 検証の読み方

共通試験はexact binding/path/range、欠落provider、取消・期限、未解決/解決後targetの不正を確認します。
Darwin試験は匿名一時folderだけで有限binary/空file/末尾/上限、symlink、hardlink、FIFO、directory、writable directory、leaf/parent/rootの持続rename、書換え、途中取消・失効、partial lookup、close失敗を確認します。
異UID・別device/FSIDは値によるunit fixtureで、実chown/mountやOS権限分離の試験ではありません。
fixtureの成功は競合の全interleavingを検証した意味ではありません。

ローカルGo 1.27.3は標準library不足でsetup失敗のため、Go red/green・通常・race・vetをローカル成功にしません。
Goの実結果は最終headの[既存CI](go-ci.md)でOS別に確認します。
Python helperのinventory検査、gofmt、静的レビューはGo実行と分けて記録します。
