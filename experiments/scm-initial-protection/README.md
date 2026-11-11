# SCMが作成するLocalServiceの保護状態を観測する

これは製品から独立した、単回Windows評価用probeである。
製品の2 service設計、認証、SSH、guest、接続gateは変更しない。
本ディレクトリのsource、fake試験、cross compileはWindows実行承認ではない。
実行は固定artifact/hashと[操作契約](SETUP-CONTRACT.md)を含む別の具体承認後に限る。

## 調べる一点

固定したSCM LocalService OWN_PROCESS serviceのprocess、観測worker thread、primary tokenに、共有LocalService等からの改変・危険handle取得を許す具体的な状態があるかを観測する。
SID typeはUNRESTRICTEDを一回だけ使い、結果を見てRESTRICTEDへ切り替えたりACLを修復したりしない。
製品のSYSTEM監督役の代わりに、観測専用の昇格controllerで準備・停止・撤去を行う。
観測のために第二のSYSTEM serviceを追加する必要はない。

| 区分 | このprobeで扱う事実 |
| --- | --- |
| 公式仕様で確定 | service SIDをtokenへ追加する設定とobjectごとのsecurity descriptorは別。object owner、token内部のTokenOwner/DefaultDacl、DACLは別項目。SID typeの設定成功だけで現在tokenへの反映を認定しない |
| 観測が必要 | 対象OS/buildで実際に生成されたtoken profile、process/thread/tokenのowner・DACL、専用SIDの実属性、必要なqueryの可否、同workerでの二時点の差分 |
| 既存契約に必要 | 通常guestや他の共有LocalServiceから、helperの認証結果・実行主体・所有参照を改変できないこと。固定配置、正規供給元、元ownerの失効・回収責任を保つこと |
| 今回の非目標 | 管理者/kernel侵害耐性、PPL、全query権の禁止、第三の攻撃service、製品の同launch認証、実SSH、OSの生成前から全threadが無改変だった証明 |

権利は対象ごとに評価する。
processの注入・handle複製、threadのcontext/token変更、tokenの複製・割当・権限変更、各objectのDACL/owner変更の経路が重要である。
queryやsynchronize権があることだけで不適合とはしない。
終了権による停止は、認可の迂回と区別し、製品ownerが失敗を検知して拒否・回収する契約との組合せで評価する。
新たな絶対的無停止保証をこのprobeの必須条件に追加しない。

## 観測と結果の限界

Go runtime起動後のservice workerをOS threadへ固定し、最初の観測と固定5秒後の観測を記録する。
このthreadをWindows processの初期threadと呼ばず、同workerの二snapshotを全runtime threadの証明にしない。
各queryは64KiB、groupは128件、privilegeは64件、最終reportは256KiBを上限とし、超過は欠測として拒否する。
cooperative予算15秒、専用probe自身の強制終了20秒を固定する。
同期Win32呼出のreturnやOS停止中のタイマー実行を保証しない。
強制終了・未終了・途中書込みは収集成功にしない。

| 判定 | 意味と次の一手 |
| --- | --- |
| 収集完了 | 必須二snapshot・最終report・正常close・実終了が揃った。危険な状態が記録されても収集自体は成功し得る |
| 具体的な不適合証拠 | 完全なSDとowner規則により、非信頼主体に危険権利を与える経路が明確。その設定をそのまま採用せず、対象権利と生成規則に限定して設計へ戻す |
| 不適合が観測されない | 二時点に反例がないだけ。生成から観測までの履歴は未観測のまま。同じ試験を繰り返して製品合格へ進めない |
| 条件未成立・欠測 | SID未反映、query不能、未知形式、report不在、取消、timeout等。該当stageだけを切り分け、別条件へ自動retryしない |
| 撤去未確認 | process退出、SCM停止、登録消失、所有file撤去のいずれかが未確認。観測成功と分離し、残留を報告する |

shared ownerやallow ACEの文字列検索だけで判定しない。
OWNER RIGHTS、deny、順序、generic mapping、取得先tokenのDACLも確認する。
単純に共有LocalServiceがownerでOWNER RIGHTSによる制限がない等、規則と全SDから示せる反証は、その固定構成の反証として扱える。
複雑・未知のACEは「安全」ではなく判定不能とする。
probe自身は保護のPASSや製品readyを返さない。

## データと実行範囲

固定service名は `RescueHostProtectionProbe-20261030`。
固定rootは `C:\ProgramData\RescueHost-ProtectionProbe-20261030`。
昇格controllerがtrusted ownerの空reportを事前作成し、probeは既存fileのみを開く。
reportを共有LocalServiceがownerになる既定値で新規作成しない。
既存root/service/fileとの衝突は中止し、流用や一括削除をしない。

report内のSID、SDDL、token識別値は非公開の診断データである。
ホスト名、IP、環境変数、任意command、credential、秘密鍵は収集しない。
公開Issueには原reportを投稿しない。
観測serviceはネットワーク・認証要求を受け付けず、logon/token生成・token調整・ACL修復・子process起動を行わない。

## sourceでの検証

専用moduleであり、製品backendへのimportや製品inventory登録はない。
既存と同じGo 1.27.3、golang.org/x/sys v0.49.0を固定し、通常/race、vet、Windows amd64/arm64 cross compileで確認する。
ビルド済みWindows実行物をMacから起動しない。
PowerShell scriptの静的レビューと、Windows上のparser・runtime確認を区別する。
固定source、artifact hash、実行済み検証、未実行のWindows検証はbuild handoffに記録する。

## 一次資料

- [SERVICE_SID_INFO](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/ns-winsvc-service_sid_info)：SID追加とsystem startの注記。
- [GetSecurityInfo](https://learn.microsoft.com/en-us/windows/win32/api/aclapi/nf-aclapi-getsecurityinfo)：保持handleからのSD取得。
- [Owner of a New Object](https://learn.microsoft.com/en-us/windows/win32/secauthz/owner-of-a-new-object)：ownerと既定owner。
- [Thread security](https://learn.microsoft.com/en-us/windows/win32/procthread/thread-security-and-access-rights)：thread objectの権利と既定SD。
- [製品ADR](../../docs/adr/0004-localservice-auth-helper.md)：採用済み2 service設計と未成立境界。
