# Resident GUI 配線の限定受入

関連: [#25](https://github.com/O2-CONNECTIVE/Tether/issues/25)、[#189](https://github.com/O2-CONNECTIVE/Tether/issues/189)。

## 製品側の変更

React の resident panel は既存の `get_resident_status` と `request_resident_intent` のみを呼ぶ薄い adapter です。通常の Tauri 入口にもこの二つの IPC を登録しますが、ResidentPort は unbound のままです。Mac の通常起動は source-unavailable、他 OS は unsupported を返し、起動・binding・policy・credential の IPC は追加しません。

受付 receipt と適用 observation は別に扱い、対応する request ID、revision、鮮度を確認します。実 Go mapper が初回認証中に返す厳密な ON pending（standby unknown / registration connecting / work idle / cleanup confirmed / 両操作不可）は操作を閉じたまま観測を続けます。cleanup unknown や transition unknown、対応しない pending はこの例外に含めません。unknown、期限超過、矛盾した応答では操作を閉じ、後着応答で安全な状態へ戻しません。CLI や GUI が独自に host の認可・状態を所有する構成にはしません。

## CI が確認する境界

`mac-resident-gui.yml` の次段階は、固定 source の React、実 WebView/Tauri IPC、既存 BoundDuplex/StandbyOwner と実 Go `residentpipe.run` / `residentmac.Standby` を接続します。従来の Python 合成 status producer は live lane では使用しません。Go の source-pin overlay と既存の自己所有 loopback TLS gateway fixture だけが合成 policy/key/CA を供給し、通常 enrollment / publisher trust は変更しません。製品の同じ loader 後の処理、mapper、JSONL、Close を使います。

driver は自分が実際にクリックした操作について、新しく受けた accepted receipt と同じ request ID / intent の applied observation を必要とします。対象ボタンの有効化だけや自然 OFF を成功にせず、request ID は推測せず実際の receipt から読み取ります。stale と新しい revision を確認した場合だけ既存の有限再試行を許し、rejected / unknown / 未確定の再試行はしません。WebView は ON の適用後、同じ status の registration が authenticated になるまで待ってから OFF を操作します。元 stdin EOF で Go の同じ Standby が Close され、その後 fixture gateway の Close / HTTP serve join が終わって初めて producer process が退出します。Rust の同じ owner の I/O・Wait・outer monitor まで確認した場合だけ成功です。失敗・unknown は元 owner を保持し、kill や timeout を cleanup 成功にしません。Go test runner の出力は stderr に分離し、JSONL stdout を汚染しません。

これは real Go producer と合成 enrollment の接続試験です。実利用者の認証、実 dot、native work 実行、通常起動、製品 Quit、署名配布は未受入です。新しい構成の成功は最終 source の hosted CI 終端で別途判定し、過去の合成 pipe 成功を引き継ぎません。

driver の段階は専用 CI context の local/main にだけ許可した event emit で固定 enum として受信します。listener は WebView 作成前に登録し、順序違い・不正・重複は失敗を保持します。この event は観測であり、元 pipe と owner の証明を置き換えません。通常 context の権限は変更しません。

この composition は `resident_gui_fixture` compile cfg と専用 example に限定し、通常入口から選択できません。実行は disposable hosted Mac ARM64 / macOS 27 / Xcode 27.1 のみです。source と所有権の独立レビューを済ませてから CI を実行します。

## 確認しない境界

この producer は実 Go host でも signed native27 launcher でもありません。Mac27 NativeSession / NativeOuterOwner の unknown 保持、通常の trust gate、production enrollment は変更しません。実 host 起動、実 dot/MCP 接続、署名・notarization、製品 Quit の解除、個人 Mac での受入は未確認です。合成 fixture の confirmed をこれらの受入へ流用しません。

## 検証版の識別と配布

専用 CI window は「カレントの検証版（合成CI）」と表示します。allowlisted JSON の検証記録には source commit、UTC build 記録時刻、GUI と Go producer の binary SHA-256、確認範囲を含めます。CI は実行後にも binary hash を照合します。正式版の semver を変更せず、CI 内だけの一時 .app に同じ binary を配置します。upload・Release 公開は行いません。この CI だけではダウンロード可能な接続検証版は提供されません。

## 診断の読み方

固定イベントで、同一 binary の `__info_plist` 区画有無、同一 process の CoreFoundation main bundle / identifier の有無、実行ファイルの `.app` 内配置、同じ generated Context の index asset、許可された root/index navigation、index response の HTTP status / HTML MIME / 非空、document commit / finish、driver 開始を区別します。識別子・path・URL・header値・body は出力しません。asset response の内容や header は変更しません。bundle の存在・配置は署名・真正性・WebView動作保証ではありません。

`page-started` は固定 wry の macOS delegate の didCommitNavigation 通知です。これが無いだけで provisional navigation request 自体が無かったとは断定しません。観測は原因の切分けです。activation policy、セキュリティ設定、所有権や期限は変更しません。

固定 Tauri の resource-response callback は asset lookup 成功後に呼ばれます。callback の欠落だけでは request 自体の不在と判断せず、embedded-index / navigation-root の観測と合わせます。otool 失敗は固定 unavailable を返して停止し、absent に読み替えません。

## 同一 binary の一時 bundle 比較

裸 binary の診断では embedded index は存在しましたが、埋込 plist / bundle identifier / .app 内配置はありませんでした。これだけで停止原因とは判断しません。次の比較は同じ build binary の SHA-256 を保った実コピーを、新規 run directory 内の排他的な `TetherCurrentGUIFixture.app/Contents/MacOS/resident_gui_ci` に置く CI 専用手順です。固定 Info.plist は現在の専用 Context の name / identifier / version と一致し、表示は「カレントの検証版（合成CI）」です。必須 version metadata は既存の 0.1.0 を使い、新しい release version を切りません。

helper は regular/no-follow 入力・512MiB 上限・固定 plist・起動前後の元/コピー hash を確認し、既存 output を上書き・再利用しません。元 keeper がコピー先の実ファイルを直接 Popen/Wait します。open / LaunchServices / shell launcher / signing / strip / quarantine / activation / entitlements / install / upload は行いません。失敗や unknown で再試行・fallback・kill をせず、元 owner と同じ60秒/180秒の監視境界を保ちます。

bundle identifier の存在は比較条件の成立であり、成功・署名・製品接続の証明ではありません。元 pipe の ON/OFF、同じ owner の Close/IO/Wait と driver 判定が全て揃う必要があります。過去の裸 binary と別 VM での比較なので、結果だけで単独因果を確定しません。

## 初回 URL の限定修正

固定 Tauri 2.12.5 の `get_app_url` は macOS の local protocol を `tauri://localhost` とし、`WebviewUrl::App(index.html)` では join を省略します。非 special URL の path は空で、同版 `get_asset` は空 path を index.html に対応させます。fixture guard はこの公式 root の空 path も許可し、従来の `/` / `/index.html` と同じ local root として扱います。scheme / host / credentials / port / query / fragment の制約は維持し、about:blank、一般 HTTP、外部 origin、任意 path を許可しません。実際の固定 Tauri Url 型で空 path と拒否条件を回帰検査します。source 上の欠陥修正であり、実 GUI 成功は新 head CI の終端で別に判断します。
