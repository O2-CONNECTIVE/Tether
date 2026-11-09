# Tetherの共通application APIとadapter境界

[#32](https://github.com/O2-CONNECTIVE/Tether/issues/32) / [製品目標 #189](https://github.com/O2-CONNECTIVE/Tether/issues/189) / [操作辞書 #188](https://github.com/O2-CONNECTIVE/Tether/issues/188)

## 今回採用する小さい境界

保持済みの固定作業について、出力本文を含まない実行メタデータ読取を
`internal/residentapi.ExecutionReader` / `Execution`へ型として切り出す。
実装は既存gatewayの`residentSession.GetExecution`で、既存
`FixedGetExecution`が同じ実装を呼び、従来のMCP JSONへ明示変換する。
新しい認可経路や別の状態ストアを作らない。

これはGo内部の読取portであり、公開REST API・SDK・任意のSessionメソッド公開ではない。
CLI・GUIからこのportへの製品bindingは未実装。共通API全体の完成でもない。
通常MCPとホストのCLI/GUIは異なる呼出主体なので、同じ型を使えることと同じ権限を持つことを区別する。

## 現実装と残る差分

| 責務 | 現在の正本・実装 | 今回と後続 |
|---|---|---|
| host待機ON/OFF・状態 | `residentmac.Standby`、`residentpipe`、所有したnative pipe | 今回変更しない。CLIとGUIは同じmapper/ownerを使う方針とし、別の承認フラグを作らない |
| HTTP認証・MCP session所有 | `agentgateway.authorize`、principal別session lookup | 今回変更しない。未認証のlocal呼出しを同等の主体とはしない |
| 有限作業の認可・実行・停止 | `residentwork`、`agenttransport`、正規`agentexec.Session` | 既存固定operation、grant、epoch/policy、失効、元ownerを維持 |
| 保持実行メタデータ | 今回のtyped `ExecutionReader`と既存`FixedGetExecution`変換 | 最初の共有契約。別adapterに渡す際も同じ認証済みscopeの供給が必要 |
| 一覧・出力・cancel/release | gatewayの既存resident実装とMCP schema | 段階的な型移行候補。今回移行済みとしない |
| 許可範囲・診断 | 既存policy・秘匿境界、#27 | caller別projection、公開fieldと保管契約は別設計。raw policy/鍵/ログを共通DTOへ足さない |
| OS差分 | Mac current-userとWindows guestのruntime | 共通型でsandbox/終了保証を均一化しない |

操作辞書やGUIを全面移動せず、小さい契約と実consumerから移行する。
ホスト管理のOFFは全新規受付の封鎖と所有作業回収、MCP cancelは特定の作業への停止要求である。
両者を一つの無認可`Stop(id)`へまとめない。

## メタデータ読取の契約

- 呼出者は、認証済みprincipal・登録host・control generationに既に束縛されたreaderを受け取る。
  portの引数にprincipal、host、generation、token、承認boolはない。
  readerを構築・取得する新公開factoryは追加しない。型の実装やIDの所持自体は権限ではない。
- request IDは32桁小文字hex。gatewayが既存のscope内で検索し、別世代・別entry・不在を
  同じ`not_found`で返す。詳細な存在情報を漏らさない。
- 読取は元collectorの保持観測をロック下で値として複製する。hostへの通信、出力ack、
  release、command開始、許可延長、現在のcontrolへの再bindingを行わない。
- 過去世代の正規sessionは自分の保持記録を読める。新世代のworkを始めるには既存の
  fresh initializeと新たな認証・認可が必要。古い記録の読取を許可の復活へ使わない。
- `Execution`は出力bytes、認証主体、host、generation、policy、診断文字列を持たない値。
  返却値の変更は保持記録や他adapterの返却値を変えない。
- 終了前にreleaseされたのではなく、既存releaseが回収条件を満たした後に残した
  tombstoneも同じ観測型で返す。元commandを一度も観測しなかった記録からexitを捏造しない。

### 状態とエラーは合成しない

`phase`、command phase、exit known/code、stop理由、terminal、command cleanup、
host-owner cleanup、gateway cleanup、collection ended、result released、work errorを独立して保持する。
`exit.known=false`のとき外部JSONにcodeを出さない。known=trueの0は実際の終了値として残す。

| 観測 | 意味・再試行の扱い |
|---|---|
| `invalid` | 読取入力が不正。修正せず同一入力を繰り返さない |
| `not_read` | nil/取消済みcontext、ロック取得後の取消、内部tombstoneの型欠落。不完全な値を返さず、作業停止・不在の証明でもない |
| `not_found` | このscopeでは保持記録を返せない。非実行を証明せず、新規Startへ自動変換しない |
| work errorあり | 読取自体は成功し得る。作業の失敗をAPI読取失敗と同一視しない |
| collection ended | gatewayの観測終了。全出力・command終了・回収完了を保証しない |
| cleanup unknown | 元ownerを保持。新ownerへの置換や成功表示で覆わない |

読取contextのdeadlineは応答予算であり、所有作業のdeadlineではない。
取消・timeoutのHTTP応答やMCP notification、OFF受付receiptを物理終了証拠として使わない。

## IDと互換性

- MCP JSON-RPC IDは一要求の相関、MCP session IDは認証主体に所有された接続文脈。
- 固定work request IDはそのscopeの重複実行防止キー、command IDは実行観測の対象。
- control generationとnative epoch/policy digestは別の失効・認可境界。
- GUI revision/intent IDは観測鮮度と制御要求の相関。work IDを代用しない。
- 内部Go型はJSON wire型ではない。`schema_version=1`、field名、エラーJSON、text mirrorは
  既存adapterの責務として維持し、DTOをそのまま自動JSON公開しない。
- 順序逆転・遅延・別payloadの再送は既存ownerとadapterの拒否規則を維持。
  今回は新しいidempotency cacheやretry機構を追加しない。

## 信頼境界と出力

| 脅威 | 強制する場所と制約 |
|---|---|
| 別利用者がsession/work IDを推測 | HTTP認証、principal所有session lookup、generation/entry限定read。IDはcredentialではない |
| 同じuserの別local processがstdin/状態を偽装 | 所有したpipe・起動/配置契約が必要。localであるだけでは認証済みにしない。今回は新local公開portなし |
| 外部tool出力が権限拡大を指示 | 出力はデータ。Goの固定operation/policy/期限/署名検証が権限正本。annotationやmodel判断は承認にならない |
| 切断・親死・回収unknown | 新規admissionを封鎖し元所有者を保持する既存契約。今回の観測型で回収を再試行・代替しない |
| 秘密や過大出力を共通API経由で流す | メタデータのみの固定field集合。出力読取は既存の別API/byte上限。診断・監査の追加出力や保管は行わない |

既存の保持上限、tombstone、明示release、HTTP同時数とMCP応答上限は変更しない。
新しい永続journalや無期限保管も追加しない。監査イベントを新設する場合は、#27で
秘匿・サイズ・回数・保管期間を先に決め、raw token/claims/出力本文を既定記録しない。

## 分割、検証、完了判定

1. 今回: メタデータのtyped port、実gateway consumer、旧MCPとのgolden互換。
   通常/race、取消、別scope、tombstone、値の非共有、認証HTTP経路を回帰確認。
2. 後続: 一覧・有限出力のtyped契約。cursor、欠落、保持終了と完了の区別を維持。
3. 後続: cancel/releaseのtyped結果。停止受付・実終了・全owner回収を分離し、対象権限も維持。
4. 後続: CLI/GUIの製品compositionへ必要な読取portを供給。caller認証・所有・鮮度・
   OFF/Quitの縦断証跡をOSごとに取得して初めてadapter統合の完了とする。

今回の受入は内部契約と旧公開JSONの互換性まで。#32全体、#189の実dot/GUI/両OS/配布、
既存L/W/U条件・二重停止・背圧・Windows timeoutの未達を完了へ変更しない。
rollbackはこの型切出しと互換変換を元に戻すsource変更。deploy/credential/永続データの変更はない。

2026-11-09に採用済み固定版MCP `2025-06-18` の
[tools](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)と
[cancellation](https://modelcontextprotocol.io/specification/2025-06-18/basic/utilities/cancellation)を再確認。
structuredContent/outputSchema/text併記と要求取消の意味を参照した。
最新MCP版への適合や実clientの解釈を確認したという主張ではない。
