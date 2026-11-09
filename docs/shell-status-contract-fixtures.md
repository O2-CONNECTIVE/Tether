# 未配線shell v1の共有契約fixture

[既存の通信契約](communication-contract.md) / [shellの確認範囲](tauri-native-shell.md) / [Issue #46](https://github.com/O2-CONNECTIVE/Tether/issues/46)

[共有JSON](../ui/tauri/contracts/shell-status-v1.json)は、現在の`get_shell_status`要求と固定未接続応答だけを表します。
Go process/stdio/SSHのprotocol案ではなく、認可・期限・失効の権威も追加しません。
評価用のstart/stop要求やprivateな評価ソースは含みません。

## 検査する境界

- Rustの既存mock IPCで、正例の応答全文とfixtureが一致することを確認します。未知版、欠落・型不正、未知field等の要求8例を拒否します。
- UI controllerの注入invokeで同じ正例を読み、`CHECKING → SHELL_UNWIRED`だけを確認します。接続成功風・許可拡大・終了確認風・欠落・型不正等の応答12例は`INVALID_RESPONSE`へ進みます。
- fixtureの読取不能・必要項目不足・ケース数不一致・ID重複は試験を失敗にします。

これらは実装済み境界の後付け回帰で、新規接続機能のTDD成功ではありません。
Rust mockとUI注入試験は、それぞれの公開interfaceで同じfixtureを使う互換確認です。
実WebViewを介した要求配送、Goとの互換、実backendやSSH、子process終了は検証しません。

## 実行

[Tauriの既存手順](../ui/tauri/README.md)に従い、`ui/tauri`で`npm test`を実行します。
共有fixture試験は既存の`tests/controller.test.ts`から呼ばれ、別のpackage scriptは不要です。
Rust側は同手順の固定toolchainによる`cargo test`へ含まれます。
新しいtoolchain導入やnative実起動を、この文書だけで承認済みとは扱いません。

fixtureを書き換える場合は、現行shell実装との一致と拒否の意味をレビューします。
新しいGo IPCや権限を採用する変更は、既存の契約・脅威モデルで別途合意します。
他のCIのgreenで[main native失敗 #45](https://github.com/O2-CONNECTIVE/Tether/issues/45)を解消済みにせず、製品接続gate閉鎖を維持します。
