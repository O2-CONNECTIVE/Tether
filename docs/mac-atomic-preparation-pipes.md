# Mac の匿名 pipe を原子的に取得する

[同じ Foundation Process の IO owner](mac-foundation-active-io.md)は、macOS 27 以上で `Darwin.pipe2(&pair, O_CLOEXEC)` を直接呼びます。2本の各 pipe は、両端点の割当と close-on-exec 設定を同じ syscall で行います。`pipe()` と後続 `fcntl(F_SETFD)` の間に別 thread の spawn / exec が割り込む隙間を残しません。

[Apple Swift System の公開実装](https://github.com/apple/swift-system/blob/main/Sources/CSystem/shims.c)は、macOS 27 SDK の `unistd.h` と availability guard の下で `pipe2(int[2], int)` を呼びます。[同 API の説明](https://github.com/apple/swift-system/blob/main/Sources/System/FileOperations.swift)でも、options を指定する pipe は macOS 27 以上です。この source は互換 shim、動的 symbol 解決、非原子的 fallback を追加しません。実 SDK での Swift 宣言・link の照合は native checker の成功が必要です。

## 機能の下限と所有

- actual `#available(macOS 27.0, *)` と native OS version を割当前に確認します。古い OS では IO 準備を拒否します。typed launch requirement の API 自体は14.4以上ですが、それを匿名 IO の対応条件と混同しません。
- 各 syscall の成功直後に両端点を元 owner へ記録し、その後の検査・部分失敗でも既存の close-once 回収へ渡します。0 / 1 / 2 と重なる端点も回収対象として保持したまま拒否します。close receipt が不明なら再 close / 置換を認めません。
- `O_NONBLOCK` は parent writer / reader にだけ設定します。child stdin / stdout を非blocking化しません。parent writer の `F_SETNOSIGPIPE` 設定・確認も維持します。
- 同じ Process / FileHandle、C handle / TLS、exact Wait、IO / supervisor / outer join の契約は変更しません。原子的割当は EOF、回収完了、Quit の許可ではありません。
- app / viewer / agent 全体の minimum OS や Intel 対応をこの変更で変更しません。fixture の lower deployment target は availability 検査用で、旧 OS での IO 実行保証ではありません。

close-on-exec は**成功した exec 後の余分な端点継承を防ぐ契約**です。bare fork の child が exec 前に一時的に端点を持つこと、同じ process 内の悪意あるコード、sandbox 隔離までは防ぎません。必要な stdin / stdout への明示移管は既存の同じ Process が行います。

## 固定合成 fixture

```sh
python3 scripts/check_mac_atomic_pipes.py
python3 -m unittest discover -s scripts/tests -p 'test_mac_*.py' -v
```

native checker は既存の Apple Silicon / macOS 27以上 / SDK 27以上を要求します。Swift 6 normal / fixture を warning-as-error で compileし、合成 fixture のみ実行します。Linux / 古い OS・SDK / 部分結果 / 0件 / 未回収 child は非0終了で、skip を成功へ変換しません。

5 mode の固定期待値は合計127条件、24回の pipe 割当、48端点の物理 close、16 self-child の開始・exact Wait です。これは期待契約であり、未実行 head の成功件数ではありません。

- `allocation`: 各 syscall の直後、後続 flag 設定前に CLOEXEC / FIFO / blocking を確認します。完了後は parent だけ nonblocking、NOSIGPIPE、元 wrapper を確認します。
- `partial` / `lost`: 最初と2本目の取得直後に固定 fault を入れ、2 / 4端点の回収と再 close / 再 configure 拒否を確認します。lost は実 close 成功後の receipt だけを失わせます。実 kernel EMFILE / close errno の注入ではありません。
- `concurrent`: allocator を各取得直後に止め、別 pthread で固定の自分自身だけを plain `posix_spawn` します。close actions / CLOEXEC_DEFAULT は使いません。意図的に継承可能にした自己所有 `/dev/null` sentinel の継承を child が確認するため、spawn 側が全 fd を隠してテストを通すことはできません。新しい pipe は child に残らず、元 child の Wait と元 pthread の join 後に取得を再開します。8回の準備で両取得境界を検査します。
- `guards`: OS version 境界と production publisher 未設定の拒否を確認します。actual 旧 OS の load を新 OS 上の version fixture で証明しません。

これは原子的割当の直接 API と固定 exec 回帰です。あらゆるスケジューリングを網羅する stress 証明ではなく、実 producer / 実署名の起動時 enforcement、protected policy、鍵、GUI、外部接続を使いません。通常 export に fixture hook や任意 path / argv / fd / trust 入力を追加しません。publisher / exact release は未設定のまま fail closed です。

## CI と未実行の扱い

2026-11-08に [GitHub runner の公式一覧](https://github.com/actions/runner-images)と [Xcode 27 image](https://github.com/actions/runner-images/blob/main/images/macos/xcode-27-arm64-Readme.md)を確認しました。公開 label は `xcode-27`、掲載 image は macOS 27.0.1、安定版 Xcode 27.1 / SDK 27.0 です。27.2 の掲載 path は Release Candidate のため選びません。[public preview の告知](https://github.com/actions/runner-images/issues/14404)にある capacity / image 更新の制約も残ります。

既存 native-source job のみをこの image と既存 Xcode 27.1 path へ変更し、実 runtime / SDK の27以上と Apple Silicon を実行時に検査します。架空の `macos-27` label、手動 OS upgrade、SDK install、失敗除外や timeout 延長は追加しません。既存 native source checker 全体、Intel compile / link、独立 Intel Go library job は維持します。

実行した exact head の各 report と CI conclusion で判定します。Linux の helper 成功、macOS 26での旧試験、公式 API の確認だけを macOS 27 native runtime 成功とは記録しません。
