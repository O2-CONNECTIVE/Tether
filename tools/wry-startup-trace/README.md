# Wry 起動境界の一時診断パッチ

本番依存を変更せず、隔離した診断入力だけを作るための小パッチです。
Wry 0.57.1 の既存 WebView2 environment/controller 処理へ固定 enum の観測を追加します。
接続 gate、権限、COM 呼出し、unsafe、retry、timeout、message pump の動作は追加・変更しません。
同期 API の既存 `?` は維持し、その error は request と外側 build-error で観測します。
request は発行段階への到達で、Windows API の受付成功を意味しません。
callback-success/error は既存 callback の結果、wait-return-success/error は
既存 wait の外側・内側両 Result の成功/失敗です。callback と wait の時刻は同義ではありません。

## 固定入力と帰属

- 上流: [tauri-apps/wry](https://github.com/tauri-apps/wry/tree/wry-v0.57.1)
- 公式 archive: https://static.crates.io/crates/wry/wry-0.57.1.crate
- SHA256: `759c50ddad0ef9913bb5496cb9d509a50433a3b78cb0238d61105526589cf0e9`
- 既存 Cargo.lock の checksum と一致。版更新・依存解決は行いません。
- `patch.json` は上流の短い preimage と置換内容、各対象の前後 SHA256 を保持します。
  full vendor source はリポジトリに保存しません。
  patch/hook/bridge の3ファイル全体も helper の固定 BUNDLE_SHA256 と照合します。
- Wry は MIT OR Apache-2.0。引用・変更部分には同梱の上流
  `LICENSE-MIT.wry` を適用します。抽出コピーには元の全ライセンスも残します。

`startup_trace.rs` の hook は一度だけ設定します。設定時だけ Box を確保し、
emit は固定 enum を渡すだけです。呼出側 hook は非 blocking・非 allocation・非 panic の
既存 memory recorder を使います。引数、URL、path、profile、HRESULT、error本文は渡しません。
setter failure は dropped evidence とし、製品の制御結果を変更しません。
設定後の callback 所有権は process lifetime です。1 process / 1 diagnostic recorder が前提です。

## 準備

事前に通常の生成 frontend `ui/tauri/dist` と固定 archive を用意します。
この helper は fetch、install、Cargo、build、起動を行いません。

```sh
python3 scripts/prepare_wry_trace.py \
  --source-root /checkout \
  --archive /temporary/wry-0.57.1.crate \
  --output /temporary/absent-diagnostic-input
```

出力先は source tree の外にある未存在 directory のみです。
親 directory は存在必須、symlink component は拒否します
（macOS の `/tmp` 等は canonical path を指定）。
Git が追跡する `ui/tauri/src-tauri` だけと、明示した生成 `dist` をコピーします。
untracked source、target、node_modules、hidden/private key、symlink は取り込みません。
dist は index.html と許可した frontend拡張子のみを受け入れます。
source tree・registry cache・元の lock に書き込みません。
archive の path traversal、link、重複、過大入力と preimage 不一致を fail closed で拒否します。

- `app/src-tauri`: 一時 manifest、lock、診断 hook 付き app source
- `app/dist`: 元の生成 frontend
- `wry`: checksum確認済み archive の隔離コピーと小パッチ
- `provenance.json`: path/rawsource を含まない固定版と SHA256
- `prepared-files.json`: build前後照合専用の相対 path→SHA256 inventory（公開不要）

一時 manifest のみ Windows direct Wry `=0.57.1`, default-features=false と
`[patch.crates-io]` を追加します。既存 runtime の feature選択は変えません。
lock は Wryのsource/checksum削除と appの同Wry依存追加だけで、それ以外のpackageは
byte単位で保持します。Windows checkout対応として生成対象の manifest/lock/Traceだけ
CRLF→LF正規化を行い、hash は正規化後の生成物を指します。その他のコピーはbyte保持です。
限定 lock parser は曖昧な複数Wryや版付きedgeを拒否します。Cargoのresolver代用ではありません。

## build後の照合

将来の承認済みCIでのみ、一時manifestに対して `cargo --locked` と
`cargo metadata --locked --format-version 1` を使います。
target は入力directory外へ置きます。metadata JSONはraw uploadしません。

```sh
python3 scripts/prepare_wry_trace.py \
  --output /temporary/absent-diagnostic-input --verify-output \
  --metadata /temporary/cargo-metadata.json
```

元inventoryの全byteとlockを再照合し、追加入力はTauriの
`src-tauri/gen/*.json` 以外拒否します。metadata指定時はWry packageが唯一で、
正しいpath/version/source=None、rootとtauri-runtime-wryの依存IDが
同じWry IDであることを確認します。fake metadata試験は実Cargo graphの証明ではありません。

provenanceの `patchSha256` は patch.json/startup_trace.rs/app_hook.rs.txt、
`preparedSourceSha256` は distを含むapp全入力、
`wrySourceSha256` は抽出・patch後Wry全入力について、
sorted relative path→file SHA256 を
`json.dumps(..., sort_keys=True, separators=(",", ":")).encode()` したSHA256です。
`generatedLockSha256` は生成lockそのもの、`archiveSha256` は固定archiveです。
inventory/provenanceは同一CIジョブの私有temp前提であり署名・改ざん防止機構ではありません。

## 検証境界

`PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts/tests -p test_prepare_wry_trace.py -v`
は準備・拒否・lock保持・fake graphのoffline fixtureです。
公式archiveへのpatch/preimage一致も別に確認します。
これらはWindows Rust compile、実resolved graph、native起動、WebView2成功、
製品接続、配布可の証明ではありません。
