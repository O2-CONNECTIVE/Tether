# Resident duplexの内部所有契約

公開済みwire/Close前提を、既存の具体Pump・Shared・Supervisorへ接続するsourceです。
通常起動、IPC、Status/provider、trusted launch factoryは未接続です。
実producer、鍵やprotected policy、GUIのON/OFF/Quitを起動・有効化しません。

## 構築の境界

macOSの`owner::launcher`は非公開の`internal_start.rs`です。
Commandを受ける`start`はその親ownerだけに見え、通常buildにcrate-visibleなCommand/path/argv/fd factoryはありません。
既存のPreparedResidentLaunchを受けるproduction wrapperは、保護されたproducer準備を公開構成へ確立するまで追加しません。
実装本体は通常buildでもcompileし、cfg(test)だけのfixture factoryが無害な自己所有子へ同じ処理を使います。
fixtureのstatus/receiptは認証やlive表示の証拠ではありません。

## 同じChildとIOの終了

監督threadを作ってから、その中でChildを一度だけspawnします。
attachment失敗・partial capture・panicでも同じChildを保持し、所有handlesだけを閉じ、同じChildをWaitします。
別Child、再接続、代替owner、signal/killで元のunknownを上書きしません。

PumpはそのChildから取得したstdin/stdoutだけを所有し、FIFO/access/CLOEXEC/nonblockingを検査します。
readerとwriterは別threadではなく、同じIO/supervisor worker内のhandlesです。
明示Closeでstdinを一度だけ半閉鎖し、通常のviewer破棄はtransportを閉じません。
process exitだけで読取を止めず、実stdout EOFまでdrainし、不正shutdown後もbounded discardを続けます。
その後IO handlesをdropし、exact Child Waitを経て、Supervisorがそのworkerの実joinを確認します。
個別reader/writer threadやouter monitorのjoinを完了したと主張するものではありません。

既存Trackingは同じlifetimeのrevision・freshness・unresolved intentを保持し、成功writeやreceiptをtransition完了にしません。
Closeは新規submitを封鎖し、queued/waiting要求をunknownにします。
malformed/partial timeout/short write/receipt不一致は同じownerのfaultとして保持します。

## guardと残る接続

production Dropはwire Close証跡・実EOF・exit0・IO/supervisor joinを要求します。
未spawnと、intent前の限定startupRejected＋実EOF＋exit2＋joinは別の結果です。
unknownでは親を保持し、reapだけで解放しません。この層の`cleanup_confirmed`は、信頼済producerからのauthorityを別途確立して使う内部結果です。
任意のwire bytes、fixture成功、process exitを実認証やアプリ終了許可へ昇格させません。

後続はprotected producer準備を持つsealed launcher、通常Status/intent経路、StandbyOwner/ExitGateのouter monitor joinとguarded Quitです。
それらの結合と実Mac GUI/producer/ON/OFF/退出は未実証です。
#199のEOF guardは継続し、凍結private callerでは到達しない単体防御という静的所見を変更しません。private実行候補は未編集です。

## 試験の範囲

純粋tracking/startup/write結果と、新規の無害なshell/Python子の実stdin/stdout/EOF/Waitを検査します。
通常fixtureはnegative proofの観測後にも実Child/IOを回収するため、test-onlyでproduction proof guardを分けます。
production guardのunknownは資源を作らないpredicateで検査し、正常Closeは同じ実IO/Child/join後のguard解放を検査します。
実GUI/host/鍵/policy/認証/外部接続/Tunnel/trustや、既存processには触れません。
