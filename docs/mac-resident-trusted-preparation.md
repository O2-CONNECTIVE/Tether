# Mac resident の native enrollment 選択と起動信頼

#214 に依存する source 準備です。#200 の Windows 阻害、stack 未統合 / CI 未実行、通常 Mac 起動 / IPC / GUI の未接続を維持します。
実 enrollment の作成・移行、実鍵・既存 policy の読取り、署名・配置・producer 起動・OS trust の変更は行っていません。

## 今回閉じる製品契約

最小版は現在ユーザーの enrollment を一つだけ扱い、そのユーザーが常駐 ON / OFF を明示します。
profile 一覧の探索や「最新」の自動選択、UI / CLI / env / remote からの policy path 指定は設けません。
native OS account database から現在 UID の home を取得し、次の固定相対名だけを選びます。

```text
<OS account home>/Library/Application Support/
  com.o2connective.rescue-host.shell/resident-policy.json
```

既存 app identifier を使う製品配置の契約であり、実ファイルが存在・登録済みであるという報告ではありません。
既存運用の文書を勝手に移動・作成・chmod せず、未配置・不正・保護不足なら拒否します。
symlink や非正規 home の解決、別 path への fallback も行いません。

`residentmac` 内部の `loadEnrolledStandby` はこの選択だけを既存 `LoadStandby` へ渡します。
新しい export / factory / loader callback はなく、producer の `run`、通常 CLI / IPC / GUI から呼ぶ入口もありません。
sealed entrypoint の後続実装はこの内部組成を使い、任意 policy flag を再導入しません。

Go 1.27.3 の `UserHomeDir` / `UserConfigDir` は環境変数を使い、`user.Current` は初回結果を cache するため、選択元にしません。
Darwin / cgo の `user.LookupId` を使い、現在の real / effective UID・GID が変わっていないこと、非 root、lookup 結果の UID、有限の context と canonical absolute home を照合します。
Darwin / cgo 以外と `osusergo` build は拒否します。同期 OS lookup の途中を強制終了する時間保証はなく、復帰後に期限と identity を再確認します。
[Go os/user](https://pkg.go.dev/os/user@go1.27.3)、[Go os](https://pkg.go.dev/os@go1.27.3#UserHomeDir)、[Apple getpwuid](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man3/getpwuid.3.html)

選択は enrollment・認証・実行許可の証明ではありません。
既存の `ReadTrustConfig` と strict factory が ancestor / owner / ACL / same-handle / cleanup、schema / fixed scope / key fingerprint / policy digest を検査する契約はそのままです。
同じ UID の悪意ある process に対する sandbox や、現在ユーザーが書き換えられる文書の独立した publisher 証明にはなりません。
policy 変更は既存 factory の recheck / block 契約に従い、unknown を新 owner や新 profile に替えて回復したことにしません。

## 署名・配置・同じ image の実行に向けた推奨

推奨は GUI と producer を一つの signed app / release に対応付け、producer を固定の nested Mach-O code として扱うことです。
release 管理下の Developer ID publisher、producer signing identifier、配布 category を固定し、必要なら署名後の CodeDirectoryHash 集合で exact release も制約します。
値は UI、任意 manifest、raw SHA-256 引数や target 自身の自己申告から受け取りません。
同 publisher の将来更新を許す identity と exact release の固定は別の条件です。CodeDirectoryHash は通常の file SHA-256 ではなく、architecture / Rosetta による選択も考慮します。
[Apple LightweightCodeRequirements](https://developer.apple.com/documentation/lightweightcoderequirements)、[CodeDirectoryHash](https://developer.apple.com/documentation/lightweightcoderequirements/codedirectoryhash)

Tauri の `externalBin` は同梱方法を提供しますが、それだけで publisher や起動 image を証明しません。
WebView に汎用 shell / spawn permission を追加せず、native owner が生成・I/O・終了を所有する構成を維持します。
署名と notarization、hardened runtime、nested code の配置・署名順、release source / executable bytes / signer の記録は別途必要です。現在の bundle 設定は変更していません。
[Tauri sidecar](https://v2.tauri.app/develop/sidecar/)、[Tauri macOS signing](https://v2.tauri.app/distribute/sign/macos/)、[Apple nested code](https://developer.apple.com/library/archive/technotes/tn2206/_index.html#//apple_ref/doc/uid/DTS40007919-CH1-TNTAG201)

静的署名検査は、後の path-based exec との間の置換を閉じません。
Apple の `SecStaticCodeCheckValidity` にも同時変更の制限があります。PID に対する起動後の検査も、その前に実行された code を取り消せません。
[Apple static validation](https://developer.apple.com/documentation/security/secstaticcodecheckvalidity(_:_:_:))

公開 API による第一候補は macOS 14.4+ の `Foundation.Process.launchRequirement` です。
requirement に合わない executable を OS が実行しない契約ですが、現在の Rust `std::process::Child` / pipe / Wait をそのまま置き換えられるとは確認していません。
resident の verified-launch 機能にこの OS 下限を採る設計を推奨し、古い OS は未対応として拒否します。今回は app 全体の deployment target を変更しません。
別の Swift executable を親として挟むだけでは exact Child が変わるため不十分です。Foundation の生成・I/O・Wait を既存 supervisor / outer monitor と同一 owner として閉じる具体的な bridge と回帰が必要です。
[Apple Process.launchRequirement](https://developer.apple.com/documentation/foundation/process/launchrequirement)、[LaunchCodeRequirement](https://developer.apple.com/documentation/lightweightcoderequirements/launchcoderequirement)

| 案 | 実務上の差と今回の扱い |
| --- | --- |
| 推奨: 単一 native enrollment + signed app + OS launch requirement | 最小の UI と選択元。実 publisher の指定と native owner bridge が後続。今回実装するのは enrollment 選択と既存 loader への内部接続まで |
| 複数 enrollment の選択画面 | registry の真正性、選択・更新・削除・世代、同意と cleanup の追加契約が必要。最小版では保留 |
| 静的 hash / 署名検査 + 通常 Child | 観測用 preflight にはなるが verify-to-exec を満たさない。製品 gate を開く代替にしない |
| suspended spawn + 後から動的検査 | resume / signal / PID / cleanup が増え、同 UID の介入保証も未確立。私有 spawn API には依存せず採用保留 |

## Fixture と未実証

今回の unit は memory 内の合成 account facts だけを private selector に渡します。
fixed relative name、env 無視、UID / GID / home / deadline の拒否、unsupported build を検査し、native の正の lookup / policy / key read は行いません。
合成 account は登録済み identity や起動許可を作らず、製品 API へ入力できません。
native provider の前後 identity sampling と lookup は source review / compile の範囲で、実 OS の改変競合・停止時刻を実証していません。

後続の bridge fixture は、誤 signer / signing ID / release hash / category / script の拒否、起動前取消、部分生成、同じ owner の EOF / Wait / IO / supervisor / outer join、unknown 保持を対象にします。
fake の成功を実 launch requirement の enforcement としません。署名済み実物と置換競合、全対応 architecture / Rosetta、library injection の制限は、別途許可された実機試験が必要です。

## 人・組織が指定する必要があるもの

source 設計では、単一 enrollment、固定位置、env / UI override なし、現在ユーザー・非昇格、署名で保護した一つの release と OS enforcement を推奨として進めます。
実際の Developer ID の Team / signing identity と release 承認者だけは、この source から決められません。署名 credentials の作成・照会は行わず、実接続前に正規所有者が指定します。
既存 policy の固定位置への移行も、実ファイルに触る別工程です。今回の架空値や私有評価 pin で代用しません。
