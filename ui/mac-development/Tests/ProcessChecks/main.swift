import Foundation
import CryptoKit
@testable import DevelopmentProcess
let manager = FileManager.default
let root = manager.temporaryDirectory.appendingPathComponent("tether-gui-fixture-\(UUID().uuidString)")
let macos = root.appendingPathComponent("Contents/MacOS")
let resources = root.appendingPathComponent("Contents/Resources")
try manager.createDirectory(at: macos, withIntermediateDirectories: true)
try manager.createDirectory(at: resources, withIntermediateDirectories: true)
let fixture = URL(fileURLWithPath: CommandLine.arguments[0]).deletingLastPathComponent().appendingPathComponent("ProcessFixture")
let helper = macos.appendingPathComponent("tether-dev-resident")
try manager.copyItem(at: fixture, to: helper)
let digest = SHA256.hash(data: try Data(contentsOf: helper)).map { String(format: "%02x", $0) }.joined()
try digest.write(to: resources.appendingPathComponent("resident.sha256"), atomically: true, encoding: .ascii)
let owner = ResidentProcess(fixtureBundle: root)
precondition(owner.mayQuit && !owner.launched)
owner.start(); owner.start()
precondition(owner.launched && !owner.mayQuit && !owner.launchFailed)
let deadline = Date().addingTimeInterval(10)
while owner.feed.observation == nil && Date() < deadline {
    owner.poll(); Thread.sleep(forTimeInterval: 0.01)
}
precondition(owner.feed.can("on", at: ResidentProcess.now()))
owner.close(); owner.close()
while !owner.mayQuit && Date() < deadline { owner.poll(); Thread.sleep(forTimeInterval: 0.01) }
precondition(owner.mayQuit && owner.reaped && owner.eof && owner.feed.closeConfirmed && !owner.feed.fault)
// Real original child emits a stale observation, receives the one close request,
// then reports collection. No alternate process or injected terminal is used.
for mode in ["recovery", "missing", "malformed", "unknown", "trailing", "partial", "nonzero"] {
    try mode.write(to: macos.appendingPathComponent("fixture-mode"), atomically: true, encoding: .ascii)
    let recovering = ResidentProcess(fixtureBundle: root)
    recovering.start()
    let until = Date().addingTimeInterval(10)
    while (!recovering.reaped || !recovering.eof) && Date() < until {
        recovering.poll(); Thread.sleep(forTimeInterval: 0.01)
        precondition(!recovering.feed.can("on", at: ResidentProcess.now()))
    }
    precondition(recovering.reaped && recovering.eof && recovering.feed.fault)
    precondition(recovering.mayQuit == (mode == "recovery"))
    precondition(!recovering.feed.can("on", at: ResidentProcess.now()))
    recovering.start() // an existing owner cannot be replaced even after collection
    precondition(recovering.reaped)
    precondition(recovering.diagnostic.contains("helper PID=") && recovering.diagnostic.contains("reason="))
}
let mismatch = ResidentProcess(fixtureBundle: root)
try "bad".write(to: resources.appendingPathComponent("resident.sha256"), atomically: true, encoding: .ascii)
mismatch.start()
precondition(mismatch.launchFailed && !mismatch.launched && mismatch.mayQuit)
// A normally exiting child and EOF without a closed report are not cleanup proof.
try manager.removeItem(at: helper)
try manager.copyItem(at: URL(fileURLWithPath: "/usr/bin/true"), to: helper)
let emptyDigest = SHA256.hash(data: try Data(contentsOf: helper)).map { String(format: "%02x", $0) }.joined()
try emptyDigest.write(to: resources.appendingPathComponent("resident.sha256"), atomically: true, encoding: .ascii)
let empty = ResidentProcess(fixtureBundle: root); empty.start()
let emptyDeadline = Date().addingTimeInterval(10)
while (!empty.reaped || !empty.eof) && Date() < emptyDeadline { empty.poll(); Thread.sleep(forTimeInterval: 0.01) }
precondition(empty.reaped && empty.eof && empty.feed.fault && !empty.mayQuit)
// Test-owned directory only, after confirmed child collection.
try manager.removeItem(at: root)
print("ProcessChecks: real synthetic child/pipe close+EOF+reap, duplicate start/close, fault recovery, terminal rejection and hash rejection passed; no resident host executed")
