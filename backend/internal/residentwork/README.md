# Resident work transcriptのsource境界

既存resident producer/policyの依存から切り出した、v1の具体的なbyte encodingです。
標準ライブラリだけを使い、ファイル・鍵・署名器・通信・grant・runtime・承認producerを持ちません。
通常CLIや公開backendから呼ぶ経路、認証/権限/常駐起動は追加していません。

`IntentTranscript`と`HostProofTranscript`は別のdomainで、bindingとnonce/policy digest、対象fieldを長さ区切り・固定byte順へ写します。
version、field長、canonical endpoint、非zeroで異なるnonce等を検査しますが、入力を信頼された設定へ変換しません。
署名の正当性、current expiry、policyの真正性、nonceの発行/単回性、認証済みpeer、実行許可は判定しません。
`Signature`は検証すべき外側の値で、署名対象自身へ含めません。正のexpiryをencodeできても現在有効という意味ではありません。
返したbytesは署名対象の形式であり、署名済み・認証済み・承認済みの証拠ではありません。

後続の具体的接続先はresident policyのbinding/scope検査、resident client/serverのwork intentとhost proofです。
それらの公開source閉包、既存保護loader、実認証・admission・cleanupとの結合は別段階です。
新しい汎用port、任意path/digestを信頼するfactory、第二のpolicy承認器は追加しません。

試験は固定された合成値とmemory上のbytesのみです。
golden digestはprotocol test vectorでありproducer binaryや端末identityのpinではありません。
鍵/credential/CA生成、file/policy/key読取、network/loopback、実producer/host/GUIは実行しません。
このpackageのcompile/race成功を、実行物の真正性や署名済み配布、実persistent accessの成立へ広げません。
