# Mac GUI と Swift library の非実行 link-layout 照合

[#24](https://github.com/O2-CONNECTIVE/Tether/issues/24) / [#189](https://github.com/O2-CONNECTIVE/Tether/issues/189) の GUI build 準備です。
[`verify_mac_gui_link_layout.py`](../scripts/verify_mac_gui_link_layout.py) は、固定位置の GUI と Swift native library の実 bytes を読み、期待 hash と限定 Mach-O link-layout を照合します。build、組立、起動、load、署名、登録、upload はしません。一致しても終了値は **2 / blocked** です。

## 固定 layout と入口

```text
<candidate>.app/Contents/MacOS/rescue-host-shell
<candidate>.app/Contents/Frameworks/libResidentPreparation.dylib
```

```sh
python scripts/verify_mac_gui_link_layout.py \
  --app <候補app> --target darwin-arm64 \
  --gui-sha256 <外から確認したGUIのSHA-256> \
  --native-sha256 <外から確認したSwift-libraryのSHA-256>
```

`darwin-arm64` と `darwin-amd64` の thin Mach-O だけを扱います。fat binary、追加 framework、追加 dylib や runtime 同梱はこの契約外です。期待 hash を同じ入力から取り出すだけでは独立した根拠になりません。hash は bytes の照合であり、publisher や source 対応の証明ではありません。

- GUI は executable、native は dylib。modern macOS `LC_BUILD_VERSION`、CPU、command 境界、文字列境界を確認します。二 image の最低 OS 宣言の最大値を報告します。実互換性は確認しません。
- GUI は `@rpath/libResidentPreparation.dylib` を通常の必須 load で一度だけ参照します。native の install name は同じ値に固定し、自分自身への load を拒否します。
- GUI の rpath は `@executable_path/../Frameworks` が必須で、追加できるのは `/usr/lib/swift` だけです。native は `/usr/lib/swift` だけを許します。重複、任意の絶対 build directory、相対 path、alias、親参照を拒否します。
- 他の load は `/usr/lib/` または `/System/Library/Frameworks/` の絶対 path、あるいは固定形式の `@rpath/libswift*.dylib` だけを扱います。Swift の rpath 参照には同じ image の `/usr/lib/swift` 宣言が必要です。system framework の weak load は許しますが、存在・利用可能性の証拠にはしません。
- `Contents/Frameworks` は固定 native library 一つだけです。他の file、空 directory、Swift runtime の影となる同梱 file を拒否します。GUI と native の両 file、全親 directory の link/reparse/special file を拒否します。
- `LC_DYLD_ENVIRONMENT`、re-export/upward/lazy/prebound load、未知 command は拒否します。GUI の dynamic linker 宣言がある場合は `/usr/lib/dyld` のみに固定します。native の dynamic linker 宣言は拒否します。

これは有限の load-command layout 検査です。symbol table、segment、code signature 等の payload 全体を検証する Mach-O loader ではありません。system path は文字列の検査だけで、その library の bytes、現在の OS dyld cache、transitive dependency、runtime search の安全性を証明しません。環境変数による dyld の変更、悪意ある同時変更、検査後の実行を保護しません。fixture が混入していないことも hash/layout だけでは判定できず、固定 source と recipe の独立確認が別に必要です。

## 読取りと診断

各 image は256 MiB、load command 領域は1 MiB、4096 commands、path は1024 ASCII文字までです。通常の入力では2 image のみを読み、framework directory は余分な一件を見つけた時点で拒否します。Info.plist、producer、notice や他の resource はこのCLIの対象外です。

同じmoduleの内部 `verify_assembly` は[通常GUIの組立入口](mac-gui-candidate-build.md)専用で、第三の固定 `Contents/Helpers/resident-pipe` を追加照合します。producerはARM64のMach-O executableとsystem library依存だけを受け、rpath、Swift/native link、私有依存を拒否します。4 filesの全配置、symlink拒否、同じ三imageの最大minimum OSとInfo.plistを照合し、`producerIncluded=true` でもblockedを維持します。

正常出力は固定の未検証集合、外部期待 hash、target と最低 OS 宣言です。失敗出力は固定 code だけで、path、例外、文字列内容や原ログを出しません。file 読取り前後と framework directory の形状を確認しますが、原子的 filesystem snapshot ではありません。入力は変更・削除しません。

- 一致: exit2 / `status=blocked` / `linkLayoutMatched=true`。
- 欠落・不一致・不正: exit1 / `status=refused`。
- help のみ exit0。実行・配布成功を示す exit0 はありません。

## 既存材料契約との関係と残件

[材料照合 v1](mac-evaluation-materials.md) は GUI と producer の二 image に限定され、この Swift dylib を含む layout を受理しません。今回 v1 を黙って拡張したり、新しい full-package manifest を作ったりしていません。両検査を通せば一つの完成 package になる、とは扱わないでください。

固定 source の実build入口は[GUI/Swiftの非実行app組立](mac-gui-candidate-build.md)で扱います。通常GUIの同じsessionへの入口は[閉じたstartup](mac-gui-trusted-startup.md)へ接続します。三imageの同じsource組立・対応記録はこの入口で行いますが、Mac notice閉包、正規署名identity、`ApprovedRelease.production`、enrollment、実dot、実終了回収と配布受入は未構成・未検証のままです。未設定gateは維持します。

## 有限試験

新しい CLI の正常 layout 入力は実装前に不在で失敗し、実装後に同じ入力で blocked の結果を確認しました。合成 Mach-O の正例と負例で期待 hash、固定 link、重複、absolute rpath、path traversal、fixture-style path、Swift 参照、種類/CPU/OS宣言、壊れた command、上限、非通常 file、診断の非漏洩を確認します。

```sh
python -m unittest scripts.tests.test_mac_gui_link_layout -v
python -OO -m unittest scripts.tests.test_mac_gui_link_layout -v
```

匿名 bytes の試験であり、実 Mac build、実 dylib の load、native 起動や配布受入ではありません。POSIX symlink fixture は Windows では予定された skip とし、通常 file/非通常 file と link-layout 検査は全 OS で行います。
