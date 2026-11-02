# 隔離 Wry startup 診断

通常の native smoke が `session-create` で期限を迎え、app trace が WebView build 開始までしか観測できない場合の、**診断専用・配布不可**の source variant です。原因修正や製品機能ではありません。実行と公開は別の承認が必要です。

## source と build の境界

- 正本は既存 `Cargo.lock` の Wry `0.57.1`、公式 crate archive とその SHA-256 です。patch と hook source の固定 preimage/postimage を照合し、不一致・未知file・不正archiveは拒否します。
- `scripts/prepare_wry_trace.py` は別の存在しない出力先へ、追跡済み app source、明示的な frontend `dist`、検証済み Wry を展開します。registry cache、元 source、通常の manifest/lock は変更しません。実行時の設定やユーザー入力で通常版を有効化する機能はありません。
- generated manifest の直接 Wry 依存と path override は同版の単一 crate を参照します。generated lock の許容差分は Wry の registry source/checksum 除去と app の直接依存追加だけです。他 package の version/checksum は保持します。将来 CI の `cargo --locked` と metadata の同一 package ID 検査が実 resolver による検証です。fixture の成功だけでは Cargo build の成立を証明しません。
- `tauri-startup-diagnostic.yml` の専用 temp copy のみを build します。通常 shell/native/evaluation jobs は元の source を使用します。`host-wry-startup-diagnostic-1` manifest は source/patch/lock の由来hashと EXE hashを記録し、通常variantと区別します。hash は改変検出と由来の記録であり、署名、製品認証や実行権限ではありません。
- 診断 EXE と一時 ZIP は artifact に公開しません。固定schemaの匿名JSONだけを保存します。ライセンスは検証済み archive 同梱文書を保持し、patchの扱いは `tools/wry-startup-trace/` の説明を参照します。

## 観測すること

Wry 内部の environment/controller request、callback の成功/固定error分類、wait 復帰だけを enum として記録します。新しい COM 操作、unsafe、待機、retry、権限変更を加えません。hook は1回だけ登録し、実行時は既存の有限memory・`try_lock` recorderへ送ります。競合・上限・登録失敗は欠測で、後から成功へ読み替えません。

trace v2 は最大16件、2048bytes以内で、hook有効印・封印理由・封印経過時刻を持ちます。旧v1の読取も維持しますが、Wry variant が hook未設定のtraceを返した場合は診断未完了です。raw HRESULT、例外文字列、URL、user-data path、request bodyや資格情報は保存しません。

`complete` は診断窓の完全な回収を表し、native試験合格、正常終了、cleanup成功を意味しません。finish marker で封印された後の進展は記録されません。callback未観測は永久deadlockの証明ではなく、wait error はその固定段階の観測だけです。callback成功とcontroller待機終了でも、その後のbuild・page-load・IPCは未証明です。

## 維持する条件と限界

request15秒、全体60秒、既存normal-close/cleanup budgets、プロセス所有検査、試験1回・retry無し、製品native gate閉鎖を維持します。通常close前にdriver cleanupで成功を作りません。旧失敗の証拠は別headの成功で消しません。

失敗が再現し完全traceを取得できた場合に、環境生成・controller生成・後続buildのどこまで到達したかを区別する診断です。1回成功しただけでは間欠失敗の原因を説明できません。OS内部原因、恒久停止、別PCでの再現性は証明しません。実Rust compile、生成lockのresolver受入、Windows callbackとnative起動は承認済みCIでのみ検証し、local source/fixtureの結果と区別してください。
