# 固定producerの同一native起動組成

専用 `resident-pipe` source に対し、元 Swift `Process` と元 IO owner を
既存 Rust supervisor から起動する内部組成です。通常 Tauri は引き続き未配線です。

## 閉じた起動入力

`ApprovedRelease.production` は nil のままです。正規 Team / Developer ID / exact release
CodeDirectoryHash は存在するものとして捏造せず、通常 prepare はここで拒否されます。
この拒否だけでは既存 record / tombstone がないことを証明できず、Refused は Unknown のままです。

後日正規 release を構成した場合でも、launch は元 TLS worker の元 handle だけです。
固定 bundle identifier と Contents/MacOS の配置を確認し、
Bundle.main の Contents/Helpers/resident-pipe だけを選びます。argv は空、environment は空、
cwd は /、stderr は null device、stdin/stdout は既存 owner の同じ FileHandle です。
任意 path / argv / env / fd / requirement を渡す公開入力はありません。
固定 path と bundle metadata は署名の証拠ではありません。
元 Process に保持された typed launchRequirement が実行時 enforcement を担います。

## 元所有者と失敗

preflightで起動未着手の場合は元 prepared owner を維持し、元 IO の退役が確認されるまで
NotSpawned と扱いません。runを試みた後の error または IO handoff失敗は active Unknown とし、
元 Process / IO / slot を保持します。未成功 run から !isRunning を根拠に Wait を作りません。
replacement、再launch、kill、fake cleanupはありません。
成功した run のみ running へ進み、元 child側 descriptor の close receipt を確認して
既存 active pump / exact Wait / raw EOF / IO receipt / worker join / outer join へ渡します。
既存 terminal tombstone と Unknown履歴の保持は変更しません。

## 検証の境界

synthetic requirement のfixtureは本物releaseではなく、production launchを許しません。
launch-refusal fixtureはその拒否が元 unlaunched Process / IO を変えず、元退役だけが成立することを確認します。
既存の有限self-child IO fixtureは同じ startConfiguredProcess の run→running→IO handoff を使います。
これは同じ機構の回帰であり、署名済みproducer、nested bundle配置、実OS署名拒否の実証ではありません。
fixture固有の path/argvはtest compilation内の別untrusted ownerに限られ、production ownerへ渡せません。

正規 identity、署名/notarization、同梱artifact、実enrollment、実dot、配布、製品全体のQuitは未受入です。
