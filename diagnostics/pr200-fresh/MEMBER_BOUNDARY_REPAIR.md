# PR217 fixture のmember-access境界を修復する

Base:57d21df71b3e635e28b7bd0398af61a6ee483d48/tree195acfc0a9404195707bbe12ea88e0ff8be58ac3。
再開時checkout clean、task配下の別process0。今回の範囲はlocal source/unit/reviewだけで、push/PR編集/CI/PowerShell導入/native/GUI実行を行わない。

## 仮説と保持証拠

編集前に順位づけした仮説は以下。いずれも元native timeoutの仮説ではない。

1. ordinary getter exceptionがmember binderでnullへ正規化される。予測は7.6.9のdot-member getterに例外catch/null帰結があり、実guard labelと整合すること。
2. label捕捉/scopeが壊れている。予測は返るlabelがguardの実行位置と無関係であること。既に特定guard labelを取得し、sourceの単一代入位置とも一致するため根拠は弱い。
3. guard分割が読取り/短絡を変えた。予測はold/new counterの不一致。PR217の四つの先行不一致predicateは偽で、source比較も反証方向。

実PR217のgetter-exception/new-detailはexpected metadata-command-line-read-exception、actual metadata-command-line-guardだった。
recordは2files/canonical2084bytes/hash e91afcf1b623f91c78cbe1adeb689bc31894b7318e5e80c915cadd9c519de6b4。そのbytesと全filehashをfixtureで不変検証し、patched passを補わない。
後続verdict/errorチェックと次のvalid-commandは旧runで未到達だった。

## 7.6.9の一次資料で定まる境界

公式annotated tag v7.6.9 (a0edd6969993766081edc6bf0306696d1adef88a)をcommit bf5fc434120666e5f1a8d1dfd6ecac7efc17a4beへ解決し、以下のsourceを独立に読み取った。

- [Binders.cs L5220–5250](https://github.com/PowerShell/PowerShell/blob/bf5fc434120666e5f1a8d1dfd6ecac7efc17a4be/src/System.Management.Automation/engine/runtime/Binding/Binders.cs#L5220-L5250): instance memberのValue読取りをgetter用のtryで包む。
- [Binders.cs L5581–5595](https://github.com/PowerShell/PowerShell/blob/bf5fc434120666e5f1a8d1dfd6ecac7efc17a4be/src/System.Management.Automation/engine/runtime/Binding/Binders.cs#L5581-L5595): 特定の制御/Method/PropertyNotFound例外は再throwし、それ以外の通常例外をnullへする。
- [MshMemberInfo.cs L1854–1882](https://github.com/PowerShell/PowerShell/blob/bf5fc434120666e5f1a8d1dfd6ecac7efc17a4be/src/System.Management.Automation/engine/MshMemberInfo.cs#L1854-L1882) と [L331–336](https://github.com/PowerShell/PowerShell/blob/bf5fc434120666e5f1a8d1dfd6ecac7efc17a4be/src/System.Management.Automation/engine/MshMemberInfo.cs#L331-L336): ScriptPropertyのgetter呼出しと通常runtime例外のgetter wrapper。
- [ExtendedTypeSystemException.cs L227/L362](https://github.com/PowerShell/PowerShell/blob/bf5fc434120666e5f1a8d1dfd6ecac7efc17a4be/src/System.Management.Automation/engine/ExtendedTypeSystemException.cs#L227): getter wrapperはGetValueException系統で、上記MethodExceptionの特例ではない。
- [CompiledScriptBlock.cs L1091–1103](https://github.com/PowerShell/PowerShell/blob/bf5fc434120666e5f1a8d1dfd6ecac7efc17a4be/src/System.Management.Automation/engine/runtime/CompiledScriptBlock.cs#L1091-L1103): SwallowErrorsのerror-pipe処理。binderの例外→nullとは別の層で、$Errorが不変/空という証明ではない。

従ってこのthrowing ScriptPropertyを`$view.CommandLine`で読むfixtureでは、read-exceptionを呼出側へ伝播する想定が境界違いだった。ErrorActionPreference Stopもこの通常getter catchを迂回しない。
これは公式sourceと既存実測が一致した説明であり、ローカルでPowerShellを動かした証明や新しいpatched runtime passではない。

## 回帰の選択と修正

選択肢Aは元のdot-memberとthrowing ScriptPropertyを保ち、厳密なguard labelと絶対的な結果を検査するもの。実observerの呼出位置に最も近く、今回これを採用する。
選択肢Bとして独立のfresh objectでdot-read/nullだけを検査する回帰も可能だが、guard差分との重複が増え、今回の一次資料と実測に対しては不要。
直接ScriptBlock呼出しは別境界。PSObject.Properties['CommandLine'].ValueもPowerShellのproperty accessを経るため、例外露出を保証するbypassではなく、今回の回帰へ置き換えない。
両label許可やcase削除は差を隠すので行わない。

変更するexpected labelはgetter-exceptionの一箇所だけで、metadata-command-line-guardを厳密に要求する。元五比較/expected read数/case順とguardの単一評価/短絡、既存old/new verdict/error/creation-error identityチェックは保つ。
追加した小さなfixture内helperは、valid-commandは両方受理、他のcaseは両方拒否、creation-error以外の拒否は固定PROFILE_IDENTITY_UNKNOWNと要求する。両方が同じ誤結果を返してもpassしない。
helperの負例controlは「両方受理した拒否入力」「両方拒否したvalid入力」「等しいが誤ったguard error」。加えてvalid受理の正例も検査する。これはfixture内の私有観測dataだけを使い、CommandLineの追加read/OS操作を行わない。
負例control自身の失敗用に一つの固定enum METADATA_BOUNDARY_CONTROL_FAILEDだけをallowlistへ追加し、schema/ログframeworkは増やさない。
creation-errorのreference identity条件はそのまま。getter-errorのidentityを試験済みとは扱わず、コメントもその範囲へ訂正する。

## 検証と未実行

Python source契約は修正前red、修正後green。静的mutantは旧誤期待への復帰、拒否の削除、valid負例controlの削除、固定error比較の削除を検出する。
新helper/controlとコメント/一期待差を除くprojectionは元fixture SHA256474dd092d55e9057938dffe15dbeb5dba28dbe51a3e510c01e4281a5757c463fへ一致し、既存のさらに前のprojectionも保持する。
これは静的source契約のred/greenで、PowerShellのpatched runtime red/greenではない。
ローカルは既存Python3.13.2/Node26.13.0。PowerShell parser/runtime、修正版五case、追加PS負例controlsは未実行であり、Python static試験を代替runtime証明にしない。

product/fixed PR200 source、workflow、15000ms/profileMatch/ownership/cleanup、request/response/error/AbortSignal、main/Macは編集しない。元run42037151371/attempt1、PR200blockedと全旧recordを保持する。

最短の次工程は、別判断で固定した修正版に対しfresh標準Windows一つ、parse＋ledger/detailを各最大一回だけ実行するfixture-only cap1。build/driver/native0で五caseと負例controlのruntime完了を確認する。
その通過は元timeoutの修正を意味しない。通過後に初めて元profile証拠の診断へ戻る判断ができる。このturnでは何も実行・公開しない。
