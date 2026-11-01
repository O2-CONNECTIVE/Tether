---
status: accepted
---

# 指定Windowsユーザーの承認資格と接続bindingを分ける

2026-10-29、初版の承認主体は保護された正規設定のOperatorSIDとkernelが確認する実message主体で認証する方式Bを採用した。
指定した同じWindowsユーザーはログインし直した後も新たに承認できるが、別SIDには許可しない。
これは[承認資格](../GLOSSARY.md)の選択であり、既存のRecord・grant・endpointを別ログオンへ自動移譲する決定ではない。

元の保護Rootが保持する固定SIDを使い、request本文のSIDや接続候補の観測値から期待policyを作らない。
実messageのtoken、kind/level、restricted/AppContainer等を検査し、復帰を確認した後の実AuthIdを同じ要求・owner・logical channelのbindingへ固定する。
同じchannelの後続要求では主体projectionの完全一致を要求し、AuthIdが変われば引き継がない。切断後は再認証と新規承認が必要で、鍵集合・期限上限・失効・再承認条件は変えない。

互換用profile/1の完全比較は残し、profile/2を明示的に分けます。AuthIdの0値をwildcardとして使いません。host operator/admin既侵害は保証対象外です。

## 実装と残る境界

今回のcoreは固定SID policy、実message認証後のreport/2、EOF・正常exit・回収後のsealed Readと単回binding消費を実装する。
元Execution ownerは保持Recordを再照合し、Service公開前に固定SID policyを一度だけ供給する。
Serviceは同Collector/owner/issuerの単回認証結果をstop gateで照合し、同一logical channelの主体projectionを固定する。
同SIDでもAuthId等が変われば書込み・dispatch前に拒否して失効し、別Serviceへのbinding移譲や旧intentの再利用は認めない。
これらの正例はprivate OS/owner memory seamであり、同一製品callerの実Windows縦断ではない。
純粋なpolicy構成は保護Rootの証明でも起動可能性でもない。正規の専用helper token・same-launch evidence・helper entryの保護sourceは未構成で、取得・Move前の拒否を維持する。
実Windows、実runtime/inner sshd、実SSH、製品gate解除の成立をこの採用やmemory試験から推定しない。
