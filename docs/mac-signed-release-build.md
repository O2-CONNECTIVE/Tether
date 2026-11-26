# 固定producerとpair helperを署名するMac組立

[#24](https://github.com/O2-CONNECTIVE/Tether/issues/24) / [#189](https://github.com/O2-CONNECTIVE/Tether/issues/189) の製品組立入口です。[非実行の通常候補](mac-gui-candidate-build.md)とは別に、承認された既存Developer ID Application identityでproducerとpair helperを署名し、producerのCDHashをSwiftへ固定してからGUIを組み立てます。pair helperは `Contents/Helpers/tether-host-pair` に同梱し、固定identifier `com.o2connective.rescue-host.tether-host-pair` で署名します。

**この入口は実際に署名します。現時点のCIでは実行しません。** 通常buildの `ApprovedRelease.production=nil` は維持します。新しい鍵・証明書・CI secretの作成、credentialのimport、Keychain設定、OS信頼設定、notarization、install、起動、enrollment、公開配布は行いません。署名済みという事実だけで、実dot接続や配布受入を完了にしません。

## 実行前に固定するもの

- 実行を承認されたMac ARM64 / macOS 27以上。Xcode 27.1、SDK 27系、Node 24.22.1、Rust 1.99.1、Go 1.27.3と事前取得済み公開依存cacheが必要です。
- レビュー済みclean Git HEADの完全なcommit SHA。別commit、dirty source、recipe bytesの不一致を拒否します。
- 所有者が独立に確認した10文字の公開Team IDと、既存Developer ID Application証明書の40桁大文字SHA-1 fingerprint。fingerprintはKeychain上の署名identityを曖昧なく選ぶためで、codeの信頼性やCDHashの代用品ではありません。
- このsource・identity・実行Macを対象にした署名操作の承認。builderの引数は承認システムではありません。秘密鍵、p12、passwordを引数やチャットへ渡しません。
- `TETHER_PAIR_GATEWAY`、`TETHER_PAIR_ISSUER`、`TETHER_PAIR_CLIENT_ID` の有効値。gatewayは小文字hostのみの `https://` authority、issuerとclient IDは `hostpairoauth.ValidClient` と同じ条件を満たす必要があります。欠落・不正・一部だけの指定を拒否します。値はpair helperのGo linker flagsとbuild recordに記録されるため、実際の公開設定だけを渡します。
- この初期recipeのentitlementsは全roleで空です。JIT、disable-library-validation、get-task-allow等を追加しません。必要になれば根拠と最小集合を別レビューします。

通常の署名なし候補とCI buildでは3変数を全て空にできます。一部だけの指定は失敗し、空ならpair helperは設定 unavailable の状態でbuildされます。

## Keychain access group の署名手順（未照合）

常駐ホスト鍵は `com.o2connective.tether.host-key` service の Keychain 項目に保存し、
`host_key_fingerprint` を account に使います。signed producer は data-protection Keychain を指定し、
コードに access group 名を持たせず、entitlement の既定 access group を使います。

署名時は enrollment helper と常駐 producer が同じ Apple Team で署名されることを確認し、
両方の entitlement に同じ Team-prefixed `keychain-access-groups` を設定します。
複数 group を指定する場合、両方で共有 group を同じ位置（既定 group）に置きます。
プロビジョニング profile がその group を許可していることを確認し、署名後に
`codesign -d --entitlements :-` で実際の entitlement、`codesign -dv` で Team ID、
profile inspection で group の許可を照合します。access group を hardcode した
`kSecAttrAccessGroup` はコードへ追加しません。

この repository の現在の署名 recipe は全 role の entitlement を空に固定します。
producer、pair helper、Swift library とGUIは同じTeamとHardened Runtimeで署名します。
pair helperは専用identifierを持ちます。そのため上記のKeychain Sharing設定はrecipeに未反映で、
署名・profile・実機の共有確認は**未照合**です。実署名と実機確認はこの変更の範囲外です。
手順の根拠は Apple の [Keychain Sharing](https://developer.apple.com/documentation/xcode/configuring-keychain-sharing),
[Keychain Entitlements](https://developer.apple.com/documentation/bundleresources/entitlements/keychain-access-groups),
および [TN3125](https://developer.apple.com/documentation/technotes/tn3125-inside-code-signing-provisioning-profiles) です。

具体承認後のコマンド形は次のとおりです。`<...>` は公開識別子等に置き換えます。

```sh
export TETHER_PAIR_GATEWAY=<公開gateway>
export TETHER_PAIR_ISSUER=<公開issuer>
export TETHER_PAIR_CLIENT_ID=<公開client-id>
python scripts/build_mac_signed_release.py \
  --source-commit <承認済み完全SHA> \
  --team-id <承認済みTeamID> \
  --certificate-sha1 <既存証明書fingerprint> \
  --output <新しいcheckout外directory>
```

`--producer`、`--provider`、`--manifest`、任意flags、entitlements、notarization、runtime identity設定は受理しません。任意binaryを持ち込む経路ではなく、固定sourceから同じ処理内でbuildします。終了2は署名済みlocal候補の記録成功、終了1は拒否です。配布可を意味する終了0はありません。

## 組立の順序と同一性

1. clean HEADの通常GUI source/locks/assetsとrecipeを固定します。署名検査helperも同じGit blobからcompileします。
2. 既存sealed producer recipeで `cmd/resident-pipe` をbuildし、pair helper recipeで `cmd/tether-host-pair` を製品buildします。`tether_development` tagは付けません。両方のGo source・lock・module閉包と元image hashを保存し、pair helperでは設定値と `go version -m` の `-ldflags` を照合します。
3. producerを固定identifier `com.o2connective.rescue-host.resident-pipe`、pair helperを `com.o2connective.rescue-host.tether-host-pair` で、それぞれHardened Runtimeとsecure timestampを付けて署名します。
4. Appleの `SecStaticCodeCheckValidityWithOnDiskRequirement` を使い、signatureIsValidとrequirementMatchedの両方を要求します。期待Team・固定identifier・Developer ID区分を確認してから、署名情報の20-byte CDHashを取得し、そのhashを加えた完全なtyped requirementでも再検証します。選択証明書fingerprint、timestamp、runtime flag、空entitlementsも照合します。単なるmetadata抽出やfile SHA-256短縮ではありません。
5. 同じ実行の検証結果だけから `generated/SignedResidentReleaseIdentity.swift` を生成します。`TETHER_SIGNED_RELEASE` の固定compile compositionだけがこの一つの入力を使い、Swiftと通常Rust GUIをbuildします。生成物はGit blobと区別してhash記録します。生成入力の欠落・重複はcompile失敗です。アプリにruntime getter/setter、環境変数やJSON manifestの信頼overrideを追加しません。
6. 固定四imageをappへコピーし、Swift library、最後に外側appを明示的に署名します。native identifierは `com.o2connective.rescue-host.resident-preparation`、app identifierは既存 `com.o2connective.rescue-host.shell`。producerとpair helperは再署名しません。`--deep`やlibrary validation解除は使いません。
7. 最終四署名、producerとpair helperのCDHash、署名済みnative/helper bytes、link layout、生成identityとsourceの不変を再照合します。app最低OSは27以上かつ実image宣言の最大値。ARM64だけを受け付け、Rosetta／universal対応とは扱いません。

出力はprivateなmode0700 directoryに `Tether-Signed-Candidate.app`、`BUILD_RECORD.json`、生成Swift証跡です。四imageはmode0700です。成功前の失敗・中断は所有している新規outputだけを削除し、既存outputは置換しません。compiler/signerは元processの終了まで待機します。appはlaunch/loadしません。

signed inventoryは四image、固定Info.plist、`Contents/_CodeSignature/CodeResources` だけです。`Contents/Helpers` には `resident-pipe` と `tether-host-pair` の二つだけを許可します。unknown file、symlink、shadow library、変更plistを拒否します。通常の非実行candidate inventoryは緩めません。notary ticketやnotice resourceはこの初期段階へ黙って許可せず、配布工程の別契約として残します。

## 記録と未完了

schemaは `tether-mac-signed-candidate-build/2`。署名前後のhash、固定source SHA/tree、recipe、producerとpair helperのbuild record、generated input、署名観測を分けます。`status=signed-local-candidate`、`activation=unverified`、`distribution=blocked` です。builderの記録は独立attestationや再現build証明ではありません。helperの検証はその時点のディスク上codeに対するものです。実launchでは引き続き元 `Process.launchRequirement` が強制し、別imageの静的検証で置き換えません。

残る条件は、Mac notice閉包、binary秘密/path監査、toolchain整合、別途承認されたApple notarizationと最終package照合、protected enrollment、実署名済み同一appの起動・作業/結果・取消/OFF・元ownerの終了回収、受領者Macと実dotの受入です。署名済みlocal候補をそのまま配布可能版と案内しません。

## CIの境界

```sh
python -m unittest scripts.tests.test_mac_signed_release -v
python -OO -m unittest scripts.tests.test_mac_signed_release -v
```

匿名compiler/signature fixtureは順序、不一致、改変、失敗時cleanupを検証します。初回の未実装import失敗と、native差替えに対する同じ公開build境界のred→greenは別の証拠として記録します。fixtureはDeveloper ID署名や署名済み起動の成立を証明しません。

[Mac native source CI](../.github/workflows/mac-native-source.yml)では署名observerの実compile、通常nil sourceのtypecheck、合成identityを一時compileするSwift libraryのABI、provider欠落/重複のcompile拒否、symlink/非正規pathの拒否、producerとpair helperのad-hoc outputをDeveloper IDと認めない拒否を確認します。合成libraryはload/launch/uploadせず一時directoryとともに破棄します。実Team・証明書・署名credentialはCIへ追加しません。通常GUIと二つのhelperの四image非実行buildも維持します。

Apple資料: [配布署名](https://developer.apple.com/documentation/xcode/creating-distribution-signed-code-for-the-mac)、[OnDiskCodeRequirement](https://developer.apple.com/documentation/lightweightcoderequirements/ondiskcoderequirement)、[署名情報](https://developer.apple.com/documentation/security/signing-information-dictionary-keys)。
