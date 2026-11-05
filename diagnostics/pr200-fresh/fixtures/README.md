# fixture の出所

`phase2-trial1-record.json` は run42084193726 / job127444793675 / attempt1 の controlled sanitized JSON blockから取得し、親側も独立検証した実保持記録である。
canonical JSONは26861bytes、SHA256は `f634cef6549184533062e33a2f7a9b7fdc4bacc8ac9cdcf6772123416f9169e8`。
全12 fileのcanonical JSON hashもrecord内に保持し、fixture試験で再検証する。
原response、例外、path、PID、commandlineの代用品ではない。

この実記録はprofileMatch=false、6 child分類行、queryComplete=false、capability unavailable、通常close/cleanup成功を保持する。
candidateの欠落/重複/型不正の区別と、observerの失敗siteは残っていない。
新しいsidecarをこの記録へ補完・移植せず、どちらもnot-retainedと読む。
元の失敗、PR208/209のinconclusive、profileの集合相関未達は変更しない。

テスト名でsyntheticと明示したcapability/exception/site/saturationケース、および `synthetic_classification()` は合成fixtureである。
合成したraw envelopeを実測responseの再構成と扱わない。
PowerShellのledger fixtureも合成で、OS/CIM/native起動の確認ではない。
