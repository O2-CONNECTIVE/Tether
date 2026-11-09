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

専用 CI window は「カレントの検証版（合成CI）」と表示します。allowlisted JSON の検証記録には source commit、UTC build 記録時刻、実行 binary SHA-256、確認範囲を含めます。CI は実行後にも binary hash を照合します。正式版の semver を変更せず、.app の生成・upload・Release 公開は行いません。この CI だけではダウンロード可能な接続検証版は提供されません。
