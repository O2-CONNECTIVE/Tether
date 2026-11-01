# Tether Host 常駐待機の最小入口

このsource候補は**常駐版の完成ではありません**。目標はmacOSの本人ログイン後・現在ユーザーでの待機です。ログイン前、root daemon、無人の恒久実行許可は対象外です。

## 今回の実装

`backend/cmd/tether-host-standby` は明示引数 `--standby` のときだけ待機します。実行許可、鍵、保護設定、接続先、子process、MCP backendを保持しません。既存の `rescue-mac-agent` は呼び出しません。

- Darwin以外、root、不正UID/GID、実効UID/GIDとの不一致を拒否します。
- 引数なし、実行flag、設定path、追加引数を拒否します。
- 標準出力へ固定行 `state=standby execution_grant=absent connection=absent` を出します。これは当該待機processのローカル状態だけです。ホスト全体・gateway・別workerの状態照会ではありません。
- 割込み、TERM、HUPを待機contextの取消へ写し、`state=stopped execution_grant=absent connection=absent` を出して終了します。状態の書込み失敗は非成功です。取消済みでの起動は待機状態を出しません。
- `stopped` は待機自身の終了です。以前の作業の失効・回収完了・副作用の巻戻しを証明しません。既存viewerのstatus pipe protocolとも異なり、viewerへ接続しません。

起動主体のUID照合だけではAquaログインsessionを証明できません。ログイン後の自動起動、session種別確認、署名・配置検証、重複起動防止は未実装です。今回LaunchAgent/plist、installer、永続設定、status用IPC、別processを止めるCLIは追加していません。

出力は同期書込みです。出力先が無応答の場合の期限付き終了は保証しません。実配備でのログsink・停止契約は別途検証が必要です。待機programを試験中に実起動・登録する手順は含めません。

## 実行許可との分離

待機を起動しても、実行許可の発行、更新、接続、command実行はできません。将来のworker連携では、毎回の本人同意・短期期限・新しい起動世代を持つ独立した作業セッションが必要です。既存 `maclaunch.Pending.ApproveLocal`、単回の `agentexec.Session` / `agentlink.Host` をloopやrebindで再利用しません。

切断後に旧grantを再使用しません。回収未確認のworkerがある場合に新しいworkerを開始してよいという契約も追加していません。discovery MCP sessionは永久にunboundで、実行にはfresh initializeが必要という既存仕様を維持します。常時MCP接続、再接続、grant workerの生成と監督、GUI承認は未接続です。

## 検証する範囲

| 受入 | source / synthetic試験で確認する内容 | 今回証明しない内容 |
| --- | --- | --- |
| R0 | 明示opt-inと現在ユーザー属性の拒否判定 | 実Aquaログインsession、配備保護 |
| R1 | runner開始→許可なし・接続なし表示→context取消→待機終了 | OS signal配送、実daemon寿命 |
| R2 | 引数・identity・出力失敗・取消済み起動の拒否 | 稼働host全体の状態や旧work回収 |
| R3 | Go build inventoryへ当該1 sourceのみ明示追加 | login item登録、常時接続、署名済み配布 |

試験は有限contextとメモリ出力を用いて実CLI runnerを直接呼びます。新しい常駐process、実terminal、鍵、認証、network、launchctlを使いません。Go通常/race試験とCI helper試験はsourceの証拠であり、常駐版が導入済み・本番利用可能とは扱いません。
