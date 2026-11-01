# 提供段階と検証状況

> **対象範囲の更新（2026-11-01）:** 以下の旧SSH / Windows / Pythonに関する本文・受入条件は、その方式と記録時点の履歴として保持しています。現在のTetherはAI commandと一時派遣を目標にMacを優先し、現状の正本は[文書索引](README.md)と[検証状態](tether-pilot-status.md)です。旧ゲート閉鎖や未達を解除・達成扱いにはしません。Macは現在ユーザー・非sandboxであり、Windows隔離guestの保証を引き継ぎません。

[READMEへ戻る](../README.md)

2026-10-28に公開main `f0c41be394fe138ce1052a005b26f562bfe54809` へ[#72](https://github.com/O2-CONNECTIVE/rescue-host/pull/72)を統合した時点の記録です。
統合元sourceは `abc12d6b5dcf3715e9e4d04ed197e83c0b115261` です。
**新規接続ゲートは閉鎖中で、本番利用できません。**
コードのmain統合、未接続GUIの評価、製品の遠隔接続とOS保護は別々に判定します。
変化するCI結果は[Actions](https://github.com/O2-CONNECTIVE/rescue-host/actions)と各PRの固定headで確認してください。

## 2026-10-30の設計採用と開発範囲

[ADR-0003](adr/0003-owned-guest-workload.md)で方式Cと利用中のみのLocalSystem監督serviceを採用しました。
端末の一時guest・停止中target・同issuerの承認・停止ownerを結ぶsourceを開発中です。
対象は固定された無害workloadで、SSH session処理や任意commandの製品入口は有効化しません。
NETWORK用の既存設定とは別にBATCH用の保護runtime設定を明示し、未設定時は拒否します。
固定評価entryのruntime選択と、同じ認証済み確認への実応答書込み後の単回開始をsourceで接続しました。応答は承認receiptであり実開始/終了成功の通知ではありません。
固定設定は空、正規message認証helperの起動資源は未構成です。entry/SCMと認証/targetのprivate OS modelによる各層の試験を、単一の実native縦断として合成しません。
実Windowsの資格情報生成、保護object、同ownerの停止/撤去、実SSHは未実証です。実機OS変更の許可や製品gate解除は含みません。
以下の過去記録にあるinner sshd依存・方式未採用の記述は当時の境界であり、現在の設計方針は本節とADRを優先します。

## 2026-10-29のsource統合追記

[#147](https://github.com/O2-CONNECTIVE/rescue-host/pull/147)は、固定guest設定の保護Root読取と同じ要求Endpointの検査leaseを追加しました。
[#148](https://github.com/O2-CONNECTIVE/rescue-host/pull/148)は、実Confirm/codec/issuerを通すsource/fakeの単回発行と回収順序を追加しました。
[#149](https://github.com/O2-CONNECTIVE/rescue-host/pull/149)は、同Endpointで検査leaseを保持してから入力を分離するAPIを追加しました。
統合main `d4cb12009cdd80786b26ca18c666a84e6b1f3f3e` の[Go 3OS](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40927998417)、[Python 3OS](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40927998519)、[日本語検査](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40927998372)は全7ジョブ成功です。
正例の認証・Record再検査・OS来歴にはmemory modelを使い、製品runtime sourceは未登録です。保持ChallengeからのLaunchや実SSHを実装済みとは判定しません。
未起動専用の明示入口では、固定guest bundle→同Endpointの検査lease→元Execution owner→実stager factoryのsource呼出しを接続しました。
private OS/owner memory seamによる各層の正例と、製品の同一caller正例は別です。固定SID policyは元ownerが保持Recordから供給しますが、実message認証用helperの起動資源は未構成で、実Windowsの入口は拒否を維持します。
実runtime証跡からのgrant/起動、内側sshdの実token/shellと停止owner、実Windows配置と保護は未接続・未実装・未検証です。
以下の#72当時の配布/native証跡は履歴であり、この新sourceの実機検証結果へ合成しません。

## 方式Bの採用とcore実装

[ADR-0002](adr/0002-operator-sid-policy.md)は、固定OperatorSIDの新規承認資格と認証後AuthId bindingを分けます。
profile/reportの明示v2と単回消費に、元Execution threadの保持Recordからのpolicy供給とService stop gate内のlogical channel登録を接続しました。
各層のprivate memory modelで、同じSIDのAuthId変更は既存channel/Record/grantを引き継がず失効し、新しいServiceでも新規承認が必要なことを検証しました。
helper tokenと保護sourceは未構成で、policy構成だけでは取得を開始しません。実機機能や実SSHの成功は増えていません。
方式Bの具体入力移譲からprofile/report・単回認証結果の消費・回収までを既存Batch内で結びました。これはprivate OS modelによる限定結合で、正規資源producerやServiceの実要求loopが完成したことを意味しません。

## 提供段階と証拠の読み方

| 提供段階 | 定義 |
| --- | --- |
| 計画 | 目標や仕様のみで、対象機能は未提供 |
| Alpha | 開発用の一部実装。制約があり、本番利用不可 |
| Beta | 対象用途を限定して試用提供する段階 |
| GA | 明示した対象環境と用途で一般提供する段階 |

現在の実装はAlphaの範囲で、BetaやGAとする機能はありません。
「部分検証済み」はモック、fixture、限定したCIや実機確認の証拠があることを示し、製品全体の合格を意味しません。
未接続GUI評価版を渡したことも、実通信版の提供開始には当たりません。

## mainに統合された範囲

| 対象 | 実装と確認範囲 | 未提供または未検証 |
| --- | --- | --- |
| Tauri GUI | 未接続状態、診断表示、状態再確認。browser試験、Rust IPC試験、限定したWindows native試験 | 開始と停止は無効。Go / SSH / sidecarと未配線 |
| Windows x64 GUI評価物 | sourceとhashを対応付けるZIP生成、license収集、展開EXEの検証入口。統合後mainの配布CIで終了確認が成功 | 新PC互換性と製品接続は未検証。旧mainの終了確認失敗は履歴として保持 |
| 既存Python版 | 設定保存、操作方針、期限と失効のオフラインモデル、旧記録の回復補助 | 実認可や確実な終了保証。GUI設定は管理者昇格の認可ではない |
| Go SSH基盤 | 鍵集合、固定宛先、期限、失効、所有接続、限定fixtureによる回帰 | 製品UIとの接続、実relay、内側SSHの操作認可 |
| Go制御入口とsupervisor core | stdio基盤、start拒否のdispatcher、純粋なgrant判定、制御codecとadapter、schemaとvector | 製品CLIは起動拒否。認証付きIPC、実service、OS上の期限執行と保護 |

Go基盤の詳細は[backend](../backend/README.md)、[stdio準備](go-stdio-preparation.md)、[制御契約ADR](adr/0001-control-contract-candidate.md)を参照してください。
既存の[構成図](architecture.md)はPython部分を示すもので、現在のGo実装全体の図ではありません。

## #72で追加統合した未接続準備コード

次の準備コードは#72でmainへ統合しました。
各候補の限定試験を、実Go/Rust間の製品配線、認証付きIPC、OS保護、実SSHの成功へ広げません。

| 準備コード | 統合時の確認範囲と限界 |
| --- | --- |
| [Rust所有境界 #55](https://github.com/O2-CONNECTIVE/rescue-host/pull/55)、[Rust codec #60](https://github.com/O2-CONNECTIVE/rescue-host/pull/60)、[Rust coordinator #64](https://github.com/O2-CONNECTIVE/rescue-host/pull/64) | 限定CIと独立レビューを確認済み。実Goとの制御交換や製品配線は未確認 |
| [Windows service host #61](https://github.com/O2-CONNECTIVE/rescue-host/pull/61) | 未設定時の起動拒否と模擬制御を確認済み。実SCM登録、サービス稼働、tokenやACLの証明ではない |
| [Go codec #62](https://github.com/O2-CONNECTIVE/rescue-host/pull/62) | 統合元head `8087c942` は[fixture修正 #68](https://github.com/O2-CONNECTIVE/rescue-host/pull/68)を取り込み、[3 OSの通常/race CI](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40625636120)成功。実Go起動や製品接続の証拠ではない |
| [再起動後に許可を復元しないissuer #69](https://github.com/O2-CONNECTIVE/rescue-host/pull/69) | 統合元head `1286e398` は取消競合を修正し、[3 OSの通常/race CI](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40626031570)成功。OS上の承認、保護、実停止は未実証 |
| [ローカル制御境界 #71](https://github.com/O2-CONNECTIVE/rescue-host/pull/71) | 公開Openは常時拒否。実pipe、token、ACL、peer identityの確認と製品IPCの認証は未実証 |

## OSと役割ごとの範囲

実リモート接続を製品として「対応済み」と呼べるOSはありません。

| 役割と確認対象 | Windows | macOS | Linux |
| --- | --- | --- | --- |
| host（アクセスを渡すPC） | 初期対象として開発中。実保護と実接続は未検証 | 未対応 | 未対応 |
| operator（操作側） | 構想段階 | 構想段階 | 構想段階 |
| Tauri GUI評価版 | x64の未接続評価物。利用者による実機起動と表示の確認報告あり | native評価未確認 | native評価未確認 |
| Python / Goの開発試験 | 固定したCI環境で限定回帰 | 固定したCI環境で限定回帰 | 固定したCI環境で限定回帰 |
| 製品の保護サービス、ACL、Job、認証付きIPC、実SSHの統合 | 未検証 | host未対応 | host未対応 |

CIのOSとCPU、toolchainは[Python CI](ci.md)、[Go CI](go-ci.md)、[Tauri手順](../ui/tauri/README.md)とworkflowを正本とします。
3 OS CIの成功を、各OSのhost対応や全OS版とCPUの互換性へ広げません。
実機起動と表示の報告は、実SSH、停止の強制力、OS保護、DPIや支援技術の全面検証を含みません。

## 公開証跡と終了観測

旧main `d84f2ccac36c276c48ac7db3d80ebc9e85205a47` では[Python](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40621486881)、[Go](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40621486949)、[Tauri品質](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40621486956)、[Windows native](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40621486964)のCIが成功しています。
一方、同じmainの[配布CI](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40621486976)は展開release EXEの終了確認で失敗しました。
buildとhash照合の成功を配布検証全体の成功へ読み替えず、子プロセスの終了観測を調査中です。
この旧失敗や他候補のgreenから、統合後mainの配布検証の合否は推定しません。

#72では[#66](https://github.com/O2-CONNECTIVE/rescue-host/pull/66)のfail-closedな終了観測契約を取り込みました。
統合後main `f0c41be394fe138ce1052a005b26f562bfe54809` に対応する6 workflow / 10 jobが終端successであることを照合しました。
固定証跡は[Go](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40632153874)、[Python](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40632153904)、[日本語commit](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40632153905)、[Tauri品質](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40632154039)、[Windows native](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40632153861)、[配布](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40632153953)です。
通常nativeと展開release EXEの両方で、実Rust IPC、通常close、観測対象プロセスの終了を確認しました。
この成功は旧失敗の原因解明や、実Go/Rust間の製品配線、OS保護の実証ではありません。

未接続評価物の生成と照合は[評価版の説明](tauri-evaluation.md)、nativeの観測境界は[native smoke](tauri-native-smoke.md)を参照してください。
ブラウザー画像はRust IPCがない表示例であり、native起動や遠隔接続の証拠ではありません。
旧Python版の公開証跡は検証集計（過去の参照資料・本文非同梱）、原ログ（過去の参照資料・本文非同梱）、v0.2.1レビュー（過去の参照資料・本文非同梱）に残しています。
過去の記録や候補ごとの成功を、現在のmain全体の成功へ流用しません。

## 初期検証の安全境界と残る条件

初期対象は、遠隔の非管理者ゲストから監督と停止処理を保護する範囲です。
host操作ユーザーまたは管理者が既に侵害された環境と、管理者権限を持つ遠隔ゲストは保証対象外です。
既に侵害された別Windows serviceからの完全防御も[保証範囲](adr/0004-localservice-auth-helper.md)に含めません。専用service SID・ACLと非管理者guestからの認証・停止保護は維持します。
別主体のサービスに監督を分離する構想で、具体的なservice identity、token、ACL、正規承認、ゲストのIPC拒否は未実証です。
同一ユーザーの自己子、継承pipe、自己Jobの試験を、その保護の代用にしません。

初期評価ではPCや監督サービスの再起動後に許可をすべて失効させ、再承認を必要とする方針です。
保存UI状態や旧callbackからの自動再開は提供せず、実OSでの再起動と保護された承認口は別途検証します。
二重停止故障時の物理遮断はNOT_MET、Windows背圧の製品受入はUNVERIFIEDのままで、P2専用runtimeは未実証です。
実relayと内側操作、実service / ACL / Job / 認証付きIPC / 実SSHの統合検証は残ります。

[受入Issue #1](https://github.com/O2-CONNECTIVE/rescue-host/issues/1)、[安全要件](security.md)、[脅威モデル](threat-model.md)、[バックエンド契約](SECURITY_BACKEND_CONTRACT.md)を参照してください。
既存受入44条件の定義や合否をこの文書更新で変更しません。
接続を閉じても、既実行の変更や所有外のプロセスを取り消す保証はありません。
