import AppKit
import SwiftUI
import Foundation
import StatusCore
import StatusUI
import Darwin

@MainActor final class WindowState: ObservableObject {
    @Published var snapshot=Snapshot()
    @Published var now=FeedClock.now()
}
struct FixtureView: View {
    @ObservedObject var state:WindowState
    var body: some View { StatusView(snapshot:state.snapshot,now:state.now,fixture:true) }
}
@MainActor final class WindowFixture: NSObject, NSWindowDelegate {
    let state=WindowState()
    let reader=PipeReader()
    var window:NSWindow!
    var writer:Int32 = -1
    var saved:Int32 = -1
    var failures=0
    var checks=0
    var timer:Timer?
    func verify(_ condition:Bool) { checks += 1; if !condition { failures += 1 } }
    func begin() {
        var fds:[Int32]=[0,0]; guard pipe(&fds)==0 else { exit(1) }
        saved=dup(STDIN_FILENO); guard dup2(fds[0],STDIN_FILENO)==STDIN_FILENO else { exit(1) }
        close(fds[0]); writer=fds[1]
        reader.startStdin()
        window=NSWindow(contentRect:NSRect(x:0,y:0,width:560,height:640),styleMask:[.titled,.closable,.resizable],backing:.buffered,defer:false)
        window.title="Tether — 表示テスト・実接続なし"
        window.isReleasedWhenClosed=false; window.delegate=self
        window.contentView=NSHostingView(rootView:FixtureView(state:state))
        window.center(); window.makeKeyAndOrderFront(nil)
        verify(window.isVisible)
        timer=Timer.scheduledTimer(withTimeInterval:0.05,repeats:true) { [weak self] _ in
            Task { @MainActor in guard let self else { return }; self.state.snapshot=self.reader.snapshot(); self.state.now=FeedClock.now() }
        }
        let obj:[String:Any]=["version":1,"source_continuous_ms":UInt64(FeedClock.now()*1000),"seq":1,"phase":"connected","reason":"none","grant_remaining_ms":30000,"starting_workers":0,"running_workers":1,"stopping_workers":0,"cleanup":"pending","terminal_cleanup_unknown":false,"terminal":false]
        let line=(try! JSONSerialization.data(withJSONObject:obj))+Data([10])
        _=line.withUnsafeBytes { write(writer,$0.baseAddress,line.count) }
        DispatchQueue.main.asyncAfter(deadline:.now()+0.5) { [self] in
            verify(reader.snapshot().fresh(at:FeedClock.now()))
            verify(reader.snapshot().frame?.phase == "connected")
        }
        DispatchQueue.main.asyncAfter(deadline:.now()+3.5) { [self] in
            verify(!reader.snapshot().fresh(at:FeedClock.now()))
            verify(reader.snapshot().remaining(at:FeedClock.now()) == nil)
            let malformed=Data("{not-json}\n".utf8)
            _=malformed.withUnsafeBytes { write(writer,$0.baseAddress,malformed.count) }
        }
        DispatchQueue.main.asyncAfter(deadline:.now()+3.9) { [self] in
            verify(reader.snapshot().fault == .malformed)
            window.performClose(nil)
        }
        DispatchQueue.main.asyncAfter(deadline:.now()+6) { [self] in
            failures += 1; finish()
        }
    }
    func windowWillClose(_ notification:Notification) { finish() }
    func finish() {
        timer?.invalidate(); timer=nil
        verify(reader.stopAndJoin())
        verify(fcntl(STDIN_FILENO,F_GETFD) == -1)
        // Viewer never closes or signals the synthetic producer's write end.
        verify(fcntl(writer,F_GETFD) >= 0)
        close(writer); writer = -1
        if saved >= 0 { _=dup2(saved,STDIN_FILENO); close(saved) }
        print("OWN_WINDOW_CHECKS",checks,"FAILURES",failures)
        exit(failures == 0 ? 0 : 1)
    }
}
@main struct CheckWindow {
    @MainActor static func main() {
        guard CommandLine.arguments.count == 1 else { exit(64) }
        let app=NSApplication.shared; app.setActivationPolicy(.accessory)
        let fixture=WindowFixture(); fixture.begin(); app.run()
    }
}
