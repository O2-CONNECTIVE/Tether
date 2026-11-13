# リモート開発向けコマンド実行の設計案

[製品目標](product/goals-and-acceptance.md) / [操作辞書 #188](https://github.com/O2-CONNECTIVE/Tether/issues/188) / [接続検証 #190](https://github.com/O2-CONNECTIVE/Tether/issues/190) / [権限モデル案](host-permission-design.md)

基準: main `8ff6517`（2026-11-12）。
これは次の開発段階の方向と、最初の実装対象 `tether_run_command` の設計案です。
**実機受入は未着手で、提供済み機能の宣言ではありません。** 実装済みの部品は「実装の分割」に記載します。gatewayとMCPの入口（`tether_run_command`）まで実装しましたが、Mac側で許可をONにする入口（分割の5）がないため、Macは汎用コマンドを実行しません。
既存の受入チェック、製品接続gate、未達の記録は変更しません。

## 目的

実ChatGPTからMacへの固定作業1回は確認できましたが、頼める操作は固定の検証コマンドだけです。
次の段階では、安全機構の追加より先に、実際の開発作業に使えることを確かめます。

1. **第1の目標:** ChatGPTなどのMCPクライアントから、自分の開発PCでリモート開発ができる。
2. **第2の目標:** 被支援者がGUIで待機と接続を許可し、支援者がエージェントを通じてそのPCを支援できる。

入口はGUIとMCPを軸にします。GUIは待機・許可・停止・表示を担う場所、MCPはエージェントの入口です。
CLIは開発とCIの検証用として現状を維持します。

## 裁定済みの事項（2026-11-12）

利用者が決めた事項です。

| 事項 | 裁定 |
| --- | --- |
| 操作の対象 | 本人のMacに加え、他人のMacも製品の対象に含める |
| 他人のMacへの許可 | この段階では、その回ごとの一時許可だけ。常設共有は後続 |
| 最初の実装 | 汎用コマンド実行1本をChatGPTから通す |
| コマンド実行の許可 | Mac側GUIで時間制限付きでONにする |

### 実装中の追加の裁定（2026-11-12）

| 事項 | 裁定 |
| --- | --- |
| intentの型 | 固定作業と汎用コマンドで型を1つにする。常駐経路がこの型を作業の相関として約10か所で受け渡すため。相関だけを共通の型に切り出す案は[#358](https://github.com/O2-CONNECTIVE/Tether/issues/358) |
| 作業root | 既存のローカル構成 `macagent.Config.WorkDir` を使う。GUIで選ぶ案は[#359](https://github.com/O2-CONNECTIVE/Tether/issues/359) |
| Macの署名 | 汎用の作業の登録と回収だけ、操作名・コマンドのhash・`cwd`・timeoutを含む新しい署名にする。固定作業の通信形式は変えない |
| `cwd` の確認 | 起動の直前にsymlinkを解決し、実体が作業rootの配下でなければ拒否する |
| コマンドの照合 | Mac側で、届いたコマンドのSHA-256と署名済みのhash、timeoutを照合する。開始要求のIDの扱いは固定作業と同じ |
| 許可の確認 | Macがintentを受け付けるときに1回確かめる。許可がなければ署名付き拒否を返す |

- 1つの承認で実行されるのは最大1回です。制約付きの実行sessionは1コマンドに限られ、同じ要求IDの再送は既存のコマンドの状態を返します。
- **実行中のコマンドは、許可の取り消しでは止まりません。** timeoutは最大240秒です。止める手段を足すかは未決です。
- `cwd` の確認から起動までの間に、ディレクトリをsymlinkへ差し替える隙は残ります。

## 提案中の事項（未裁定）

以下は設計案で、利用者の裁定を受けていません。

- 自動起動と無人再接続は、この段階の対象外とする。手動の待機ONまでを扱う。
- 社内への受け渡しは、固定sourceからのbuildとhash照合で行う。下の「Apple Developer登録なしの社内利用」を参照。
- クライアントは、実接続を確認済みのChatGPTを先行する。
- 専用ツールはコマンド実行の後に、実際の失敗の記録から必要な順に足す。候補はディレクトリ一覧、ファイル読取、検索、部分編集。
- 許可プロファイルを2つに分ける。下の「許可プロファイル」を参照。

## 現状（コード上の事実）

- 実行のruntimeは任意コマンドに対応済みです。[Mac runtime](../backend/internal/macagent/runtime.go)は `/bin/sh -s` を起動し、コマンドをstdinで渡します。旧 `rescue_start` と固定作業がこのruntimeと[実行session](../backend/internal/agentexec/session.go)を共有しています。
- 固定されているのは3か所です。gatewayの定数（[resident.go](../backend/internal/agentgateway/resident.go)）、通信路の署名付きintent（[resident_intent.go](../backend/internal/agenttransport/resident_intent.go)）、Mac側の照合（[resident_wire.go](../backend/internal/maclaunch/resident_wire.go)）。コマンド本文は通信路に流れていません。
- Mac側は許可する操作の一覧を[resident policy](../backend/internal/maclaunch/resident_policy.go)に持ちます。
- 常駐経路は実行ごとの本人同意を通りません。事前に固定した操作だけを受ける前提です（[resident_source.go](../backend/internal/maclaunch/resident_source.go)）。
- 開発GUIは待機ON/OFFをpipeでGo側へ送ります（[ResidentProcess.swift](../ui/mac-development/Sources/DevelopmentProcess/ResidentProcess.swift)、[residentpipe](../backend/internal/residentpipe/run_darwin.go)、[standby](../backend/internal/residentmac/standby.go)）。個々のコマンドの許可を送る入口はありません。
- 既存の `tether_fixed_status` / `cancel` / `release` / `list` / `get_execution` / `read_output` は、要求IDと接続世代で実行を特定します。操作名や固定コマンドを照合していません。
- 署名の型は2つ並存します。現行経路が使うのは通信路の署名で、[residentwork の intent](../backend/internal/residentwork/intent.go)は本体からの呼び出しがなく、テストだけが使っています。
- 開発GUIは状態と、実行したコマンドの本文（直近5件）を表示します。

## `tether_run_command` の仕様案

gatewayとMCPの入口の実装（4c-1）では、次のとおりにしました。

- `request_id`・`command`・`cwd` は必須です。
- `timeout_ms` は省略時60000、範囲は15000〜240000です。
- `command` は1〜65536 UTF-8 bytesです。
- 形式が不正な引数は、作業を作る前に `invalid_run_scope` で拒否します。
- 同じ `request_id` に異なる `command`・`cwd`・`timeout_ms` を指定すると、`request_scope_mismatch` で拒否します。結果を解放した後も同じです。

| 項目 | 案 |
| --- | --- |
| 追加するツール | `tether_run_command` の1本。状態・出力・取消・解放・一覧は既存の `tether_fixed_*` を使う |
| 引数 | `request_id`、`command`、`cwd`（作業root内の相対パス）、`timeout_ms` |
| 操作名 | 固定作業とは別の名前を追加する。固定作業は接続確認用として残す |
| 署名 | コマンドのhash、`cwd`、`timeout_ms` を通信路の署名付きintentに含め、Mac側が検証する |
| timeout | 既定60秒、下限15秒、上限240秒。作業全体の期限で、要求を受けた時点から数える |
| 同時実行 | 1 sessionに1件（現行どおり） |
| 状態の持ち越し | なし。作業ディレクトリは毎回引数で渡す。対話シェルは作らない |
| 表示 | 開発GUIに、実行中と直近のコマンドを表示する |

- timeoutの下限を15秒にするのは、固定作業の期限と同じにするためです。期限がMacの応答より先に切れた作業は回収されず、そのsessionの常駐作業をgatewayの再起動まで止めます。
- 下限を置いても、コマンドの後始末を確認できない作業は同じようにsessionを止めます。固定作業と違い、居座るprocessを残すコマンドで起こせる可能性があります。Macが何を確認済みと報告するかは未確認で、止まったsessionを解除する手段は未設計です。
- timeoutの上限を240秒にするのは、中継の制御接続に5分の寿命上限があり、通信断で作業が失効する既存契約を変えないためです。より長い作業は、切断後も作業を続ける契約変更が必要なので後続とします。
- 結果を同期で待つ機能は初版に入れません。開始してから状態を照会する既存の形を使います。
- `cwd` の制限はsandboxではありません。コマンド自体は現在ユーザー権限で、そのユーザーが読めるファイルとネットワークへ届きます。
- 数値は案です。実装前に既存の上限と照合します。

## 期限付きの実行許可

- Mac側GUIで「コマンド実行を許可」を一定時間ONにします。GUIからGo側へは、待機ON/OFFと同じpipeで伝えます。
- 許可は待機の1周期の状態として持ち、期限切れ・待機OFF・GUIからのOFFで失効します。中継の張り直しでは失効しません。1回の許可は最大15分で、期限は経過時間と壁時計の両方で判定します（スリープ中も進みます）。当初案のresident policyへの追加は採りませんでした。
- 延長はMac側の操作だけで行います。MCPの引数やモデルの出力は許可になりません。
- 待機ONだけでは、コマンド実行を許可しません。
- GUIからGo側へ伝えるpipeの入口（`allow` / `deny`）と、開発GUIのボタン・許可の表示・実行したコマンドの表示を追加しました。記録の制約は[development-resident](development-resident.md)を参照。
- 許可を待機の1周期に置くこと、上限15分、古い取り消し要求を適用しないことは、実装上の設計で未裁定です（[PR #357](https://github.com/O2-CONNECTIVE/Tether/pull/357)）。

## 安全上の変化

現在は、gatewayや中継が破られても、Macは固定コマンドしか実行しません。
任意コマンドを通すと、Mac側の照合が「完全一致」から「許可の範囲内か」に変わり、この保証はなくなります。
そのため、Mac側の期限付き許可とGUIでの表示が、このツールの安全の中心になります。

維持する契約は次のとおりです。

- 認証、本人とMacの対応、待機ON/OFF。
- 期限・取消・通信断での失効と、同じownerによる回収。
- cleanup unknownを成功にしないこと。結果不明を新しい要求IDで自動再実行しないこと。
- 再接続や再起動で旧許可を復活させないこと。
- コマンド出力を未信頼データとして扱うこと。

## 許可プロファイル（提案）

| プロファイル | 対象 | 許可の形 |
| --- | --- | --- |
| 開発 | 自分の開発PC | 待機ON時に時間制限付きで、実行とファイル操作を許可 |
| 支援 | 被支援者のPC | 被支援者がGUIで待機と接続を許可。コマンド実行は1件ずつGUIで承認 |

ツールの辞書と実装は共通にし、違いは「どのツールを誰の承認で通すか」だけにします。
判定は端末側の共通サービスに1か所だけ置きます。

コマンド実行を許可したsessionでは、ファイルツールの厳密なroot拘束は安全性を上げません。
シェルから同じファイルに届くためです。
そこで、開発プロファイルのファイルツールは作業rootの既定とパス正規化で公開し、[読取core](bounded-file-read-core.md)に残る競合時の保証は支援プロファイルの公開条件とする案です。
これは認可の境界に関わるため、採否は利用者の裁定とします。

## Apple Developer登録なしの社内利用

前提（利用者の表明、2026-11-12）: Apple Developer Programには登録しておらず、Developer ID署名とnotarizationは作れません。
不特定多数への配布は現時点の前提にせず、チームと社内で使えることを目標にします。
接続の安全性は妥協しません。

### 2種類の「署名」を分ける

- **コード署名:** Appleの仕組みで、appの発行者と改変の有無をOSが確認するもの。Developer登録が必要なのはこちらです。
- **通信の署名:** gatewayとMacの間で、要求が改変されていないことを端末鍵で確認するもの。Tether自身の仕組みで、Developer登録とは無関係です。

### 登録なしで得られない保証と代替

| 得られないもの | 影響 | この段階の代替 |
| --- | --- | --- |
| 発行者の証明（Developer ID） | 受け取ったappが誰のbuildかをOSが示さない | 固定source SHAからのbuildと、別経路で渡すhashの照合（[#351](https://github.com/O2-CONNECTIVE/Tether/pull/351)の方向） |
| notarization | Appleによる検査を経ていない | 各自がsourceからbuildする。build元を本人が確認できる |
| Gatekeeperの通過 | ダウンロードしたappはOSが起動を止める | ダウンロードで配らない。各自がbuildするか、同じチーム内の信頼できる経路で渡す |
| 署名identityによる起動元の固定 | 「この発行者のappだけがhelperを起動できる」という照合を使えない | 既存の限定開発用の信頼モデルを使う。下を参照 |
| 更新をまたぐOS権限の安定 | buildのたびにOSの許可を求められる可能性がある | 未確認。実機で観測する |

Gatekeeperを外す設定変更や、隔離属性を手で外す手順は採りません。

### 登録に依存しないもの

接続の安全は、コード署名と独立に成り立ちます。

- 認証と、本人とMacの対応づけ。
- 端末鍵による通信の署名、接続世代、再送の拒否。
- 待機ON/OFF、期限付きの実行許可、期限・取消・通信断での失効。
- 所有processの停止と回収、cleanup unknownを成功にしない契約。

コード署名がないことで弱くなるのは「Mac上のappが、意図したbuildと同じものか」の確認です。
同じMacの同じユーザーで動く別のプログラムからの干渉は、署名の有無にかかわらず現行の保証対象外です。

### 土台にする経路（提案）

実ChatGPTからの接続が通ったのは、[限定開発用の常駐](development-resident.md)と開発GUIの経路です。
これは「利用者が個別に承認したローカル開発物を直接起動する別の信頼モデル」で、Developer IDの成功を偽装しません。
社内利用の段階では、この経路を土台にします。

一方、次の経路はDeveloper IDを前提にしており、登録がない間は最終の受入に到達できません。

- [署名後producerをGUIへ固定する組立](mac-signed-release-build.md)。
- 通常GUIの起動時の発行者確認と、`ApprovedRelease.production` の設定。
- 署名済み製品の起動、配布物の受入。

これらに向けた進行中の作業（[#326](https://github.com/O2-CONNECTIVE/Tether/pull/326)ほか）を続けるか保留するかは、利用者の裁定とします。
この文書は、進行中の作業を止めたり変更したりしません。

### 社内での受け渡し方（案）

| 案 | 内容 | 保証 | 制約 |
| --- | --- | --- | --- |
| A. 各自がbuild | 固定source SHAから、各自のMacでbuildする | build元を本人が確認できる。OSに止められない | 全員に開発環境が必要 |
| B. 1人がbuildして渡す | hashを別経路で渡し、受け取った側が照合する | hashの経路を信頼できる範囲で、bytesが同一 | 発行者の証明はない。渡し方によってはOSが起動を止める |
| C. 社内の自己署名証明書 | 自分たちで作った証明書でコード署名する | build間で同じidentityになり、OS権限が安定する可能性がある | Gatekeeperは通らない。効果は未確認で、証明書の管理が増える |

おすすめはAです。この段階の利用者は開発者で、開発環境を持っているためです。
Bの照合は、Aでbuildした結果が期待どおりかを確かめる用途にも使えます。

### 再判断の契機

第2の目標では、被支援者は開発環境を持たない想定です。
build済みのappを渡すことになり、OSによる起動の制止と、発行者を確認できないことが利用の障害になります。
被支援者のPCへ渡す段階に進むときに、Developer登録を再判断します。
それまでは、登録を完了条件にも着手条件にもしません。

### この節の未確認事項

- 対象のmacOSで、未署名のappがOS権限の許可をbuildごとに求めるか。
- 実行したコマンドが利用者のフォルダーへ届くときに、OSの許可表示が出るか。
- 自己署名証明書（案C）の実際の効果。
- 限定開発用の常駐にある15分の終了期限を、リモート開発でどう扱うか。

## session上限の429

- MCP sessionは1主体あたり4件までで、無通信30分で失効します（[gateway.go](../backend/internal/agentgateway/gateway.go)）。
- 失効したsessionの掃除は、新しい接続の開始時と、そのsessionへの要求時だけに走ります（[http.go](../backend/internal/agentgateway/http.go)）。上限は定数で、設定からは変えられません。
- Macの常駐接続は中継の寿命で5分ごとに張り直され、そのたびに接続世代が変わります。古い世代のsessionは新しい作業を開始できませんが、30分間は上限を消費します。Mac未接続時に作られた発見専用sessionも同じです。
- したがって、クライアントが世代の更新ごとに接続を開始し直すと、約20分後の5回目が429になります。実接続で起きた429がこの経路だったかは未確認です。

### 実装した修正（[PR #353](https://github.com/O2-CONNECTIVE/Tether/pull/353)）

- 常駐作業用の接続開始が上限に達していたら、同じ主体の、世代が現行と異なる常駐sessionのうち、最後の利用が最も古い1件を終了して入れ替えます。
- 発見専用session、現行世代の常駐session、承認済みrouteに束縛されたsession、他の主体のsessionは入れ替えません。
- 該当するsessionがなければ従来どおり429です。上限4件と無通信30分は変えていません。
- 当初の案（作業記録が残っていない旧世代sessionだけを掃除する）は採りませんでした。作業記録はprocessの寿命の間消えないため、実利用では429が解消しません。
- 確認したのはsource試験とbuild、敵対的レビュー1本までです。実機・実クライアントでの確認は未実施です。
- 当初は発見専用sessionも入れ替えの対象でした。その後mainへ入った回帰試験（[#352](https://github.com/O2-CONNECTIVE/Tether/pull/352)）が、発見専用sessionは上限を共有し、満杯なら429を返して既存sessionを終了しないことを固定していたため、利用者の裁定（2026-11-12）で対象から外しました。発見専用sessionだけで満杯のときの429は残ります。

### 残る課題

- 入れ替えられた旧世代sessionからは、その世代の結果を読めなくなります。新しいsessionからは旧世代の記録を読めない既存契約のためです。作業の完了後、結果を読む前に世代が変わって入れ替えが起きると、結果を受け取れません。
- 対処の候補は、同じ主体の新しいsessionから旧世代の結果を読めるようにする契約変更です。閲覧の認可境界を変えるため、別の変更として設計します。
- 5分ごとの張り直しそのものは変えていません。
- ChatGPTが404を受けて自動で接続を開始し直すかは未確認です。

## 実装の分割

| 順 | 内容 | 前提 |
| --- | --- | --- |
| 1 | 429: 世代が変わった常駐sessionの入れ替え（[PR #353](https://github.com/O2-CONNECTIVE/Tether/pull/353)）。実クライアントでの確認は未実施 | なし |
| 2 | 新しい操作の署名付きintent（[PR #356](https://github.com/O2-CONNECTIVE/Tether/pull/356)）。コマンドのhash・`cwd`・timeoutを署名で束縛し、不一致を拒否する | なし |
| 3 | Mac側の期限付き許可の状態（[PR #357](https://github.com/O2-CONNECTIVE/Tether/pull/357)）。期限切れと待機OFFでの失効。GUIからのON/OFFは5 | 2 |
| 4a | intentを通信路の両端に通す。gatewayの送信関数とMacの受信。Macの受付は拒否のまま | 3 |
| 4b | Mac側の受付と実行。実行層（hashの照合、`cwd` の確認）、受付（許可の確認）、登録と回収の署名 | 4a |
| 4c-1 | gatewayとMCPに `tether_run_command` を追加。引数の検査、署名する範囲、同じ要求IDの照合。状態照会と取消はgatewayの試験で確認 | 4b |
| 4c-2 | MCPからMacの実行・回収までを通す結合試験。出力の読取と解放を含む既存ツールとの結合。**未実施。** CIの実producerのfixtureは、gatewayの `/mcp` が観測専用で実行のbackendにつながっておらず、そのままでは `tether_run_command` を通せない | 4c-1 |
| 5 | 開発GUIに許可の切替とコマンド表示。pipeの入口。source試験まで。実機では、待機ONで `allow` が受理されること（CLIで駆動）と、実行したコマンドがGUIに表示されることを確認（2026-11-13、下の記録） | 3 |
| 6 | 実機受入。**項目1・3・6は通過、項目2は未通過、項目4・5は一部だけ確認（2026-11-13、下の記録）** | 4c-2、5 |

- 2〜4は認可の境界を変えます。独立レビューを通し、統合の判断は利用者に上げます。1〜3は利用者の指示で統合しました（2026-11-12）。4c-1でgatewayが汎用のintentを送るようになりました。Mac側で許可をONにする入口（5）がないため、Macは汎用コマンドを実行しません。
- 6の実機受入が済むまで、機能を有効にする統合はしません。
- 各PRで、公開契約（[出力契約](resident-mcp-output-contract.md)ほか）とfixtureを同じ変更単位で更新します。

## 受入

固定したsource SHAと実行版で、次を1名・1 Macの実ChatGPT接続で確認します。

1. 実リポジトリで `git status` 相当の短いコマンドを実行し、同じ実行の出力と終了結果を受け取る。
2. 数十秒かかるコマンドを実行し、状態照会と出力の分割読取で結果を受け取る。
3. 停止可能なコマンドを取り消し、停止受付・processの終了・回収を個別に確認する。
4. 実行中に待機OFFにし、新規要求が拒否されることを確認する。
5. 許可OFF、期限切れ、署名の不一致、作業root外の `cwd`、上限超過のtimeoutで実行されないことを確認する。
6. 実行したコマンドが開発GUIに表示されることを確認する。

source試験、CI、実クライアント接続、実OS実行、GUI操作は別の証拠として記録します。
### 実機受入の記録（2026-11-13）

source `eeed0cc`、gatewayは `bc7018f` のbuild、Macの常駐は `eeed0cc` のbuildをforeground pipeで駆動、clientは実ChatGPTの開発コネクタ、1名・1 Macです。

| 項目 | 結果 |
| --- | --- |
| 1 | 通過。ただし作業ディレクトリは空で、実リポジトリの `git status` ではない。`pwd && uname -s && echo tether-acceptance-1`、`cwd` は `.`、`timeout_ms` は30000。ChatGPTが受け取った結果は、stdout 3行、`exit.code` 0、`command_cleanup`・`owner_cleanup`・`gateway_cleanup` がいずれも `confirmed`。常駐の作業状態は `preparing` → `running` → `idle`（回収 `confirmed`） |
| 2 | **未通過。** `for i in 1 2 3 4 5 6 7 8; do echo tick-$i; sleep 4; done; echo done-2`（`timeout_ms` 60000）を2回試した。途中の状態照会は `running` で、`next_cursor` は14 → 42（1回目）、35（2回目）と進み、stdoutは `tick-1` から順に届いた。完了後の状態照会はどちらも `RATE_LIMITED: too many MCP sessions for this principal` で失敗し、ChatGPTは終了コードと回収の結果を受け取れなかった。Macの常駐は2回とも約31秒実行して `idle`（回収 `confirmed`）へ戻っている |
| 3 | 通過。`echo start-3; sleep 120; echo never-3` を開始し、約80秒後に `tether_fixed_cancel`。取消の応答は取消前の状態（`running`、`stop` は `none`）だった。常駐は `stopping` から3秒で `idle`（回収 `confirmed`）へ戻り、Macに `sleep 120` のprocessは残らなかった。続く状態照会は `phase` が `finished`、`command_phase` と `stop` が `canceled`、`terminal` が `true`、3つのcleanupが `confirmed`、stdoutは `start-3` だけ |
| 4 | **一部。** `echo start-4; sleep 60; echo never-4` の実行中に待機OFFにすると、常駐は2秒で `off`・`idle`（回収 `confirmed`）になり、`sleep 60` のprocessは残らなかった。待機OFFのまま送った新規要求は実行されなかったが、返ったのは待機OFFの拒否ではなく上と同じ `RATE_LIMITED` で、待機OFFによる拒否の応答は観測できていない |
| 5 | **一部。** 許可OFF: 実行の応答は `pending` で、状態照会は `error` が `admission_refused_or_uncertain`、`phase` が `finished`（`terminal` は `false` のまま）。常駐の作業状態は変わらず、実行記録にも載らなかった。作業root外の `cwd`（`/etc`）: `invalid_run_scope`。上限超過のtimeout（240001）: ChatGPT側のschema検証で拒否され、gatewayには届いていない。期限切れと署名の不一致は実機では未実施（source試験だけ） |
| 6 | 通過。開発GUIに「全1件のうち直近1件」と、同じ時刻・ディレクトリ・コマンドが表示された。画面の読取はcodexのコンピュータユーズで行い、記録ファイルの内容と一致した |

受入の前に、環境側で次の2つを変えています。どちらもこの変更のコードではありません。

- Worker `tether-dev` の `TETHER_RESIDENT_GATE` を `closed` から `open-resident-tether-control` へ変更（本人がデプロイ）。変更前は常駐用の登録が400で拒否され、待機ONが成立しなかった。
- ChatGPTのコネクタを再接続。

受入の途中で分かったこと。対処は未裁定です。

- **再接続から24時間を超えると `/mcp` が403になる。** Managed OAuth経由のassertionは、`iat` が最初の認証時刻のまま、`exp` だけがaccess tokenの更新（10分）で延びる。gatewayは `exp - iat` が24時間を超えるassertionを拒否する（`MaxTokenLifetime`）。観測値は `exp - iat` が142540秒で、再接続後は674秒。Accessのgrant sessionは1週間の設定なので、現状は24時間ごとに再接続が要る。
- **gatewayは拒否の理由を記録しない。** 上の原因は、gatewayの応答本文（`the Cloudflare Access assertion is not valid`）と、assertionのclaim名・判定結果だけを取り出して特定した。
- **最初の呼出しが `Resident catalog only` で拒否された。** 同じ会話の2回目の呼出しは通った。1回目の送信から約5秒後に、常駐が `connecting` へ落ちて約6秒で `authenticated` へ戻っている。因果は未調査。
- **MCP sessionの上限（1名4件）に実ChatGPTが当たる。** 項目2が通らない直接の原因で、G1の「429を解消」に当たる。gatewayの入口で要求の種類とsessionの発行だけを数えて確かめた（session IDは短いhashで照合）。
  - ChatGPTはtool callのたびに `initialize` → `notifications/initialized` → `tools/call` を送り、毎回新しいsessionを受け取る。`DELETE` は送らない。1回のtool callが1件のsessionを消費する。
  - 同じ応答の中の2回目以降は、session headerなしの `tools/call` を先に送って400を受け、そのあと `initialize` し直す。満杯のときは、このheaderなしの要求に429が返る。
  - 同じ接続世代で順に5回呼ぶと、4回目まで通り、5回目が429になった。入れ替え（PR #353）は世代が現行と異なるsessionだけが対象なので、同じ世代のsessionは無通信30分まで残る。
  - 待機OFFの間は接続世代がなく、入れ替えが働かない。前の世代のsessionで満杯なら、1回目から429になる（項目4の観測）。
  - 受入のときは3回の後に429になった。入れ替えの対象外のsessionが1件残っていたとみられるが、種類は確かめていない。約20分後の計測では4回通っている。
- 待機を入れ直した直後の `tether_run_command` が1回、`busy_or_refused`（`owner_cleanup` は `unknown`）で拒否された。直前の周期で、実行中のコマンドを待機OFFで止めている。原因は未調査。
- **待機を入れ直すと、前の周期の `request_id` は `not_found` になる。** 429を避けるために待機を入れ直すと、実行済みの結果を読めなくなる。
- ChatGPTの確認ダイアログは、同じツールでも出る回と出ない回があった。
- Workerのrelayは300秒で切れるため、常駐は1回290秒以内で起動し直した。待機が再接続をまたいでも、許可は有効なままコマンドが実行された。


## 段階の目安（提案）

| 段階 | 完了条件 |
| --- | --- |
| G0 | 実接続の記録を#189・#190へ反映し、429と5分上限の追跡先を作る |
| G1 | 既存ツールで429を解消し、コマンド履歴を残す |
| G2 | `tether_run_command` を1名・1 Macで上の受入まで通す |
| G3 | 開発GUIに接続状態とコマンド履歴を表示する |
| G4 | 2人目のMacで受領し、4名へ広げる |

支援プロファイルと他人のMacへの一時許可は、G4の後に設計します。
進行中のWindowsの作業は止めず、この段階の完了条件には含めません。

## 対象外

- 対話シェル、PTY、環境変数や作業ディレクトリの持ち越し。
- 240秒を超える作業と、切断後の作業継続。
- 常設共有、複数ホストの一覧、履歴の永続保存。
- 自動起動、無人再接続、一般配布。
- Developer ID署名とnotarization。登録がないため作れません。
- 専用のmacOSユーザーによる実行の分離。後続の候補です。

## 未決・未確認

- ChatGPT側のtool callの時間上限と、書込み系ツールへの確認表示の有無。
- ChatGPTはsessionを閉じず、tool callのたびに接続を開始し直す（2026-11-13に確認、「実機受入の記録」）。同じ世代のsessionをどう空けるかは未裁定。404を受けたときの挙動は未確認。
- 新しいsessionから旧世代の結果を読めるようにするかどうか（「session上限の429」の残る課題）。
- 作業rootをGUIで選べるようにするかどうか（[#359](https://github.com/O2-CONNECTIVE/Tether/issues/359)）。初版は既存のローカル構成を使います。
- 許可の既定時間。上限は15分で実装しました（未裁定）。
- 実行中のコマンドを、許可の取り消しで止めるかどうか。
- 汎用のintentが通信路の順番待ちを最大240秒占有する間、固定作業の提案が止まること。固定と汎用が1世代32本の上限を分け合うこと。
- コマンド履歴の保存先と保持期間。[Decision #36](https://github.com/O2-CONNECTIVE/Tether/issues/36)のDEC-03、[診断ログ #27](https://github.com/O2-CONNECTIVE/Tether/issues/27)と調整します。コマンドと出力には秘密が入り得るため、Mac内だけに置く前提で検討します。
- [#176](https://github.com/O2-CONNECTIVE/Tether/pull/176)の扱い。同PRが実行sessionへ加えた照合は既にmainにあり、今回の実装の前提にはしません。再利用か退役かは別に判断します。
- `tether_fixed_*` という名前を、固定作業以外の実行にも使うことの分かりやすさ。改名は別の変更とします。

## 追跡先

[#189 製品受入](https://github.com/O2-CONNECTIVE/Tether/issues/189)、[#190 接続検証](https://github.com/O2-CONNECTIVE/Tether/issues/190)、[#188 操作辞書](https://github.com/O2-CONNECTIVE/Tether/issues/188)、[#32 共通設計](https://github.com/O2-CONNECTIVE/Tether/issues/32)。
この文書は下書きで、各Issueへの反映は利用者の確認後に行います。
