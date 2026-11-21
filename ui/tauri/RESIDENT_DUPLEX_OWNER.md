# Resident duplexの内部所有契約

公開済みwire/Close前提を、既存の具体Pump・Shared・Supervisorへ接続するsourceです。
通常の`run()`は`ResidentState::startup()`のstateを`commands_with_state`へ渡し、resident status/intent commandを登録します。
標準buildは`resident_native_source` cfgを有効にせず、sourceは`Unbound`でsidecarを起動しません。statusはmacOSで`source-unavailable`、Windowsなど非対応OSで`unsupported`、intent receiptは`unavailable`です。
macOS標準buildでは`Unbound`の`request_exit()`がtrueとなり、`ExitRequested`を保留しません。
macOSで`resident_native_source` cfgを明示した構成では、`NativeSession`が`NativeOuterOwner`を保持します。`run()`は`ExitRequested`時にownerの判定がfalseなら`prevent_exit()`を呼び、outer monitorの安全な結果と実joinを待ちます。
標準buildは`Unbound`のままでproducerを起動しません。macOSで`#[cfg(all(target_os = "macos", resident_native_source))]`を有効にした署名候補には、生成されたrelease identityに結び付く登録済みproducerを固定bundle内から起動するsource経路があります。この経路の実機起動は未検証です。
標準buildへのproducer接続、配布bundleと信頼した配置、実機でのproducer回収確認は「#456 の後続」です。鍵やprotected policy、GUIのON/OFFも実機未検証です。

## 構築の境界

macOSの`owner::launcher`は非公開の`internal_start.rs`です。
Commandを受ける`start`はその親ownerだけに見え、通常buildにcrate-visibleなCommand/path/argv/fd factoryはありません。
Rust側に既存のPreparedResidentLaunchを受ける公開production wrapperはありません。これは前述した署名候補のSwift固定起動経路とは別の境界です。
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
Close要求後からterminal前までのstatusは、Closureで形・不変条件・source時刻順序を検証した後、Trackingが表示用にbest effortで記録します。記録はrevisionが進むか、同じrevisionで`validForMs`以外が同一の場合だけ行い、操作の対応づけとfreshness検査はしません。記録条件の不一致や記録失敗はstreamを止めません。Close証跡はClosureの検証と`closed`のoutcomeで決まり、表示記録は証跡になりません。
malformed/partial timeout/short write/receipt不一致は同じownerのfaultとして保持します。

## guardと残る接続

production Dropはwire Close証跡・実EOF・exit0・IO/supervisor joinを要求します。
未spawnと、intent前の限定startupRejected＋実EOF＋exit2＋joinは別の結果です。
unknownでは親を保持し、reapだけで解放しません。この層の`cleanup_confirmed`は、信頼済producerからのauthorityを別途確立して使う内部結果です。
任意のwire bytes、fixture成功、process exitを実認証やアプリ終了許可へ昇格させません。

protected producer準備を持つsealed launcherから通常stateへ接続する作業は「#456 の後続」です。outer monitor joinとguarded QuitのGUI経路は、明示的な`resident_native_source` cfgのmacOS構成に接続済みです。
実Mac GUI/producer/ON/OFF/退出は未実証です。
#199のEOF guardは継続し、凍結private callerでは到達しない単体防御という静的所見を変更しません。private実行候補は未編集です。

## 試験の範囲

純粋tracking/startup/write結果と、新規の無害なshell/Python子の実stdin/stdout/EOF/Waitを検査します。
通常fixtureはnegative proofの観測後にも実Child/IOを回収するため、test-onlyでproduction proof guardを分けます。
production guardのunknownは資源を作らないpredicateで検査し、正常Closeは同じ実IO/Child/join後のguard解放を検査します。
実GUI/host/鍵/policy/認証/外部接続/Tunnel/trustや、既存processには触れません。
