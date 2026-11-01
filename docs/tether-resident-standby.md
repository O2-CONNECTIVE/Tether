# Tether Host 常駐待機の最小入口

このsource候補は**常駐版の完成ではありません**。目標はmacOSの本人ログイン後・現在ユーザーでの待機です。ログイン前、root daemon、無人の恒久実行許可は対象外です。

## 今回の実装

`backend/cmd/tether-host-standby` は明示引数 `--standby` のときだけ待機します。実行許可、鍵、保護設定、接続先、子process、MCP backendを保持しません。既存の `rescue-mac-agent` は呼び出しません。

- Darwin以外、root、不正UID/GID、実効UID/GIDとの不一致を拒否します。
- 引数なし、実行flag、設定path、追加引数を拒否します。
- 既定の `--standby` は標準出力を触らず、無出力で待機します。追加の `--status-stdout` を明示した場合だけ、標準出力へ固定行 `state=standby execution_grant=absent connection=absent` を出します。これは当該待機processのローカル状態だけです。ホスト全体・gateway・別workerの状態照会ではありません。
- 割込み、TERM、HUPを待機contextの取消へ写し、`state=stopped execution_grant=absent connection=absent` を出して終了します（状態出力を選んだ場合のみ）。状態の書込み失敗はstickyな非成功です。以後のframe送信を止め、待機の取消は通常どおり進め、終了値を非0にします。取消済みでの起動はpipeを取得せず、取得直後の取消も状態を出さずに所有dupを閉じます。
- `stopped` は待機自身の終了です。以前の作業の失効・回収完了・副作用の巻戻しを証明しません。既存viewerのstatus pipe protocolとも異なり、viewerへ接続しません。

起動主体のUID照合だけではAquaログインsessionを証明できません。ログイン後の自動起動、session種別確認、署名・配置検証、重複起動防止は未実装です。有効なLaunchAgent、installer、永続設定、status用IPC、別processを止めるCLIは追加していません。後述の未解決placeholder入りtemplateは登録・起動可能な設定の提供ではありません。

状態出力は、既に `O_NONBLOCK` の付いた専用write-only FIFOだけを受理します。fd 1をCLOEXEC付きで複製し、その同じ所有dupに対して種類とflagsを検査します。通常file、terminal、socket、blocking pipeを拒否し、非blocking flagの変更・復元は行いません。dupはopen-file-descriptionを共有するため、呼出元がpipeを専有しflagsを変更しないことが前提です。これは同一ユーザーによる干渉に対する保護ではありません。

各短い固定行は非blocking writeを1回だけ試みます。EAGAIN、EINTR、reader喪失、短いwriteも失敗扱いで、retryや出力worker goroutineは作りません。成功したwriteもreaderによる受領確認ではありません。閉じるのは所有dupだけでfd 1ではなく、取得後の全経路で1回だけ試行します。close失敗も非成功として記録をrunner終了まで保持し、再利用されたfdを閉じないため自動retryしません。OS故障・close syscall自体の無応答まで期限保証するものではありません。

現在の検証はsyscall seamのfakeだけです。実pipeの背圧・signal配送・OS上の停止は未実証で、待機programを実起動・登録する手順は含めません。

## 実行許可との分離

待機を起動しても、実行許可の発行、更新、接続、command実行はできません。将来のworker連携では、毎回の本人同意・短期期限・新しい起動世代を持つ独立した作業セッションが必要です。既存 `maclaunch.Pending.ApproveLocal`、単回の `agentexec.Session` / `agentlink.Host` をloopやrebindで再利用しません。

切断後に旧grantを再使用しません。回収未確認のworkerがある場合に新しいworkerを開始してよいという契約も追加していません。discovery MCP sessionは永久にunboundで、実行にはfresh initializeが必要という既存仕様を維持します。常時MCP接続、再接続、grant workerの生成と監督、GUI承認は未接続です。

## 検証する範囲

| 受入 | source / synthetic試験で確認する内容 | 今回証明しない内容 |
| --- | --- | --- |
| R0 | 明示opt-inと現在ユーザー属性の拒否判定 | 実Aquaログインsession、配備保護 |
| R1 | runner開始→許可なし・接続なし表示→context取消→待機終了 | OS signal配送、実daemon寿命 |
| R2 | 引数・identity・取得/出力/close失敗・取消済み起動、専用pipeの拒否判定 | 稼働host全体の状態や旧work回収 |
| R3 | Go build inventoryへ共通mainと対象OSのstatus sourceだけを明示追加 | login item登録、常時接続、署名済み配布 |

試験は有限contextとメモリ出力を用いて実CLI runnerを直接呼びます。新しい常駐process、実terminal、鍵、認証、network、launchctlを使いません。Go通常/race試験とCI helper試験はsourceの証拠であり、常駐版が導入済み・本番利用可能とは扱いません。


## 待機専用のblocked評価archive

`scripts/package_mac_standby.py` は既存agentとは別の専用producerです。明示されたbinary、期待SHA256、Darwin architecture、source manifest SHA256、固定notice root、未存在の出力先を受け取り、実行せずにローカルarchive候補を組み立てます。既存 `package_mac_agent.py` の読取り・hash・Mach-O検証helperを再利用しますが、既存agent archiveの契約・member・説明は変更しません。

固定memberは `bin/tether-host-standby`、`templates/local.tether.host.standby.evaluation.plist.in`、project LICENSE、Goとsysの固定4 notice、RECIPIENT.txt、MANIFEST.jsonだけです。入力directoryを再帰copyせず、同じ検証済みbinary bytesを同梱し、既存出力を上書きしません。任意bundle・設定・鍵・endpoint・viewerは同梱しません。Go module一覧は当該binaryのlinked dependency closureを証明しません。

`.plist.in` は固定placeholder `@TETHER_STANDBY_EXECUTABLE@` を残した非有効templateで、producerは置換しません。固定label、2要素のargv（placeholderと `--standby`）、Aqua、RunAtLoad=true、KeepAlive=falseのみを持ちます。完全に固定したbytesのhashを検査し、追加key・任意argv・既に置換されたpathを拒否します。HOME・実配置path・shell・環境・stdout fileの入力はありません。templateをLaunchAgentsへ移したりloadしたりする手順は提供しません。

AquaとRunAtLoadは将来の明示opt-in時の制約案であり、template生成がログイン登録や自動起動を行うわけではありません。runtimeのAqua証明、manual重複起動防止、終了・更新・撤去、固定current-user配置とその保護は別工程です。既定のsilent待機だけを想定し、専用FIFOが必要な `--status-stdout` やSwift viewerを配線しません。

manifestは常にdistribution blocked、unsigned-unverifiedとし、binary/source対応、製品binary同一性、依存閉包、notice完全性、秘密/build path監査、notarizationを未確認のまま保持します。SHA一致とMach-O header検証は、そのbinaryが本当にこの待機commandである証明ではありません。minimum OS/SDKは宣言であり互換性保証ではありません。署名・一般配布・実binary archive生成前の検査を、このsource/fake試験から達成扱いにしません。

検証入口は `python3 -m unittest scripts.tests.test_mac_standby_package scripts.tests.test_mac_agent_package`（通常と `python3 -OO`）です。合成Mach-Oと公開test用notice文字列を一時directoryだけで使い、binaryは実行しません。正確なmember/permission/hash、非有効template、拒否、非上書き、import時の無操作を確認します。fixture noticeを実配布のlicense証拠には使いません。実archive・実登録・実常駐動作は未実施です。
