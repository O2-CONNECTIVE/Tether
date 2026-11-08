# 同じ native IO worker の supervisor

2026-11-08の変更起点はmain `387bdb503a76366d8b06433b687871651e7883ce`です。#240 / #241 / #243は#267で統合され、続く#268でsession試験の時刻差を修正済みです。各文書の未統合・draft保留の記録は当時の履歴であり、現在の統合状態ではありません。過去のWindows失敗や本番接続の未達は解消扱いにしません。

これは source-only の所有接続です。[active Pump](mac-native-active-pump.md)と同じ NativePreparation / TLS worker / Foundation Process を使い、具体的な NativeIoSupervisor が worker の実 join までを所有します。通常起動は publisher nil 拒否 / source-unavailable のままです。

```text
NativeIoSupervisor.prepare (no arguments)
  original Rust IO worker
    NativePreparation.prepare → publisher nil: Refused
    active source variant / fixed synthetic fixture only:
      Pump.take_active → existing Shared / Closure / drive_io
      clean Pump EOF: original control half-close → original poll_exit / exact Wait
      failed Pump: shutdown-only discard ⇄ original poll_exit / exact Wait
        raw EOF and actual exit are both required before IO retirement
      original retire_active_io
  actual JoinHandle.join → NativeJoined
```

引数なしの通常準備は既存の閉じた NativePreparation.prepare を元 worker 上で呼びます。未起動準備が得られても retire し、active の終了証拠へ読み替えません。通常の active trusted launcher はまだありません。fixture 専用の固定 self-child enum と unwind 選択は別 cfg で、任意 path / argv / fd / hash / callback factory を入力にしません。

既存 BoundDuplex / CloseHandle を再利用し、viewer の寿命と process 終了要求を分けます。親は worker を先に保持してから attachment を待ちます。attachment の受取人が失われれば同じ CloseHandle を要求します。元 owner は worker 内の attachment / Pump unwind の外側に残り、元 Process の終了が観測されるまで保持します。Drop も Close を要求して同じ worker を join し、pending worker を detach しません。実時間の終了上限は主張しません。

## component の証拠と全回収の境界

NativeJoined の private field は実際の join 後にだけ作ります。Refused、未起動退役、active の元 Wait / IO state / unwind、worker unknown を別結果として保持します。wire Close の判定も別 component です。loop return、actual Wait、raw EOF、個別 close receipt、IO worker join を一つの楽観的成功へ圧縮しません。accepted intent は applied ON ではありません。

この IO worker は Read / Write を扱う一つの worker であり、別々の reader / writer thread の join ではありません。既存 std Child 専用 Supervisor / End、StandbyOwner、outer monitor、cleanup_confirmed / can_release / Quit API は変更・接続しません。NativeJoined に全回収の許可はありません。join 後も元 native record は Unknown と禁止置換を保持します。

Unknown active guard は通常の Read / Write / half-close を引き続き拒否します。request_active_close は同じ private handle / 元 TLS worker の control を close-once で閉じ、Unknown と shutdown 状態を保持します。destructor の abandon と異なり、既に exit が観測されていても observation を早期退役しません。元 Process の終了後にも残りの bytes を読めるようにします。未起動では no-op です。

shutdown 専用 discard ABI は active / 未退役 / 同じ handle / 元 TLS worker / close 要求済みを検査します。Swift 内の固定4096-byte scratchへ1回だけ nonblocking readし、progress / pending / raw EOF / error / rejectedだけを返します。payloadをRustへ返さず、parser・control write・認証・cleanup許可に使いません。新しい export は request_active_close と discard_observation の2つで、allowlistを同時に更新します。Rustは元ownerのmutable borrowを要求し、借用IOと同時のclose・discardをcompilerが拒否します。

Pumpが Ok(true) で終わった場合だけ通常の EOF 完了として扱います。parser fault、attachment failure、unwind、既にUnknownのownerはshutdown要求後、有限量のdiscardと元Processのpoll_exitを交互に行います。元Wait後もraw EOFまではdrainを続け、Unknownを解除せず、別owner・PID再構築・signal・close再試行・detachは行いません。

**終了時間の上限も、全故障での回収完了も保証しません。** discardのError / Rejectedはraw EOFではなく、元Waitが済んでもEOF条件を満たしません。その場合はowner / workerを保持し続けます。無限出力、停止しないchild、終了観測のUnknown、回復しないread障害は引き続き残件です。pending TLS exit後の全回収もこの単位の対象外です。有限出力のbackpressure解消をnative full Supervisor / Quit許可へ広げません。

## 合成試験と実行境界

最初の実 consumer は未実装 module の compile red → 同じ固定 self-child の wire / EOF / original Wait / IO 回収 / actual join green です。追加 negative / Drop / 未起動側は実装後回帰です。

```sh
python3 scripts/check_mac_native_supervisor.py
```

通常準備7条件、fixture17 modes は272条件、計279条件をstrict reportで確認します。16 active childrenの元Wait、64 active endpoints、14 actual supervisor IO worker joins、2 direct discard worker joins、未起動4 endpointsと2 preparation worker joinsを確認します。従来177条件に、有限出力の4 supervisor modesと2 discard契約modesを追加します。

固定childは不正な最初のframeに続けて1 MiBを書きます。pre-close parser fault、IO unwind、launch時点で既にUnknownの3条件で旧実装は元Wait / join後にExit(88)となるred、変更後はExit(0) / Unknown / close4 / Wait1のgreenです。child自身のstdoutはnonblockingで、5秒の固定escapeを持ちます。これは赤試験を回収するためのchild内の失敗終了であり、親のtimeout成功・kill・製品の時間上限ではありません。

direct契約は、元Wait後に残ったbytes、PendingとEOFの区別、4096-byte以下の進捗、wrong handle / wrong thread / 未起動 / 退役済み拒否、通常Unknown read/write拒否、close-once、置換拒否を検査します。元Wait後もError / Rejected / PendingはEOFにならない実supervisorと同じ判定を確認します。Pending試験は、固定childのcontrol EOFをfixture専用CLOEXEC複製writerで保留し、空でwriterが開いたpipeを実際に観測してからそのgateを一度だけ閉じます。schedulerの隙間やsleepをPendingの証拠にしません。追加gateのclose receiptと二度目のclose拒否も同じ契約で確認し、childの固定timerは失敗時の自己回収だけに使います。

回収不能なworkerを残すため、永続read障害でsupervisorが永久保持する経路自体を起動する試験は行いません。

normal / fixture の build / clippy は locked / offline、既存11 dependency packagesのみ。既存 active bridge は未起動 close no-op の1条件を加え240条件、借用 close 拒否を加え compile refusal9。Intel は同じ source の owner / active / Pump の normal / fixture compile/link、refusal18を検査し、foreign image を実行しません。prepared Pump133、active Pump129と従来 owner / C ABI / Swift の契約は弱めません。

実 GUI / producer / host、実鍵 / protected policy、外部 / Tunnel / OS trust、署名・配置・persistent access は未実行。signed launch / verify-to-exec / atomic inheritance、native full Supervisor / outer joins / Quit と実機は残件です。a4 / a5 / task-7 は無変更、解除連絡待ちです。base の Windows debug session timeout は未解明のまま保持し、この source 試験で解消したとは扱いません。driver 起点の drop 時刻と forward 所要時間、別 Instant の Host 時刻も混同しません。

[後続の outer monitor](mac-native-outer-monitor.md) が同じ supervisor を保持し、別の monitor join component を検査します。native Unknown は解除せず、全回収・Quit 許可にはしません。

## 今回のローカル確認と未実行

arm64 Macの既存Rust 1.99.1 / Python 3.13.2を使用しました。supervisor279、outer301、session251、active bridge240＋compile refusal9、Rust owner92＋compile refusal9と、Mac checkerのPython116試験を確認しています。Swiftのnormal / fixtureはarm64とIntelの双方でcompileしますが、Intel Rust stdが存在せず、`check_mac_native_cross.py` は準備段階で停止しました。Intelの18 compile refusal / link / clippyはこの環境では未実行です。導入や設定変更は行いません。

Pythonのsyntax / tokens / sizesは成功しましたが、一般の`dev.py check`は既存interpreterに`_tkinter`がなく停止しました。PythonのCI固定版3.13.17での確認、CI結果、最終headの独立レビューは別判定です。実GUI・実producer・実認証・終了許可の成功とは扱いません。

## 進捗ごとの待機とCI失敗の切分け

PR #269のhead `2568b978cb17e646aab3c88963562c3c730a9109` は、CI run42437710534のnative source job128600122286でsupervisor checkerが失敗しました。残された記録は汎用の失敗markerだけで、当該CIの失敗mode・条件・child終了値は未特定です。同じheadの固定binaryではローカル20全mode周回が成功しましたが、それでCIの失敗を取り消しません。

別の制御された再現として、fixture専用の25ms timer配送を固定し、4096-byteのProgress後にも毎回sleepする旧loopがchildの5秒escapeへ到達することを確認しました。redは `supervisor-finite-coalesced` / condition3 / child exit88です。Progress後は元Processをpollしてyieldし、Pending / errorなどだけsleepする変更後、同じ固定入力でexit0となります。read量の上限、毎chunkの元Wait観測、公平なyield、Unknown保持は変えず、childの期限延長やtimeout成功で解消しません。この再現を元CIの実timer測定や確定原因とは扱いません。

checker失敗時には固定phase / mode、実returncode、範囲検査済みのfailed conditionとchild exitだけを出します。生のstderr・payload・path・例外文を転記しません。Swift build内の失敗などexit値を捕捉できない箇所はnullのままにし、取得したように扱いません。診断の秘匿とmodeごとの条件上限をPython試験で検査します。

元Wait後のdirect discard試験も、EINTR由来のPendingをProgressやEOFと混同せず、固定回数だけ再読します。budget切れは失敗であり、元childのWaitとIO回収・worker joinを完了してからassertします。
