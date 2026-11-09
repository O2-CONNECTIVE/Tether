# Tauri の最小未配線 shell

[開発手順](development.md) / [設計基準案](design-accessibility.md) / [通信契約](communication-contract.md)

## 今回の意図と受入範囲

tokens → 共通部品 → 画面の最初の縦断として、Rust の狭い IPC から「バックエンド未接続、開始不可」を読み取ります。
既存[Issue #16](https://github.com/O2-CONNECTIVE/Tether/issues/16)と[操作性 #25](https://github.com/O2-CONNECTIVE/Tether/issues/25)の native shell 段階で、実通信を提供する変更ではありません。
視覚上は既存のライト tokens と部品を使い、主状態、許可相手と範囲と期限、目立つ停止、独立して開ける診断の順に読み取れる画面にします。

表示状態は「状態を確認中」「バックエンド未接続」「状態を取得できません」の三つです。
どの状態でも開始と停止は無効で、許可条件と終了状態は未確認のままです。
接続中、終了確認済、残り時間の成功表示は作りません。
ブラウザー表示で Rust IPC がなければ、その事実を示します。

受入は、IPC の失敗、不正応答、待機切れでも成功表示にしないこと、診断が IPC を待たずに開くこと、keyboard と focus と拡大の確認です。
画面破棄後の遅延応答を無視する UI 境界を検証します。
native のウィンドウを閉じる実操作とブラウザーの破棄試験は区別します。

Go、SSH、sidecar、資格情報、保存設定、旧セッション回復、配布、署名、更新、dark theme は非目標です。
既存 Tkinter の起動経路と製品ゲートを維持し、未公開 backend のコードを取り込みません。
認可、TTL、失効と終了の根拠は既存契約が所有します。

## IPC と権限

shell 専用 `get_shell_status` は `{ "request": { "protocolVersion": 1 } }` だけを受け付けます。
`request` 内の未知 field と未対応 version を拒否し、出力の型と値も UI で検証します。
この version は shell 専用で、未確定の Go 製品 protocol の採用ではありません。
Rust は未配線である事実だけを返し、既存の TCP や別プロセスの終了を観測しません。

application manifest と `main` 限定 capability で、この command だけを許可します。
shell、filesystem、外部 URL、イベント購読、プロセス起動の permission は追加しません。
bundled page だけを使い、CSP と navigation の制限、React の text node 表示で外部ページと HTML 注入を避けます。
ウィンドウを閉じても通信終了の証明にはならないことを画面に示します。
sidecar の起動、親死、中断、停止のライフサイクルは未配線です。
製品から参照しない [Rust の有限自己子 fixture](rust-process-fixture.md)で、所有子の起動・応答上限・停止と回収の試験準備を行います。
この試験は実 Go の protocol 採用や sidecar 配線ではありません。

## 検証と依存の扱い

TDD の境界は GUI controller から Rust IPC への要求と応答です。
ブラウザーの IPC fixture、Tauri の mock runtime による command 試験、native build、実機起動を別々に記録します。
fixture で実接続や実停止の成功を作りません。
システムに Rust を導入せず、標準 GitHub hosted Windows runner の専用 CI で公式 toolchain を使います。
CI は readonly、action SHA 固定、有限 timeout、secret なし、deploy なしです。
build が成功しても、新しい PC での起動、native WebView、支援技術、OS の DPI と高コントラストは合格済みとしません。

[React 移行とアイコン方針](tauri-react-ui.md)では同じ IPC 契約を維持し、表示層だけを置き換えます。
版、lock、直接依存の license と実行手順は [shell README](../ui/tauri/README.md)にまとめています。
Rust の lock は自己所有 CI で生成した候補を回収しました。
全406 packages の内訳は自作アプリ1と外部依存405で、外部依存が公式 registry と checksum を持つ形であることを確認してコミットしました。
以後の CI は `--locked` を使い、lock を生成して成功扱いする工程を残しません。
完全な依存 license 棚卸し、配布 bundle、WebView2 のない環境とオフライン配布は[配布条件](distribution.md)の後続で扱います。

## 確認した結果と未確認

GUI controller の5件は、未配線の応答、不正な成功風応答、IPC error、有限待機、画面破棄の順に赤を観測し、最小実装で緑にしました。
Chrome の3件は実 HTML/CSS を描画し、keyboard、focus、320 CSS px、文字200%、診断の独立開閉、HTML風応答の拒否、待機中の画面破棄を確認しました。
reduced motion と forced colors はブラウザー模擬の確認であり、実 OS 設定の確認ではありません。

Windows CI の[最初の失敗](https://github.com/O2-CONNECTIVE/Tether/actions/runs/40494752899)は、試験 EXE の `STATUS_ENTRYPOINT_NOT_FOUND` で、TDD の赤には数えません。
[再現と import 診断](https://github.com/O2-CONNECTIVE/Tether/actions/runs/40495893299)では同じ失敗を再現し、直接 import の欠落はありませんでした。
[Tauri の既知問題](https://github.com/tauri-apps/tauri/issues/13419)に沿い、試験 EXE にも Common Controls v6 の manifest を埋め込むことで起動できました。
manifest は所有するアプリと試験に対する `asInvoker` の設定で、OS やサービスの設定は変更していません。
その後、[公開IPC試験の赤](https://github.com/O2-CONNECTIVE/Tether/actions/runs/40496623565)で `SHELL_NOT_IMPLEMENTED` を観測し、[最小応答の緑](https://github.com/O2-CONNECTIVE/Tether/actions/runs/40497095768)へ進めました。
Windows の6件は Tauri mock runtime と実際の command handler と capability を通る試験であり、WebView2 の操作試験ではありません。
同じ CI で Windows の debug native executable の build も成功しました。
installer や配布用 Release は生成していません。

![実HTML/CSSのブラウザー描画。Rust IPCがない取得不能状態であり、native起動や実接続の証拠ではありません。](assets/tauri-native-shell-browser.png)

後続の [Windows hosted native smoke](tauri-native-smoke.md)で、限定した WebView2 起動・実 IPC・通常 close と観測済み子の終了を確認しました。
新しい PC、配布物、native keyboard、実 DPI、高コントラストの実 OS 設定、スクリーンリーダー、macOS と Linux の native build は未検証です。
browser の破棄で遅延応答を抑止できたことを、native process や所有通信の終了証明へ読み替えません。
この shell には終了すべき sidecar や接続資源を作る処理がありません。
同一ユーザー攻撃者の隔離も完成したとは扱いません。

公式参照: [Rust command](https://v2.tauri.app/develop/calling-rust/)、[capability](https://v2.tauri.app/security/capabilities/)、[CSP](https://v2.tauri.app/security/csp/)、[公開IPCの試験API](https://docs.rs/tauri/2.12.5/tauri/test/index.html)。
