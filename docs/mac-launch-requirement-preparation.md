# Typed launch requirement と起動前 owner の source 準備

#216 に依存する未統合 source です。#200 の Windows 阻害と stack の CI 未実行を維持します。
通常 Tauri / Rust / Go には link・配線せず、アプリの minimum OS、bundle、権限、既存 Child owner を変更しません。

## 今回の具体的な範囲

[`ResidentLaunchPreparation.swift`](../ui/tauri/native/resident-launch/ResidentLaunchPreparation.swift) は、Apple の public typed API で requirement を作り、一つの起動前 Foundation `Process` を private に保持します。
別の Swift parent executable、helper service、新しい起動権限や runtime callback は追加しません。
source module を独立 compile し、後続の in-process native bridge が使う準備段階だけを検証します。

production の publisher / exact release は private immutable な未設定値です。
通常 `prepare()` は OS 未対応か publisher 未設定で拒否し、未設定時に Process を作りません。runtime から値を設定する入口もありません。
Team / hash を渡す constructor と観測用 snapshot は `RESIDENT_LAUNCH_FIXTURE` compile flag だけに存在します。通常構成に合成 identity を混ぜません。

typed requirement は次のすべてを AND にします。

- release で承認した TeamIdentifier。
- 固定 producer role の SigningIdentifier `com.o2connective.rescue-host.resident-pipe`。採用する source 上の役割名であり、実 signed artifact が存在するという意味ではありません。
- ValidationCategory.developerID。
- 別途承認した非空 CodeDirectoryHash 集合のいずれか。現在の source 制約は20 bytes・非ゼロ・重複なし・最大8件です。raw file SHA-256 とは別です。

Team の字形・hash のサイズ検査は、署名や publisher の証明ではありません。
期待値を candidate の path / PID / raw hash や任意 manifest から取得せず、未確定の値は欠けたまま拒否します。
実 publisher の指定は実署名・実物用構成の前提であり、この合成 source / fixture を進めるためのユーザー質問は不要です。

## macOS と保証範囲

Process.launchRequirement / LaunchCodeRequirement は macOS 14.4+ です。[後続の原子的な匿名 IO](mac-atomic-preparation-pipes.md)は別に macOS 27以上を要求し、割当前に拒否します。
actual `#available` と native OS version の両方を確認し、14.4 未満は当該機能を拒否します。fixture の version 引数で actual guard を回避できません。
現在のアプリの実効 deployment target はこの source から推定しません。アプリの最低 OS を引き上げる設定変更もありません。
[Apple Process API](https://developer.apple.com/documentation/foundation/process/launchrequirement)、[LaunchCodeRequirement](https://developer.apple.com/documentation/lightweightcoderequirements/launchcoderequirement)

Apple の起動時契約は、実際に起動される executable が指定 requirement に合わなければ OS が実行しないことです。
Team / identifier / category だけでは同 publisher の別 release も一致し得ます。exact release を制約するには承認した CodeDirectoryHash 集合との結合が必要で、architecture / Rosetta による選択も別途検証します。
script の場合は interpreter が対象なので、固定 Mach-O producer の代わりに shell / env / script を選びません。
[Apple CodeDirectoryHash](https://developer.apple.com/documentation/lightweightcoderequirements/codedirectoryhash)

この requirement 準備の製品入口は executableURL / argv / launch / run / PID / signal / Wait / readiness を公開しません。
後続の [Swift 内部の終了観測](mac-foundation-process-completion.md)は固定 self-child の exact Wait を試験しますが、この requirement 準備の producer 起動や same-image enforcement は未実証です。
型付き setter/getter が使えること、同じ object が残ること、OS が実際に拒否することは別の証拠です。
静的署名検査や PID / path / hash の再確認を起動時保証へ昇格しません。
[Apple static validation](https://developer.apple.com/documentation/security/secstaticcodecheckvalidity(_:_:_:))

ObjC の launchRequirementData に raw dictionary / PropertyList / 独自 JSON を渡す bridge は作りません。
Swift の typed setter を使い、Codable は fixture の観測だけに用います。観測 JSON を起動要件の bytes として再投入しません。
将来 Rust と結ぶときも、private な Process を同じ worker に保持して native I/O と内部終了観測をつなぐ設計が必要です。固定 child の Swift Wait 成功だけでは、製品経路の Wait / joins は成立しません。
現在の Rust std::process::Child / Pump / supervisor / outer monitor は不変です。Swift の別親を挟んで exact Child の意味を変える方式は採用しません。
[Apple launchRequirementData](https://developer.apple.com/documentation/foundation/process/launchrequirementdata)

## 合成 fixture の実行と結果の読み方

既存の Swift compiler / macOS SDK がある Darwin 開発環境で、repo root から明示的に実行します。install や developer tools の既定変更は行いません。

```sh
python3 -B scripts/check_mac_launch_requirement.py
```

checker は normal source と fixture flag の source を arm64 / x86_64 に compile し、host architecture の専用 fixture だけを実行します。
compile target の macOS 11.0 は、14.4 より低い明示的な試験条件です。製品や既存 artifact の minimum OS の宣言ではありません。
LightweightCodeRequirements が fixture binary で weak-linked されることも検査します。実際の old OS での実行・ロードは未検証です。
generated source / compiler cache / binary は自己所有の temporary directory に置き、同じ driver の終了後に削除します。

fixture は合成 Team / hash だけで、availability、未設定拒否、固定 AND 構造、同じ未起動 object、異なる Team / release の typed 非同値、入力制約と owner 解放を確認します。
GUI / producer / real policy / key / signer / filesystem identity は使いません。Process オブジェクトの `.run()` を一度も呼びません。

`LaunchCodeRequirement.Equatable` は macOS 15.0+ です。
setter/getter は AND 条件の順序を変え得るため、Codable byte 一致を論理同値の条件にはしません。初回にこの誤った fixture 期待で失敗し、独立した最小 probe で同じ条件集合と公式 typed 比較の true を確認しました。
fixture は公式 Equatable で同値を確認し、Team / release を変更した2負例も同じ比較で false を要求します。自作の predicate 正規化や判定緩和は行いません。
[Apple Equatable conformance](https://developer.apple.com/documentation/lightweightcoderequirements/launchcoderequirement)

14.4 上では native preparation API の範囲でも、fixture の typed 同値比較は未実行なので exit 2です。14.4 未満も native 部分未実行の exit 2です。checker はどちらも成功にしません。
supported host の完全な fixture は35件・失敗0・producer起動0を要求し、0件・部分成功・skip・起動があれば拒否します。Apple framework の getter/encoding の観測成功は、実 signed code の enforcement の成功ではありません。

## 残る具体接続

[後続の未起動 FFI / IO 準備](mac-prelaunch-ffi-io.md)は、同じ owner の保持と匿名 pipe の準備・解放までを扱います。
実 producer の EOF、exact Process の終了待ちと終了結果、active IO / supervisor / outer monitor joins を既存 owner に接続する工程は残ります。
requirement / owner を作っただけでは Closed、OFF、回収完了や GUI Quit の許可にしません。unknown の元 owner を保持し、別 process に置換して回復したことにしません。
actual signed release、未対応 OS、不一致 publisher / code hash、置換、部分失敗、architecture / Rosetta、runtime / library の制約を、別途許可した実物試験で検証する必要があります。
production config は未設定、通常起動は未接続です。a4 / task-7 / private は変更せず、GUI 再開はユーザーの解除連絡まで待機します。
