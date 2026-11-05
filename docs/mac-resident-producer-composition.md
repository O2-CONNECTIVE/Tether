# Standby と owned pipe の source 組成

#210 と先行 #200〜#207 に依存する source 準備です。#200 の Windows 阻害、未統合 stack の CI 未実行、通常 Mac の未接続は解除しません。

## 具体的な所有と入出力

既存の protected policy / opaque receiver / worker を、residentmac の固定 factory・Standby・wire・observation と、residentpipe の codec・mapper・owned pipe・内部 run loop・startup outcome へ接続します。
新しい generic port や認証済み marker を作らず、既存の同じ factory / receiver / source / worker / common owner / control transport を保持します。

Standby は accepted lease を逐次消費し、receiver 自身の単回 claim を通して source を1回だけ開きます。
元 source の終了時には同じ common owner の WaitSource を待ち、元 lease の Finish と control の Run / Close を分けます。
host の停止は source cleanup 中でも同じ Owner.Stop と元の予算へ合流し、monitor と cycle の終了が確定するまで OFF 完了や再 ON を許しません。
unknown は元 cycle と owner を保持し、遅い物理終了でも unknown を成功へ変えません。

codec は有限 frame、LF、strict UTF-8 / JSON、重複 field、canonical integer、固定 intent を検査します。
mapper は元 revision、monotonic freshness、request high-water、receipt と transition を分け、観測だけで実行許可を作りません。unknown は sticky で、過去の認証履歴を現在の認証と表示しません。
Darwin / cgo の owned pipe は決められた stdin / stdout の匿名 FIFO だけを取得し、nonblocking / CLOEXEC の元 duplicate を所有します。FIFO metadata は相手認証ではありません。
内部 run loop は元 Standby.Close の結果を terminal closed frame に反映し、unknown なら元 owner を保持して回収を再試行しません。有限 write の失敗を成功 frame と扱いません。

## 起動と信頼の境界

run loop は package 内部の `run` だけで、公開する `Run(path)` や新しい command entrypoint はありません。
private の汎用 `Load` / `LoadFixed` を移さず、固定 scope の `LoadStandbyResident` だけへ接続します。
通常 CLI / IPC / GUI の route、認証・権限・persistent access を有効化しません。任意 Command / path / argv / fd / digest / CA / trust root override も加えません。
内部の policy path は将来の trusted native composition が選ぶ必要があり、今回その信頼を構成していません。

private の `cmd/resident-pipe` は `--protected-policy` を受けて起動するため移しません。
sealed entrypoint、publisher identity、source と artifact の同一性、保護配置、verify-to-exec、trusted native policy 選定は未確立です。
未署名 archive や呼出側の path / hash だけでこの境界を代用しません。

## source 試験と未実証

Standby の lifecycle / common owner の終了回帰は fake control / worker だけです。具体的 consume と共有する cycle.waitSource を通りますが、認証済み lease や全 authenticated consume を通った証拠ではありません。
codec / mapper / startup outcome は pure unit、owned IO は自己所有の匿名 pipe だけを使います。
stdin / stdout の capture は test binary の専用 helper child でだけ確認し、元 Child Wait と IO copier を同じ Output 呼出しで完了させます。helper は metadata / capture / write / close だけで、producer・policy・接続・work を起動しません。

idle の component 組成試験では、新 checkout 内の自己所有の合成 key / policy だけを既存 native 保護 reader で読みます。
ON・接続・認証・work を行わず、実 Standby の初期状態、OFF intent の拒否、同じ serve loop の実 input EOF、同じ idle Standby.Close、その後の terminal frame と owned output EOF を確認します。
`run` / CLI / producer child は実行せず、fixture file と元 descriptor を最後に削除・閉鎖します。運用 key / policy、ACL / OS trust は変更しません。

この idle EOF と別の test helper の Child Wait を、実 producer の一つの回収証拠へ合算しません。
実際の authenticated work、native command、authoritative closed と exact Child Wait / IO / supervisor / outer monitor 全 join、GUI の終了許可、配布実行の信頼は未実証です。
凍結 private / a3 / a4 は編集せず、新しい公開 source の結果を旧 artifact に適用済みと扱いません。
