# 固定Go依存の本文収集

[backend同梱前の契約](tauri-evaluation-integrity.md#実backend同梱前にownerが確定する契約)のGo notice準備に使う、限定した収集器である。
関連する既存残件は[Go CIの依存一覧](go-ci.md#固定依存の一覧)の#24。
[`collect_go_licenses.py`](../scripts/collect_go_licenses.py)は、取得済みのGoとmodule cacheから固定7本文を読み、本文と`DEPENDENCIES.json`を新しいdirectoryへ保存する。
Go、外部command、build、download、installerを実行しない。
収集完了は配布可能の判定ではなく、接続版の準備manifest・3role・ZIP・製品gateを変更しない。

## 固定対象と正本

| 対象 | 固定版 | 対象本文の正本 |
| --- | --- | --- |
| `github.com/coder/websocket` | `v1.8.15` | [LICENSE.txt](https://github.com/coder/websocket/blob/v1.8.15/LICENSE.txt) (ISC) |
| Go | `go1.27.3` | [LICENSE](https://github.com/golang/go/blob/go1.27.3/LICENSE)、[PATENTS](https://github.com/golang/go/blob/go1.27.3/PATENTS) |
| `golang.org/x/crypto` | `v0.58.0` | [LICENSE](https://github.com/golang/crypto/blob/v0.58.0/LICENSE)、[PATENTS](https://github.com/golang/crypto/blob/v0.58.0/PATENTS) |
| `golang.org/x/sys` | `v0.49.0` | [LICENSE](https://github.com/golang/sys/blob/v0.49.0/LICENSE)、[PATENTS](https://github.com/golang/sys/blob/v0.49.0/PATENTS) |

Goとgolang.org/x配下の2moduleについては、2026-10-30に各固定tagの公式repository本文を取得し、Git blob SHAと本文SHA-256を照合した。websocket本文の取得確認は末尾の2026-10-31追補を参照する。
Go系6本文はそれぞれ同名の本文と同一であり、固定期待値は次のとおり。
入力metadataが自己申告する本文hashを信頼しない。

| 本文 | Git blob SHA | 本文SHA-256 |
| --- | --- | --- |
| LICENSE | `2a7cf70da6e498df9c11ab6a5eaa2ddd7af34da4` | `911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad` |
| PATENTS | `733099041f84fa1e58611ab2e11af51c1f26d1d2` | `96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc` |

これはbackend/go.modが直接固定する3moduleとGo rootの2本文を準備する範囲である。
`x/crypto`自身のgo.modには別の依存もあり、この一覧を全module graph、実binaryの依存閉包、全runtime noticeの網羅としない。
法律上の十分性やlicense互換性の認定も行わない。

## CLI入力と独立して照合する値

```sh
python scripts/collect_go_licenses.py \
  --module-root backend \
  --module-cache <取得済みmodule-cache> \
  --go-root <取得済みGo-root> \
  --metadata <明示3moduleのJSONstream> \
  --source <完全40桁source-SHA> \
  --output <存在しない出力directory>
```

出力の親directoryは既存のものを指定する。
`--metadata`は既存の`go list -m -json github.com/coder/websocket golang.org/x/crypto golang.org/x/sys`相当の、3つのJSON objectを連結したUTF-8 streamである。
`all`、JSON配列、main moduleを含むgraph入力は受け付けない。
収集器はこのcommandを実行せず、入力streamの真正性を認証しない。

| 入力 | 検査する内容 |
| --- | --- |
| module-root | `go.mod`と`go.sum`だけ。module名、Go版、3つのrequireと版、6つのchecksum行が現在の固定値に一致すること。空白差を許すが、追加directive、依存追加、replace、欠落、重複、checksum違いを拒否 |
| metadata | 必須`Path`・`Version`・`Sum`・`GoModSum`を固定3moduleとlockへ照合。`Time`・`Dir`・`GoMod`・`GoVersion`の文字列と`Indirect`のbooleanだけを追加許容。未知field、replace、local/main、未知module、重複key/object、欠落・版違いを拒否 |
| module-cache | metadataの`Dir`・`GoMod`を使わず、固定module pathと版から`github.com/coder/websocket@v1.8.15`、`golang.org/x/crypto@v0.58.0`、`golang.org/x/sys@v0.49.0`を組み立てる。websocketは直下`LICENSE.txt`のみ、Go modulesは直下`LICENSE`・`PATENTS`のみを読む |
| Go root | `VERSION`の固定版宣言とroot直下の`LICENSE`・`PATENTS`だけを読む。VERSIONは版行と任意の`time`行を許す |
| source | 小文字hex40桁の宣言。checkoutや実binaryとの対応はこのCLIでは検証しない |

go.sumの`h1:`はmodule内容またはgo.modのchecksumであり、本文のSHA-256ではない。
metadataとlockのchecksum一致から、module実directory全体が未改変であるとは証明しない。
本文の実bytesは固定期待SHA-256で独立に照合し、その同じbytesを保存する。
GoのVERSION宣言と本文一致も、toolchain全体・公式archive・compiler・実binaryの来歴を証明しない。

## ファイル境界と出力

指定root、rootから固定pathまでの内部directory、入力fileはsymlinkとWindows reparse pointを拒否する。
入力は通常fileに限り、metadataは64KiB、各lockと各本文は16KiB、VERSIONは512bytesまで読み取る。
任意の本文pathやfile名をmetadataから受け取らない。
明示rootより上の配置元の保護や、敵対者が同時にfilesystemを書き換える場合のsandbox・TOCTOU保証は提供しない。

全入力を検査してから出力directoryを新規作成する。
既存file・directory・symlinkには上書きせず、入力の拒否時は出力を作らない。
書込み中に失敗した場合は`GO_LICENSE_OUTPUT_INCOMPLETE`で終了し、部分出力が残る場合がある。
その部分出力を完了扱いにせず、再実行でも上書き・自動削除しない。

保存するfileは以下の8件だけである。

- `go-toolchain/go1.27.3/LICENSE`と`PATENTS`
- `crypto-v0.58.0/LICENSE`と`PATENTS`
- `sys-v0.49.0/LICENSE`と`PATENTS`
- `websocket-v1.8.15/LICENSE.txt`
- `DEPENDENCIES.json`

inventoryはsource宣言、対象版、固定upstream参照、moduleとgo.modのlock checksum、読んだlockのSHA-256、各本文の相対pathとSHA-256を持つ。
`scope=fixed-direct-modules-and-toolchain-texts-only`、`sourceCommitVerified=false`、`moduleGraphVerified=false`、`binaryDependenciesVerified=false`、各moduleの`directoryVerified=false`、toolchainの`archiveVerified=false`を保持する。
ローカルpath、metadataの余剰情報、環境変数、原ログは保存しない。
`distributionStatus=blocked`と`productionGateClosed=true`を保持し、[接続版準備validator](connected-evaluation-package-contract.md)のblockedを解消しない。

CLIは収集完了なら終了値0で`status=collected`、拒否または失敗なら終了値1で固定codeを含むJSONを1件だけ返す。
引数や例外の原文は診断へ複製しない。
明示的な`--help`だけは通常のhelpを返す。
これは製品診断bundleや任意fileの秘密scannerではない。

## 検証の境界

[`test_go_licenses.py`](../scripts/tests/test_go_licenses.py)はCLIを起動し、生成fileと拒否時の未生成を確認する。
正常入力の未実装拒否、未知moduleの受理、checksum不変の本文改変の受理、本文symlinkの受理について、同じCLI境界でREDからGREENを確認した。
その他のlock・metadata・file・サイズ・出力非上書き・診断の負例は実装後回帰である。
fixtureには上記公式本文だけをJSON文字列として保持し、checkoutの改行変換に依存せず元のLF bytesを再構成する。
fixtureのmetadataとsourceは有限の合成入力であり、取得元の認証や実binaryの証拠ではない。

既存Python offline CIの`python -m unittest discover -s scripts/tests -v`がこの試験も収集する。
GoCI、workflow、既存配布manifest、ZIP生成器は変更しない。
実SSH、Windowsの保護、helper資源、実binaryの依存・配布・終了の受入は別の未接続または未検証項目として残る。

## TetherのWebSocket依存追加（2026-10-31）

`github.com/coder/websocket v1.8.15` のISC本文を固定対象へ明示追加しました。module/版/module checksum/go.mod checksumは現在のlockと完全一致させ、未知依存を許可する変更ではありません。metadataが本文path・URL・hashを自己申告しても採用しません。

公式本文：[LICENSE.txt](https://raw.githubusercontent.com/coder/websocket/v1.8.15/LICENSE.txt)（固定tag）。2026-10-31にこの公開URLから取得した実bytesは720 bytes、SHA-256 `cc0975a5f6305145bdd7b41ce9479632fdac3870e6ac4281f28017f18c767c4e`、Git blob形式のSHA-1 `7e79329fc1e2e56559fad28823487f6b02a4e57f` です。web表示から空行を推定せず、実bytesをfixtureへ保存しました。このGit blob形式hashは取得bytesから計算した値で、GitHub API側のobject IDとの独立照合を主張しません。

- 本文はISCのcopyright・permission・disclaimerを改変せず保存します。websocketに存在すると確認していないPATENTSを要求・生成しません。
- `go.mod`は個別requireとrequire blockを受理しますが、宣言のmodule/go版、固定3module集合、版、重複なしを厳格に確認します。追加directive、comment、replace、exclude、未知moduleや空/未終端blockは拒否します。汎用Go parserとしては扱いません。
- 合成fixtureのCLI試験は15 tests成功、fail/skipなし。既存collectorでのred後に、固定3module＋7本文の成功、module/本文改変、欠落、symlink、無制限依存追加拒否を確認しました。試験用temp directoryだけを読み、実module cache、Go command、Docker、networkは試験で使いません。
- この変更では実cacheからlicense収集をしていません。公開URLで取得したlicenseをfixtureとして持つことと、配布binaryに対応した最終notice artifactの収集・同梱確認は別です。現在の`distributionStatus=blocked`、依存graph/実binary閉包未検証、製品gate閉鎖を維持します。
- scratch imageがbinaryだけを含む場合、project LICENSE、toolchain/実binary依存の必要notice、hashとsource対応を別の配布物で漏れなく渡す必要があります。このcollector単体はproject LICENSEやarchive作成・署名・配布を担当しません。
