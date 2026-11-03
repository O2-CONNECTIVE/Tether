# 未配線の Tauri React shell

[画面層とアイコン方針](../../docs/tauri-react-ui.md) / [IPC と確認範囲](../../docs/tauri-native-shell.md) / [tokens の状態見本](design/README.md)

Rust が接続機能の未配線状態だけを返す開発用 shell です。
通常 App の [Mac 状態表示経路](MAC_STATUS_APP.md)は backend 固定 platform を使います。Mac の本番応答は native 入力未接続だけで、観測注入は test build と browser fixture に限定します。
操作する担当者向けの[操作側GUIプロトタイプ](operator/README.md)は別入口・別buildです。host shellと実接続の配線は共有しません。
Go、SSH、sidecar の起動や停止は実装していません。
既存 Tkinter、旧記録の回復、製品接続ゲートと起動方法は変更しません。
開始と停止は常に無効で、通信終了は未確認のままです。

## フロントエンドの確認

Node 24 以上と導入済み Chrome を使い、`ui/tauri` から実行します。
システム toolchain やブラウザーのインストール、OS 設定変更は含みません。

```bash
npm ci --ignore-scripts --no-audit --no-fund
npm run typecheck
npm run lint
npm run knip
npm run knip:production
npm test
npm run build
npm run test:browser
```

型検査は strict `tsc --noEmit` で app、controller、tests、TS config を検査し、Vite build とは別に実行します。
ESLint は typed recommended、Hooks recommended、Promise と bootstrap 外の native IO import 制限を使います。
MJS の build/license script と ESLint config は通常 lint の対象です。
Knip は Vite の HTML module entry、実際の tests と package scripts から到達性を解析します。
全 src を entry にせず、所有 shadcn 部品も未使用検出の対象です。
通常解析と production 解析を分け、ignore や無視する依存は追加していません。
文字列の IPC command と Rust capability の一致は境界試験で扱い、Knip の対象外です。

Vite はローカル依存を `dist/` に bundle し、外部 CDN を必要としません。
`design/tokens.css` が正本で、shadcn の既定 palette を取り込みません。
build は npm runtime の license と notice 全文、所有 shadcn の MIT notice を `dist/THIRD_PARTY_NOTICES.txt` に同梱します。
生成物はコミットしません。
CI の controller/build/browser は同一 step 内で順に実行し、各非0終了を伝播します。build と browser の別 step 化は後続の設計です。
ブラウザー試験は自己所有 loopback サーバーと既存 Chrome を使い、終了後に破棄します。
`SHELL_SCREENSHOT_DIR` の指定時だけ実描画を保存します。
IPC fixture は表示と操作の検証であり、Rust や実通信の成功証拠ではありません。

Mac status入力の別型・strict decoder・pure feed state候補は[Mac status core](MAC_STATUS_CORE.md)を参照してください。固定公開050e48fのSwift golden156行のみローカル実行済みです（このPR基点の同tree内Swiftとは異なります）。Rust goldenは既存Rust CIでこれから検証し、既存Swift CIには新golden harnessは未接続です。両言語実行同値とnative pipe配線は未完です。

## 内部 reader と状態表示の境界

`ReaderTask` は既に渡された byte stream を別 worker で読み、最大16KiBの JSONL frame を既存 codec / coordinator に渡します。
queue は1 frame、worker が保持する frame と4096 byteの読取 buffer は別に有限です。
不正 frame・世代相関違反・I/O失敗・panic・cancel は閉鎖し、後から届く入力や EOF で最初の fault を消しません。
EOF の通知は queued frame の処理と worker の実 join 後です。
所有子 adapter は既存 `ChildOwner` の実回収を別に観測し、停止 receipt、peer の終了主張、reader EOF、process 回収を同一視しません。

汎用 reader の cancel は協調停止で、任意の blocking `Read` を中断しません。
poll の予算切れは `Pending` と所有 handle を残し、OS scheduling や最終 join の実時間上限を保証しません。
未完了 reader を Drop すると detach し得るため、実 endpoint の取消完了と owner 寿命を確立するまで製品配線は禁止です（[Rust JoinHandle](https://doc.rust-lang.org/stable/std/thread/struct.JoinHandle.html)）。
Windows 専用の内部 `NativeClient` 候補は、固定 local pipe、個別 access、実 WinAPI の read/write と server process 照合へ接続します。
同じ cancel token で対象 OVERLAPPED の取消を要求し、実完了・reader join・最後の owner の解放を分けます。
Go localcontrol の1 message = 1 JSONL frameを、内部16KiB bufferで全体検査してから reader へ渡します。
[候補 client の確認範囲](WINDOWS_CONTROL.md)と[localcontrol 境界](../../docs/windows-local-control.md)を参照してください。
認証済み bootstrap と Go 起動は未接続で、既定 runtime は endpoint なしです。実 pipe/WinAPI 動作、別 account の query 権限、OS 保護は未実証です。

shell は [Tauri managed state](https://v2.tauri.app/develop/state-management/) に reader の所有者を保持する入口を用意しますが、既定は endpoint なしです。
GUI の5 field応答は未接続・開始不可・停止不可・終了未確認を維持し、peer の active / terminated 主張では解除しません。
reader fault は4種類の固定 code だけ診断へ渡し、生 error / HTML / 未知の object を表示しません。
試験は有限 memory IO、既存の無害な自己子の実 stdout・回収、Tauri IPC、browser fixture を分けます。
自己子の試験v1応答は製品 control/1として拒否し、実 Go / SSH の成功証拠へ流用しません。

## 固定した依存と由来

| 直接依存 | 版 | license / 用途 |
| --- | --- | --- |
| React / react-dom | 19.3.1 | MIT / UI runtime |
| Lucide React | 1.62.0 | ISC と Feather 由来 MIT の全文 / 必要 SVG のみ |
| Radix Slot / Collapsible | 1.5.0 / 1.2.1 | MIT / shadcn primitive |
| class-variance-authority | 0.7.1 | Apache-2.0 / Button variant |
| Vite / React plugin | 8.3.5 / 6.1.2 | MIT / build のみ |
| TypeScript | 6.0.3 | Apache-2.0 / typescript-eslint 対応範囲 `<6.1` 内 |
| ESLint / typescript-eslint / Hooks plugin | 10.13.0 / 8.72.1 / 7.1.1 | MIT / lint のみ |
| Knip | 6.43.0 | ISC / 未使用検出のみ |
| Playwright Core | 1.64.1 | Apache-2.0 / 既存 Chrome 試験のみ |

型定義等を含む全 npm 解決版、公式 registry と integrity は `package-lock.json` に固定します。
shadcn は npm runtime を追加せず、[固定元・hash・改変内容・license](third-party/README.md)を記録した所有ソースです。
Lucide の package 全体を動的に読み込まず、画面で使う5種類を named import します。
製品 UI は絵文字禁止で、SVG は可視ラベルと組み合わせます。
アプリと既存 R の OS app icon は repo の MIT に従います。
OS app icon は識別用の暫定資材で、Lucide の操作アイコンとは別です。

## native と配布の確認範囲

専用 readonly Windows CI は Node 24.22.1、公式 Rust 1.99.1 を固定します。
Rust 直接依存は Tauri 2.12.5、tauri-build 2.7.4、serde 1.0.229、serde_json 1.0.154 と、Windows 限定の windows-sys 0.61.2（MIT OR Apache-2.0）です。Cargo.lock の既存解決版を維持します。
承認済み toolchain のある環境だけ、frontend build 後に実行します。

```bash
cargo +1.99.1 fmt --manifest-path src-tauri/Cargo.toml --check
cargo +1.99.1 clippy --locked --manifest-path src-tauri/Cargo.toml --features custom-protocol --all-targets -- -D warnings
cargo +1.99.1 test --locked --manifest-path src-tauri/Cargo.toml --features custom-protocol
cargo +1.99.1 build --locked --manifest-path src-tauri/Cargo.toml --features custom-protocol
cargo +1.99.1 run --locked --manifest-path src-tauri/Cargo.toml --features custom-protocol
```

ローカル Rust は不在で、native build は CI の証跡と分けます。
通常の close で通信終了を捏造しません。
Windows hosted runner の限定した WebView2 起動・実 IPC・通常 close は [native smoke の実測](../../docs/tauri-native-smoke.md)で確認しました。
新 PC、配布 executable、native keyboard、スクリーンリーダー、OS DPI と高コントラスト、macOS/Linux の native 表示は未検証です。
ブラウザーの文字200%と forced colors/reduced motion 模擬だけで OS 実機を合格にしません。
CI は installer、bundle、署名、更新、Release を作成・公開しません。
Rust を含む完全な配布 license 棚卸し、WebView2 未導入とオフライン配布は[配布条件](../../docs/distribution.md)の後続です。
