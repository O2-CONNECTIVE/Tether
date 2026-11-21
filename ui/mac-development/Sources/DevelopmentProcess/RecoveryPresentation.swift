import Foundation

// Values rendered directly by AppKit; no effects or alternate cleanup proof.
public struct RecoveryPresentation {
    public let status, detail, prepareTitle, quitTitle, diagnostic, run: String
    public let canPrepare, canOn, canOff, canExtend, canAllow, canDeny, canStop: Bool
}

extension RecoveryController {
    public func presentation(at now: UInt64) -> RecoveryPresentation {
        let f = feed
        var status: String, detail: String
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
            status = "停止・回収を確認しました"
            detail = "「もう一度準備」で新しい開発ホストを準備できます。待機を ON にするには、あらためて操作してください。"
        } else if f.fault || f.closing {
            status = f.fault ? "操作受付を停止し、回収を確認中" : "停止と回収を確認中"
            detail = quitRequested ? "回収を確認したらアプリを終了します。新しい準備は行いません。" : "元のホストの終了を待っています。回収を確認したら「もう一度準備」を選べます。"
        } else if !f.fresh(at: now) && launched {
            status = "最新の状態を確認中"
            detail = "新しい状態を受け取るまで ON/OFF は操作できません。"
        } else if let o = f.observation {
            let connection: String
            switch o.registration {
            case "authenticated": connection = "認証済み"
            case "reconnecting": connection = "再接続中"
            default: connection = "未確認"
            }
            if let pending = f.pending {
                status = o.standby == "unknown" && pending.intent == "on" ? "接続を確認中・待機 ON は未確認" : "待機を \(pending.intent == "on" ? "ON" : "OFF") に変更中"
            } else if o.standby == "stopping" {
                status = "待機を停止中"
            } else if o.standby == "on" {
                switch o.registration {
                case "authenticated": status = "待機中"
                case "reconnecting": status = "待機 ON・再接続中"
                case "needs_action": status = "待機 ON・要対応"
                default: status = "待機 ON・接続を確認中"
                }
                if let until = o.standbyUntil {
                    let clock = Self.clock(until)
                    status = o.standbyEndingSoon
                        ? "まもなく期限（\(clock)まで）。延長するか作業を終えてください"
                        : status + "（\(clock)まで）"
                }
            } else if o.standby == "off" && o.work == "idle" && o.cleanup == "confirmed" {
                status = "待機は OFF"
            } else {
                status = "停止と回収を確認中"
            }
            if f.pending != nil {
                detail = "状態変更の結果を確認しています。完了するまで新しい要求はできません。"
            } else {
                switch o.registrationReason {
                case .some("identity_refused"):
                    detail = "このホストの身元が拒否されました。別の端末に置き換えられた可能性があります。もう一度ペア登録してから、待機を OFF にして ON にし直してください。"
                case .some("connection_occupied"):
                    detail = "別の接続がこのホストを使っています。その接続を終えてから、待機を OFF にして ON にし直してください。"
                case .some("gateway_refused"):
                    detail = "gateway が接続を拒否しました。設定を確認してから、待機を OFF にして ON にし直してください。"
                default:
                    detail = "接続: \(connection) ／ 操作: \(o.work == "idle" ? "なし" : "処理中") ／ 回収: \(o.cleanup == "confirmed" ? "確認済み" : "確認中")"
                }
            }
            if f.pending == nil, o.standby == "off", o.work == "idle", o.cleanup == "confirmed", o.standbyExpired {
                status = "期限で停止しました"
                detail = "待機の期限が来たため、待機を OFF にして停止・回収しました。続けるには待機を ON にし、あらためてコマンド実行を許可してください。"
            }
        } else {
            status = "まだ起動していません"
            detail = "専用の開発用設定を用意した後に開始してください。"
        }
        let diagnosticText = (launched || launchFailed ? "現在: " + diagnostic : "")
            + (previousDiagnostic.map { "\n前回: " + $0 } ?? "")
        let run = f.runAllowed
            ? "コマンド実行: 許可中（待機を OFF にするまで）"
            : "コマンド実行: 許可なし（待機 ON だけでは実行されません）"
        return RecoveryPresentation(
            status: status, detail: detail,
            prepareTitle: launched || launchFailed ? "もう一度準備" : "開発ホストを準備",
            quitTitle: mayQuit ? "終了" : "停止して終了", diagnostic: diagnosticText,
            run: run,
            canPrepare: preparation != .blocked,
            canOn: !quitRequested && f.can("on", at: now), canOff: !quitRequested && f.can("off", at: now),
            canExtend: !quitRequested && f.can("extend", at: now), canAllow: !quitRequested && f.can("allow", at: now), canDeny: !quitRequested && f.can("deny", at: now),
            canStop: launched && !quitRequested && !f.closing && !f.closed)
    }
    // With the date: a 24-hour end would otherwise read as the current time.
    private static func clock(_ ms: UInt64) -> String {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX"); formatter.dateFormat = "M/d HH:mm"
        return formatter.string(from: Date(timeIntervalSince1970: Double(ms) / 1000))
    }
}
