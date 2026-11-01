# Tether: Mac / MCP 開発チェックポイント（2026-11-01）

この WIP は Tether の AI command と一時 agent 派遣へ方針転換したローカル成果の保存です。main 統合、配布可、本番提供を宣言しません。[現在の検証状態](tether-pilot-status.md)と[文書索引](README.md)を正本とします。

## 含むもの

- 共通の承認・期限・失効・所有回収、Mac current-user runtime、認証済み channel と MCP / HTTP gateway。
- ローカル CLI の fresh 同意、保護設定・鍵 loader、静的 discovery、Cloudflare の閉鎖既定の source / synthetic 設定。
- Mac の読取専用 SwiftUI viewer とローカル評価 .app の包装 source。ビルド済み .app や実行ファイルは含めません。
- 既承認の Windows helper / supplier / service provision の途中 source と回帰。Mac の実行前提とはせず、Windows 実機完成を示しません。
- 検証・ライセンス inventory helper、評価 package source、現在と履歴を分けた文書。

人向け SSH の途中版は別の `wip/ssh-owned-shell-20261031` に保存されています。今回それを書き換えたり、内側 SSH shell を Mac の必須依存へ戻したりしません。

## 証拠を混ぜない

| 範囲 | 証拠と限界 |
| --- | --- |
| Native | 固定 binary に対する1名・1 Macの正常、取消、要求なし期限切れ、実行中通信断の4 case。後続 build に自動転用しない |
| Claude Code | ローカル stdio fixture と実 HTTP/OAuth 接続・4 tool discovery。実 command は未試験 |
| SwiftUI | source の実 status producer 配線、build、合成 pipe / window、評価 .app の自己 window 終了。実 agent + GUI は未試験 |
| dot | workspace の MCP plugin 作成が403。管理者の利用許可変更待ちで、別 client の成功では解消しない |
| 配布 | agent build の最低 OS 宣言26.0、viewer13.0を分離。依存ライセンス閉包・署名・公証・一般受領者の受入は未完 |

実試験の入口と専用 gateway は閉鎖・停止済みです。この source を取得しても公開経路・新しい認証・host grant は作られません。4名向け policy は4名の backend identity / 鍵登録完了ではありません。

Mac は現在ユーザー権限で動き、sandbox ではありません。取消は既実行の副作用を戻さず、群を離れた子孫の停止を保証しません。cleanup unknown は非成功で、過去の unknown を後の成功で書き換えません。

## 公開対象と検証

実秘密鍵、token、個人の identity / email、実配置設定、生成物、認証・実行ログ、cache、試験 artifact はこのチェックポイントへ含めません。合成 test fixture の固定値は実環境の credential ではありません。

Go runtime source は既存最終 manifest と照合し、適用する検証結果を source 範囲ごとに記録します。GUI/package と文書はそれぞれ独立した build / fixture / link 検査の証拠であり、全実機成功へ拡張しません。既存 workflow は main / main 向け PR のみを対象とし、この WIP push の CI 未起動を合格とは呼びません。
