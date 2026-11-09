# Resident GUI 配線の限定受入

関連: [#25](https://github.com/O2-CONNECTIVE/Tether/issues/25)、[#189](https://github.com/O2-CONNECTIVE/Tether/issues/189)。

## 製品側の変更

React の resident panel は既存の `get_resident_status` と `request_resident_intent` のみを呼ぶ薄い adapter です。通常の Tauri 入口にもこの二つの IPC を登録しますが、ResidentPort は unbound のままです。Mac の通常起動は source-unavailable、他 OS は unsupported を返し、起動・binding・policy・credential の IPC は追加しません。

受付 receipt と適用 observation は別に扱い、対応する request ID、revision、鮮度を確認します。unknown、期限超過、矛盾した応答では操作を閉じ、後着応答で安全な状態へ戻しません。CLI や GUI が独自に host の認可・状態を所有する構成にはしません。

## CI が確認する境界

`mac-resident-gui.yml` は固定 source の React を実 WebView で表示し、実 Tauri IPC、既存 BoundDuplex、匿名の synthetic pipe producer、同じ StandbyOwner を通して ON → OFF → Close を確認します。invoke の JavaScript mock は使いません。producer は固定した二つの intent と revision を検査し、元 stdin の EOF 後だけ closed を返します。同じ owner の I/O・Wait・outer monitor の回収まで確認できた場合だけ成功です。失敗や unknown は保持し、強制終了を cleanup 成功と扱いません。

driver の段階は専用 CI context の local/main にだけ許可した event emit で固定 enum として受信します。listener は WebView 作成前に登録し、順序違い・不正・重複は失敗を保持します。この event は観測であり、元 pipe と owner の証明を置き換えません。通常 context の権限は変更しません。

この composition は `resident_gui_fixture` compile cfg と専用 example に限定し、通常入口から選択できません。実行は disposable hosted Mac ARM64 / macOS 27 / Xcode 27.1 のみです。source と所有権の独立レビューを済ませてから CI を実行します。

## 確認しない境界

この producer は実 Go host でも signed native27 launcher でもありません。Mac27 NativeSession / NativeOuterOwner の unknown 保持、通常の trust gate、production enrollment は変更しません。実 host 起動、実 dot/MCP 接続、署名・notarization、製品 Quit の解除、個人 Mac での受入は未確認です。合成 fixture の confirmed をこれらの受入へ流用しません。

## 検証版の識別と配布

専用 CI window は「カレントの検証版（合成CI）」と表示します。allowlisted JSON の検証記録には source commit、UTC build 記録時刻、実行 binary SHA-256、確認範囲を含めます。CI は実行後にも binary hash を照合します。正式版の semver を変更せず、CI 内だけの一時 .app に同じ binary を配置します。upload・Release 公開は行いません。この CI だけではダウンロード可能な接続検証版は提供されません。

## 診断の読み方

固定イベントで、同一 binary の `__info_plist` 区画有無、同一 process の CoreFoundation main bundle / identifier の有無、実行ファイルの `.app` 内配置、同じ generated Context の index asset、許可された root/index navigation、index response の HTTP status / HTML MIME / 非空、document commit / finish、driver 開始を区別します。識別子・path・URL・header値・body は出力しません。asset response の内容や header は変更しません。bundle の存在・配置は署名・真正性・WebView動作保証ではありません。

`page-started` は固定 wry の macOS delegate の didCommitNavigation 通知です。これが無いだけで provisional navigation request 自体が無かったとは断定しません。観測は原因の切分けです。activation policy、セキュリティ設定、所有権や期限は変更しません。

固定 Tauri の resource-response callback は asset lookup 成功後に呼ばれます。callback の欠落だけでは request 自体の不在と判断せず、embedded-index / navigation-root の観測と合わせます。otool 失敗は固定 unavailable を返して停止し、absent に読み替えません。

## 同一 binary の一時 bundle 比較

裸 binary の診断では embedded index は存在しましたが、埋込 plist / bundle identifier / .app 内配置はありませんでした。これだけで停止原因とは判断しません。次の比較は同じ build binary の SHA-256 を保った実コピーを、新規 run directory 内の排他的な `TetherCurrentGUIFixture.app/Contents/MacOS/resident_gui_ci` に置く CI 専用手順です。固定 Info.plist は現在の専用 Context の name / identifier / version と一致し、表示は「カレントの検証版（合成CI）」です。必須 version metadata は既存の 0.1.0 を使い、新しい release version を切りません。

helper は regular/no-follow 入力・512MiB 上限・固定 plist・起動前後の元/コピー hash を確認し、既存 output を上書き・再利用しません。元 keeper がコピー先の実ファイルを直接 Popen/Wait します。open / LaunchServices / shell launcher / signing / strip / quarantine / activation / entitlements / install / upload は行いません。失敗や unknown で再試行・fallback・kill をせず、元 owner と同じ60秒/180秒の監視境界を保ちます。

bundle identifier の存在は比較条件の成立であり、成功・署名・製品接続の証明ではありません。元 pipe の ON/OFF、同じ owner の Close/IO/Wait と driver 判定が全て揃う必要があります。過去の裸 binary と別 VM での比較なので、結果だけで単独因果を確定しません。
