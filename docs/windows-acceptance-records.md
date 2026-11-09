# 配布版の受入手順と結果記録

[Windows限定評価手順](windows-evaluation-runbook.md) / [配布条件](distribution.md) / [受入 #1](https://github.com/O2-CONNECTIVE/Tether/issues/1) / [実機評価 #7](https://github.com/O2-CONNECTIVE/Tether/issues/7)

本書は配布候補ごとの実行計画と記録方法です。
照合基点はmain `3b4a3ac75c1307071ad6c94f15f6a35330729d53` で、実機試験は未実行です。
新規接続gateは閉鎖中で、現行の配布物はbackendを含まない未接続GUIです。
本書、記録の形式合格、CI成功によってgateを開きません。
設定変更と撤去の承認範囲は[限定評価手順](windows-evaluation-runbook.md)を正本とし、重複する導入コマンドは追加しません。

[単発版の配置に関する受入計画](ephemeral-deployment-acceptance.md)は、採用済み配置の責任分担を確認する補助計画です。
既存44条件、以下8件のID、JSON schemaと合否は変更しません。
[失効と回収の限定試験との対応](acceptance-lifecycle-coverage.md)は、既存source/fake試験と不足する組合せを整理する補助文書です。
その成功を以下8件の実機合格へ転記しません。

## 現在実行できる範囲

| 範囲 | 現在の扱い | 記録できる証拠と限界 |
| --- | --- | --- |
| sourceと配布物の整合 | 候補と独立に記録したhashがあれば、[ZIP検証CLI](tauri-evaluation-integrity.md)で展開も起動もせず確認できる | source、ZIP、EXEの対応。実SSHとOS保護を証明しない |
| 未接続GUIの表示と終了 | 既存の[評価手順](tauri-evaluation.md)と[native smoke](tauri-native-smoke.md)の範囲。既存の同一候補の報告は再利用する | browser、実Rust IPC、native、配布EXEの証拠を区別する。開始と停止は無効 |
| 公開要約の記録形式 | 本書のテンプレートとCLIをオフラインで検査できる | 記入の整合だけを検査する。実測の真正性と受入合否は認定しない |
| 正常接続、権限外拒否、TTL、短lease、切断、古い許可拒否、停止確認失敗 | 実装と配布契約の確定待ち。下記8件はすべて `not_run` | source/fakeの成功を実配布物の成功へ転記しない |
| 無入力時間による非活動失効 | 定義待ちで `not_run / definition_pending` | 期限、入力の種類、活動観測主体が未決。短leaseの更新喪失で代用しない |

既存44条件のIDと合否は変更しません。
以下の `normal` 等は本書の記録欄IDであり、既存受入条件の代替IDではありません。
実施前に評価責任者が #1 の該当条件と照合し、非公開作業票と公開PRの要約へ対応を記します。
P2 handshakeの専用再現、代替試験、既存sshdへの接続、22番の待受作成は対象外です。
固定宛先の正例を別ポートで代用しません。

## 候補ごとの開始条件

評価責任者は、次の条件が揃った行だけを実施可能にします。
不足がある行は `not_run` と理由を残し、別の経路や設定fallbackで埋めません。

1. 完全なsource SHA、配布ZIPとEXEのSHA256、manifest、同じheadのCI runを固定する。
   現行ZIP検証器はbackend同梱を拒否するため、接続版には[同梱前の契約](tauri-evaluation-integrity.md)の更新が必要。
2. 対象OS build、CPU、既存Runtime、対象候補との差分を確認する。
   実機名、IP、SID、ログオン情報、絶対パスは非公開作業票だけに保持する。
3. 正規operator、非管理者guest、保護監督の主体と実効権限、承認口、所有processとJob、停止口、回収と復元方法を確定する。
   [指定Windowsユーザーの承認資格](adr/0002-operator-sid-policy.md)と接続ごとの認証後bindingを分け、同SIDの再ログオンで旧許可を引き継がない。
4. 試験対象と変更一覧、具体的な操作、相手、回数、実行上限、期限閾値、回収方法を承認範囲へ照合する。
   現在は実行コマンドが未確定であり、本書は実接続やOS設定変更を実行する指示ではない。
5. 同じ起動に属する認可判断、接続両端、reader、processとJobを、GUI表示から独立に観測できるようにする。
   観測不能なら操作を始めない。署名認証成功と操作の実行許可も別に確認する。

## 共通の終了確認と停止条件

各試験は独立した新規承認から始め、通常停止で終えます。
各行の固有観測に加え、次の7欄を記録してから次の試験へ進みます。

| 公開記録の欄 | 同じ起動について記録する観測 |
| --- | --- |
| `revoked` | 正規の停止または期限等による許可失効。stop receiptだけでは確認済みにしない |
| `old_permission_rejected` | 同じ旧許可での新規要求と再接続の拒否。鍵の所持だけで許可が復活しない |
| `both_end_eof` | 今回の既存通信の両端でのEOFまたは終端。片側Close成功だけで埋めない |
| `reader_join` | 同じ起動のreaderのjoin。EOF観測とは分ける |
| `process_exit_reap` | 今回所有するprocessのexitとreap。Owned 0やPID不在の推定で埋めない |
| `job_cleanup` | 今回所有するJob等の回収と残存の確認 |
| `resource_restoration` | 今回の資源だけの撤去と作業前後比較。未作成の場合は非公開作業票でその事実を確認する |

本人の停止要求、候補や主体の不一致、権限外への作用、禁止先へのdial、所有対象の同定不能、観測不能、承認済み時間上限の超過で中止します。
失効失敗や終了未確認を残したまま、追加起動、再承認、次の試験へ進みません。
既存の確定した自己所有資源の回収方法だけを使い、回収不能なら未確認を維持して報告します。
強制回収は通常終了と別の証拠へ残し、失敗した試行を後の成功で上書きしません。

## 試験ごとの操作と判定

下表は操作を確定するための手順で、現在は全行未実行です。
権限外操作の各負例は非公開作業票で個別に結果を採り、一つでも欠ければ行全体を `pass` にしません。
通常試験の `pass` は固有観測と上記7欄のすべてが確認済みの場合だけです。

| 記録欄IDと対象 | 開始条件 | 承認範囲内で行う操作 | 期待結果と固有観測 | 終了後確認と個別の停止条件 |
| --- | --- | --- | --- | --- |
| `normal` 正常接続 | 保護承認、専用相手、許可鍵、固定宛先の正例、無害な往復内容が確定 | 専用鍵で署名認証し、現在の実行許可を確認して、許可先と一往復する | `authenticated_roundtrip`：同じ候補と経路で両端の往復を確認。外側認証だけで内側操作成功にしない | 正規停止後に共通7欄。相手、許可、片側観測の不一致で停止 |
| `permission_denied` 権限外操作拒否 | 拒否要求の一覧、作用とdialを独立観測する方法が確定 | 集合外の評価鍵、許可外channel、転送専用でのshell/exec/PTY/SFTP/逆転送/agent/X11、別表記host/IPv6/許可外portを個別に提示。guestから制御IPCも要求する | `request_rejected`、`forbidden_dial_zero`、`forbidden_effect_zero`：認証、channelまたは操作を該当する境界で拒否し、禁止先dialと権限外作用が0。拒否文字列だけでは合格にしない | 既存sshdと22番には接続しない。共通7欄。1件でも作用、dial、主体や観測の不明で停止 |
| `maximum_ttl` 最大期限 | 監督が所有するhard deadline、観測起点と上限が確定 | 無害な通信と状態pollを続け、最大期限を越える要求を観測する | `deadline_not_extended`：通信、poll、lease更新でhard deadlineが延長されず失効し、旧許可を拒否する。閾値と失効までの経過を非公開作業票へ記す | 共通7欄。期限超過で未失効、終了未確認、時刻観測不能なら停止。時計設定は変更しない |
| `inactivity` 無入力期限 | 定義された活動の種類、閾値、保護された観測主体が確定するまで未実施 | 定義に該当する入力を止め、pollや背景通信をどう扱うか決定済みの条件で観測する | `idle_definition_confirmed`：定義どおりの無入力で失効し、背景通信で不当に延長されない | 共通7欄。idleが未決なら開始しない。短lease喪失をこの行へ転記しない |
| `lease_loss` 短lease喪失 | 監督の短lease、更新の所有者、専用更新経路だけを停止する方法が確定 | 今回のlease更新だけを止め、状態pollを継続する | `deadline_not_extended`：pollが更新を代行せず、短lease満了で失効。最大期限と別の観測として残す | 共通7欄。所有外process停止や全端末の通信遮断が必要なら実施せず保留 |
| `disconnect` 切断 | 今回の論理接続だけを切断する入口が確定 | 当該接続を切断し、保護側の失効、通信終端、回収を観測する | `disconnect_observed`：同じ起動の切断を確認し、旧許可の再利用を拒否。アカウント削除の完了前に失効する | 共通7欄。切断受付だけ、片側終端だけ、旧許可復活で停止 |
| `stale_permission` 古い許可による再接続拒否 | 前行までの失効と回収を確認済み。旧許可との対応を非公開で保持 | 再承認せず、同じ旧許可で新規要求と再接続を試みる。切断、TTL、lease失効、明示停止の各原因を個別に記録 | `fresh_approval_required`：旧許可を拒否し、要求のdispatch、dial、操作が発生しない。再接続は新規認証と新規承認が必要 | 共通7欄。旧許可が一度でも受理されたら停止。service/PC再起動は別承認の[既存手順](windows-evaluation-runbook.md)へ分ける |
| `stop_unconfirmed` 停止確認失敗 | 他の独立試験を終え、今回の終了観測だけを失敗させる確定手段、実失効の独立観測、保留と回収手順がある | 今回所有する対象に限定した故障を注入し、正規停止を要求する。単なるGUI表示fixtureは実機のこの行へ転記しない | 許可失効と旧許可拒否は確認する。物理終了の少なくとも一欄を `unavailable` または `failed` のまま残す。`ui_end_unconfirmed` と `restart_blocked`：終了未確認の表示、回復情報の保持、追加起動の阻止を確認 | この異常系の観測が `pass` でも `must_stop=1`。以後は承認済み回収と報告だけを行う。通常終了へ改変しない |

停止確認失敗の故障手段が未実装なら、その行は `not_run / implementation_pending` です。
未知のprocessを止めたり、ACLを弱めたり、OS保護を回避して故障を作りません。
GUI fixtureの確認結果は別の試験記録へ残し、製品の実停止結果を補完しません。

## 非公開作業票と公開要約

非公開作業票には、候補と承認範囲、各変種、開始条件の確認者、操作順、期待値、実値、開始と終端、期限閾値、各観測の証拠参照、回収と復元、未実行理由を残します。
観測ごとに正の整数の証拠番号を割り当て、同じ候補と起動との対応は非公開に保持します。
公開要約には証拠番号だけを転記し、実行時刻、端末名、IP、ユーザー名、SID、PID、pipe名、起動ID、token、鍵、パスワード、絶対パス、生の例外やログ、画像を追加しません。
秘密鍵ファイルのhashやtokenのhashも、配布物hash欄に入れません。

採取対象は上記の判定に必要な観測だけとし、profile、環境変数、ホームディレクトリ、端末inventoryを一括収集しません。
原ログの自動収集、アップロード、診断bundle生成は追加しません。
必要な原証拠の保管場所、アクセス主体、保管期限、破棄担当は実施前に非公開作業票で確定し、不明なら原ログ採取を始めません。
Git除外だけで安全な保管とみなさず、既存許可外の資格情報を新たに読む操作は行いません。

[JSONテンプレート](contracts/windows-acceptance-record.template.json)は全8件が未実行の記入例です。
候補ごと、試行ごとにコピーし、失敗や取得不能を後続試行で上書きしません。
同じ公開要約には一つの候補と環境だけを記録します。

| 欄 | 記入規則 |
| --- | --- |
| `version`、`source_sha` | 形式は1、完全な40桁source SHA。テンプレートの基点を実候補のSHAへ置き換える |
| `artifact` | ZIPとEXEの各SHA256。候補未確定は `null`。実施した行には両方必要 |
| `ci_run_ids` | 同候補のGitHub Actions run番号。詳細URLとhead照合は非公開作業票またはPRの要約に対応付ける。実施した行には必要 |
| `environment` | `windows`、`x64`、5桁のOS buildと任意のrevision（例 `22631.1234`）。未記録は `null`。端末識別子は入れない |
| `status` | `pass`（その行の期待結果を確認）、`fail`（失敗）、`unavailable`（取得不能）、`not_run`（未実行）。実行0件を成功と呼ばない |
| `reason` | 成功は `none`。未実行は `implementation_pending`、`definition_pending`、`approval_pending`、`candidate_unavailable`。失敗と取得不能は `unexpected_result`、`observation_missing`、`time_limit` |
| `elapsed_ms`、`limit_ms` | 実施した試験の経過と承認済み実行上限。非負整数、上限は正数。TTL等の個別閾値と起点は非公開作業票に別記する。未実行は両方 `null` |
| `evidence_ids` | 非公開証拠の正整数参照。原文やpathを入れない。実施した行には少なくとも1件、未実行は空配列 |
| `checks` | 固定の観測欄ごとに `confirmed`、`failed`、`unavailable`、`unobserved`。未実行はすべて `unobserved` |

## オフラインの形式検査

リポジトリルートで、導入済みのPython 3.11以上を使います。
Windowsでは同じ引数を `py -3.13` に渡します。
CLIはファイルを読むだけで、接続、OS設定変更、採取、公開を行いません。

```sh
python3 -I scripts/check_windows_acceptance_record.py --record docs/contracts/windows-acceptance-record.template.json
python3 -I scripts/check_windows_acceptance_record.py --record .work/evidence/candidate-record.json --require-executed
python3 -I -m unittest discover -s scripts/tests -p test_windows_acceptance_record.py -v
python3 -m unittest discover -s tests -p test_backend_policy_lease.py -v
```

未実行テンプレートの出力は `FORMAT_VALID executed=0 not_run=8 must_stop=0 acceptance=NOT_EVALUATED` です。
終了値0は形式適合だけを意味し、失敗を正しく記録したファイルも0になります。
`--require-executed` は0件を拒否しますが、1件実施を全件合格へ変換しません。
`must_stop=1` は失敗、取得不能、または停止確認失敗の行があることを示し、後続起動を保留します。
この値が0でも、実機での安全と次操作の実行許可を証明しません。

検査器は64KiB上限、必須8行、重複ID、欠落観測、未知欄、自由文、型違い、重複JSONキー、候補未確定の実施記録、時間上限超過の成功を拒否します。
出力は固定codeと件数だけで、拒否した入力、path、例外原文を表示しません。
意味を偽ったhashや整数、虚偽の `confirmed`、非公開証拠との不一致は検出できないため、公開前に評価責任者が対応と秘匿をレビューします。
これは任意ログのredactionや秘密検出scannerではありません。

未実行の明示と終了未確認を成功にしないCLI境界はRED→GREENで確認しました。
OS build欄へIP表記が入る負例も拒否前の失敗を確認して修正しました。
未知欄と秘密の合成canaryの非表示、破損JSON、0件拒否、既存Pythonモデルで例外原文を記録しない動作は回帰検証です。
製品SSH、実Windows保護、配布EXEの起動のTDDまたは実証には加算しません。
