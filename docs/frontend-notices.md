# frontend runtime noticeの生成と照合

[配布条件](distribution.md) / [開発手順](development.md) / [追跡Issue #40](https://github.com/O2-CONNECTIVE/Tether/issues/40)

対象は公開済みfrontendのruntime npm依存とowned shadcnのlicense/notice本文です。
Rust、Go、Python、WebView2、OS、配布EXE全体の部品一覧や法的適否の判定は含みません。

## コマンド

`ui/tauri`から、導入済みNode 24以上で実行します。
CIの固定参照版は24.22.1、ローカルの別版はその環境だけの結果です。

```sh
node --test tests/notices.test.mjs
npm ci --ignore-scripts --no-audit --no-fund
npm run build
node scripts/notices.mjs --check
```

fixture試験は追加依存やネットワークなしで、匿名の一時コピーに対して実CLIを呼びます。
`npm ci`とbuildは公開lockに固定した依存を使用し、CIは生成と照合を別stepで実行します。
新しいcredential、OS設定、secretは不要です。

既定の`node scripts/notices.mjs`は`dist/THIRD_PARTY_NOTICES.txt`を生成します。
`--check`は同じlockと導入済みlicense/NOTICEから期待内容を作り、生成済みファイルと全文照合します。
欠落・改変・古い内容では非0終了し、checkがファイルを再生成して失敗を隠すことはありません。
package相対名、版、license表記と本文を保持し、件数とgenerate/checkの別を出力します。

## 拒否条件と限界

runtime対象0件、必要licenseの不存在・空本文、空shadcn本文、版/license表記の不足、不正なpackageパス、root外へ向くlinkを拒否します。
エラーは固定の一般文だけで、license本文や絶対パスを再表示しません。
dev-only依存は対象外です。
現公開lockにoptional runtimeはありません。将来追加された場合は黙って除外せず失敗し、実際の同梱対象とOS差を別途レビューします。
lockとlicenseを両方意図的に改変する行為や、検査中のファイル差替えに対する信頼境界は提供しません。
licenseファイル名と非空本文の確認は、上流の法的適切さや同梱物全体の適合を保証しません。

[専用3OS CI](../.github/workflows/frontend-notices.yml)は対象のlock/script/fixture/owned notice/本手順が変わる場合に実行します。
UI操作とnative実起動、installer・EXEの同梱検証は別試験です。
44条件や本番利用可の判定へ読み替えず、製品接続gateは閉鎖を維持します。
