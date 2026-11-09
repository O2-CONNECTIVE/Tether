# 固定 enrollment の producer entry

`backend/cmd/resident-pipe` は、既存の package 内部に閉じていた JSONL producer 組成を
専用 executable の source へ接続します。旧 private command の `--protected-policy` は移しません。

- argv はゼロ個だけです。help、空文字、path、fd、dialect、credential の指定も exit 1 で拒否します。
- Darwin/cgo の entry は、引数なしの `residentpipe.RunEnrolled()` と既存 `ExitCode` だけを使います。
- 固定 enrollment は既存 native OS-account lookup / protected loader を使用します。
  HOME や caller policy path に切り替える override、ON の自動要求、接続先の追加はありません。
- 対応しない build は exit 1 です。exit 2 は既存の正に出力済み startupRejected の結果だけです。
  argv の拒否、一般 error、frame write の失敗を policy rejection と表示しません。
- 同じ `runBootstrap` / `runOwnedInput` が元 Standby を保持します。
  Close が unknown の場合は無期限保持し、成功退出や replacement を作りません。

## 検証と未実証

command unit は argv の純粋判定と不正 argv の早期拒否だけを検査します。
unsupported build のゼロ引数拒否は別 test で確認します。
Darwin/cgo のゼロ引数を test から呼ばず、実 account / enrollment / policy / key は読みません。
既存 core fixture の結果を新 executable の実起動成功へ読み替えません。

Go inventory は共通 main と各 OS の entry を限定登録し、未知 source や欠落を引き続き拒否します。
source build recipe は引き続き library archive 専用で、producer executable や配布物を生成しません。

通常 `tether-host` / Tauri startup は未接続のままです。
この新 source は実 publisher、署名済み image、nested bundle 配置、OS launch enforcement、
同一 native Process の起動・IO handoff、利用者の実 enrollment を成立させません。
Developer ID / Team / CodeDirectoryHash / key / policy を架空値で埋めません。
次段は同じ Swift owner から固定 nested producer を verified launch する接続であり、
その動作と actual wire / EOF / Wait / IO / supervisor / outer joins は別途検証が必要です。
