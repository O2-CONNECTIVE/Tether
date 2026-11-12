import AppKit
import DevelopmentCore
import DevelopmentProcess
import Darwin

final class App: NSObject, NSApplicationDelegate, NSWindowDelegate {
    private let resident = RecoveryController()
    private var window: NSWindow!
    private var timer: Timer?
    private var lastHistoryRead: UInt64 = 0
    private var historySize = 0, historyShown = "", historyKept = ""
    private let status = NSTextField(wrappingLabelWithString: "まだ起動していません")
    private let detail = NSTextField(wrappingLabelWithString: "専用の開発用設定を用意した後に開始してください。")
    private let diagnostic = NSTextField(wrappingLabelWithString: "")
    private let runState = NSTextField(wrappingLabelWithString: "コマンド実行: 許可なし（待機 ON だけでは実行されません）")
    private let history = NSTextView()
    private var start: NSButton!, on: NSButton!, off: NSButton!, allow: NSButton!, deny: NSButton!, stop: NSButton!, quit: NSButton!
    func applicationDidFinishLaunching(_ notification: Notification) {
        signal(SIGPIPE, SIG_IGN)
        NSApp.setActivationPolicy(.regular)
        window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 760, height: 760), styleMask: [.titled, .closable, .miniaturizable, .resizable], backing: .buffered, defer: false)
        window.title = "Tether Development · 開発専用"; window.delegate = self
        window.isReleasedWhenClosed = false
        window.contentMinSize = NSSize(width: 620, height: 560)
        let stack = NSStackView(); stack.orientation = .vertical; stack.alignment = .leading; stack.spacing = 18
        stack.edgeInsets = NSEdgeInsets(top: 28, left: 28, bottom: 28, right: 28)
        let badge = NSTextField(labelWithString: "開発専用・Developer ID 未検証")
        badge.font = .boldSystemFont(ofSize: 15); badge.textColor = .systemOrange
        status.font = .boldSystemFont(ofSize: 25)
        let warning = NSTextField(wrappingLabelWithString: "通常製品とは別のローカル開発アプリです。待機 ON だけでは固定の検証操作だけを受け付けます。コマンド実行は、下のボタンで許可している間だけ通ります。署名・公開・認証の保証を追加しません。")
        let limit = NSTextField(wrappingLabelWithString: "起動から15分で終了を要求します。再接続でも延長しません。永続運転・ログイン起動ではありません。サーバー側の期限設定が別途必要です。")
        limit.textColor = .secondaryLabelColor
        start = button("開発ホストを準備", #selector(prepare))
        on = button("待機を ON", #selector(enable)); off = button("待機を OFF", #selector(disable))
        allow = button("コマンド実行を15分許可", #selector(allowRun)); deny = button("許可を取り消す", #selector(denyRun))
        stop = button("ホストを停止", #selector(stopHost))
        quit = button("終了", #selector(stopAndQuit))
        let row = NSStackView(views: [start, on, off]); row.spacing = 10
        let runRow = NSStackView(views: [allow!, deny!]); runRow.spacing = 10
        let endRow = NSStackView(views: [stop!, quit!]); endRow.spacing = 10
        diagnostic.font = .monospacedSystemFont(ofSize: 11, weight: .regular)
        diagnostic.textColor = .secondaryLabelColor; diagnostic.isSelectable = true
        runState.font = .monospacedSystemFont(ofSize: 12, weight: .regular)
        history.isEditable = false; history.isSelectable = true
        history.font = .monospacedSystemFont(ofSize: 11, weight: .regular)
        history.textContainerInset = NSSize(width: 5, height: 5)
        let scroll = NSScrollView(); scroll.documentView = history; scroll.hasVerticalScroller = true
        scroll.heightAnchor.constraint(equalToConstant: 220).isActive = true
        for view in [badge, status, detail, warning, limit, row, runRow, runState, scroll, endRow, diagnostic] { stack.addArrangedSubview(view) }
        for label in [status, detail, warning, limit, runState, diagnostic] {
            label.widthAnchor.constraint(equalTo: stack.widthAnchor, constant: -56).isActive = true
        }
        scroll.widthAnchor.constraint(equalTo: stack.widthAnchor, constant: -56).isActive = true
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
    @objc private func allowRun() { resident.intent("allow"); update() }
    @objc private func denyRun() { resident.intent("deny"); update() }
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
        allow.isEnabled = state.canAllow; deny.isEnabled = state.canDeny; runState.stringValue = state.run
        let now = ResidentProcess.now()
        if now >= lastHistoryRead, now - lastHistoryRead >= 1000 {
            lastHistoryRead = now
            history.string = renderHistory()
        }
        diagnostic.stringValue = state.diagnostic; diagnostic.isHidden = state.diagnostic.isEmpty
        if resident.shouldTerminate { NSApp.terminate(nil) }
    }
    // The whole record is read: the host keeps it near 1 MiB, and one command
    // can be a 384 KiB line, so a tail read could drop the newest record.
    private func renderHistory() -> String {
        let url = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/com.o2connective.tether.development/run-history.jsonl")
        let data = (try? Data(contentsOf: url)) ?? Data()
        // A command runs as this user and can rewrite the record. A record that
        // got shorter is said so, and what was on screen before stays.
        if data.count < historySize { historyKept = "【記録が短くなりました。入れ替えか書き換えの可能性があります。直前の表示】\n" + historyShown + "\n\n" }
        historySize = data.count
        historyShown = RunHistory.text(data, limit: 5)
        return historyKept + historyShown
    }
}
let app = NSApplication.shared
let delegate = App(); app.delegate = delegate
app.run()
