# 診断要約の純粋fixture境界

[Issue #27](https://github.com/O2-CONNECTIVE/Tether/issues/27) の独立小項目。
`scripts/diagnostic_boundary.py` は匿名の診断投影だけを検証する純粋関数です。
producer runtime、既存workflow、keeper/observer、実Mac診断には接続しません。
任意のrawログを探すscanner、ファイル収集、共有や自動送信の入口はありません。

## 正本と許可項目

enum値は既存の [DIAGNOSTIC_ENUMS.json](../ci-producer/DIAGNOSTIC_ENUMS.json)
を読み、別のenum定義は作りません。入力はUTF-8 JSON objectのbytesのみです。
必須の `stage` と `failure_code`、任意の `result_fields` 群と `native_fields` 群
だけを許可します。任意群を含める場合は、その群の全フィールドが必要です。
群が無いことは未観測のまま維持し、前回の値から補いません。
これは完全なproducer markerや既存workflow出力を受け付ける契約ではありません。

秘密鍵、token、実ユーザー名、実IP、端末固有path、argv、例外本文、子プロセス出力、
外部応答、command ID、認可情報は許可項目にありません。未知フィールドを捨てて成功にせず、
レコード全体を非成功にします。許可フィールドに入れた未知値、型不正も同じです。
拒否時は固定reasonと空fieldsだけを返し、入力値・未知キー・例外本文を返しません。
診断参照や継続追跡IDは追加せず、認可情報との結合も行いません。

16,384 byteを上限とし、超過、重複キー、不正UTF-8/JSON、非標準数値、欠落・部分取得は
`validation: unavailable` です。この上限は既存producer summaryの読み取り上限に合わせた
fixture境界であり、ローカルログの容量や保存方針ではありません。
`record-valid` は許可項目の検証が通った意味だけです。`complete/none` であっても
接続成功、cleanup confirmed、observerの成功判定を生成しません。
失敗を表すenumも有効な診断です。取得不能を成功に置換しません。

native snapshotは `native_capture: matched` のときだけ保持し、そうでなければ
capture以外を `unavailable` にします。各first/finalの診断flagは対応する
`diag_available: yes` が必要です。`owned_group_quiescent_snapshot` は瞬間の記録であり、
再観測・signal・cleanup再試行の許可にも、cleanup成功の根拠にもなりません。

## 実行と検証範囲

```sh
python3 -m unittest discover -s scripts/tests -p test_diagnostic_boundary.py -v
```

Python 3.11以上、標準ライブラリのみ。匿名fixtureは
`scripts/tests/fixtures/diagnostic-boundary/cases.json` に置きます。
模擬token/path/例外/出力/IP/argv/識別子、各enumと型不正、全群の欠落、byte上限、
重複キー、深い不正JSON、native recorder未取得、呼出間の状態非共有を検証します。
公開する検証結果は件数・対象版・制約の要約だけとし、入力を診断として表示しません。
新関数は、同じ入力と期待結果を使い、無検証でJSONを返す試作の失敗から検証実装の成功へ
対応づけました。これは開発toolの有限fixture検証であり、製品経路のTDDではありません。

[producer CI](producer-connection-ci.md) と
[supervisor](../ci-producer/supervise_ci.py) が持つ原process終了・同一invocation・
immutable verdictの意味を置き換えません。
[Mac診断](mac-group-observer-diagnostic.md) の未実証境界も維持します。
実GUI、実接続、完全な漏洩防止、ファイル権限、取得/書込/容量不足/中断/削除の実機挙動は
この検証からは判断できません。共有用最小要約とローカル原証跡は別扱いとし、
原証跡の取り込みや公開を追加しません。
保存先・保持日数・削除・鍵管理は [#36 DEC-03](https://github.com/O2-CONNECTIVE/Tether/issues/36)
の判断待ちです。Issue #27全体の完了にはしません。
