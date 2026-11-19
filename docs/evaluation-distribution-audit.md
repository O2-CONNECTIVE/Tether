# 評価版配布準備の点検記録

2026-10-29、main `929d516173dbbf772ba224d5dde500f6e51f1501`を基準に配布入口を点検した。
この記録は配布担当向けで、候補の起動成功、実SSH接続、限定配布の承認を表さない。
READMEと実機試験表の更新は別工程とする。

## 配布入口と必要ファイル

| 対象 | 現存する入口と同梱物 | 現時点の判定 |
| --- | --- | --- |
| 旧Python/Tk版 | `build_windows.ps1`（#416 で削除）が`dist/RescueHost.exe`を想定。Upterm、固定known_hostsを埋込み、対応する`third_party/UPTERM-LICENSE.txt`を別添する | 依存lockのCIと最終EXEの生成検証は別。新規接続の開始拒否を維持 |
| 未接続Tauri GUI | [ZIP生成CLI](../scripts/package_tauri_evaluation.py)と[専用CI](../.github/workflows/tauri-evaluation.yml)。`rescue-host-shell.exe`、`LICENSE`、`README_JA.txt`、Cargo/npm lock、依存一覧とlicense本文、`MANIFEST.json`を同梱。ZIP横に`.zip.sha256`を置く | 未署名の内部評価候補。Go、SSH、サービスEXEを追加しない。候補ごとのCI終端と証跡が必要 |
| 実接続版 | [準備契約](connected-evaluation-package-contract.md)のGUI、service、backendの3役。`bin/rescue-service-host.exe`等は提案名 | 準備JSONのみ。サービスlibraryを完成した配布EXEと扱わない。validatorは宣言が整合しても終了値2で停止 |

Tauri生成CLIは一時領域にZIPを組み立て、[既存validator](../scripts/verify_tauri_evaluation.py)を通してから出力先を作る。
license inventoryと本文の欠落、未知の実行物、本文hashの不一致は生成時にも拒否する。
入力の不正時は固定codeだけを出し、引数原文、例外原文、端末の絶対パスを診断へ複製しない。
これは任意のファイル内容に含まれる秘密を発見するscannerではないため、入力するlicense本文やinventoryは既存の収集手順で選別する。

## 版とビルドの対応

| 識別情報 | 正本と限界 |
| --- | --- |
| source | 全40桁のcommit SHA。専用CIはPR headまたはmain pushのSHAをcheckoutし、`git rev-parse HEAD`をmanifestへ渡す |
| GUIの開発版 | Cargo、Tauri設定、npmはいずれも`0.1.0`。これはソースの宣言で、配布EXEから独立に照会した版ではない |
| 旧GUIの開発版 | `v0.2.1-dev`。Tauriの`0.1.0`とは別系統 |
| 接続版component版 | 準備JSONでは`unverified`のみ。製品版、build版、IPC版を同一視しない |
| 配布単位 | `RescueHostTauriUnwiredEvaluation-x64.zip`のSHA-256と、内部の各fileのSHA-256。manifest自身は内部files一覧へ入れず、ZIP全体のhashで対応づける |
| 起動試験 | 同じsourceとZIP hash、展開したEXE hash、CI runとattempt、対象OS、WebView2版、終了と残留の結果を一つの候補へ結ぶ |

専用CIは固定Node、Rustとlockを使ってfrontendとlicenseを準備し、`--release --locked --features custom-protocol`でEXEを作る。
同じrunで未署名状態を確認し、生成後のZIPを独立の期待source/EXE hashで再検証してから展開する。
展開した同じrelease EXEの起動、IPC、通常終了を試験し、その後の成功stepだけが評価ZIPをartifactとして保存する。
失敗時のartifactは評価用ZIPを含めず、検査対象の診断JSON等に限る。
実際に成功した候補として扱うには、そのsource SHAのrunが終端まで成功し、artifactと証跡が残っていることを確認する。

一方、生成CLI単独は渡されたEXEの実装、PEのCPU、署名、埋込み版、release build、sourceとの来歴を証明しない。
任意の非空fixture bytesも試験入力として受け取るため、生成成功だけでWindows実行物を検証済みにはできない。
manifestの`buildProfile=release`や`signature=unsigned`は生成時の宣言で、実物との対応は上記CIの観測と合わせて読む。
hashは破損や差替えとの照合に使い、配布元の真正性や再現ビルドの保証には使わない。

## 起動前にそろえる証拠

未接続GUI候補では、評価者が取得したZIPと外部記録のhash、完全source SHA、候補の専用CI結果を対応づける。
ZIP内部のmanifestと実fileのhashをvalidatorで照合し、未知の同梱物やgateの開放があれば停止する。
既存WebView2、Windows x64、通常ユーザーでの実行条件と、同じapp identifierの既存データの有無を確認する。
Runtime欠落、OS警告、終了未確認は阻害として記録し、installerによる補完や保護設定の変更で回避しない。
具体的な評価操作は既存の[同梱説明](tauri-evaluation-readme.txt)を参照する。

実接続版は、binaryの来歴確認に加えて、サービスprovisioning、保護stdio、operator承認、component版、依存runtime、Windows上の保護境界、配布の信頼根が未確定である。
準備validatorの`blockers`はこの7分類を保持しており、GUIの表示成功やGoの部品試験で解消した扱いにしない。
新しいサービスの登録、guest作成、sshd設定変更、実SSH接続は、この点検では行わない。

## 匿名診断と保持

配布検査の成功記録はsource SHA、ZIP/EXE hash、file件数、backend不在、gate閉鎖を含む固定schemaのJSONを使う。
生成失敗は固定codeだけとし、生の例外、入力値、username、IP、SID、環境変数、端末pathを出力しない。
ライセンス本文を含むZIP全体を匿名診断ログとして収集しない。

Windows native試験は既存のinventoryと、所有processの終了結果、匿名化したdriver段階を別証跡として保持する。
専用CIのartifact保持は7日で、[診断feature専用CI](../.github/workflows/tauri-startup-diagnostic.yml)のEXEとZIPは配布artifactへ含めない。
通常配布候補のビルドには`startup-diagnostics`を指定しない。
一般利用者向けの診断bundle収集器、保持期限の自動制御、profile全体の匿名化は未実装とする。

## 終了と清掃の範囲

未接続Tauri ZIPにはinstaller、service登録、guest作成を含めない。
終了後の削除対象は今回専用の展開directoryと、今回取得したZIPに限定する。
削除前に今回起動したappとWebView子の終了を確認し、確認不能なら削除を完了扱いにせず阻害を記録する。
名前一致による他processの一括終了は行わない。

WebView2が`com.o2connective.rescue-host.shell`に対応するユーザーデータを作る可能性がある。
既存profile、Runtime、旧Tk state、鍵、他の利用者の資源を巻き込む自動削除は提供しない。
旧Tkの回復状態は[既存の回復手順](recovery.md)へ委ねる。
未来の接続版に必要なservice停止、同じ所有者の子回収、アカウント失効と削除、異常終了後や再起動後の清掃は、設計とWindows実測が残る。
単発版の一時nonadminアカウントと将来常駐版の専用保持アカウントは、配布点検から実作成済みへ進めない。

## この変更の検証境界

生成CLIで、license inventory不足の拒否と、引数エラー時の原文非表示を赤から緑で確認する。
未知の実行物、破損inventory、本文欠落、本文hash不一致は実装後の回帰として確認する。
正常fixtureは既存の未接続manifestとhash形式を維持し、validator試験にも同じlicense fixtureを使う。
fixtureの成功を実Windows、署名、OS保護、実SSH、アカウント清掃の成功へ読み替えない。
最終headの独立レビューとCI結果はPRへ記録する。
