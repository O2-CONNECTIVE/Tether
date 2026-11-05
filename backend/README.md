# 未配線の SSH バックエンド基盤

> **対象範囲の更新（2026-11-01）:** 以下の旧SSH / Windows / Pythonに関する本文・受入条件は、その方式と記録時点の履歴として保持しています。現在のTetherはAI commandと一時派遣を目標にMacを優先し、現状の正本は[文書索引](../docs/README.md)と[検証状態](../docs/tether-pilot-status.md)です。旧ゲート閉鎖や未達を解除・達成扱いにはしません。Macは現在ユーザー・非sandboxであり、Windows隔離guestの保証を引き継ぎません。

現行Goの入口は `cmd/rescue-gateway`、`cmd/rescue-mac-agent`、`cmd/rescue-mcp` です。以下は旧forward-only SSH基盤の説明であり、Go module全体にCLIやlistenerが存在しないという現在の宣言ではありません。

一時非管理者guestの単発ライフサイクルは[設計文書](../docs/temporary-guest-account.md)と[PR #131](https://github.com/O2-CONNECTIVE/rescue-host/pull/131)を参照してください。
作成・保護journal・SID指定清掃と[限定権限検査](../docs/guest-limited-inspection.md)には内部source候補と合成試験があります。
正規provisioningとの接続と実Windowsでの保護成立は未検証で、公開入口は未設定拒否を維持します。
[固定配置adapterの接続範囲](../docs/guest-staging-adapter.md)は、実行時token gateと実機操作の残件を分けて説明します。

この Go モジュールは製品向けの基盤候補です。
Python と GUI の開始拒否は変更せず、CLI、listener の生成、GUI からの呼び出し、Upterm への配線を持ちません。
この基盤の追加や試験成功だけで製品の開始ゲートを解除できません。

## C本線の固定workload候補

[ADR-0003](../docs/adr/0003-owned-guest-workload.md)で端末内実行を製品が所有し、利用中だけLocalSystem監督serviceを使う設計を採用しました。
保護runtime設定からguestと固定無害targetを準備するsourceを統合し、固定評価入口から同じ確認応答の書込み完了後に単回開始する接続を追加しています。
既存の準備専用APIは開始へ昇格せず、評価用固定値は未設定で実message認証helperの起動資源も未構成です。
これは既存forward-only基盤の宛先やsession拒否を変えず、実SSH dispatcherの実装・製品caller・実Windows保護の証明にはしません。
公開entryの未設定拒否と製品gate閉鎖は維持します。

## 分離した処理

| 処理 | 所在 | 範囲 |
|---|---|---|
| 認証 | `internal/auth` | 起動時の raw Ed25519 公開鍵集合を複製して固定。署名検証は固定版 Go SSH ライブラリへ委譲 |
| 宛先と要求 | `internal/forward` | direct-tcpip、literal 127.0.0.1、wire uint32 の22を完全一致で判定。名前解決や正規化なし |
| 期限と失効 | `internal/lease` | 起動同一性、状態遷移、最大期限、リース、更新連番、接続中を含む収容数 |
| 接続所有 | `Session` | 認証中と転送先の接続、worker を保持。取消と close、失敗参照の保持、期限付き終了確認 |

`Config` に任意宛先、dial、fixture ポート、ゲート解除の項目はありません。
公開 `New` は固定の `tcp4 127.0.0.1:22` を選び、生成だけでは接続しません。
高位 fixture は非公開の構築関数を使う `_test.go` の中だけにあります。
通常のモジュールへ試験の listener や設定選択は入りません。

## 呼び出し条件

状態は prepared で始まり、同じ `Identity` を使う `Ready`、`Activate` の順でのみ active になります。
この遷移はローカルの所有者向けであり、保護された ready 証明や IPC 認証ではありません。
現在の製品開始拒否を迂回するために呼んではいけません。

起動 ID、世代、方針ダイジェストを起動単位に固定します。
`Identity.LaunchID` が SSH ユーザー識別子になり、OS アカウントへのログインは行いません。
識別子の生成と永続的な再利用防止、ホスト秘密鍵と呼び出し元の保護は未実装です。
鍵集合を入力側で書き換えても、作成済み集合は変わりません。
証明書と security-key 形式の認証は提供しません。

鍵照会を認証成功と数えず、ライブラリの署名検証後とハンドシェイク完了後にも有効性を検査します。
session チャネル、global request、TCP チャネル内の制御要求は既定拒否です。
内側 SSH のヘルパーや shell の認可は実装していません。

`Serve(conn)` の `ErrNotAccepted` は所有を移さない拒否です。
その場合は呼び出し元が接続を閉じます。
受理後は Session が所有するため、呼び出し元は同じ接続を並行して終了しません。
SSH ライブラリ自身の close も同じ所有ハンドルへ集約し、成功した close は冪等に扱います。
`MaxTransports` は認証前・認証中・認証済 idle を含む受理済 SSH 接続の合計上限で、0は既定8、明示値は1〜64です。
`MaxHandshakes` はその内の同時認証処理数であり、二つの値を足して総接続数とはしません。
終了途中や close 失敗の transport は所有参照を回収するまで枠を保持し、転送先TCPは別の MaxChannels で数えます。

## 失効と終了確認

最大期限は prepare 時点で固定し、ready、activate、renew で延長しません。
prepared / ready には独立した固定5秒の準備期限を設け、ready や renew で延長しません。
期限前に active へ遷移した後は、prepare 時の最大期限と短期リースを適用します。
時計API境界の nil / panic / zero は拒否または clock-unavailable による失効にしますが、非returnの時計や実OS障害の保証ではありません。
元 Python モデルの初期連番 -1 と同じく、初回0を許可し、以降は厳密に増加する uint64 を要求します。
別世代、重複、逆順、失効後の更新を拒否します。
Go の経過時間比較と壁時計の前進を確認し、後退でも失効します。
実 Windows の時計変更やスリープを試験したものではありません。

接続処理中から枠を予約し、失効後に返った接続を公開しません。
close 失敗時は新規認可を失効させ、対象参照を保持します。
`Close(ctx)` が期限内に終了を確認できなければ失敗し、REVOKING のまま再試行できます。
同じ停止中の対象に無制限の close 試行や終了待ちを増やしません。
転送先はアダプターへ渡す前の生 TCP 参照を別に保持し、停止要求ごとにアダプターの close と独立した worker から閉じます。
後者は前者の lock を待ちません。
実 TCP 本人の既閉だけを冪等化し、任意アダプターのエラーを成功扱いしません。
停止中の各経路は所有対象あたり1試行だけを保持します。
`Snapshot.CutoffPending` と `CutoffFailures` は独立経路の未完了と失敗を示し、`Owned` はアダプターの終了も確認するまで減りません。
`Serve` へ渡された実 `*net.TCPConn` 本人だけは独立停止参照を捕捉します。
任意の `net.Conn` wrapperの内部は展開せず、その入力の独立遮断は保証しません。
物理 TCP が閉じても、戻らないアダプターと worker が残れば `Close(ctx)` は失敗し、REVOKING と参照を保持します。
転送先の容量はI/O終了時ではなく、所有対象の両停止経路とSSHチャネルの終了を確認した時に返却します。
相手のチャネル終了応答がない場合も、終了待ちworkerを同じ容量で保持し、追加チャネルで上限を回避しません。
`Snapshot.Active` は終了未確認対象が保持する容量を含み、`Forwarding` は実行中のI/O処理数を示します。
正常EOFでは書込側だけを半閉鎖し、反対方向の応答を待ちます。エラー時は全停止経路を要求します。
両方向のcopyは読み取ったデータを書き込む前に認可を再確認します。
この検査は失効確定後に新しく読み取ったデータの転送を拒否する補助です。
検査を通過したWriteは、実呼出しが失効後へずれた場合も取消しを保証せず、物理切断も保証しません。
停止経路を両方故障させたfixtureでは、補助検査で新規転送を拒否できてもraw TCPのcanaryが到達する未達を保持します。

子プロセスを使う寿命試験も `_test.go` だけにあります。
親が保持し続ける高位IPv4 loopback待受へ子が接続し、子のSessionを実SSHで検査します。
秘密鍵は親子それぞれのメモリだけに置き、継承pipeでは公開鍵と固定した試験情報のみを渡します。
独立3秒TTL、pipe EOF、親が保持する自分の子handleのKill/Waitを検査しますが、保護されたsupervisor、製品保護用途のWindows Job、任意の内側プロセス群の寿命、OS故障への保証ではありません。
予算期限の注入も試験用ハンドラの制御モデルで、製品の監視主体を実装したものではありません。
Windowsでは、新しく作る無名JobのKillOnCloseを、継承handle経由で自己子だけへ割り当てる試験も分離しています。
子が自分自身を割り当て、継承したJob handleの閉鎖を確認してから親へ準備完了を返します。
親が保持する最終handleのCloseと子Wait/両端終端を検査し、TTLや後片付けのProcess.Killによる終了をこの成功には数えません。
既存Job、別process、ACLは変更せず、割当拒否時にbreakaway等へ進みません。保護主体を実装したという意味ではありません。

Windowsの実TCP背圧ケースは、固定64KiB/有限観測では前提を作れずfixture FAILを保持しています。
合格条件を変更せず、このケースを明示未実施にした他全case回帰を別実行します。
その結果を全suite成功やWindows背圧の合格とは扱いません。

高位 loopback fixture では、転送先アダプターの close がエラーを返す場合と、戻らない場合の双方で、独立経路による相手側の読み取り終了と raw TCP の新規送受信拒否を1秒以内に検査します。
SSH transport / channel の切断、新規認証と更新の拒否、アダプターの未終了、回復後の所有参照0を別々に検査します。
期限切れの試験は echo 直後からの待ち時間も1秒に含めます。
失効後に完了した dial も公開せず、同じ独立経路で終了します。

アダプターと独立経路の双方を試験故障にすると、1秒後にも転送先 TCP の両端で新しい識別子が届きます。
この負例の物理切断は未達であり、回帰テストの成功と区別します。
これは実 OS の socket close 故障を再現した試験ではありません。
実 OS close の失敗、プロセス停止中の期限執行、スケジューラー停止は未検証であり、同一プロセスの別 worker だけでは保証できません。

別試験では今回生成した子プロセスだけを通常権限で終了し、その終了待ちと所有 TCP の相手側終了を1秒以内に観測します。
これは保護 supervisor、製品保護用途のJob Object、終了権限の保護、再起動後の失効を実装したことにはなりません。
TCP 終了後も fixture の独立作業が継続することを観測し、専用の取消で片付けます。
汎用 sshd の別プロセスや既実行の副作用を取消す保証はありません。

## 試験と未検証

Go 1.27.3、x/crypto v0.58.0、x/sys v0.49.0 を固定しています。
事前に公式依存を取得した専用環境では、次で回帰を実行できます。

```sh
GOTOOLCHAIN=local GOPROXY=off GOWORK=off go test -race ./...
```

テストだけが高位 loopback listener、メモリ内鍵、無害な転送先を生成します。
実 dial は所有中の高位 listener に限り、22番、DNS、LAN、外部先を使いません。
固定 wire 方針の正例を試験用アダプターで高位 fixture に転送しても、本番22番への実接続正例には数えません。
本番固定アダプターの正例は未検証です。

保護 supervisor、ACL、製品保護用途のJob Object、認証付き IPC、実 Upterm、内側の操作認可、OS 停止中の失効は未実装または未検証です。
外側通信の終了は、汎用 sshd が別に起動した処理や既実行の変更の取消ではありません。
両モードの外側権限は同じであり、full でも管理者昇格を認可しません。
実接続の再有効化には既存の `docs/SECURITY_BACKEND_CONTRACT.md` の全条件と別途の承認が必要です。

## resident workerのsource準備

[`internal/workerowner`](internal/workerowner/README.md)は同じ取得・Run・Closeと元source寿命を保持する内部依存です。
通常caller・認証・実producerは追加せず、unknown後のowner保持とnative回収・退出guardは具体的callerの責任として残ります。

既存channel/Host/Approved/Clientの[正規transport owner前提](../docs/mac-canonical-transport-owner.md)は、共通metadataと部分取得・cleanup観測を提供します。
通常resident callerや認証を追加せず、実native回収・配置信頼は未実証です。

[fixed intentの引継ぎ](../docs/mac-fixed-intent-handoff.md)は既存Session／wire内で正規namespaceと有限runtime期限を保ちます。
元timer・取消・単回ownerを維持し、通常resident／認証や実producerは未接続です。
