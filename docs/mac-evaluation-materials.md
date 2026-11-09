# Mac 検証候補の材料照合

[配布前チェック #24](https://github.com/O2-CONNECTIVE/Tether/issues/24) の小項目です。
[`verify_mac_evaluation_candidate.py`](../scripts/verify_mac_evaluation_candidate.py) は、将来の Mac 評価候補 directory にある manifest、GUI と producer、lock、宣言された notice の実 bytes を読み取ります。build、ZIP 作成、起動、署名、登録、配布、upload は行いません。
一致時も終了値は **2 / blocked** です。ファイルの対応を確認したことを配布許可や実接続の成功へ変えません。

## 現在地と対象

既存の [`package_mac_agent.py`](../scripts/package_mac_agent.py) は `rescue-mac-agent` 単体を扱い、今回の GUI と producer を含む候補を作りません。
[`collect_go_licenses.py`](../scripts/collect_go_licenses.py) の固定7本文を再利用しますが、それだけでは stdlib vendored、platform、実 binary の依存閉包を網羅しません。
[`collect_tauri_licenses.py`](../scripts/collect_tauri_licenses.py) の現行 record は Windows x64 の範囲です。その成功を Mac GUI の notice 完了へ読み替えません。

今回の検査は以下の限定 layout を扱います。実在する評価 app や正式な製品名を宣言するものではありません。
`<candidate>.app` は manifest 内の単一 ASCII component で、正式 app 名・bundle ID・publisher の決定とは別です。

```text
MANIFEST.json
<candidate>.app/Contents/Info.plist
<candidate>.app/Contents/MacOS/rescue-host-shell
<candidate>.app/Contents/Helpers/resident-pipe
LICENSE
RECIPIENT.txt
locks/go.mod
locks/go.sum
locks/Cargo.lock
locks/package-lock.json
notices/go/<固定 collector の7本文>
notices/gui/FRONTEND_THIRD_PARTY_NOTICES.txt
notices/gui/rust-toolchain/COPYRIGHT.html
notices/gui/rust-toolchain/COPYRIGHT-library.html
notices/gui/rust/<crate と版>/<license または notice 本文>
notices/go/stdlib-vendor/<対象>/<license または notice 本文>  # 任意の追加宣言
```

`Contents/Helpers/resident-pipe` は native の固定 nested producer path と合わせます。通常 launch の gate は変更しません。
GUI と producer は同じ宣言 target に対応する thin Mach-O executable と `LC_BUILD_VERSION` を要求します。
fat binary、framework、追加 resource、installer 等はこの最初の layout の対象外で、必要なら契約と負例をレビューして拡張します。
header の minimum OS / SDK は宣言の読み取りで、実互換性や動的 library の安全性の証拠ではありません。

既存の `TetherCurrentGUIFixture.app` は名前で拒否し、plist の executable は `rescue-host-shell`、表示は「カレントの検証版」、package type は `APPL` に一致させます。
これは既知の CI wrapper の誤入力を防ぐ限定検査です。名前を変えた fixture binary や compile flag の混入を検出する機構ではありません。CI fixture の配布転用を防ぐには、固定 source・recipe・最終 binary の独立した対応確認が別に必要です。
Info.plist のその他の設定、bundle ID、entitlement、署名設定を承認・認証しません。

## Manifest と外から照合する期待値

CLI は次の形です。値は当該候補を作った工程の記録と独立レビューで確認して渡します。

```sh
python scripts/verify_mac_evaluation_candidate.py \
  --root <候補directory> --source <完全40桁source-SHA> \
  --manifest-sha256 <MANIFEST.jsonのSHA-256> \
  --gui-sha256 <GUIのSHA-256> --producer-sha256 <producerのSHA-256>
```

期待 hash を manifest 自身から取り出して渡しただけでは独立した根拠になりません。
検査は source commit の Git object、実 build 入力、binary と source の対応、署名者を認証しません。
外部の期待値も含め、source SHA / UTC build 時刻 / raw file hash はこの段階では追跡用の記録です。
正式 version numbering を作らず、表示 label とこれらの追跡識別を分けます。

schema は `tether-mac-evaluation-materials/1`、repository は `O2-CONNECTIVE/Tether` です。
構造の完全な有限例は [`CandidateFixture`](../scripts/tests/test_mac_evaluation_candidate.py) に置きます。fixture の Mach-O と依存資料は合成であり、生成されたものを配布候補として使いません。

- `sourceCommit`、`buildUTC`、`target`、`label`、`appDirectory` を固定型・形式で確認します。UTC は実在する秒精度の `YYYY-MM-DDTHH:MM:SSZ`、target は `darwin-arm64` または `darwin-amd64` に限定します。
- `images` は `gui` と `producer` だけで、固定の相対 path と外部期待 hash を照合します。追加 role や arbitrary executable path は拒否します。
- `files` は manifest 以外の全 file の相対 path と SHA-256、`notices` は同梱を宣言する本文の path 一覧です。固定材料と notice の和以外の file、欠落、重複、case alias、空 directory、symlink/reparse point、特殊 file を拒否します。
- Go 固定7本文は collector の既知 hash と実 bytes を照合します。GUI の frontend と Rust toolchain 2本文、少なくとも1個の crate notice を要求しますが、実 crate graph との一致や内容の法的十分性は確認しません。
- 任意追加の本文は所定の Rust crate / Go stdlib-vendor prefix、その下の少なくとも1段の対象 directory と license 等の basename に限ります。prefix 直下の本文だけで crate / 対象の宣言を代用しません。本文が非空 UTF-8 で hash に一致することと、対象の全依存を網羅することは別です。
- 未知 field、重複 JSON key、偽の配布可・署名済・検証完了の主張を拒否します。`productionGateClosed=true`、`distributionStatus=blocked`、`signingStatus=unverified` と全ての未検証 flag / scope を保持します。

未検証 scope は binary-source 対応、linked dependency 閉包、stdlib vendored / platform notice、build tool 依存、toolchain / SDK 来歴、binary 秘密監査、publisher / 署名、notarization、verify-to-exec、実 runtime / cleanup です。宣言に本文を追加してもこの未検証集合を減らしません。

## 読取り境界と診断

manifest は128 KiB、本文は各4 MiB、各 image は256 MiB、合計640 MiB、宣言 file は2048件までです。未知の file を読まず、出力は固定 code、hash、件数、header の数値宣言と未検証項目だけにします。入力 path、本文、例外、原ログを再表示しません。
source manifest と各 file の hash / 型検査は取得した同じ bytes に適用します。manifest と tree 形状は最後にも照合しますが、敵対的な同時変更に対する原子的 snapshot や、検査後の path-based exec の安全性は提供しません。取得後に元 file が変わっても、報告は読んだ bytes だけを指します。実行する後続処理はありません。

全入力に対して読取りだけを行い、失敗時も file の変更・削除・再試行はしません。内容の secret scanner ではなく、hash や license らしい path は秘密がない証拠になりません。

- 材料一致: 終了値2、`status=blocked`、`materialsMatched=true`。
- 不正・欠落・不一致: 終了値1、`status=refused`、固定 code。
- `--help` のみ通常の help 終了です。配布・起動成功を表す終了値0は返しません。

## 検証と次の工程

初回の有限 fixture は実装後の回帰試験です。独立review後の追補では、不正 XML、未知 encoding、plist root の key、不正 integer、壊れた binary plist の5負例について、正しい file / manifest hash を付けた同じ CLI 入力で red から green を確認します。例外は parser 呼出しの境界で固定 `CANDIDATE_PLIST_INVALID` に変換し、stderr に traceback や入力値を出しません。notice prefix 直下の本文2負例も red から green を確認します。正常材料、全 file の改変・欠落、source / hash 不一致、不正 role、notice 不足、偽の完了 flag、不正 UTC、特殊 file、symlink、未知 file、サイズ上限、診断への私的入力非表示を確認します。native / cache / network / build / 署名 / upload は使いません。

```sh
python -m unittest scripts.tests.test_mac_evaluation_candidate -v
```

実候補へ進めるには、Mac の source-pinned GUI と sealed producer の build recipe、二つの image を同じ release に結ぶ記録、Mac 対象の依存 graph と必要本文、binary の秘密・path監査、recipient 説明を確定する必要があります。その後も署名 publisher、OS launch enforcement、実 enrollment、受領者環境での実行と回収、配布の許可は別の工程です。この照合器や合成 CI の成功で #24 全体を完了にしません。
