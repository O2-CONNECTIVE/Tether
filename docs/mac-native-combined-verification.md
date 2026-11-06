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
