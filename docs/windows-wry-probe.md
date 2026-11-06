# Wry completion boundary の限定診断

このsource overlayは診断専用・DO NOT MERGE。製品修正でもtimeout解消でもない。
固定比較元はPR240 `4ba768e910af595f26176eb69aaf2e21d663b726` / tree `a4bb126e61fad53330d78207b54d2a57e31e3795`。
実行物はこのoverlayの新buildで、破棄済みの失敗EXEと同一とは扱わない。

## 実際のred signalと限界

元のstartup release jobは、build/package後に次の入口を一度実行して失敗した。

```powershell
./ui/tauri/native-smoke/supervise.ps1 -ToolsDirectory $tools -OutputDirectory $evidence -ApplicationPath $exe -StartupDiagnostic -StartupManifest $manifest -StartupSource $source
```

[run42277585224/job128097840476/attempt1](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/42277585224/job/128097840476) の保存JSONを再照合した。
Node総観測15013ms、request15008ms、`NATIVE_DRIVER_TIMEOUT`、文書0、driver `forward-start` から `request-dropped`、headers/errorなし。
driver記録9/9・drop0・sealed、Host記録はsetup35321us / build-start35341us。
相関app1 / WebView2、normalClose=false、cleanup/disposal失敗0。
Host完備判定は記録窓の完備でありbuild成功ではない。
15000ms要求を実行するこのhosted loopは実際にredになったが、遅く非決定的であり、速いlocal Windows再現はまだない。
下記fixtureはprobe自身の境界検証で、timeoutの再現と呼ばない。

## 一回の観測が答える質問

Host buildが返らない同じ窓で、固定Wryのenvironment/controller関数はどこまで戻ったか。
順位付き仮説と一次資料は[調査](windows-startup-research.md)に分けた。
controller内停止、controller後の初期化停止、environment内停止を区別する。
stack samplerはtool/thread権限/所有/確実なresumeを未実証なので実行しない。
代替はtarget自身の固定memory markerで、thread停止、dump、memory読取り、権限追加を要しない。

## 最小overlay

`prepare_wry_probe.py` はlock固定の公式Wry0.57.1 archive SHA256
`759c50ddad0ef9913bb5496cb9d509a50433a3b78cb0238d61105526589cf0e9`だけを別tempへ展開する。
registry cacheは書き換えない。元mod.rs SHA256 `9ef674af12e52bc3519b0e300752c1042798b7f336e0e0dafca6a8944f348888`、
patch後 `8d4a637727ce94dab6c6fb8e676dde8f2e7a4d0c10681fef0b82dbda42654700`。
二つの関数の各4点だけに、単一u8引数の診断symbol呼出を追加する。

| marker | 実際の受領境界 | 証明しないもの |
| --- | --- | --- |
| environment/controller-enter | Wry関数入場直後 | WebView2 API入場そのもの、COM成功 |
| api-ok | 元の即時API/設定処理が`?`を通過して戻った後 | completion成功、全setup完了 |
| result-sent | 元のtx.send(result)がOkになった後のhook | 内側resultの成功、callback全体exit、受信前の時刻 |
| wait-returned | 元wait_with_pump(rx)?の外側Ok受領後 | 内側resultの成功、WebView全体build成功 |

callbackはapi-okより前にも、sendがreceiverを起こした後のwait-returned/controller-enterより後にも記録され得る。
欠測markerをcallback不着と断定しない。複数呼出・重複・dropは不完備であり、別invocationをjoinしない。
callbackのonTraceThreadはTrace開始threadとの等否だけで、生threadIDを出さない。
原tx.send・Result・`?`の各評価は一回、同じ値/エラーを返す。新message送信、wait、retry、timeout、wake-up修正はない。

Cargoの明示path patchだけに使用する。元lockのWry source/checksum二行だけを落とし、他の依存と版を変更しない。
LFまたはその全行CRLFという正確なworking representationだけを受理し、元lock SHAを検証する。
標準featureでこのWry patchを使用しない。startup-diagnostics featureのHostだけがsymbolを提供する。

Host hookは最大8eventの予約済みVecとtry_lock/atomics/Weak参照を用いる。threadのsuspend、I/O、wait、joinは行わない。
この同期処理にも時間とsynchronizationの擾乱はあり、timing-freeではない。
workerが別file `wry-completion-probe.json` を最大2048bytesで先に書き、元Host fileを最後に閉じる。
元fileのclose判定を借り、欠測・未sealed・drop・未知field・非canonical/duplicate JSON・slot重複を厳格拒否する。
sealElapsedUsはprobe自身のInstant起点で、Node/driver時計や旧Host時計から差し引かない。
原Host六段階記録、supervisor、request/body/AbortSignal、15000ms、profile、所有/normal-close/cleanup判定は変更しない。
新fileのwriteも元回収budgetに対する擾乱であり、別途成功へ読み替えない。

## 実行境界

準備・fixture・独立最終head reviewが通ってから、一つのfresh standard public windows-2022 job、attempt1、native最大1。
元release/custom-protocol/startup-diagnosticsとdriver固定を維持するが、instrumented Wryなので原binary/原挙動と同一とは呼ばない。
preflight/identity/cleanup不明なら停止。retry、matrix拡張、成功までrerun、EXE/ZIP/cache転送、raw stack/path/payload出力を行わない。
成功はno-repro。既存#240失敗を置換せず、同じ失敗窓の新markerが得られて初めて次の一変数を選ぶ。
新しいproduction policyや依存patchの採用・mergeは、このprobeの成功だけでは正当化されない。
