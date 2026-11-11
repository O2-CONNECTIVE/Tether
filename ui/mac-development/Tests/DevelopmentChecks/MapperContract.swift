import Foundation
import DevelopmentCore

func runMapperContract() {
    let url = Bundle.module.url(forResource: "mapper-pending", withExtension: "jsonl", subdirectory: "Fixtures")!
    let frames = try! String(contentsOf: url, encoding: .utf8).split(separator: "\n").map { Data($0.utf8) }
    check(frames.count == 7, "Go mapper fixture has all phases")
    func begun() -> Feed {
        var f = Feed(); f.ingest(frames[0], at: 1000)
        check(f.command("on", at: 1000) == "on 1 1\n", "mapper OFF admits exact ON")
        f.ingest(frames[1], at: 1000)
        return f
    }
    var f = begun(); f.ingest(frames[2], at: 1000)
    check(!f.fault && !f.cleanupInvalid && f.pending != nil, "Go connecting is pending, not unknown cleanup")
    check(!f.can("on", at: 1000) && !f.can("off", at: 1000), "pending does not admit new work")
    f.ingest(frames[3], at: 1000)
    check(!f.fault && f.pending != nil && !f.can("off", at: 1000), "fresh pending heartbeat remains non-authoritative")
    f.ingest(frames[4], at: 1000)
    check(!f.fault && f.pending == nil && f.can("off", at: 1000), "Go authenticated transition completes ON")
    check(f.command("off", at: 1000) == "off 2 4\n", "mapper authenticated admits exact OFF")
    f.ingest(frames[5], at: 1000); f.ingest(frames[6], at: 1000)
    check(!f.fault && f.pending == nil && f.observation?.standby == "off", "Go collected OFF completes")
    _ = f.requestClose(); f.ingest(terminal(time: 1000), at: 1000)
    check(f.closeConfirmed && !f.cleanupInvalid, "valid original terminal after mapper sequence")
    let pending = String(decoding: frames[2], as: UTF8.self)
    let mutations = [
        ("\"cleanup\":\"confirmed\"", "\"cleanup\":\"unknown\""),
        ("\"outcome\":\"pending\"", "\"outcome\":\"unknown\""),
        ("\"requestId\":1", "\"requestId\":2"),
        ("\"caller\":null", "\"caller\":\"unexpected\""),
        ("\"registration\":\"connecting\"", "\"registration\":\"unknown\""),
        ("\"work\":\"idle\"", "\"work\":\"unknown\""),
        ("\"canRequestOff\":false", "\"canRequestOff\":true"),
        ("\"canRequestOn\":false", "\"canRequestOn\":true"),
        ("\"intent\":\"on\"", "\"intent\":\"off\""),
        ("\"cleanup\":\"confirmed\"", "\"cleanup\":\"pending\"")
    ]
    for (old, new) in mutations {
        check(pending.contains(old), "negative fixture mutation exists")
        var bad = begun(); bad.ingest(Data(pending.replacingOccurrences(of: old, with: new).utf8), at: 1000)
        bad.ingest(terminal(time: 1000), at: 1000)
        check(bad.fault && bad.cleanupInvalid && !bad.closeConfirmed, "invalid unknown standby cannot salvage terminal")
    }
    var unknownReceipt = begun(); unknownReceipt.ingest(receipt("unknown"), at: 1000)
    unknownReceipt.ingest(terminal(time: 1000), at: 1000)
    check(unknownReceipt.cleanupInvalid && !unknownReceipt.closeConfirmed, "true unknown receipt remains poisoned")
    var closing = begun(); _ = closing.requestClose(); closing.ingest(frames[2], at: 1000)
    closing.ingest(terminal(time: 1000), at: 1000)
    check(closing.closing && closing.closeConfirmed && !closing.cleanupInvalid, "pending while closing preserves original terminal")
    var duplicatePending = begun(); duplicatePending.ingest(frames[2], at: 1000)
    duplicatePending.ingest(frames[2], at: 1000)
    check(duplicatePending.cleanupInvalid, "replayed pending does not earn exception")
    var noReceipt = Feed(); noReceipt.ingest(frames[0], at: 1000)
    _ = noReceipt.command("on", at: 1000); noReceipt.ingest(frames[2], at: 1000)
    check(noReceipt.cleanupInvalid, "pending exception requires accepted receipt")
    var unrequested = Feed(); unrequested.ingest(frames[0], at: 1000)
    unrequested.ingest(frames[2], at: 1000)
    check(unrequested.cleanupInvalid, "pending exception requires original requested ON")
    var latched = begun(); latched.fail("synthetic-observation-fault")
    latched.ingest(frames[2], at: 1000)
    check(latched.fault && !latched.can("on", at: 1000) && !latched.can("off", at: 1000), "pending never clears prior fault")
    _ = latched.requestClose(); latched.ingest(terminal(time: 1000), at: 1000)
    check(latched.closeConfirmed && !latched.cleanupInvalid && latched.fault, "same-owner terminal remains distinct from admission fault")
    var immediate = begun(); immediate.ingest(frames[4], at: 1000)
    check(!immediate.fault && immediate.can("off", at: 1000), "authentication may precede published pending")
    var rejected = begun(); rejected.ingest(frames[2], at: 1000)
    let rejection = String(decoding: frames[6], as: UTF8.self)
        .replacingOccurrences(of: "\"requestId\":2", with: "\"requestId\":1")
        .replacingOccurrences(of: "\"intent\":\"off\"", with: "\"intent\":\"on\"")
        .replacingOccurrences(of: "\"outcome\":\"applied\"", with: "\"outcome\":\"rejected\"")
    rejected.ingest(Data(rejection.utf8), at: 1000)
    check(!rejected.fault && rejected.pending == nil && rejected.can("on", at: 1000), "confirmed rejected ON preserves explicit retry contract")
    print("Go mapper contract checks passed; no transport or real host")
}
