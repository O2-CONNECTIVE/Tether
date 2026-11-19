import Foundation
import DevelopmentCore

var checks = 0
func check(_ value: @autoclosure () -> Bool, _ name: String) {
    checks += 1
    guard value() else { fatalError("FAILED: \(name)") }
}
func status(_ revision: Int = 1, time: Int = 1000, standby: String = "off", transition: String = "null", runUntil: String = "null", ending: Bool = false, expired: Bool = false) -> Data {
    Data("""
    {"protocolVersion":1,"type":"status","sourceContinuousMs":\(time),"snapshot":{"protocolVersion":1,"source":"resident-owner","state":"source-observation","revision":\(revision),"validForMs":3000,"standby":"\(standby)","registration":"\(standby == "on" ? "authenticated" : "disconnected")","caller":null,"work":"idle","cleanup":"confirmed","canRequestOn":\(standby == "off"),"canRequestOff":\(standby == "on"),"transition":\(transition),"runUntil":\(runUntil),"runEndingSoon":\(ending),"runExpired":\(expired)}}
    """.utf8)
}
func receipt(_ disposition: String) -> Data {
    Data("{\"protocolVersion\":1,\"type\":\"receipt\",\"receipt\":{\"protocolVersion\":1,\"requestId\":1,\"intent\":\"on\",\"disposition\":\"\(disposition)\"}}".utf8)
}
let applied = "{\"requestId\":1,\"intent\":\"on\",\"outcome\":\"applied\"}"
var f = Feed()
check(!f.can("on", at: 1000), "initial disabled")
f.ingest(status(), at: 1000)
check(!f.fault && f.can("on", at: 1000), "valid OFF enables ON")
check(!f.can("off", at: 1000), "OFF unavailable when OFF")
check(!f.can("on", at: 4000), "source expires at 3 seconds")
check(f.command("on", at: 1000) == "on 1 1\n", "fixed command")
check(f.command("on", at: 1000) == nil, "repeat click rejected")
f.ingest(receipt("accepted"), at: 1000)
check(f.pending != nil && !f.can("off", at: 1000), "receipt is not application")
f.ingest(status(2, standby: "on", transition: applied), at: 1000)
check(f.pending == nil && f.can("off", at: 1000), "correlated ON transition")
check(f.requestClose() == "close\n" && f.requestClose() == nil, "close once")
check(!f.can("off", at: 1000), "close disables intent")
f.ingest(Data("{\"protocolVersion\":1,\"type\":\"closed\",\"sourceContinuousMs\":1000,\"outcome\":\"confirmed\"}".utf8), at: 1000)
check(f.closeConfirmed && !f.fault, "explicit close report")
f.ingest(status(3), at: 1000)
check(f.fault, "post-terminal input rejected")
for bad in [
    String(decoding: status(), as: UTF8.self).replacingOccurrences(of: "\"revision\":1", with: "\"revision\":1,\"revision\":2"),
    String(decoding: status(), as: UTF8.self).replacingOccurrences(of: "\"revision\":1", with: "\"revision\":1,\"revis\\u0069on\":2"),
    String(decoding: status(), as: UTF8.self).replacingOccurrences(of: "\"revision\":1", with: "\"revision\":1.0"),
    String(decoding: status(), as: UTF8.self).replacingOccurrences(of: "\"revision\":1", with: "\"revision\":true"),
    String(decoding: status(), as: UTF8.self).replacingOccurrences(of: "\"cleanup\":\"confirmed\"", with: "\"cleanup\":\"unknown\""),
    String(decoding: status(), as: UTF8.self).replacingOccurrences(of: "\"caller\":null", with: "\"caller\":[]"),
    String(decoding: status(), as: UTF8.self)+"x", String(repeating: "x", count: 4097)
] {
    var badFeed = Feed(); badFeed.ingest(Data(bad.utf8), at: 1000)
    check(badFeed.fault, "malformed rejected")
    badFeed.ingest(status(2), at: 1000); check(badFeed.fault, "fault sticky")
}
for now: UInt64 in [999, 4000] {
    var stale = Feed(); stale.ingest(status(), at: now); check(stale.fault, "future/stale source rejected")
}
var rollback = Feed(); rollback.ingest(status(), at: 1000); rollback.ingest(status(2), at: 999)
check(rollback.fault, "receiver clock rollback rejected")
var duplicate = Feed(); duplicate.ingest(status(), at: 1000); duplicate.ingest(status(), at: 1000)
check(duplicate.fault, "revision replay rejected")
var refused = Feed(); refused.ingest(status(), at: 1000); _ = refused.command("on", at: 1000)
refused.ingest(status(2, transition: applied.replacingOccurrences(of: "applied", with: "rejected")), at: 1000)
check(!refused.can("on", at: 1000), "wait for prior receipt")
refused.ingest(receipt("stale"), at: 1000)
check(!refused.fault && refused.can("on", at: 1000), "refusal preceding receipt")
check(refused.command("on", at: 1000) == "on 2 2\n", "explicit retry new ID")
var unknown = Feed(); unknown.ingest(Data("{\"protocolVersion\":1,\"type\":\"closed\",\"sourceContinuousMs\":1000,\"outcome\":\"unknown\"}".utf8), at: 1000)
check(unknown.fault && !unknown.closeConfirmed, "unknown is not closed")

// Command-run allowance. Standby ON alone never lets an agent run a command;
// the person at the Mac opens an allowance of 2 to 24 hours, and this screen
// shows it only from the owner's status (runUntil), never from a receipt.
func runReceipt(_ id: Int, _ intent: String, _ disposition: String) -> Data {
    Data("{\"protocolVersion\":1,\"type\":\"receipt\",\"receipt\":{\"protocolVersion\":1,\"requestId\":\(id),\"intent\":\"\(intent)\",\"disposition\":\"\(disposition)\"}}".utf8)
}
func standingBy() -> Feed {
    var feed = Feed(); feed.ingest(status(), at: 1000)
    _ = feed.command("on", at: 1000); feed.ingest(receipt("accepted"), at: 1000)
    feed.ingest(status(2, standby: "on", transition: applied), at: 1000)
    return feed
}
let until = "1800000000000"
var idle = Feed(); idle.ingest(status(), at: 1000)
check(!idle.can("allow", at: 1000) && idle.command("allow", at: 1000, hours: 2) == nil, "allowance needs standby ON")
var run = standingBy()
check(run.can("allow", at: 1000) && !run.runAllowed(at: 1000), "standby ON alone is not an allowance")
// This screen's view of the allowance can lag the owner's. Withdrawing must
// stay possible whenever standby is ON, even if the screen shows none.
check(run.can("deny", at: 1000), "withdrawal is always offered while standby is ON")
for bad in [nil, 0, 1, 3, 25, -2] as [Int?] {
    check(run.command("allow", at: 1000, hours: bad) == nil && run.can("allow", at: 1000), "allow without a listed length sends nothing")
}
check(run.command("deny", at: 1000, hours: 2) == "deny 2 2\n", "deny carries no length")
run.ingest(runReceipt(2, "deny-run", "rejected"), at: 1000)
for (hours, line) in [(2, "allow 3 2 2\n"), (4, "allow 4 2 4\n"), (8, "allow 5 2 8\n"), (12, "allow 6 2 12\n"), (24, "allow 7 2 24\n")] {
    check(run.command("allow", at: 1000, hours: hours) == line, "allow command with \(hours) hours")
    run.ingest(runReceipt(Int(line.split(separator: " ")[1])!, "allow-run", "rejected"), at: 1000)
}
check(run.command("on", at: 1000) == nil, "on is unchanged and not offered while standby is ON")
var first = standingBy()
check(first.command("allow", at: 1000, hours: 2) == "allow 2 2 2\n", "allow command")
check(first.pending == nil, "allowance is not a standby transition")
check(!first.runAllowed(at: 1000) && !first.can("off", at: 1000) && !first.can("allow", at: 1000), "a request is not an allowance; wait for its receipt")
first.ingest(runReceipt(2, "allow-run", "accepted"), at: 1000)
check(!first.fault && !first.runAllowed(at: 1000), "a receipt alone does not open the allowance")
first.ingest(status(3, standby: "on", runUntil: until), at: 1000)
check(!first.fault && first.runAllowed(at: 1000) && first.runAllowed(at: 3_000_000), "the owner's status opens the allowance, with no local clock")
check(first.observation?.runUntil == 1_800_000_000_000 && first.observation?.runEndingSoon == false && first.observation?.runExpired == false, "status carries runUntil and the two flags")
check(first.can("off", at: 1000) && first.can("deny", at: 1000) && first.can("allow", at: 1000), "allowance does not block standby OFF; extending is offered")
check(first.command("allow", at: 1000, hours: 24) == "allow 3 3 24\n", "extending is another allow")
first.ingest(runReceipt(3, "allow-run", "accepted"), at: 1000)
first.ingest(status(4, standby: "on", runUntil: "1800000100000", ending: true), at: 1000)
check(first.runAllowed(at: 1000) && first.observation?.runEndingSoon == true, "ending-soon status keeps the allowance")
check(first.command("deny", at: 1000) == "deny 4 4\n", "deny command")
check(first.runAllowed(at: 1000), "still open until the owner reports it withdrawn")
first.ingest(runReceipt(4, "deny-run", "accepted"), at: 1000)
check(!first.fault && first.runAllowed(at: 1000), "a receipt alone does not withdraw the allowance")
first.ingest(status(5, standby: "on"), at: 1000)
check(!first.fault && !first.runAllowed(at: 1000) && first.can("allow", at: 1000), "withdrawn")
var refusedRun = standingBy(); _ = refusedRun.command("allow", at: 1000, hours: 2)
refusedRun.ingest(runReceipt(2, "allow-run", "rejected"), at: 1000)
check(!refusedRun.fault && !refusedRun.runAllowed(at: 1000) && refusedRun.can("allow", at: 1000), "refused allowance stays closed and may be retried")
var crossed = standingBy(); _ = crossed.command("allow", at: 1000, hours: 2)
crossed.ingest(runReceipt(2, "deny-run", "accepted"), at: 1000)
check(crossed.fault && !crossed.runAllowed(at: 1000), "receipt for another intent is not an allowance")
var stopped = standingBy(); _ = stopped.command("allow", at: 1000, hours: 2)
stopped.ingest(runReceipt(2, "allow-run", "accepted"), at: 1000)
stopped.ingest(status(3, standby: "on", runUntil: until), at: 1000)
check(stopped.runAllowed(at: 1000), "allowance open before standby ends")
stopped.ingest(status(4, standby: "off", transition: applied, expired: true), at: 1000)
check(!stopped.fault && !stopped.runAllowed(at: 1000) && stopped.observation?.runExpired == true, "standby OFF ends the allowance")
stopped.ingest(status(5, standby: "on", transition: applied), at: 1000)
check(!stopped.runAllowed(at: 1000), "a later standby ON does not bring an old allowance back")
var faulted = standingBy(); _ = faulted.command("allow", at: 1000, hours: 2)
faulted.ingest(runReceipt(2, "allow-run", "accepted"), at: 1000)
faulted.ingest(status(3, standby: "on", runUntil: until), at: 1000); faulted.fail()
check(!faulted.runAllowed(at: 1000), "a fault never shows an open allowance")
// The three keys are always present and strictly typed.
let plain = String(decoding: status(runUntil: until), as: UTF8.self)
for bad in [
    plain.replacingOccurrences(of: ",\"runUntil\":\(until)", with: ""),
    plain.replacingOccurrences(of: ",\"runEndingSoon\":false", with: ""),
    plain.replacingOccurrences(of: ",\"runExpired\":false", with: ""),
    plain.replacingOccurrences(of: "\"runExpired\":false", with: "\"runExpired\":false,\"extra\":1"),
    plain.replacingOccurrences(of: "\"runUntil\":\(until)", with: "\"runUntil\":\"\(until)\""),
    plain.replacingOccurrences(of: "\"runUntil\":\(until)", with: "\"runUntil\":1.5"),
    plain.replacingOccurrences(of: "\"runEndingSoon\":false", with: "\"runEndingSoon\":null"),
    plain.replacingOccurrences(of: "\"runExpired\":false", with: "\"runExpired\":0"),
] {
    var badFeed = Feed(); badFeed.ingest(Data(bad.utf8), at: 1000)
    check(badFeed.fault && badFeed.observation == nil, "malformed run keys rejected")
}
var withKeys = Feed(); withKeys.ingest(status(runUntil: until, ending: true, expired: true), at: 1000)
check(!withKeys.fault && withKeys.observation?.runEndingSoon == true && withKeys.observation?.runExpired == true, "run keys accepted")

// The record of what ran. The gateway signs only a hash, so this list is how
// the person at the Mac sees the command text. A damaged line is shown as
// damaged, never dropped: a hidden record would hide a command that ran.
let history = Data("""
{"at":"2026-11-12T01:00:00Z","dir":"/w/src","command":"make test\\n"}
not json
{"at":"2026-11-12T01:01:00Z","dir":"/w","command":"pwd\\n"}

""".utf8)
let unreadable = RunRecord(at: "", dir: "", command: RunRecord.unreadable)
check(RunHistory.recent(history, limit: 5) == [
    RunRecord(at: "2026-11-12T01:01:00Z", dir: "/w", command: "pwd\n"), unreadable,
    RunRecord(at: "2026-11-12T01:00:00Z", dir: "/w/src", command: "make test\n"),
], "newest first, damaged line kept")
check(RunHistory.recent(history, limit: 1) == [RunRecord(at: "2026-11-12T01:01:00Z", dir: "/w", command: "pwd\n")], "limit keeps the newest")
check(RunHistory.recent(Data(), limit: 5).isEmpty, "no record, nothing shown")
// The screen shows a few records and the start of each. It must say so:
// a silent cut would hide the rest of a long command, or an earlier one.
let long = String(repeating: "x", count: 301)
let shown = RunHistory.text(Data("{\"at\":\"t1\",\"dir\":\"/w\",\"command\":\"a\"}\n{\"at\":\"t2\",\"dir\":\"/w\",\"command\":\"b\"}\n{\"at\":\"t3\",\"dir\":\"/w\",\"command\":\"\(long)\"}\n".utf8), limit: 2)
check(shown == "全3件のうち直近2件\n\nt3  /w\n" + String(repeating: "x", count: 300) + "\n…（全301文字。以下省略）\n\nt2  /w\nb", "count and cut are stated")
check(RunHistory.text(Data(), limit: 5) == "実行したコマンドはありません", "empty record")
check(RunHistory.recent(Data("{\"at\":\"x\",\"dir\":\"/w\",\"command\":\"id\\n\",\"extra\":1}\n{\"at\":\"x\",\"dir\":\"/w\"}\n".utf8), limit: 5) == [unreadable, unreadable], "unexpected shape is damaged, not trusted")
print("DevelopmentChecks: \(checks) passed; synthetic protocol only, no host launched")

var timely = LineBuffer()
check(try! timely.receive([65], at: 1000).isEmpty, "partial buffered")
check(try! timely.receive([10], at: 1999) == [Data([65])], "assembly just before deadline")
for completion: UInt64 in [2000, 2001] {
    var late = LineBuffer(); _ = try late.receive([65], at: 1000)
    do { _ = try late.receive([10], at: completion); fatalError("late LF accepted") }
    catch { check(true, "late LF rejected before clearing partial") }
}
print("Total including line deadline regressions: \(checks) passed")

// Regression: sealing admission must not discard a later original-owner close.
func terminal(_ outcome: String = "confirmed", time: UInt64 = 5000) -> Data {
    Data("{\"protocolVersion\":1,\"type\":\"closed\",\"sourceContinuousMs\":\(time),\"outcome\":\"\(outcome)\"}".utf8)
}
var recoverable = Feed()
recoverable.ingest(status(), at: 5000) // stale complete observation
check(recoverable.fault && !recoverable.can("on", at: 5000), "stale observation seals admission")
_ = recoverable.requestClose()
recoverable.ingest(terminal(), at: 5000)
check(recoverable.fault && recoverable.closeConfirmed, "fault retains strictly validated owner close")
check(recoverable.command("on", at: 5000) == nil, "recovered collection never restores admission")
for badTerminal in [terminal("unknown"), terminal(time: 999), Data("{\"type\":\"closed\"}".utf8), Data("not-json".utf8)] {
    var blocked = Feed(); blocked.ingest(status(), at: 5000)
    blocked.ingest(badTerminal, at: 5000); blocked.ingest(terminal(), at: 5000)
    check(blocked.cleanupInvalid && !blocked.closeConfirmed && blocked.fault, "bad or unknown terminal stays blocked")
}
for trailing in [terminal(), status(2, time: 5000), Data("x".utf8)] {
    var blocked = Feed(); blocked.ingest(terminal(), at: 5000)
    blocked.ingest(trailing, at: 5000)
    check(blocked.cleanupInvalid && !blocked.closeConfirmed, "post-terminal frame invalidates collection")
}
var framedFailure = Feed(); framedFailure.invalidateCleanup("frame-invalid")
framedFailure.ingest(terminal(), at: 5000)
check(framedFailure.cleanupInvalid && !framedFailure.closeConfirmed, "lost framing cannot salvage terminal")
print("Total including original-owner recovery: \(checks) passed")

var rejectedClock = Feed()
rejectedClock.ingest(status(), at: 1000)
rejectedClock.ingest(status(2), at: 5000)
rejectedClock.ingest(terminal(time: 4000), at: 4000)
check(rejectedClock.cleanupInvalid && !rejectedClock.closeConfirmed, "invalid observation retains receiver clock high-water mark")
let staleUnknown = Data(String(decoding: status(), as: UTF8.self).replacingOccurrences(of: "\"cleanup\":\"confirmed\"", with: "\"cleanup\":\"unknown\"").utf8)
for unknownFrame in [staleUnknown, receipt("unknown")] {
    var rejectedUnknown = Feed()
    rejectedUnknown.ingest(unknownFrame, at: 5000)
    rejectedUnknown.ingest(terminal(), at: 5000)
    check(rejectedUnknown.cleanupInvalid && !rejectedUnknown.closeConfirmed, "unknown wins over stale or uncorrelated observation")
}
print("Total including review regressions: \(checks) passed")

runMapperContract()
print("Total including mapper contract: \(checks) passed")
