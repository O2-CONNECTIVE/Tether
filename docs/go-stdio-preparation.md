# Go stdio 入口の準備

[Issue #25](https://github.com/O2-CONNECTIVE/Tether/issues/25) / IPC仕様案（過去の参照資料・本文非同梱） / [製品gate](security.md)

製品schemaの採用前に使う入出力基盤です。
`cmd/rescue-backend` は引数によらず固定診断をstderrへ書き、exit 2で終了します。
stdoutへ応答や一般ログを出さず、入力、環境変数、実行pathを診断へ転記しません。
listener、鍵、SSH Session、shell、外部子processは起動しません。
Tauri/Python/GUIから呼び出す配線や製品のstart受付はありません。

## 独立した境界

`internal/stdio.Run` は呼出側が渡した入力・出力pipeとserviceを所有します。
LFを含むbyte上限、frame件数、context期限、cleanup期限を呼出側が明示します。
製品の16KiB等の既定値、操作名、版、field、状態、error codeは定義しません。
LF framingはこの未配線基盤の機能で、製品protocolの採用判断ではありません。

Runは1 frameずつ直列にserviceへ渡し、1 frameの応答を書きます。
読み込みbufferは4KiB固定で、1 frameは指定上限まで保持します。
上限超過検出の1byteはserviceへ渡さず、応答・queueを無制限に蓄積しません。
LFなしの途中EOF、件数超過、複数行/未完/上限超過の応答、短い書込みは失敗です。
frame間のEOFは入出力loopの終端であって、製品の正常停止・失効・通信終了の証明ではありません。

decode、UTF-8/JSON、未知/重複field、版、認可、状態、順序はservice/契約adapterの責務です。
現時点ではそのadapterを接続せず、製品入力を受け付けません。
共有JSON検査の重複実装や、未決の開始・状態・停止frameは追加していません。

期限切れは所有pipeを閉じ、通常終端と失敗時もserviceのCloseを1回呼びます。
返却前にpipe watcherとcleanupの終了を確認します。
pipeのCloseが並行I/Oを解除し、serviceのHandle/Closeがcontextへ従うことが前提です。
任意の戻らないadapter/serviceや停止したOSへ強制期限を提供するsupervisorではありません。
業務処理と書込みは直列なので、長いstart中もstopを受け付ける製品要件を満たしていません。
この制約が解決する前に製品serviceを配線しません。

## 証跡と後続

`internal/control` は既存の `*backend.Session` を引き取るcoordinatorと、Go内の操作dispatcherです。
snapshotは同じIdentityのGo Snapshotを読み、stopは同じIdentityのRevoke→Close→Snapshotへ対応します。
startは常に拒否し、Ready/Activate/New/Serveは呼びません。
終了確認期限の欠落・既に取り消されたcontextでは処理を開始しません。
呼出側の切断時の失効やcleanupは、新しい有限の終了確認contextを持つ保護所有者の責務として残ります。
別Identityや対象のIdentity変化を拒否し、Closeの失敗・期限切れ・非terminatedを終了成功へ変換しません。
Closeの生errorは外へ返さず、固定の内部errorにします。
これらはwire error codeの新設ではなく、契約adapterへ渡すGo内の結果です。
snapshotはGoの観測だけで、Rustの観測や所有process回収を合成しません。
SessionのClose成功だけで製品の正常終了を表示せず、同じIdentityのClose成功と所有process回収の両方が必要です。
coordinatorはwireの版・generation・連番・request IDを検査するdecoderとは未接続です。

TDDの最初の縦断はopaqueな試験frame3件の応答とcleanup、別の縦断は実binaryの起動拒否/終了です。
coordinatorは純粋な状態seamで、同一Identity停止、別Identity拒否、未確認終了/期限の拒否、start拒否、snapshot/dispatcherを赤から緑へ進めました。
この試験はSessionを生成せず、実SessionやSSHの動作証明には数えません。
境界負例は実装後の回帰確認として区別します。
frameの試験文字列は製品schemaではなく、開始/状態/停止の意味を表しません。
自己pipeとメモリだけを使い、新listener・SSH接続・P2専用/代替再現は行いません。

製品schemaの実装候補は契約ownerが正本とvectorを管理し、この基盤へはまだ接続していません。
IPC認証、artifact照合、独立失効、親死/子孫所有、背圧中の停止は未実装です。
別保護supervisor/非管理者guestの設計方向と、具体的なservice identity/OS設定の採用・実行を区別します。
既存#50の試験専用v1を共通化する場合も、契約ownerの共有fixture到着後に別差分で接続します。
製品schemaや製品版へ自動昇格させません。
P2専用runtime実証未完了、double-close NOT_MET、Windows背圧UNVERIFIED、製品gate閉鎖を維持します。
