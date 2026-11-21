import Foundation
import DevelopmentCore

func runMapperContract() {
    let url = Bundle.module.url(forResource: "mapper-pending", withExtension: "jsonl", subdirectory: "Fixtures")!
    let frames = try! String(contentsOf: url, encoding: .utf8).split(separator: "\n").map { Data($0.utf8) }
    check(frames.count == 7, "Go mapper fixture has all phases")
    func begun() -> Feed {
        var f = Feed(); f.ingest(frames[0], at: 1000)
        check(f.command("on", at: 1000, hours: 2) == "on 1 1 2\n", "mapper OFF admits exact ON")
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
    _ = noReceipt.command("on", at: 1000, hours: 2); noReceipt.ingest(frames[2], at: 1000)
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

// #466: after authentication a dropped connection is reconnecting, and a
// refusal is needs_action; standby stays ON in both. A fault here makes
// ResidentProcess close the host on the first network blip.
func runReconnectContract() {
    let url = Bundle.module.url(forResource: "mapper-reconnect", withExtension: "jsonl", subdirectory: "Fixtures")!
    let frames = try! String(contentsOf: url, encoding: .utf8).split(separator: "\n").map { Data($0.utf8) }
    check(frames.count == 10, "Go reconnect fixture has all phases")
    func on() -> Feed {
        var f = Feed(); f.ingest(frames[0], at: 1000)
        check(f.command("on", at: 1000, hours: 2) == "on 1 1 2\n", "reconnect fixture admits exact ON")
        f.ingest(frames[1], at: 1000); f.ingest(frames[2], at: 1000); f.ingest(frames[3], at: 1000)
        check(!f.fault && !f.cleanupInvalid && f.pending != nil && f.observation?.registration == "reconnecting", "Go initial ON retrying stays pending without a fault")
        check(!f.can("on", at: 1000) && !f.can("off", at: 1000), "initial ON retrying admits no new request")
        f.ingest(frames[4], at: 1000)
        check(!f.fault && f.pending == nil && f.can("off", at: 1000), "Go authenticated ON completes")
        return f
    }
    var f = on(); f.ingest(frames[5], at: 1000)
    check(!f.fault && f.observation?.standby == "on" && f.observation?.registration == "reconnecting", "Go reconnecting keeps standby ON without a fault")
    check(f.can("off", at: 1000) && !f.can("on", at: 1000), "reconnecting still offers OFF")
    f.ingest(frames[6], at: 1000)
    check(!f.fault && f.observation?.registration == "authenticated", "Go reconnection returns to authenticated")
    // The owner closes admission in needs_action and rejects extend and allow,
    // so this view must not offer them or keep showing a granted allowance.
    var permitted = f
    check(permitted.command("allow", at: 1000) == "allow 2 6\n", "authenticated standby admits allow")
    permitted.ingest(receipt("accepted", id: 2, intent: "allow-run"), at: 1000)
    check(permitted.runAllowed, "accepted allow grants command permission")
    permitted.ingest(frames[7], at: 1000)
    check(!permitted.fault && !permitted.runAllowed && !permitted.can("allow", at: 1000) && !permitted.can("extend", at: 1000), "needs_action drops the allowance and offers neither allow nor extend")
    f.ingest(frames[7], at: 1000)
    check(!f.fault && f.observation?.standby == "on" && f.observation?.registration == "needs_action", "Go needs_action keeps standby ON without a fault")
    check(f.can("off", at: 1000) && !f.can("on", at: 1000), "needs_action offers OFF, the way to retry")
    check(f.command("off", at: 1000) == "off 2 7\n", "needs_action admits exact OFF")
    f.ingest(frames[8], at: 1000); f.ingest(frames[9], at: 1000)
    check(!f.fault && f.pending == nil && f.observation?.standby == "off" && f.can("on", at: 1000), "OFF ends needs_action and offers ON again")
    _ = f.requestClose(); f.ingest(terminal(time: 1000), at: 1000)
    check(f.closeConfirmed && !f.cleanupInvalid, "valid original terminal after reconnect sequence")
    let needs = String(decoding: frames[7], as: UTF8.self)
    for reason in ["connection_occupied", "gateway_refused"] {
        var other = on(); other.ingest(Data(needs.replacingOccurrences(of: "\"identity_refused\"", with: "\"\(reason)\"").utf8), at: 1000)
        check(!other.fault && other.can("off", at: 1000), "needs_action reason \(reason) is accepted")
    }
    // The reason tells the user what to fix, so it comes with needs_action and
    // nothing else, and only as a value the owner can produce. needs_action is
    // also only standby ON with no work and confirmed cleanup, as the mapper requires.
    let reconnecting = String(decoding: frames[5], as: UTF8.self)
    let malformed = [
        needs.replacingOccurrences(of: "\"registrationReason\":\"identity_refused\",", with: ""),
        needs.replacingOccurrences(of: "\"identity_refused\"", with: "\"rate_limited\""),
        needs.replacingOccurrences(of: "\"identity_refused\"", with: "null"),
        reconnecting.replacingOccurrences(of: "\"caller\":null", with: "\"registrationReason\":\"gateway_refused\",\"caller\":null"),
        needs.replacingOccurrences(of: "\"standby\":\"on\"", with: "\"standby\":\"off\"")
            .replacingOccurrences(of: "\"canRequestOff\":true", with: "\"canRequestOff\":false")
            .replacingOccurrences(of: "{\"requestId\":1,\"intent\":\"on\",\"outcome\":\"applied\"}", with: "null"),
        needs.replacingOccurrences(of: "\"work\":\"idle\"", with: "\"work\":\"preparing\"")
    ]
    for (i, frame) in malformed.enumerated() {
        check(frame != needs && frame != reconnecting, "reason mutation \(i) exists")
        var bad = on(); bad.ingest(Data(frame.utf8), at: 1000)
        check(bad.fault && !bad.can("off", at: 1000), "malformed registration reason \(i) is rejected")
    }
}
