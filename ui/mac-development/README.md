# Tether Development — 別の開発用 Mac GUI

通常の Tauri GUI と署名 gate を変更せず、#333 の専用開発 CLI を固定の
同梱位置から起動する、ローカル評価専用の AppKit アプリです。
Developer ID の結果をモックに置換しません。別 bundle ID、常時警告、
別 enrollment を使う、明示的に未署名の開発用 trust model です。

## 操作と制限

- アプリを開くだけでは helper を起動しません。「開発ホストを準備」で
  固定同梱 `tether-dev-resident foreground` を一度だけ起動します。
- 同梱 helper の SHA-256 は取り違え検出です。publisher 認証や同一ユーザーの
  改変への防御、コード署名・notarization の代替ではありません。
- 新鮮な owner 状態に対する ON/OFF だけ。固定検証操作以外の command/path/env
  入力はありません。重複クリックと pending 中の操作を拒否します。
- 受領 receipt と状態変更、認証済み接続、停止回収を区別します。未知の状態、
  JSON 異常、時刻逆行、source age 3秒以上では操作を止めます。
- Quit とウィンドウ閉鎖は同じ元 owner に close を一度要求します。
  confirmed 終了frame、EOF、元 child の正常終了/回収が揃うまで終了を許可しません。
  unknown を kill・再起動・時間切れで成功へ変更しません。
- **起動から15分で終了を要求する限定CLIです。永続運転ではありません。**
  LaunchAgent、ログイン起動、自動再起動はありません。期限終了時の非0 exit は
  保守的に回収未確認として残します。server-side bounded pilot は別途必要です。
- 起動失敗の診断や caller、credential、endpoint、生JSONを表示・保存しません。
  実ユーザーでの実行は現在ユーザー権限です。専用OSユーザーは自動作成しません。

## ソース検証と非実行 build

macOS、既存 Swift 6 toolchain、リポジトリ指定版の Go が必要です。
追加依存や package plugin はありません。以下は GUI/host を起動しません。

```sh
cd ui/mac-development
swift build
swift run DevelopmentChecks
# リポジトリ指定の Go を使う。絶対パスを選ぶ場合は呼出側で設定する。
(cd ../../backend && go build -tags tether_development -o ../ui/mac-development/.build/tether-dev-resident ./cmd/tether-dev-resident)
python3 scripts/package_local.py
```

Swift cache の標準保存先が使えない環境では、checkout 内の `.build/module-cache`
を作成し `CLANG_MODULE_CACHE_PATH` と `SWIFTPM_MODULECACHE_OVERRIDE` をその絶対pathに
指定できます。権限制約を解除する操作は含みません。

packaging は固定 `.build` 内のローカル出力を別名 app にコピーします。
既存出力は上書きせず、失敗時の部分生成物は保持します。署名、インストール、
登録、実行、外部公開、credential 作成・コピーは行いません。
実行前に同じsource/buildの確認、専用 enrollment/identity、server期限、接続先、
公開範囲、実 credential と必要な個別承認を揃えてください。
[限定CLIの信頼境界](../../docs/development-resident.md)をそのまま維持します。

## 証跡の境界

DevelopmentChecks は匿名の JSON/state fixture です。AppKit compile、実描画、
owned process/pipe、実 host、ChatGPT 接続、OFF/取消/終了回収は別の検証です。
この GUI の追加だけでは ChatGPT 実接続や永続常駐は完了しません。
EOF・process exit単独を停止保証にせず、解消していない状態を保持します。

### 2026-11-11 ローカル検証

- macOS 27.2.1 / Swift 6.4.2: AppKit と全 check target の build 成功。
- DevelopmentChecks: 合成 protocol/state/部分frame期限40件成功。
- ProcessChecks: 実 pipe と無害な合成childで重複開始/close、明示終了＋EOF＋回収、
  hash不一致の起動拒否、終了報告なしのexit/EOF拒否を確認。
- package helper: 合成bytesの配置、上書き・symlink拒否の2試験成功。
- 固定dev CLIは Go 1.27.3 で非実行build。同梱appの初期画面、ON/OFF無効、
  開発表示、未起動時の終了操作を実GUIで確認。helper準備は押していません。
- Python 3.13.2 の tokens/sizes は成功。共通checkは `_tkinter` 欠落で失敗し、
  Python既存試験は未実行です。CI参照3.13.17とは異なります。
- 独立静的レビューの2指摘（read前の時刻採番、部分frame期限の確認順）を修正し、
  差分再レビューで追加の具体的不具合なし。CI/実通信/実credentialは未実行です。

追加検証入口:

```sh
swift run ProcessChecks
python3 -m unittest discover -s scripts -p 'test_*.py' -v
```

このローカルbundleは本人のsource評価用で、一般配布物ではありません。
配布用の署名・notarization・license/notice閉包・binary監査は別受入です。

専用CI [Development Mac GUI source and synthetic pipes](../../.github/workflows/mac-development.yml)
は固定checkoutを記録し、Mac ARM64 / Xcode 26.5でappのbuild、DevelopmentChecks、
ProcessChecks、合成bytesのpackaging試験を実行します。app/実resident起動、
credential、enrollment、公開、配布artifactは作成しません。ローカルのSwift 6.4.2
での確認とは別の実行結果として、固定headのCI終端を確認してください。

### 操作停止後の回収確認

開発 GUI は操作を許可する状態と、元の子プロセスの回収証拠を分けます。
完全な状態通知の期限切れ・拒否で操作を停止した後も、同じ専用 pipe から来た
厳格な終了通知を読みます。新しい ON、操作、helper の置換は許可しません。
`confirmed` の終了通知、EOF、元の child の通常 exit 0 と reap がすべて揃った
場合だけ、「停止して終了」で GUI を閉じられます。操作停止の事実は残ります。

未知の回収結果、不正な終了通知、終了後の追加データ、部分 frame の期限切れ・
過大入力、読取り失敗、clock rollback、非 0 exit は回収成功に変更しません。
EOF や process 不在だけでは終了可能にせず、同じ owner を保持します。
終了操作の連打や再接続で証拠を作り直す経路はありません。

診断表示は固定の理由コード、GUI/helper PID、検証した helper hash の短縮値、
終了通知・EOF・reap・正常 exit の有無だけです。設定本文、鍵、token、caller、
endpoint、原 frame、stderr は表示・保存しません。hash は publisher 認証では
ありません。起動前の一般的な失敗を、回収済みと推測する変更は含みません。

### 初回接続中の観測と回収不明の区別

Go mapper は初回 ON の認証前に `standby=unknown` を出します。この値だけでは
回収不明を意味しません。受理済みの同じ ON 要求に対応する pending transition、
connecting・idle・cleanup confirmed・caller null・両操作不可が全て一致する時だけ、
GUI は「接続を確認中・待機 ON は未確認」と表示して待ちます。新しい要求や実行は
許可しません。cleanup、receipt、transition、terminal の unknown は従来どおり
回収証拠を無効化し、以前の fault/close を解除しません。

`backend/internal/residentpipe/development_contract_test.go` は実 mapper と Encode の
出力を `Tests/DevelopmentChecks/Fixtures/mapper-pending.jsonl` と byte 比較します。
Swift は同じ公開 fixture を読むため、片側だけの手書きモデルの一致ではありません。
Go の共通検査と Mac の DevelopmentChecks を両方実行してください。閉じる通知と
negative mutation は別の合成入力であり、実通信・実 host の回収を証明しません。

実試験では ON を一度押した後に owner-result-unknown、EOF/reap/exit0 を観測しました。
当該実行の raw frame は取得していません。修正前の Swift に Go mapper の接続中出力を
与える回帰試験は失敗し、契約の不一致を再現しています。動作中の旧 GUI の証拠を
後から補完したり、unknown の状態をリセットする変更ではありません。

公開 JSONL は wire の LF も含めて固定 bytes とし、完全一致 path の `.gitattributes`
で checkout 時の文字変換を無効にします。`test_development_fixture_bytes.py` は
一時 Git repository の `core.autocrlf=true` checkout で同一 bytes を検査します。
Go 側の byte 比較は改行を正規化しません。Windows の実 normal/race CI はこの
checkout 合成試験とは別に必要です。
