import AppKit
import DevelopmentCore
import DevelopmentProcess
import Darwin

final class App: NSObject, NSApplicationDelegate, NSWindowDelegate {
    private let resident = ResidentProcess()
    private var window: NSWindow!
    private var timer: Timer?
    private let status = NSTextField(wrappingLabelWithString: "まだ起動していません")
    private let detail = NSTextField(wrappingLabelWithString: "専用の開発用設定を用意した後に開始してください。")
    private var start: NSButton!, on: NSButton!, off: NSButton!, stop: NSButton!
    func applicationDidFinishLaunching(_ notification: Notification) {
        signal(SIGPIPE, SIG_IGN)
        NSApp.setActivationPolicy(.regular)
        window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 620, height: 460), styleMask: [.titled, .closable, .miniaturizable], backing: .buffered, defer: false)
        window.title = "Tether Development · 開発専用"; window.delegate = self
        window.isReleasedWhenClosed = false
        let stack = NSStackView(); stack.orientation = .vertical; stack.alignment = .leading; stack.spacing = 18
        stack.edgeInsets = NSEdgeInsets(top: 28, left: 28, bottom: 28, right: 28)
        let badge = NSTextField(labelWithString: "開発専用・Developer ID 未検証")
        badge.font = .boldSystemFont(ofSize: 15); badge.textColor = .systemOrange
        status.font = .boldSystemFont(ofSize: 25)
        let warning = NSTextField(wrappingLabelWithString: "通常製品とは別のローカル開発アプリです。現在のユーザー権限で固定の検証操作だけを受け付けます。署名・公開・認証の保証を追加しません。")
        let limit = NSTextField(wrappingLabelWithString: "起動から15分で終了を要求します。再接続でも延長しません。永続運転・ログイン起動ではありません。サーバー側の期限設定が別途必要です。")
        limit.textColor = .secondaryLabelColor
        start = button("開発ホストを準備", #selector(prepare))
        on = button("待機を ON", #selector(enable)); off = button("待機を OFF", #selector(disable))
        stop = button("停止して終了", #selector(stopAndQuit))
        let row = NSStackView(views: [start, on, off]); row.spacing = 10
        for view in [badge, status, detail, warning, limit, row, stop!] { stack.addArrangedSubview(view) }
        window.contentView = stack
        let menu = NSMenu(); let item = NSMenuItem(); menu.addItem(item)
        item.submenu = NSMenu(); item.submenu?.addItem(withTitle: "停止して終了", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        NSApp.mainMenu = menu
        timer = Timer.scheduledTimer(withTimeInterval: 0.05, repeats: true) { [weak self] _ in self?.update() }
        window.center(); window.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true)
        update()
    }
    private func button(_ title: String, _ action: Selector) -> NSButton {
        NSButton(title: title, target: self, action: action)
    }
    @objc private func prepare() { resident.start(); update() }
    @objc private func enable() { resident.intent("on"); update() }
    @objc private func disable() { resident.intent("off"); update() }
    @objc private func stopAndQuit() { NSApp.terminate(nil) }
    private var quitRequested = false
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        if resident.mayQuit { timer?.invalidate(); return .terminateNow }
        quitRequested = true; resident.close(); update(); return .terminateCancel
    }
    func windowShouldClose(_ sender: NSWindow) -> Bool { NSApp.terminate(nil); return false }
    private func update() {
        resident.poll()
        let f = resident.feed, now = ResidentProcess.now()
        start.isEnabled = !resident.launched && !resident.launchFailed
        on.isEnabled = f.can("on", at: now); off.isEnabled = f.can("off", at: now)
        if resident.launchFailed {
            status.stringValue = "起動できませんでした"
            detail.stringValue = "同梱ファイルと開発用ビルドを確認してください。接続は開始していません。"
        } else if resident.mayQuit && resident.launched {
            status.stringValue = f.fault ? "操作を停止し、元のホストの回収を確認しました" : "停止・回収を確認しました"
            detail.stringValue = "終了報告・出力終了・元プロセスの正常終了と回収を確認しました。「停止して終了」で閉じられます。再接続は行いません。\n" + resident.diagnostic
        } else if f.fault {
            status.stringValue = "停止・回収を確認できません"
            detail.stringValue = "操作は停止しました。元のホストの回収証拠が不足しているため、この画面を保持します。終了の連打・再接続では復旧しません。次の診断情報を確認してください。\n" + resident.diagnostic
        } else if f.closing {
            status.stringValue = "停止と回収を確認中"
            detail.stringValue = "完了するまで、この画面と元のホストを保持します。"
        } else if !f.fresh(at: now) && resident.launched {
            status.stringValue = "最新の状態を確認中"
            detail.stringValue = "新しい状態を受け取るまで ON/OFF は操作できません。"
        } else if let o = f.observation {
            let connected = o.registration == "authenticated"
            if o.standby == "unknown" && f.pending?.intent == "on" {
                status.stringValue = "接続を確認中・待機 ON は未確認"
            } else {
                status.stringValue = o.standby == "on" ? (connected ? "開発ホストは待機中" : "待機 ON・接続を確認中") : "待機は OFF"
            }
            detail.stringValue = f.pending != nil ? "要求を受け付けました。実際の状態変更を確認しています。" : "接続: \(connected ? "認証済み" : "未確認") ／ 操作: \(o.work == "idle" ? "なし" : "処理中") ／ 回収: \(o.cleanup == "confirmed" ? "確認済み" : "確認中")"
        }
        if quitRequested && resident.mayQuit { NSApp.terminate(nil) }
    }
}
let app = NSApplication.shared
let delegate = App(); app.delegate = delegate
app.run()
