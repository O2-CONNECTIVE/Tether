# Mac native stack の combined 検証条件

[Pump 接続](mac-native-pump.md)までの source は未統合 stack です。個別 PR のローカル成功を、将来の main 候補や実 GUI / producer の成功へ合算しません。
この文書は検証の手順と証拠契約で、workflow の無効化、失敗除外、merge 許可、実機操作の許可は追加しません。

## 統合 tree の固定

1. 統合担当は最新 main、各依存 PR の exact head、open PR の編集所有を読み取り確認する。Windows 診断用 PR を製品 stack へ混ぜない。
2. 独立 checkout で履歴を保持した候補を作り、base / head / tree、全依存の祖先関係と conflict 解決差分を記録する。private snapshot や directory 上書きで解決しない。
3. 同じ候補 source に対して下表を実行する。途中の source 変更は新しい head / tree に固定し、影響する検査と独立レビューへ戻す。過去 head の件数だけで新候補を green にしない。
4. main 前進時は適用差分と base freshness を確認する。必要 CI と安全要件を満たすまで ready / merge しない。統合を別途承認された場合も期待 head を指定し、最終 merge SHA の CI 終端を別に確認する。

## 同じ候補で必要な検証

| 層 | 同一 tree での実行と記録 | 合格へ拡張しない範囲 |
| --- | --- | --- |
| 通常アプリ | README の正規 frontend build 後、Rust の default / custom-protocol test、clippy、fmt、build。Mac unavailable と Windows output 契約の既存回帰を残す | GUI 起動、resident binding、実通信 |
| Native source | `check_mac_launch_requirement.py`、`check_mac_preparation_ffi.py`、`check_mac_rust_owner.py`、`check_mac_native_pump.py`。各 positive / compile refusal、所有 IO の終了、normal / fixture export 分離を独立記録 | 署名済み image の実 enforcement、実 Process Wait、active joins |
| 明示 source cfg | 同じ app の `resident_native_source` compile と fixture の locked / offline build・clippy。通常 build で cfg が有効化されていないことも確認 | startup / IPC への binding |
| Architecture / OS | Swift と Rust の arm64 / Intel を言語別、compile / 実行別に記録。macOS 14.4 未満の fail closed と古い OS の load は別に実証 | Swift Intel compile だけによる Rust Intel 成功、host 上だけの旧 OS 対応 |
| Producer 基盤 | 公開 Go CI 手順の build / normal / race / vet と対象 cross build、strict inventory / API 差分を同じ候補で再確認 | 実 protected policy、full authenticated consume、native producer の起動 |
| Frontend / tools | README と各 workflow の typecheck / lint / Knip / tests / notices、Python check / tool tests / syntax / tokens / sizes / commit 検査 | browser fixture による Rust IPC や実 GUI の成功 |
| CI | 候補 exact head の必須 workflow と実 check conclusion / URL を確認。Windows の元の失敗と診断証拠を区別し、担当が定義した真の安全条件へ照合 | rerun だけによる根因解消、診断 profile の成功を製品判定へ読み替えること |

既存 Python 環境の Tk 不足による失敗は全件成功に含めません。既存テストを skip / 削除せず、必要な正規環境の結果を別途得ます。
選択した既存 toolchain に Rust Intel std が無ければ「未実行」とし、無断 install しません。
この文書自体は上表の combined 実行済み証拠ではありません。実行した項目だけを exact head、環境、終了値、成功・失敗・skip と結び付けます。

## Native supervisor の次の閉包

現在の Foundation owner は未起動準備だけを提供し、Process の executable / arguments / run / Wait API を公開しません。production publisher は未設定で拒否します。既存 Supervisor は std Child の実 Wait を要求するため、両者を安全に接続する native End の生成元はまだありません。
PID から別 Child / Process を作る、path / hash を再確認する、raw EOF や未起動 retire を `Reaped` とする方法は採りません。

次の閉包には、同じ Foundation Process の sealed launch とその exact completion、起動後 child 側端点の解放、親 IO の half-close / EOF / close-once、SIGPIPE・peer 喪失・inheritance、元 worker の終了と supervisor / outer monitor の実 join を一続きで扱う必要があります。
authoritative Close、実 EOF、exact Wait、IO worker join、supervisor と outer monitor join は別々の条件として保持し、一つの receipt へ楽観的に統合しません。unknown / 不明な回収は元 owner を保持し、別 owner の成功で置換しません。

sealed release には承認 publisher / exact release と保護配置・artifact 対応、protected policy 選択、verify-to-exec の同一 image 保証が別途必要です。未署名 artifact や任意 path / argv / digest / trust override で代用しません。
これらの実物を使う検証と GUI は、必要な承認・環境・操作範囲が揃ってから実施します。source fixture の完了は、その許可や実証を追加しません。


## 同じ head を実行する CI 入口

`mac-native-source.yml` は通常 startup へ接続せず、既存の native source checker 群、合成 enrollment replay、source library build、Intel compile/link を実行します。実 producer、実 policy、鍵、署名・配置・OS trust は使いません。
既存製品 workflow のコマンド・timeout・判定を維持し、checkout だけを event の exact head に固定します。`check_ci_checkout.py` は実 `HEAD` / tree と期待 head を照合し、PR base と event SHA を別に記録します。event SHA が synthetic merge ref でも、それを実行した head と読み替えません。
これは branch protection の設定や他 PR の変更を追加しません。base 前進時の freshness は統合担当が別に確認します。

Intel checker は既存 Rust 1.99.1 の `x86_64-apple-darwin` std が無い場合に失敗し、自動 install や skip はしません。disposable CI 側が明示的に target を準備します。同じ source の Swift dylib、Rust owner と fixture executable、Pump crate を normal / source の両構成で Intel compile/link し、`lipo` で出力 architecture を確認します。
9件の所有・借用・test API の compile refusal は元の診断 code / message を維持します。foreign executable は一度も実行せず、Intel runtime・OS enforcement・Process Wait の証拠にはしません。

native CI の JSON reports は source-only の有限 fixture と build record だけを保存し、library / executable / policy / key は upload しません。既存 source checker の全 compiler / fixture は同じ所有子の終端まで待ち、一時出力を解放します。
各 job の成功と実行件数はその exact head の CI 終端で判断します。この CI 定義自体や過去 PR のローカル成功を、未実行候補の green と扱いません。以前の個別資料に残る「未統合」「CI 未実行」「#200 blocker」は各 source 準備時の記録であり、新しい候補の現在値は exact head / merge SHA の記録へ照合します。


## source 終了時の同じ consume / lease

combined review で、native source の終了が健全な receiver の `Next()` へ伝わらず、元の lease の `Finish` が OFF / control traffic まで進まない経路を確認しました。source 閉鎖と policy digest drift の両方が、実 memory 相互認証の後に同じ症状を再現します。通常 startup / IPC は未接続なので、実 GUI の障害再現とは扱いません。
修正は native revoke 後の source context 取消を元 receiver へ伝え、intake / send / reply でも同じ source を監視します。取消 callback 自体を join してから、同じ WaitSource の結果、同じ lease の Finish を既存順序で確認します。receiver の取消を native collection の証拠にはしません。

`check_mac_standby_consume.py` は Go compiler overlay だけで具体的な protected factory / memory connector / Standby を結びます。`testdata` の bridge は通常 Go source に存在せず、認証 marker・receiver・grant を偽造しません。既存の public fixed-seed fixture と実 protected reader の自己所有文書だけを使い、実 policy / 鍵 / socket / producer / native Session は使いません。
`testing/synctest` で実 Next が健全な control 上で待機した点を固定し、source 終了後、OFF を送る前に元 lease の Finish を要求します。normal / race の各20回で同じ判定と元 Standby / handler / memory streams の終了を確認し、不完全・skip・失敗は非0終了です。診断だけの receiver 取消 probe は回帰 fixture に残しません。


CI checkout record は workflow 自体の SHA / ref も別欄へ保持します。[GitHub の変数仕様](https://docs.github.com/en/actions/reference/workflows-and-actions/variables)に従い、source checkout と実行定義の revision を同一視しません。
Go source archive は arm64 と Intel の各 host 上で別に build し、Intel Rust の cross compile とも区別します。archive builder の文書化された成功は「blocked record を作成した exit 2」であり、一般の exit 0 や入力失敗 exit 1 を成功へ変換しません。CI wrapper は exit 2 と固定 code、source SHA / tree、host architecture、実 archive hash、activation / distribution blocked と未署名の flags を照合してから source 検証だけを成功とします。
