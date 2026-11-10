# GUIの閉じたnative起動入口

通常の `run()` は `ResidentState::startup()` を一度呼び、同じstateを既存の
IPCとlifetime guardへ渡す。Macのnative libraryを明示linkしたbuildでは、
元native workerがゼロ入力の `rh_resident_preparation_startup` を呼ぶ。
nativeをlinkしない既存buildと他OSは従来どおりunboundである。

## 未設定とUnknownを混同しない

従来の `create=0` / `Preparation::Refused` は、未設定だけでなく既存ownerや
tombstoneの拒否も含むため、Quit許可へ変更しない。

新しいstartupは元slotのlock下で、recordなし・発行済handleなしを確認する。
`ResidentLaunchPreparation.prepare()` がProcess/IO取得前に返す
`unsupportedOS` / `publisherUnconfigured` だけを、起動前未構成として区別する。
この場合は元slotを永久にsealし、後から通常createやfixture ownerを作れなくする。
null output、既存owner、過去の発行、再startup、その他errorは曖昧な拒否のままである。
任意release/path/argv/envを受ける引数や、別途のavailable判定はない。

起動前未構成も `NativeSession` 全体で保持する。元workerのjoinと元outer monitorの
実joinが成立してからだけ `NotSpawned` へ分類する。defaultへの置換やnative stateの
再bindは行わない。元owner取得後のmonitor生成失敗は、単なるErrとしてGUIへ戻して
終了させず、同じnative slotの不確実性を残して退出を保留する。

## まだ成立していない製品接続

`ApprovedRelease.production=nil` は維持する。実Developer IDのTeamと同じproducerの
CodeDirectoryHashを捏造せず、署名や運用enrollmentを追加しない。
通常Cargoの自動Swift link、GUI/Swift/producerの同一署名releaseの組立、
実Macでの通常GUI起動・実dot接続はこのsource変更だけでは完成しない。
[固定GUI/Swift build](mac-gui-candidate-build.md)は同じstartup exportを含む通常libraryを
実compile/linkし、起動せずに固定配置を検査する。正規releaseや接続済み製品の代わりにしない。

新規native回帰は同じ起動入口・元slot・元worker/outer joinを使う。
Python helperの成功だけをSwift/Rust compile、native回帰実行や実製品接続の成功にしない。

monitor生成失敗fixtureは、実spawn位置で元closureをdropする一度限りの注入である。
元supervisorが閉鎖・joinされた後、実際の非return分岐への到達をbounded channelで確認する。
元childのWait、IO close、Unknown保持、replacement拒否を別々に要求し、timeoutは失敗とする。
自己所有のfixture processに残した呼出しthreadをprocess終了で終えることは、productのQuit許可や
そのthreadのjoin証拠ではない。実OSの資源枯渇を起こした試験とも扱わない。
