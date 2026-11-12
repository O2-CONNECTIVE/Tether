# 社内local buildの読取専用preflight

Apple Developer Program登録なしのMac開発・試験と、通常のDeveloper ID署名releaseは別経路です。
[Appleの開発案内](https://developer.apple.com/help/account/membership/programs-overview/)は個人端末での開発・試験にmembership不要とし、[XcodeのCopy App](https://help.apple.com/xcode/mac/current/en.lproj/dev31de635e5.html)はmacOS appの署名なしexportを提供します。
これは現Tetherが社内運用完成済みという意味ではありません。

本checkerは既存[Development app](../ui/mac-development/README.md)だけを対象に、独立に保持した期待値と手元のbytesを照合します。
署名、build、install、起動、enrollment、接続、OS trust変更、公開配布はしません。通常launcherのDeveloper ID gate、TLS、Access認証、相互鍵認証、固定操作、期限、元owner回収は変更しません。

## 入力

レビュー・build記録から得た完全source SHAとrole別期待SHA-256を、対象appと別に保持します。
期待値の由来を独立に確認してください。同梱物からその場でhashを作り直して一致させるだけでは、改変や配布元の真正性を確認できません。
recordは厳密なJSON objectで、次の4 memberだけを受け付けます。

- `schema`: `tether-internal-local-build/1`
- `source_commit`: 40桁の小文字Git SHA。CLIの独立期待値と完全一致が必要。
- `profile`: `development-local-only`だけ。
- `sha256`: `gui`、`resident`、`plist`、`resident_digest`の4 role。それぞれ64桁小文字SHA-256。

固定inventoryは以下です。未知file/directory、欠落、symlink、非regular fileは拒否します。

- `gui`: `Contents/MacOS/TetherDevelopment`
- `resident`: `Contents/MacOS/tether-dev-resident`
- `plist`: `Contents/Info.plist`
- `resident_digest`: `Contents/Resources/resident.sha256`

plistのbundle ID/executableがDevelopment用と一致し、resident.sha256の本文が同じresident hash + LFであることも確認します。
recordは4KiB、各binaryは128MiB、その他fileは16KiBを上限とします。

```sh
python scripts/check_internal_mac_build.py \
  --app '<対象のDevelopment.app>' \
  --record '<別途保持した期待record.json>' \
  --source-commit '<独立に確認した完全SHA>'
```

成功は終了0と`development-bytes-match`のJSONです。source SHA、role別hash、development区分だけを表示します。
`launch_authorized`、`publisher_verified`、`production_ready`、`distribution_authorized`は常にfalse、`source_binary_provenance`は常に`not-established`です。
入力拒否は終了1と固定`preflight-refused`だけで、原文・path・例外を出しません。CLI構文誤りはargparseの通常usageです。秘密を引数に渡しません。

## 証明しないもの

source SHAは記録の照合であり、そのsourceからbinaryがbuildされた証明ではありません。compiler/toolchain、Mach-O/architecture、実行権限、署名、notarization、依存notice、malware不在は検査しません。
任意bytesを用いるfixtureも通せるため、終了0から実行物の正当性を推測しません。別途レビューしたbuild来歴と実Mac検証が必要です。

これは非敵対的なlocal build出力のsnapshot比較です。leafの差替えとsymlinkを検査しますが、同一ユーザーによる同時directory差替え、照合後の改変、実行時の同一性を保証しません。安全なruntime launch制約の代わりにはしません。

Appleの[コードidentity解説](https://developer.apple.com/documentation/technotes/tn3127-inside-code-signing-requirements)では、ad hoc署名は特定版に紐づき、更新間の同一identity保持に制限があります。
[notarization](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution)は通常Developer IDを要求し、Apple Development/ad hocを代用しません。社内用途だからその保証が自動的に不要になるとも、Developer IDが社内利用全体の必須条件であるとも扱いません。

## 検証

```sh
python -m unittest scripts.tests.test_internal_mac_build -v
python -OO -m unittest scripts.tests.test_internal_mac_build -v
```

合成bytesによるrole取り違え、改変、欠落、余剰、recordの重複/未知member、product区分、誤bundle、source不一致、symlink拒否と固定診断を検査します。実Mac、実app、署名、接続、ユーザー登録は行いません。
