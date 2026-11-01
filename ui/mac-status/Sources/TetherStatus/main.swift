import SwiftUI
import AppKit
import StatusCore
import StatusUI

@MainActor final class Model: ObservableObject {
    static let shared = Model()
    @Published var snapshot = Snapshot()
    @Published var now = FeedClock.now()
    let fixture: Bool
    private let reader = PipeReader()
    private var timer: Timer?
    init() {
        fixture = Array(CommandLine.arguments.dropFirst()) == ["--fixture"] || Array(CommandLine.arguments.dropFirst()) == ["--check-own-window-fixture"]
        if fixture && now.isFinite && now >= 0 {
            var state = FeedState()
            let json = #"{"version":1,"source_continuous_ms":0,"seq":1,"phase":"connected","reason":"none","grant_remaining_ms":118000,"starting_workers":0,"running_workers":1,"stopping_workers":0,"cleanup":"pending","terminal_cleanup_unknown":false,"terminal":false}"#
            let stamped=json.replacingOccurrences(of:"\"source_continuous_ms\":0",with:"\"source_continuous_ms\":\(UInt64(now*1000))")
            state.ingest(Data(stamped.utf8),at:now); snapshot=state.snapshot
        } else if !fixture { reader.startStdin() }
        timer=Timer.scheduledTimer(withTimeInterval:0.25,repeats:true) { [weak self] _ in
            Task { @MainActor in
                guard let self else { return }; self.now=FeedClock.now()
                if !self.fixture { self.snapshot=self.reader.snapshot() }
            }
        }
    }
    func stop() -> Bool { ViewerShutdown.attempt(join:{ reader.stopAndJoin() },stopUpdates:{ timer?.invalidate(); timer=nil }) }
}
enum RenderFailure: Error { case image }
@MainActor func renderFixtures() throws {
    _ = NSApplication.shared
    let output = URL(fileURLWithPath:FileManager.default.currentDirectoryPath).appendingPathComponent(".build/fixture-images")
    try FileManager.default.createDirectory(at:output,withIntermediateDirectories:true)
    let scenarios:[(String,String,Double,Double,ColorScheme,ContentSizeCategory)] = [
        ("connected-light","connected",560,620,.light,.large),
        ("connected-dark","connected",560,620,.dark,.large),
        ("cleanup-unknown","unknown",560,700,.light,.large),
        ("cleanup-unknown-dark","unknown",560,700,.dark,.large),
        ("terminal-closed","closed",560,700,.light,.large),
        ("not-started","not-started",560,700,.light,.large),
        ("stale-narrow","stale",360,760,.light,.large),
        ("large-text","connected",560,900,.light,.accessibilityExtraExtraExtraLarge),
        ("setup-config","checking_config",560,780,.light,.large),
        ("setup-key","loading_key",560,780,.light,.large),
        ("setup-preparing","preparing",560,780,.light,.large),
        ("setup-approval","awaiting_approval",560,780,.light,.large),
        ("setup-connecting","connecting",560,780,.dark,.large),
        ("setup-config-error","config_error",560,780,.light,.large),
        ("setup-key-error","key_error",560,780,.light,.large),
        ("setup-preparation-error","preparation_error",560,780,.light,.large),
        ("setup-connection-error","connection_error",560,780,.light,.large),
        ("setup-approval-incomplete","approval_not_completed",560,780,.light,.large),
        ("setup-stale","connecting-stale",360,840,.light,.large),
        ("setup-eof","connecting-eof",560,780,.light,.large)]
    for (name,kind,width,height,scheme,size) in scenarios {
        var state=FeedState()
        let unknown=kind == "unknown", closed=kind == "closed", notStarted=kind == "not-started"
        var object:[String:Any] = ["version":1,"source_continuous_ms":100000,"seq":1,"phase":unknown || notStarted ? "failed" : (closed ? "closed" : "connected"),"reason":unknown ? "terminal_cleanup_unknown" : (closed ? "deadline" : "none"),"grant_remaining_ms":unknown || closed || notStarted ? 0 : 118000,"starting_workers":0,"running_workers":unknown || closed || notStarted ? 0 : 1,"stopping_workers":0,"cleanup":unknown ? "unknown" : (notStarted ? "not_started" : (closed ? "confirmed" : "pending")),"terminal_cleanup_unknown":unknown,"terminal":unknown || closed || notStarted]
        if ["checking_config","loading_key","preparing","awaiting_approval","connecting","connecting-stale","connecting-eof"].contains(kind) {
            object["phase"] = kind.hasPrefix("connecting-") ? "connecting" : kind
            object["running_workers"] = 0
            object["grant_remaining_ms"] = kind.hasPrefix("connecting") ? 118000 : NSNull()
            object["cleanup"] = "not_started"
        } else if ["config_error","key_error","preparation_error","connection_error","approval_not_completed"].contains(kind) {
            object["phase"]="failed";object["reason"]=kind;object["terminal"]=true
            object["running_workers"]=0;object["grant_remaining_ms"]=NSNull();object["cleanup"]="not_started"
        }
        state.ingest(try JSONSerialization.data(withJSONObject:object),at:100)
        if closed || notStarted || kind == "connecting-eof" { state.fail(.eof) }
        let view=StatusView(snapshot:state.snapshot,now:kind == "stale" || kind == "connecting-stale" ? 104 : 100,fixture:true,renderStatic:true,textScale:name == "large-text" ? 1.5 : 1)
            .environment(\.sizeCategory,size).frame(width:width,height:height,alignment:.topLeading).background(Color(nsColor:.windowBackgroundColor)).environment(\.colorScheme,scheme)
        let renderer=ImageRenderer(content:view); renderer.scale=2
        if let image=renderer.nsImage,let data=image.tiffRepresentation,
           let bitmap=NSBitmapImageRep(data:data),let png=bitmap.representation(using:.png,properties:[:]) {
            guard png.count > 1000 else { throw RenderFailure.image }
            try png.write(to:output.appendingPathComponent(name+".png"))
        } else { throw RenderFailure.image }
    }
}
@MainActor final class AppDelegate: NSObject, NSApplicationDelegate {
    private var checkPassed = false
    private var checkClosing = false
    private var checkMode: Bool {
        let args=Array(CommandLine.arguments.dropFirst())
        return args == ["--check-own-window-no-input"] || args == ["--check-own-window-fixture"]
    }
    func applicationDidFinishLaunching(_ notification:Notification) {
        guard checkMode else { return }
        DispatchQueue.main.asyncAfter(deadline:.now()+3.6) { [self] in
            let model=Model.shared
            let windows=NSApplication.shared.windows.filter { $0.isVisible }
            let fixture=Array(CommandLine.arguments.dropFirst()) == ["--check-own-window-fixture"]
            let stateOK=fixture
                ? model.fixture && model.snapshot.frame?.phase == "connected" && !model.snapshot.fresh(at:FeedClock.now())
                : !model.fixture && model.snapshot.fault == .unavailable
            checkPassed = !windows.isEmpty && stateOK
            checkClosing = true
            for window in windows { window.performClose(nil) }
            if windows.isEmpty { NSApplication.shared.terminate(nil) }
        }
        DispatchQueue.main.asyncAfter(deadline:.now()+8) { [self] in
            checkPassed=false; checkClosing=true
            print("OWN_BUNDLE_WINDOW_TIMEOUT")
            NSApplication.shared.terminate(nil)
            // If join refuses termination, preserve ownership; never force exit/close.
        }
    }
    func applicationWillTerminate(_ notification:Notification) {
        if checkMode {
            print(checkPassed && checkClosing ? "OWN_BUNDLE_WINDOW_PASS" : "OWN_BUNDLE_WINDOW_FAIL")
            if !checkPassed || !checkClosing { exit(1) }
        }
    }
    func applicationShouldTerminateAfterLastWindowClosed(_ sender:NSApplication) -> Bool { true }
    func applicationShouldTerminate(_ sender:NSApplication) -> NSApplication.TerminateReply {
        Model.shared.stop() ? .terminateNow : .terminateCancel
    }
}
@main struct TetherStatusApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    init() {
        let args=Array(CommandLine.arguments.dropFirst())
        guard args.isEmpty || args == ["--fixture"] || args == ["--render-fixtures"] || args == ["--check-own-window-no-input"] || args == ["--check-own-window-fixture"] else { exit(64) }
        if args == ["--render-fixtures"] {
            do { try renderFixtures(); exit(0) } catch { exit(1) }
        }
    }
    @StateObject private var model = Model.shared
    @State private var textScale: CGFloat = 1
    var body: some Scene {
        WindowGroup("Tether — この Mac の状態") {
            StatusView(snapshot:model.snapshot,now:model.now,fixture:model.fixture,textScale:textScale)
        }.defaultSize(width:560,height:600)
        .commands {
            CommandGroup(replacing:.newItem) {}
            CommandMenu("表示") {
                Button("文字を大きく") { textScale=min(1.6,textScale+0.2) }.keyboardShortcut("+",modifiers:.command)
                Button("文字を小さく") { textScale=max(1,textScale-0.2) }.keyboardShortcut("-",modifiers:.command)
            }
        }
    }
}
