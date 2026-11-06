# Resident outer ownerのsource境界

この候補は未統合のdraft #200（`c6c5ae5342fcde6b1bf77eff744ad48ddce5b8da`）に積んだsourceです。
#200のWindows session-create timeoutは未解決であり、main適用済み・安全な統合完了とは扱いません。

## 具体的な所有と終了判定

既存の`StandbyOwner`は、同じduplex `Supervisor`をouter monitorへmoveし、その同じ`CloseHandle`を`ExitGate`に保持します。
別producer、再接続、代替owner、任意Command/path/argvの通常factoryを追加しません。
実行入口は閉じたままで、通常Mac起動とIPCは未接続です。

明示退出はtransportのCloseを要求し、以後のsubmitを封鎖してから判定します。
`Confirmed`はwire Close、実EOF、exact Child Wait/exit0、IO/supervisor workerの実joinを前段に要求します。
未spawn、intent前の限定startup rejectionは、それぞれ別結果です。
さらに、このouter monitor自体の実joinが成功するまでは退出を許可しません。
wire closed、物理reap、結果の通知、handleを取り出したことだけを全回収にしません。
reader/writerは#200の単一IO worker内のhandlesであり、別threadのjoinを実施したとは主張しません。

`request_exit`/`finish_requested`は未完了・unknown・monitor panic・自己join・join競合時にfalseを返します。
join競合でmonitor callbackを待たせず、別callerが実join中のhandleを完了扱いしません。
`StandbyOwner::join`とDropは実outer joinと安全な結果の両方を待ち、unknownなら親を保持します。
`guarded`はsetup成功・error・unwindのいずれも同じownerをjoinしてから返します。
wakeは再確認を促す通知だけです。通知や`outcome()`単独には終了の権限がありません。

## 今回の境界検査と残件

凍結privateから移した本体に、monitor callbackが別callerのjoin lockへ再入する負例を先に追加しました。
同じmutexを待つと自己monitor判定へ届かず、相互待ちを作る境界をredにし、競合時の拒否でgreenを確認します。
凍結GUIの既存wakeはmain-threadへ転送するため、読取した実callerでは同期再入しません。
この所見を凍結候補の実GUI障害や修正適用済みの根拠にせず、凍結sourceも変更していません。

自己所有の無害なpipe fixtureで、実EOF/Child/IO workerからouter joinまでを検査します。
negative fixtureはtest-onlyの内側guardで物理回収後のunknownを観察し、outer終了許可がfalseのままであることを確認します。
production unknown Dropが資源を放棄して帰るという試験ではありません。

後続はtrusted identity/protected policyを持つsealed launcher、Status/intent配線、native QuitとGUI event経路の結合です。
通常のGUI/producer/ON/OFF/退出、鍵・認証、配布物は未実証です。
private sourceを組成し直す場合は新しいsource/build pinと、Close/EOF/Wait/全join/unknown・GUIの再検証が必要です。
#199のEOF guardについても凍結caller非到達の静的所見を維持し、private実行候補への適用済みとは扱いません。
