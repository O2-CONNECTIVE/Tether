# 同じsourceのGUI・Swift・producer組立

[#24](https://github.com/O2-CONNECTIVE/Tether/issues/24) / [#189](https://github.com/O2-CONNECTIVE/Tether/issues/189) の限定評価準備です。
通常Tauri GUI、fixture defineなしのSwift library、固定sealed Go producerを同じclean Git HEADからbuildし、固定配置の三image `.app` に組み立てます。起動版や接続可能版の配布ではありません。

## 固定入口

```sh
python scripts/build_mac_gui_candidate.py --output <新しいcheckout外directory>
```

既存の使い捨てhosted ARM64 Mac、Xcode 27.1、macOS SDK 27系、Rust 1.99.1、Node 24.22.1、Go 1.27.3と事前取得済みの公開依存cacheに限定します。自己申告のrunner変数は隔離や認可の証明ではありません。許可済みCI以外へ実行範囲を拡張する入口ではありません。

- `ui/tauri` の追跡blobをsource-relative snapshotへ取り、symlink、build生成物、追加Cargo/npm設定を拒否します。lock・設定・assetsを含む全snapshotと実行recipeのhashを記録します。fixtureの試験sourceもsnapshotに含みますが、fixture defineやexample/test targetは選択しません。
- frontendはoffline `npm ci --ignore-scripts` と通常の `npm run build`、生成notice再照合です。任意のpath/flags/target/credential/fixture設定を受け付けません。
- Swift 6言語mode、固定4 sourceとheader、`arm64-apple-macosx14.4`、通常ABI export集合を使います。native install nameは `@rpath/libResidentPreparation.dylib`。toolchainのstdlib rpathを抑止し、`/usr/lib/swift` を明示します。
- Rustは `--locked --offline --release --target aarch64-apple-darwin --bin rescue-host-shell --features custom-protocol`。追加cfgは `resident_native_source` のみです。通常起動が使う閉じたstartup ABIと同じnative imageの必須loadを保持し、GUI rpathは `@executable_path/../Frameworks` に固定します。絶対stage pathはlink時の入力にだけ使い、runtime rpathへ残しません。
- producerは[既存のsealed build](mac-sealed-producer-build.md)を同じ実行内で呼び、`cmd/resident-pipe` を通常のDarwin/cgo source選択からbuildします。持ち込んだbinaryや外部hashを組み込む入口はなく、GUIとproducerのsource SHA/tree、target、role、実image hash/sizeを照合します。
- deployment target 14.4はcompilerへの指定です。組立plistの最低OSは三imageが実際に宣言した値の最大値から作り、記録と再照合します。実互換性の保証ではありません。通常GUIは[同じsessionのstartup](mac-gui-trusted-startup.md)へ接続し、Swiftの `ApprovedRelease.production=nil` は維持します。

新規outputには `Tether-CI-Candidate.app` と `BUILD_RECORD.json` だけを保持します。appは `Contents/MacOS/rescue-host-shell`、`Contents/Frameworks/libResidentPreparation.dylib`、`Contents/Helpers/resident-pipe`、固定 `Contents/Info.plist` の4 files。imageはmode0600、outputはmode0700とし、一度もlaunch/loadしません。既存outputは置換せず、失敗時は自分の新規outputだけを除去します。

copy前のimage hashを固定し、組立後bytesと[link-layout検査](mac-gui-link-layout.md)を照合します。producerは通常native launcherと同じnested pathに置き、別architecture・dylib・外部rpath・私有依存・余分なfileやdirectoryを拒否します。

記録schemaは `tether-mac-gui-candidate-build/2`。三imageのpath/hash/sizeと同じsource SHA/treeを記録し、`producerBuild` にGo source/lock/recipe・module閉包・flags・非実行の原記録を保持します。producer単独記録の `GUI_BUILD_UNBOUND` はその単独buildの範囲であり、三imageの対応は外側recordが別に検証します。従来の評価材料manifest v1を拡張したものではなく、noticeを含む配布材料契約の完了も主張しません。これはbuilder自身の対応記録で、独立attestation、署名やsource再現性の証明ではありません。linkerが生成するad-hoc metadataを正規publisher署名とは扱いません。

## CIと検証境界

成功buildもexit2、`status/activation/distribution=blocked`。不正入力や失敗はexit1、helpのみexit0です。CLI診断へpath、秘密、compiler stderrを流しません。

[Mac native source CI](../.github/workflows/mac-native-source.yml) の独立laneが実buildと記録・組立bytes・非実行mode・link-layoutを再照合します。同じlaneでGUI/SwiftとGoの公開依存を事前取得し、三imageをoffline buildします。producer単独buildとnative fixture laneも維持します。保存artifactはJSONのみで、app/imageはupload、署名、notarize、登録、起動しません。OS信頼設定やcredentialに変更はありません。

```sh
python -m unittest scripts.tests.test_mac_gui_candidate_build -v
python -OO -m unittest scripts.tests.test_mac_gui_candidate_build -v
```

匿名compiler/Mach-O fixtureはbuild contractの回帰検証です。実Mac build、dylib load、GUI表示、ON/OFF、実dot接続・終了回収の成功とは別です。tool不在のCLI失敗だけを製品TDDの証明と数えません。

## 接続できる検証版まで

1. Rust/Swift/Goを含むMacのnotice閉包、binaryの秘密/path監査を確定します。frontend notice成功だけでは閉包完了ではありません。
2. [producer先行の専用署名recipe](mac-signed-release-build.md)で正規publisher identityと署名後producer CDHashを固定release構成へ結びます。実署名とnotarizationは未実施です。署名credentialと認証enrollmentは別の具体承認と検証が必要です。
3. 通常GUIの同じNativeSessionを使う起動sourceを、正規release・実同意・認証・失効へ接続し、同じcandidateで起動、作業/結果、OFFと元ownerの終了回収を実証します。
4. 配布受入後、対象Macと実dotで評価します。このCI-only appはそのまま渡して使える検証版ではありません。
