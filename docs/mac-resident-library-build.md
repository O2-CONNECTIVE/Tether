# resident library の build record

#212 に依存する source 準備です。sealed launcher、producer command、installer、実行許可の検証器ではありません。
通常 Mac / CLI / IPC / GUI は未接続のままです。#200 の Windows 阻害と未統合 stack / CI 未実行を解除しません。

## 固定 source と実出力を結ぶ範囲

`scripts/build_mac_resident_source.py` は、現在の clean Git HEAD と tree を固定し、既存 GoCI の Darwin / cgo production inventory を選びます。
Git blob から Go source と go.mod / go.sum を専用 compile snapshot へ置き、実 source の Go 選択が厳密 inventory と一致することも検査します。
未登録の commit 済み Go file、別 package、追加 C/header/object/embed input を黙って省いて成功にしません。recipe の2 source fileも同じ Git HEAD の bytes に一致させます。
module は offline / readonly で verifyし、replacement を拒否します。dependencyのversion/checksum、固定recipe、選択source、Go version / target / flagsを記録します。

build 対象は `internal/residentpipe` の library archive 一つだけです。Go 1.27.3、Darwinの現在のarm64 / amd64 host、cgo有効、trimpath、buildvcs=false、buildmode=archive、並列1に限定します。
Go cacheは新しい専用temporary directoryで、既存のmodule cacheはcallerが事前に用意したものを使います。GOPROXYはoffで、download / install / toolchain変更をしません。
通常のGOFLAGS、workspace、cgo flagの環境overrideを取り込まず、role / compiler argvを引数で受けません。compilerは元のprocessの終了を待ち、取消で別processへ置換せず、中断したbuildを成功にしません。

出力は新しいcheckout外directoryの `.a` と `BUILD_RECORD.json` です。既存outputやsymlinkを上書きせず、temporary source / cacheは削除します。
archiveの同じopened file・size・identityを確認し、実bytesのhashを記録します。実行bitは付けず、artifactを起動・load・配布しません。
実行可能producerのhashやsame-imageの証拠と、このlibrary archiveのhashは別です。

## 実行方法と判定

既存のGo1.27.3と必要module cacheを、承認済みの方法で事前に用意してください。この工具はそれらを導入しません。

```sh
python3 -B scripts/build_mac_resident_source.py --output ../resident-library-build
```

`--output` は新しいbuild結果の書込先だけで、launcher / policy / source rootの入力ではありません。checkout内部とsymlinked parent、既存directoryは拒否します。
成功したbuild recordでもexit codeは2、status / activation / distributionはすべてblockedです。入力不正やbuild失敗は1、明示helpだけは0です。
stdoutは固定codeと非秘密のsource/output hashだけで、入力path・compiler stderr・環境変数を反映しません。

recordのschemaは `rescue-mac-resident-library-build/1` です。
sourceCommit / sourceTree、snapshotGoFiles、locks、recipe、compiledRepositoryPackages、modules、settings / flags、実archiveの相対名 / bytes / hashを持ちます。
producerExecutableとcryptographicAttestationはfalse固定です。これらの値を入力で申告する入口も、recordを受けて起動gateを開くconsumerもありません。

## 信頼と未実証

これは信頼したbuilderが実施したsourceと出力の対応記録です。署名済みattestationではなく、builderやcompiler / SDKの独立した真正性・完全性を証明しません。
同じ入力元がsource / tool / manifestを差し替える攻撃や、同UIDの悪意あるbuild環境に対するsandboxではありません。
新しいpublisher identity、署名、OS trust、既存policyへのアクセス、protected placement、任意hashからの実行許可を作りません。

次のblockerを常に保持します。

- producer entrypointとprotected policy選択のtrusted native bindingが未構成。
- publisher / 署名済み配布・保護配置が未検証。
- 同じ実行imageのverify-to-exec、compiler / SDKの独立真正性、runtime互換性が未検証。
- license / dependency notices の完全性とartifact内の秘密・build path等の監査は未実施。

同じsourceから2つの専用buildを比較する試験は、そのhost / compiler / SDK条件だけでのbyte再現性です。別OS / CPUや実配布の再現性・信頼へ広げません。
pure unitのcompiler metadata / archiveは合成fixtureです。実Go compilerのlibrary build記録と区別し、実producer / GUI / native work / 全回収の証拠に合算しません。

sealed entrypointを有効化するには、承認されたpublisher / 配置の信頼契約とpolicy選択元、実行する同じimageの検証境界が必要です。
unsigned archiveや同じ入力元のhash / recordをtrusted launcherへ昇格させず、不明ならfail closedを維持します。
