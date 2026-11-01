# Tether: 検証状態と一時派遣の残件

[プロジェクト入口](../README.md) / [文書索引](README.md)

更新: 2026-11-01。これは限定 pilot の証跡索引と再開手順であり、起動許可・配布許可・認証情報ではありません。

## 確認した境界

| 項目 | 状態 | 含まない保証 |
| --- | --- | --- |
| Mac CLI と実 transport | 登録済み 1 名・1 Mac で固定 printf、sleep の正常実行・取消・要求なし期限切れ・実行中通信断を確認 | 他端末・他利用者、任意 workload、sandbox、逃げた子孫の永久停止 |
| MCP 静的 discovery | 認証・member 検証後、host grant なしでも固定 schema を返す。実行には別の有効な host grant が必要 | discovery session の後付け grant への自動束縛 |
| Claude Code stdio fixture | 固定 4 schema と生成 tool call を確認、実 dispatch 0 | 実 HTTP/OAuth |
| Claude Code HTTP/OAuth | 実認証・接続、画面上の 4 tools を確認。実 tool call 0 | 個別 schema 全比較、HTTP 404 後の自動再 initialize、Claude からの実 command |
| dot の MCP 登録 | workspace が MCP plugin 作成を 403 拒否 | Claude の成功でこの権限制限が解決したとはしない |
| 一般利用者への一時派遣 | 前景 CLI は実装済み。配布・初回登録・一般利用者受入は未完 | 無認証 remote install、自動承認、恒久常駐 |

実機試験は後述の固定 snapshot に対するものです。後続の docs や package 変更で同一バイナリの証拠を新しい実装全体へ広げません。以前の unknown は成功へ書き換えません。

## 人間側の実装済み入口

`backend/cmd/rescue-mac-agent` の `-config <path>` が入口です。

1. 受領者が自分の Mac で前景起動する。最初の配送は受領者による取得・明示起動を最小経路とする。
2. 保護された設定と、同じホスト内の既登録鍵を読み取る。公開 fingerprint の照合を省略しない。
3. 現在ユーザー権限・隔離なし・期限・相手鍵を端末で示し、fresh 確認文字列を照合する。package に承認や grant は入れない。
4. 同じ owner を 1 回だけ登録する。再接続・自動復活はしない。
5. 取消・期限・通信断では同じ owner の回収へ進む。unknown は警告・非成功・再開不可として扱う。

既存 CLI の設定契約は [agent CLI](agent-cli.md)、権限・回収の制約は [Mac runtime](mac-agent-runtime.md) を参照。
CLI の source 上の期限上限と、実試験で承認された 180 秒 grant / 5 分公開入口は別です。例示設定の長い期限を試験承認と見なさないでください。

## 最小の受領者手順（未実演の runbook）

以下は既登録 Mac に対する準備手順です。新しい鍵・本人 identity・gateway mapping がなければそこで停止し、初回登録を別途行う必要があります。

1. 対象 architecture のバイナリ、source snapshot、license、外部で取得した期待 hash を照合する。同梱 manifest だけでは配布者の真正性を証明できない。
2. 既存の保護された local config / host key / gateway principal pin を使用する。秘密鍵・OAuth token を package やチャットへ入れない。
3. gateway と認証の準備、および今回の短期公開・操作範囲が許可済みであることを確認する。入口が閉じていれば無断で開けない。
4. `rescue-mac-agent -config <保護された設定の絶対 path>` を受領者の端末から起動し、その回の表示内容を確認して入力する。引数で同意を渡す仕組みはない。
5. 終了理由と cleanup を確認する。正常 command 終了、agent の期限終了、unknown terminal の終了値は区別する。失敗・unknown を新 request ID で盲目的に再実行しない。
6. 試験終了時は今回の入口・gateway・agent の停止を確認する。保持した鍵・設定・archive は無断削除しない。

この手順だけで未登録ホストを操作できるわけではありません。既存管理接続による配送も、相手側明示起動の代わりとして未実証です。

## 次の順序

1. **配布準備の残件:** 読取専用status .appのローカルbuild・合成window試験は完了。agent packageのnotice実取得・依存閉包・秘密/build path監査は未完で、実archive作成・一般配布は保留。実agentとGUIの同時動作も未検証。自動install、署名、認証、公開を開始しない。
2. **個別の実試験:** Claude の実 tool call → gateway → 承認済み Mac の固定 printf。今回の discovery 成功と native 成功をつなぐ最後の実証。新しい短期 grant / 公開窓 / 認証の必要性を確認してから実行する。
3. **人間の派遣受入:** 既登録の受領者が配布物だけで起動・同意・終了できるか確認。その後に初回 enrollment の手順を評価する。
4. **4 名分離:** 残る利用者の verified identity と鍵登録は未完。架空 subject やメールだけの代替認可は不可。source の拒否試験と実 enrollment を区別する。
5. **dot:** workspace 管理者の MCP 利用許可変更を確認してから、失敗した作成操作の再試行可否を判断する。別 client による権限迂回はしない。

## 固定証跡（秘密・実設定を含まない索引）

非公開の実ログ・個人 identity・実 host path は repository に複製しない。

- Native 4-case manifest SHA256: `8c8253943691585a462d92b0658df0683d4c8b334924dfd0c75c058da1635c0c`
- Native 実行 binary SHA256: `1372837640da8f572194d5df069cb8a93d8cb3cfc94e2242f867c8a62df04c24`
- 静的 discovery 218-file manifest SHA256: `9952be3a9dd1cd58f32337baff12faa1e4b2d7cee5b75d6c6a52eb8635b75346`
- Claude stdio result manifest SHA256: `bfee312d051e5f2500d2ef258c9f7432a2e396a453f78520ae6cbc7cbc232bc1`
- Claude HTTP/OAuth final manifest SHA256: `598df8965a6bdd3c543537abaae1b0cd22d21e4d568743a9f82b5e7177497895`

HTTP/OAuth 試験後は公開入口閉鎖、専用 container exit 0、専用 port 待受なし、CLI 終端を確認。本人の認証 clear 報告と設定上の保守的な grant 期限経過を記録しましたが、provider 失効を直接証明したとは扱いません。OAuth 設定そのものは本人が保存した状態のままです。

## ローカル評価 package の source 準備

`scripts/package_mac_agent.py` は明示指定した既存バイナリ・期待 SHA256・Darwin architecture・source manifest SHA256・固定 license 本文から評価 archive を作るツールです。新規 install / 実行 / enrollment / 署名 / 公開は行いません。

- 同梱する同じ bytes の hash、Mach-O header / CPU / LC_BUILD_VERSION を検査し、宣言された macOS 最低版・SDK・linker を記録する。欠落・重複・不整合は拒否し、固定ファイル名・権限で梱包する。宣言値は実互換性や完全な Mach-O loader 妥当性の証明ではない。
- 既存出力を上書きせず、リンク・非通常ファイル・過大入力・hash 不一致を拒否する。
- 設定、鍵、token、ログ、任意 directory の内容は同梱しない。固定 license allowlist だけを読む。
- archive の `distributionStatus` は常に `blocked`。binary と source の対応、依存閉包・license 完全性、binary 内の秘密・build path の監査、署名・notarization が未確認のため、配布可能とはしない。
- exit 0 はローカル archive 作成を示すだけ。真正性・起動許可・配布許可ではない。

`scripts/tests/test_mac_agent_package.py` の合成 Mach-O bytes による 10 tests を通常実行と Python -OO で確認。arm64 / amd64、CLI 引数からの生成、固定 manifest、拒否、非上書き、検証後の入力変更でも同じ bytes の同梱を含みます。Mac Python 3.9.6 での対象 fixture のみで、全 CI / 全 tool tests は未実行です。実 binary の archive 作成・配布・受領者起動は行っていません。

## 同一環境での offline 二重 build（2026-11-01）

既存 trusted Go 1.27.3、Darwin arm64、CGO=1、`-mod=readonly -trimpath`、download 無効で新しい候補を 2 回 build し、両方 exit 0・同一 SHA256 `dcd8be47ce1921b3e780fe66c1eea9592ef05e725f6c3567303c6d28519a5cb8` を確認しました。732 source / lock ファイルの前後 hash は同一です。

- source manifest SHA256: `7ba249d7a0f028b85addfc368dc2faaf1ac0c6470968f3e9ab53b09a817bdbef`。未追跡 source を含む固定集合で、base commit だけを入力全体とはしません。buildinfo の `vcs.modified=true` は保持しています。
- 宣言 minimum macOS **26.0**、SDK **26.5**、linker **1267.0**。それ以前の macOS に対応するとは案内できません。この宣言は対応 OS 上での実動作保証でもありません。
- 同一 host / toolchain / cache での連続一致に限定され、別環境や clean build の再現性は未確認です。新しい候補は実行しておらず、以前の native 4-case PASS を引き継ぎません。
- 以前の実試験 binary の外部 module 3 件は固定 notices 対象と一致しますが、stdlib 内 vendored crypto / net / text と Darwin cgo の platform library / toolchain の inventory は別途整理が必要です。同一 license 本文 hash は法的な閉包の証明ではありません。
- 実取得 notice artifact は既存の限定記録から確認できませんでした。既存 collector の記録は fixture 検証までのため、実 archive 生成は保留です。fixture 本文を実取得の代用とせず、未確認の cache 読取り権限も拡張していません。

次に必要なのは、正確な取得元と対象を確定した notice artifact、その binary に対応する依存 / platform 条件・秘密 / build path の監査です。署名・notarization・配布・受領者での起動は別工程として残します。

## Mac の読取専用ステータス GUI（2026-11-01 source 実装）

[SwiftUI viewer の契約・build・検証](../ui/mac-status/README.md)を追加しました。既存 Windows Tauri を配線済みと扱わず、Mac 専用の小さな viewer を別に設けています。

- **実 producer に配線済み:** Mac CLI の明示 status mode から、同じ Session / 認証済み Host の純粋な集約状態を送ります。新しい承認・停止・再接続を発生させる観測 API ではありません。
- **実 viewer に配線済み:** 起動時に渡された専用 stdin pipe のみを読み、接続・承認残時間・管理中 worker 件数・停止/回収・terminal unknown を表示します。fixture 専用画面だけを作ったものではありません。
- **未実証の境界:** 実 grant を発行した Mac agent と GUI の同時起動は未実施。今回の Go/SwiftUI source・build、Go encoder→Swift decoder の合成結合、実 pipe helper、専用合成 NSWindow の検証は、その live 実演を代替しません。
- 状態の生成時刻は同じ Mac の sleep を含む連続時計で束縛します。3 秒を超えた報告・未来/後退時刻・不正 frame は、現在の接続状態として表示しません。
- GUI を閉じても agent は停止しません。writer の閉じた pipe / 満杯による失敗は status 配信だけを終え、既存の承認・期限・cleanup を変えません。
- 名前・command 内容・出力・鍵・token・endpoint・path は status schema にありません。件数は OS 全体の process 数ではなく管理 worker 状態です。
- GUI は開始・許可・取消を行わず、本人の fresh `/dev/tty` 同意は従来どおりです。0 秒・EOF・window 閉鎖を回収確認と読み替えません。

viewer は macOS 13 以上を宣言しますが、現在の agent build は macOS 26.0 を宣言しています。viewer の下限を製品セット全体の互換性と見なさないでください。一般配布用の署名済み GUI package / installer / 新端末 enrollment は未完です。

### 今回の検証結果

- Go: 許可範囲の 15 package、normal / race とも top 440 + sub 1060、fail / skip 0。vet、4 構成 compile、native Darwin CGO1 compile 成功。status 専用 helper の reader close / full pipe も成功しましたが、production agent の実行ではありません。
- Swift: 標準 build 成功、dependency-free core 21 checks、専用の実 NSWindow fixture 9 checks、失敗 0。Go production encoder の合成 frame を Swift decoder に渡す結合を含みます。XCTest module 欠落の初回試行は 0 件・環境失敗として保持し、成功へ読み替えていません。
- 表示: 同じ SwiftUI content の 8 fixture 描画（明/暗、更新停止の狭幅、文字 1.5 倍、回収未確認、未開始終了、終了報告）を確認。自分の NSWindow の表示・合成状態変更・閉鎖・reader join も確認し、他の desktop は撮影していません。
- 最終独立レビューで既知の Blocking はありません。手動 VoiceOver / keyboard 操作、macOS 13 実機、実 agent と GUI の live 同時動作、署名済み配布は未確認です。


## ローカル評価 status .app（2026-11-01）

読取専用SwiftUI viewerからローカル評価 `Tether Status.app` を生成しました。標準build、core 21 checks、共有viewのwindow 9 checks、packaging 10 checksが成功。実bundle内の実行ファイルは、no-inputと合成connected→staleの2つの所有window試験に成功し、通常delegate/reader join経由で閉鎖・exit 0・所有PID不在を確認しました。不正入力の検証は別の共有view試験で、bundleの3件目のlive試験とは数えません。

実agent、grant、command、認証情報、公開endpoint、署名identity、notarization、恒久installは使っていません。Finder/Gatekeeper起動、手動アクセシビリティ、macOS 13実機、実agentとのGUI同時動作は未検証です。linkerのad-hoc署名のみでresourcesは未seal、license inventory・release noticesも未完のため、bundle生成は配布許可ではありません。

[viewerの手順](../ui/mac-status/README.md)に契約と検証方法をまとめています。同梱はviewer executable・固定Info.plist・評価用noticeのみで、Go agent・鍵・設定・tests・fixture dataは含みません。既存native4case、Claude HTTP/OAuthの結果は別snapshotの証拠として保持します。
