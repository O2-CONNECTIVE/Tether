# 非公開の有限ファイル読取コア

[操作辞書の設計 #188](https://github.com/O2-CONNECTIVE/rescue-host/issues/188) / [目標5 #189](https://github.com/O2-CONNECTIVE/rescue-host/issues/189) / [共通権威 #32](https://github.com/O2-CONNECTIVE/rescue-host/issues/32)

## 提供段階

`backend/internal/fileread/read.go` は、匿名メモリfixtureで有限読取・再認可・closeの契約を確認する非公開コアです。
コア自体はpath引数やfile openを持ちません。
同じ非公開packageへ、後続の[選択folder読取候補](selected-folder-read.md)を追加しました。
Darwin adapterは呼出側が既に所有するdirectory handleから有限に辿りますが、通常caller・root選択・実権威provider・MCP登録・transport配線はありません。
既存command grantから `files:read` を派生せず、context発行や本人の実folder取得は追加しません。
`tether_read_file` の提供開始や目標5/#188の完了ではありません。
既存の固定作業状態照会・保持出力読取と、実filesystem読取を区別します。

## 最小契約と所有

`readOwned` は、信頼される呼出側から既に取得済みのhandle一個と、期待する観測値、offset、最大bytes、認可再確認関数を受け取ります。
コアはhandleの取得方法に依存しません。Darwin候補の取得方法と未解決の境界は別文書で定義します。
期待値のidentity/versionは固定長のopaque tokenで、通常file種別と非負sizeも比較します。
非zero tokenは認可やOS同一性の証拠ではありません。コアはtoken生成・強度・OS別の意味を定義しません。Darwin候補はmetadata hashを使いますが、credentialやcontent hashとは扱いません。

認可関数には期待対象と要求rangeの全体を値として渡します。
将来の信頼できる組成側が、同じ対象・主体・操作・範囲・有効期間に結び付く既存権威を再確認する責任を持ちます。
この関数自体は認可providerでも承認機構でもなく、呼出側がnil/errorなら拒否します。
認可・inspect・readの生errorは返さず、固定の分類へ変換します。

- entryでhandleの単独所有権を移譲します。要求不正でも一回だけcloseします。呼出側の併用・再利用・二重closeは禁止です。
- 要求上限は1〜48KiB、offsetは0〜観測sizeです。無効値を既定値へ置換しません。差分から読取量を計算し、int64上限でも加算を溢れさせません。
- readは最大64回、scratchと返却copyはそれぞれ最大48KiBです。零進捗、不正count、途中EOF、その他error、call予算超過を拒否します。短い成功readは残量だけ継続します。
- read前、途中の次read前、全read後に認可と観測値を再確認します。close後にも認可・contextを確認します。
- 観測sizeの末尾では空bytesを返せます。EOFは観測sizeへ到達した意味で、後続変更がない保証ではありません。range外の追加readを行いません。
- bytesはUTF-8へ強制変換しません。binaryをそのまま有限sliceで返します。返却copyはscratch/handleから分離します。
- 失敗時はpayloadもcursor/EOFも返しません。scratchと作成済み返却copyを可能な範囲でclearします。Goやadapterが作った全copyの消去保証ではありません。
- close失敗は独立した `errCleanup` による **cleanup unknown** です。既に別の失敗があれば固定errorをjoinし、`errors.Is` で両方を判定できます。成功、未取得、再closeで回収済みへ読み替えません。自動retryはありません。

handleは同期的に完了し、read bufferを保持せず、取消後もcloseを試み、隠れた資源やpanicを発生させない信頼済みportであることを前提とします。
その前提違反への隔離機構はありません。単独所有の契約は、同じhandleを別callerが無断利用することを強制的に防ぐ仕組みではありません。

## 保証しないこと

deadlineは必須ですが、取消・期限は各port呼出しの間で観測する協調式です。
read/inspect/authorize/closeが停止しなければ、時間上限内の復帰・物理停止を保証しません。
背後で処理を残してtimeoutだけを返すgoroutineは作りません。

前後の同一identity/version/size観測は原子的snapshotではありません。
観測間の変更と復元、同一stampの書換え、混合bytes、確認後の変更を防げません。
認可の再確認も、その後に起こる失効とbytes公開を原子的に順序付けるものではありません。
最終確認と返却の間の失効を完全に防ぐには、実権威側との公開時点の契約が別途必要です。

fakeが返す通常file種別やidentity/versionは、symlink、reparse、TOCTOU、root内拘束、volume、所有者/ACL、秘密file除外のOS保証ではありません。
この実装を根拠に実pathを開いたり、Macの現在user権限をsandboxと扱ったりしません。

## 次段階の最低限の決定と証拠

1. 既存権威との統合: 主体・host・root・files:read・期限・失効の結合と、bytes返却時点の失効順序。
2. rootと秘密fileの方針: 設定を誰が承認するか、どのfileを拒否するか。既存shell権限への暗黙追加は禁止。
3. OS別adapter: no-follow取得、既取得handleの所有、root/name拘束、通常file判定、identity/version、変更検出限界、close失敗後の回収責任。
4. 公開契約: versionとread bytesの結合、binary符号化、cursor、encoded結果上限、同時実行・総量・保持予算、エラーと再試行。48KiBは内部上限で、公開予算の負荷実証ではありません。
5. 匿名契約試験とは別に、承認された対象の実OS故障注入・実client縦断・配布条件を検証。成立前は実file toolを登録しません。

## 検証の読み方

コアのGo試験は匿名in-memory handleだけを使います。
後続adapterのDarwin試験は新規の匿名一時directoryだけを使い、既存fileや本人の実folderを使いません。
binary、空file、末尾、上限、offset、短いread、途中error/EOF、無進捗、count偽装、call予算、対象/版/種別の変化、取消・失効・期限、close失敗、copy分離を検査します。
観測間の復元を検出できない例と、非協調readが終了するまで取消応答も返らない例を明示します。
Python helperではproduction inventoryへの明示したfile限定追加、欠落/未知adapter拒否、package結果欠落拒否、exported入口・production importerなしを確認します。

Goの通常/race/vet/buildは最終headの[Go CI](go-ci.md)を照合します。
ローカルの標準library不足、未実行、hosted CIの成功を混同せず、PRで対象SHAと各結果を記録します。
この変更ではローカルGoのred/greenを実行できておらず、TDDの実行証拠とは呼びません。
