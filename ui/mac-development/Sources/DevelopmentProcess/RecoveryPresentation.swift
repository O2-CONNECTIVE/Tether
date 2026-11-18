// Values rendered directly by AppKit; no effects or alternate cleanup proof.
public struct RecoveryPresentation {
    public let status, detail, prepareTitle, quitTitle, diagnostic, run: String
    public let canPrepare, canOn, canOff, canAllow, canDeny, canStop: Bool
}

extension RecoveryController {
    public func presentation(at now: UInt64) -> RecoveryPresentation {
        let f = feed
        let status: String, detail: String
        if f.cleanupInvalid {
            status = "回収未確認・再準備できません"
            // Quit and Cmd-Q are refused here; say so instead of an unchanged screen.
            detail = (quitRequested ? "回収を確認できないため終了できません。閉じるボタンではウィンドウだけを閉じ、アプリは残ります（Dock から開き直せます）。" : "")
                + "新しい操作は受け付けません。元のホストの回収を確認できないため、状態を保持します。下の診断情報を確認してください。終了の連打や再準備では解消できません。"
        } else if shouldTerminate {
            status = "終了します"
            detail = launched ? "元のホストの停止・回収を確認しました。" : "ホストは起動していません。"
        } else if launchFailed {
            status = "起動できませんでした"
            detail = "接続は開始していません。同梱ファイルと開発用ビルドを確認してから、「もう一度準備」を押してください。"
        } else if mayQuit && launched {
            // The helper's fixed 15-minute lifetime (developmentLifetime in
            // backend/internal/residentpipe) starts after launchedAt, so an
            // unrequested close received at least that long after it is the expiry.
            let expired = f.unrequestedCloseAt.flatMap { at in launchedAt.map { at >= $0 && at - $0 >= 900_000 } } == true
            status = expired ? "15分の期限で停止しました" : "停止・回収を確認しました"
            detail = (expired
                ? "開発ホストは起動から15分で終了する限定運転のため停止しました。停止・回収を確認しました。続けるには「もう一度準備」を押してください。"
                : "「もう一度準備」で新しい開発ホストを準備できます。") + "待機を ON にするには、あらためて操作してください。"
        } else if f.fault || f.closing {
            status = f.fault ? "操作受付を停止し、回収を確認中" : "停止と回収を確認中"
            detail = quitRequested ? "回収を確認したらアプリを終了します。新しい準備は行いません。" : "元のホストの終了を待っています。回収を確認したら「もう一度準備」を選べます。"
        } else if !f.fresh(at: now) && launched {
            status = "最新の状態を確認中"
            detail = "新しい状態を受け取るまで ON/OFF は操作できません。"
        } else if let o = f.observation {
            let connected = o.registration == "authenticated"
            if let pending = f.pending {
                status = o.standby == "unknown" && pending.intent == "on" ? "接続を確認中・待機 ON は未確認" : "待機を \(pending.intent == "on" ? "ON" : "OFF") に変更中"
            } else if o.standby == "stopping" {
                status = "待機を停止中"
            } else if o.standby == "on" {
                status = connected ? "開発ホストは待機中" : "待機 ON・接続を確認中"
            } else if o.standby == "off" && o.work == "idle" && o.cleanup == "confirmed" {
                status = "待機は OFF"
            } else {
                status = "停止と回収を確認中"
            }
            detail = f.pending != nil ? "状態変更の結果を確認しています。完了するまで新しい要求はできません。" : "接続: \(connected ? "認証済み" : "未確認") ／ 操作: \(o.work == "idle" ? "なし" : "処理中") ／ 回収: \(o.cleanup == "confirmed" ? "確認済み" : "確認中")"
        } else {
            status = "まだ起動していません"
            detail = "専用の開発用設定を用意した後に開始してください。"
        }
        let diagnosticText = (launched || launchFailed ? "現在: " + diagnostic : "")
            + (previousDiagnostic.map { "\n前回: " + $0 } ?? "")
        let run: String
        if f.runAllowed(at: now), let until = f.runAllowedUntil {
            let remaining = until - now
            let minutes = remaining / 60_000 + (remaining % 60_000 == 0 ? 0 : 1)
            run = "コマンド実行: 許可中（残り約\(minutes)分）"
        } else {
            run = "コマンド実行: 許可なし（待機 ON だけでは実行されません）"
        }
        return RecoveryPresentation(
            status: status, detail: detail,
            prepareTitle: launched || launchFailed ? "もう一度準備" : "開発ホストを準備",
            quitTitle: mayQuit ? "終了" : "停止して終了", diagnostic: diagnosticText,
            run: run,
            canPrepare: preparation != .blocked,
            canOn: !quitRequested && f.can("on", at: now), canOff: !quitRequested && f.can("off", at: now),
            canAllow: !quitRequested && f.can("allow", at: now), canDeny: !quitRequested && f.can("deny", at: now),
            canStop: launched && !quitRequested && !f.closing && !f.closed)
    }
}
