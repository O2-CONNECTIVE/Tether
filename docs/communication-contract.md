# GUIとバックエンドの通信契約

[構成と実装境界](architecture.md) / [安全要件](security.md) / [脅威モデル](threat-model.md)

製品の新規接続ゲートは閉鎖したままです。
認可、期限、失効の判断はGo側に置く設計であり、GUIの表示、ボタン、保存設定は権限の根拠になりません。
有効化条件は既存の[バックエンド契約](SECURITY_BACKEND_CONTRACT.md)が所有します。
本書は通信の意味と実装境界を補足し、新しい製品protocolの採用を宣言しません。

## 対象版と根拠

| 区分 | 固定した参照と提供範囲 |
| --- | --- |
| 公開実装 | main `43ebd35e9357ca0d5a25d42a12f2faf2e87a4152`。Python/Tkinterと旧Upterm CLI、オフラインモデル。GoとTauriへの製品配線はない |
| 評価ソースの観察 | 2026-10-27に読み取り照合した限定実通信fixture。未公開で、確定スナップショットの代用にはしない。下記の内容ハッシュで照合範囲を固定 |
| Tauri adapter案 | 未実装の設計課題。command名、event名、wire版、error code、IPC方式は未決定 |

評価ソースの参照名は将来の公開候補内の相対名です。
公開mainに存在しないファイルへリンクは張りません。
内容が変わった場合は再照合し、この観察を新しい版の保証へ読み替えません。

| 観察したファイル | SHA-256 |
| --- | --- |
| `src/rescue_communication.py` | `5a05db53ccaaeebf469025246ac8e5d83c488ed71483efcb1345d622cdcdefc4` |
| `backend/evaluation_fixture.go` | `6e7e60cbec27166d20b433b3343b76b874d286b3c16f1c15e6d4bf4d6f15b733` |
| `backend/session.go` | `e7c48e02752a06064c247cd04d399fd5b717b826ec10debd58ea14de9ef283be` |
| `backend/internal/lease/authority.go` | `55bc883c172ca42be88664ccf3e7e4d99f5d3443f8f5d40899ad17d643c15035` |

## 公開mainのGUIと旧CLI

[GUI](../src/rescue_host.py)はワーカースレッドの結果をプロセス内のQueueへ入れ、150msごとに取り出します。
`start_ok/error`、`stop_ok/error`、`info_ok/error` はPython内部のイベント名で、Goのeventではありません。
エラーpayloadは例外の文字列で、安定したwire error codeはありません。
新規開始はGUI入口とワーカー入口の両方で常時拒否します。
残っている開始処理の実行可能性を、通信契約の提供済み機能に数えません。

[旧CLIラッパー](../src/rescue_core.py)の `session info` はJSONの名前と状態を検証します。
既知状態は `starting/ready/reconnecting/disconnected/ending/ended` です。
古い `info_ok` は現在の名前と一致しなければ無視し、`stop_ok` も一致する名前だけを画面から消します。
名前には起動世代の証明がないため、`ended` 応答だけで回復情報を削除しません。
CLIの停止受付、GUI終了、鍵ファイル削除を既存接続の終了証明にしない扱いは[回復処理](recovery.md)に従います。

CLI呼出しの予算はversion確認10秒、info 15秒、stop 65秒です。
stop後の確認ループは既定5秒ですが、個別info呼出しの待ち時間もあるため全停止の5秒上限を意味しません。
Uptermの版確認とアプリの `0.2.1-dev` は、将来のGUI↔Go protocol版とは別です。
この経路に要求ID、配送保証、全eventの重複排除、起動世代による順序保証はありません。

## 評価fixtureのstdio

限定評価GUIは、同梱helperの所在と固定SHA-256を確認して `--stdio` で自己子を起動します。
Go側は `rescue_evaluation` build tagの専用入口で、自己所有のIPv4 loopback高位ポートだけを使います。
wire上の `127.0.0.1:22` 判定と、物理的な高位ポートfixtureへの接続を分離しています。
この成功は製品22番、実relay、内側shellの検証ではありません。

要求はUTF-8のJSON objectを1行ずつ送り、内容は `action` だけです。
固定した秘密のない[最小要求例](contracts/evaluation-requests.ndjson)は次の2行です。
実行用スクリプトではありません。

```json
{"action":"start"}
{"action":"stop"}
```

Goのdecoderは重複field、未知field、欠落field、未知action、余剰JSONを拒否します。
scanner bufferは256 bytes、入力readerは1,025 bytes、要求行数は8件までに制限されています。
これらは現コードの資源上限で、任意長メッセージの互換性を約束しません。
初回要求は5秒以内の `start` が必要です。
初期選択ではEOFと要求が競合し得るため、EOF直前に送った要求の処理完了は保証しません。

| 入出力 | 実装で観察した意味 |
| --- | --- |
| `start` | 子1回につき1回のfixture実行。ready後の再startは不正入力として中止する。冪等な再送ではない |
| `stop` | GUI controllerは同じ実行への送信を1回に抑える。Goはready後に読み取って失効し、後続要求の個別ackや重複応答は返さない |
| `boot` | 子のPID、scope、45秒hard TTLを報告。親は所有PopenのPIDと照合し、WindowsではJob加入も確認する。IPC認証ではない |
| `check` | 個別検査のname、passed、evidence。製品接続の許可や受入44条件の合格通知ではない |
| `ready` | 当該fixtureのデータ往復後の観測。Go authorityの `ready` 状態とは別で、この時点のauthorityは既にactive |
| `final` | 通常完了時はpassed、checks、failed、未達と未検証の配列等を報告。初回stopの拒否時はkind、passed=false、reason=start-requiredだけでexit 2。初期EOFや5秒待機切れではfinal自体がない。子の終了証明ではない |
| `owner` | Goのwire eventではなく、親controllerがWait、Job、所有TCPの観測後に生成する結果 |

親は `boot/check/ready/final` だけを受け付け、bootより前の他eventと二重bootを拒否します。
出力は1行16,384 bytes、合計65,536 bytes等で制限し、有限Queueへ渡します。
全eventに連番やrequest IDはなく、boot以外の重複や逆順を一般的に検証するprotocolではありません。
correlationは所有Popenとそのpipe、bootのPID、実行中のcontrollerに依存します。
PIDだけで起動世代や認証済みprincipalを証明しません。

親はfinalの `passed=true` に加え、exit code 0、Wait完了、WindowsではJob active=0、取得可能な所有TCP観測に残留がないことを確認します。
欠落final、異常exit、予算切れ、JSON不正、所有終了の未確認は成功にしません。
TCP観測が取得できない場合もあり、親の成功は独立観測による全資源不存在の証明ではありません。
wireのerrorは統一codeではなく、`final.reason`、失敗check、非0 exit、親のerror文字列へ分かれます。
stdoutを一般ログと混在させず、UIへの表示と公開時の選別を分ける必要があります。

## 期限、切断、親終了

評価子の独立hard TTLは45秒、親controllerの実行予算は30秒です。
fixtureは最大12秒、lease 8秒で、更新要求はありません。
これらは既存[受入Issue #1](https://github.com/O2-CONNECTIVE/Tether/issues/1)の最大20秒、lease 3秒と1秒ごとの更新という主受入設定とは別です。
UI timerでGoの期限を延長する経路はありません。

stdinのEOFは、初回要求前なら起動失敗、fixtureの待機段階なら `parent-eof` として失効します。
検査中のstopやEOFは待機段階へ達するまで処理が遅れる可能性があり、pipe切断直後の遮断を一律に保証しません。
stdoutの切断や親の無応答は、明示的に検証済みの停止ackではありません。
親は終了時にstop、stdin閉鎖、有限join、所有Jobのhandle閉鎖、必要時の所有PopenのkillとWaitを試みます。
強制終了ではfinalを受け取れない場合も、final送信後に終了が滞る場合もあります。
final欠落、異常exit、終了未確認を通常検査成功へ変換しません。
同一ユーザーの自己JobとTTLは、別保護主体のsupervisorやOS停止中の期限強制を実証しません。

## 観察したGo APIと状態

Goの `Session` はlistenerを作らず、受理済み接続を受け取るAPIです。
`New/Ready/Activate/Renew/Revoke/Snapshot/Close` はGo内のメソッドで、stdio command名ではありません。
`Identity` は `LaunchID/Epoch/PolicyDigest` で、呼び出し側が生成、保護、再利用防止を担います。
Pythonモデルの `schema=1/run_epoch` や方針JSONの `schema=1` とGo struct名を同じwire定義と見なしません。

| 状態と操作 | 観察した制約 |
| --- | --- |
| `prepared → ready → active` | 同じIdentityで指定の一段階だけ遷移。New時に最大期限と5秒の準備期限を固定する |
| `Renew` | ready/activeかつ同じIdentityで、既受理値より厳密に大きいuint64連番のみ。初回は0も許容。最大期限と準備期限は延長しない |
| `Revoke` | 同じIdentityを要求。失効は不可逆で、同じ失効要求で復活しない |
| `revoking` | 新規認可を拒否し、既存の所有通信を終了処理する。close故障時は参照と失敗を保持する |
| `terminated` | Closeが所有通信、予約、worker等の終了を確認できた場合。単なるRevoke受付やauthority timerの終了とは別 |

Goのerrorには `ErrInvalidConfig/ErrNotAccepted/ErrUnconfirmed` と内部の拒否errorがあります。
`Serve` の `ErrNotAccepted` だけは接続の所有を移さず、呼出元が閉じます。
これらをそのままUI向けwire error codeに採用する合意はありません。
Closeのcontext期限切れや未確認を終了成功へ変換しません。

## 製品接続の未完境界

Tauri製品adapterのwire版・envelope・実保護主体は、この旧評価fixture契約から確立しません。製品開始gateは閉鎖し、自己子・stdioの成功を実通信や別主体隔離へ転用しません。現在のMac読取専用viewerは[専用仕様](../ui/mac-status/README.md)を参照してください。
