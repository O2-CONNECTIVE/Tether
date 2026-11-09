# 固定登録producerの非実行build

[#24](https://github.com/O2-CONNECTIVE/Tether/issues/24) / [#189](https://github.com/O2-CONNECTIVE/Tether/issues/189) の評価候補準備です。
`build_mac_sealed_producer.py` は clean Git HEAD の固定sourceから `cmd/resident-pipe` を実Mach-Oへcompileします。従来のlibrary archiveを実行imageと誤認せず、署名前imageのsource・依存・flags・bytesの対応記録を作ります。

## 入口と出力

```sh
python scripts/build_mac_sealed_producer.py --output <新しいcheckout外directory>
```

Go1.27.3、Darwin ARM64、cgo有効、既存offline module cacheに限定します。固定flagsは `-mod=readonly -trimpath -buildvcs=false -buildmode=exe -p=1`。role、source root、compiler flags、policy、argv、credentialsのoverrideはありません。Git blobのDarwin/cgo inventoryとlockを新規snapshotへ取り、実package選択とclosure/module checksumを確認します。recipe/import helperのbytesも同じHEADへ照合します。従来の `build_mac_resident_source.py` のarchive schema/flags/出力と既定呼出しは不変です。

新規directoryに `resident-pipe` と `BUILD_RECORD.json` を作ります。生成imageは保持時mode0600で、build recipeは一度も起動・loadしません。既存outputとsymlink parentを拒否し、失敗時は自分が新規作成したoutputだけを除去します。compile取消は元compilerの終了を待ち、再試行や別processへの置換はしません。

schemaは `tether-mac-sealed-producer-build/1`。source SHA/tree、UTC build時刻、固定role/target/Go設定/flags、snapshot source/lock/recipeのhash、Goの選択repository package/module version/checksum、実imageのSHA256/size/Mach-O headerを記録します。CPUとexecutable種別、modern macOS build-version headerを検査します。headerのminimum OS/SDKは宣言であり実互換性証明ではありません。配布名は決めず、正式version numberingも作りません。

正常buildでもexit2・status/activation/distributionはblockedです。`producerExecutable=true` はimage形式だけを表し、`producerExecuted`、`signingPerformed`、`enrollmentAccessed`、`cryptographicAttestation` はfalseです。入力不正やcompile失敗はexit1、helpのみ0。stdoutにpath、環境変数、compiler stderr、policy内容を出しません。

## 検証境界

匿名compiler/Mach-O fixtureは実装後の回帰です。実Go buildは既存 `mac-native-source.yml` の承認済hosted ARM64 laneで別に行い、checkerがexit2、固定record、同じimage bytesを照合します。既存library buildとnative owner試験は維持します。CI artifactにはJSONだけを含め、実imageをuploadしません。PowerShell/GUI/native source fixtureの合格をこのbuildの成功へ代用しません。

記録は信頼したbuilderが実施した対応記録であり、独立attestationではありません。compiler/SDKの真正性、linked native library閉包、license/notice完全性、binary内秘密/path、署名、notarization、配布/起動の受入は未確認です。Go linkerが生成するad-hoc metadataを正規publisher署名とは扱いません。実行しないので現accountのenrollment/policy/keyを読みません。

## 実接続までの残工程

1. GUIの固定source/native link/layout build recipe。現native fixtureの一時dylib/絶対rpathを配布物へ流用しません。二imageとMac notice閉包を同じ候補へ対応づけます。
2. 正規publisher Team/Developer ID、固定producer signing IDと署名後CDHashのrelease構成。`ApprovedRelease.production=nil` はこの変更で不変。現在の未署名image hashをtrustへ昇格しません。
3. 実利用者の同意、保護された固定enrollment、host/client認証と失効の用意。実credentialの生成/入力/保存、永続accessは別許可・別検証です。
4. 同じNativeSessionを通常起動へ繋ぎ、同じimageをOS launch enforcementで起動し、ON/OFF・作業/結果・元owner回収を確認します。現在GUIのdefaultはunboundのままです。
5. 配布許可、署名/notarization、受領者Macと実dot接続で評価します。材料recordやCI合格だけでは「ダウンロードして接続できる版」になりません。

sealed commandと固定enrollment loaderはsourceとして存在しますが、従来archiveの `PRODUCER_ENTRYPOINT_UNBOUND` / `POLICY_SELECTION_UNBOUND` はそのarchiveに対するtrusted native binding未構成を指します。今回recordは実commandを明記し、未構成release/enrollmentを個別blockerとして残します。
