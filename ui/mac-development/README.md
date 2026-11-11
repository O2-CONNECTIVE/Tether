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
