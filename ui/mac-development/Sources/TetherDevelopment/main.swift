import AppKit
import DevelopmentCore
import DevelopmentProcess
import Darwin

final class App: NSObject, NSApplicationDelegate, NSWindowDelegate {
    private let resident = RecoveryController()
    private var window: NSWindow!
    private var timer: Timer?
    private let status = NSTextField(wrappingLabelWithString: "まだ起動していません")
    private let detail = NSTextField(wrappingLabelWithString: "専用の開発用設定を用意した後に開始してください。")
    private let diagnostic = NSTextField(wrappingLabelWithString: "")
    private var start: NSButton!, on: NSButton!, off: NSButton!, stop: NSButton!, quit: NSButton!
    func applicationDidFinishLaunching(_ notification: Notification) {
        signal(SIGPIPE, SIG_IGN)
        NSApp.setActivationPolicy(.regular)
        window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 620, height: 560), styleMask: [.titled, .closable, .miniaturizable, .resizable], backing: .buffered, defer: false)
        window.title = "Tether Development · 開発専用"; window.delegate = self
        window.isReleasedWhenClosed = false
        window.contentMinSize = NSSize(width: 620, height: 560)
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
        stop = button("ホストを停止", #selector(stopHost))
        quit = button("終了", #selector(stopAndQuit))
        let row = NSStackView(views: [start, on, off]); row.spacing = 10
        let endRow = NSStackView(views: [stop!, quit!]); endRow.spacing = 10
        diagnostic.font = .monospacedSystemFont(ofSize: 11, weight: .regular)
        diagnostic.textColor = .secondaryLabelColor; diagnostic.isSelectable = true
        for view in [badge, status, detail, warning, limit, row, endRow, diagnostic] { stack.addArrangedSubview(view) }
        for label in [status, detail, warning, limit, diagnostic] {
            label.widthAnchor.constraint(equalTo: stack.widthAnchor, constant: -56).isActive = true
        }
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
    @objc private func prepare() { resident.prepare(); update() }
    @objc private func enable() { resident.intent("on"); update() }
    @objc private func disable() { resident.intent("off"); update() }
    @objc private func stopHost() { resident.close(); update() }
    @objc private func stopAndQuit() { NSApp.terminate(nil) }
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        resident.requestQuit()
        if resident.shouldTerminate { timer?.invalidate(); return .terminateNow }
        // The timer renders the pending request. Do not recursively terminate
        // from inside this delegate callback when proof arrives during a poll.
        return .terminateCancel
    }
    func windowShouldClose(_ sender: NSWindow) -> Bool { NSApp.terminate(nil); return false }
    private func update() {
        resident.poll()
        let state = resident.presentation(at: ResidentProcess.now())
        start.title = state.prepareTitle; start.isEnabled = state.canPrepare
        on.isEnabled = state.canOn; off.isEnabled = state.canOff
        stop.isEnabled = state.canStop; quit.title = state.quitTitle
        status.stringValue = state.status; detail.stringValue = state.detail
        diagnostic.stringValue = state.diagnostic; diagnostic.isHidden = state.diagnostic.isEmpty
        if resident.shouldTerminate { NSApp.terminate(nil) }
    }
}
let app = NSApplication.shared
let delegate = App(); app.delegate = delegate
app.run()
