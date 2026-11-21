import Foundation
import DevelopmentCore

var checks = 0
func check(_ value: @autoclosure () -> Bool, _ name: String) {
    checks += 1
    guard value() else { fatalError("FAILED: \(name)") }
}
func status(_ revision: Int = 1, time: Int = 1000, standby: String = "off", transition: String = "null", until: String = "null", ending: Bool = false, expired: Bool = false) -> Data {
    Data("""
    {"protocolVersion":1,"type":"status","sourceContinuousMs":\(time),"snapshot":{"protocolVersion":1,"source":"resident-owner","state":"source-observation","revision":\(revision),"validForMs":3000,"standby":"\(standby)","registration":"\(standby == "on" ? "authenticated" : "disconnected")","caller":null,"work":"idle","cleanup":"confirmed","canRequestOn":\(standby == "off"),"canRequestOff":\(standby == "on"),"transition":\(transition),"standbyUntil":\(until),"standbyEndingSoon":\(ending),"standbyExpired":\(expired)}}
    """.utf8)
}
func receipt(_ disposition: String, id: Int = 1, intent: String = "on") -> Data {
    Data("{\"protocolVersion\":1,\"type\":\"receipt\",\"receipt\":{\"protocolVersion\":1,\"requestId\":\(id),\"intent\":\"\(intent)\",\"disposition\":\"\(disposition)\"}}".utf8)
}
let applied = "{\"requestId\":1,\"intent\":\"on\",\"outcome\":\"applied\"}"
var f = Feed()
check(!f.can("on", at: 1000), "initial disabled")
f.ingest(status(), at: 1000)
check(!f.fault && f.can("on", at: 1000), "valid OFF enables ON")
check(!f.can("off", at: 1000), "OFF unavailable when OFF")
check(!f.can("on", at: 4000), "source expires at 3 seconds")
check(f.command("on", at: 1000) == nil && f.can("on", at: 1000), "ON without a length sends nothing")
check(f.command("on", at: 1000, hours: 2) == "on 1 1 2\n", "fixed command carries the standby length")
check(f.command("on", at: 1000, hours: 2) == nil, "repeat click rejected")
f.ingest(receipt("accepted"), at: 1000)
check(f.pending != nil && !f.can("off", at: 1000), "receipt is not application")
f.ingest(status(2, standby: "on", transition: applied), at: 1000)
check(f.pending == nil && f.can("off", at: 1000), "correlated ON transition")
var priorApplied = Feed()
priorApplied.ingest(status(), at: 1000)
check(priorApplied.command("on", at: 1000, hours: 2) == "on 1 1 2\n", "first ON before prior applied transition")
priorApplied.ingest(receipt("accepted"), at: 1000)
priorApplied.ingest(status(2, standby: "on", transition: applied), at: 1000)
check(!priorApplied.fault && priorApplied.pending == nil, "prior ON transition applied")
check(priorApplied.command("off", at: 1000) == "off 2 2\n", "second OFF becomes pending")
priorApplied.ingest(receipt("accepted", id: 2, intent: "off"), at: 1000)
priorApplied.ingest(status(3, standby: "on", transition: applied), at: 1000)
check(!priorApplied.fault && priorApplied.pending?.id == 2, "prior applied transition keeps newer pending request")
let appliedOff = "{\"requestId\":2,\"intent\":\"off\",\"outcome\":\"applied\"}"
priorApplied.ingest(status(4, standby: "off", transition: appliedOff), at: 1000)
check(!priorApplied.fault && priorApplied.pending == nil, "newer OFF transition resolves pending")
var priorRejected = Feed()
priorRejected.ingest(status(), at: 1000)
_ = priorRejected.command("on", at: 1000, hours: 2)
priorRejected.ingest(receipt("accepted"), at: 1000)
let rejectedOn = applied.replacingOccurrences(of: "applied", with: "rejected")
priorRejected.ingest(status(2, transition: rejectedOn), at: 1000)
check(!priorRejected.fault && priorRejected.pending == nil, "prior ON transition rejected")
check(priorRejected.command("on", at: 1000, hours: 2) == "on 2 2 2\n", "retry becomes pending with new ID")
priorRejected.ingest(receipt("accepted", id: 2), at: 1000)
priorRejected.ingest(status(3, transition: rejectedOn), at: 1000)
check(!priorRejected.fault && priorRejected.pending?.id == 2, "prior rejected transition keeps newer pending request")
let appliedOn2 = "{\"requestId\":2,\"intent\":\"on\",\"outcome\":\"applied\"}"
priorRejected.ingest(status(4, standby: "on", transition: appliedOn2), at: 1000)
check(!priorRejected.fault && priorRejected.pending == nil, "newer ON transition resolves pending")
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
var refused = Feed(); refused.ingest(status(), at: 1000); _ = refused.command("on", at: 1000, hours: 2)
refused.ingest(status(2, transition: applied.replacingOccurrences(of: "applied", with: "rejected")), at: 1000)
check(!refused.can("on", at: 1000), "wait for prior receipt")
refused.ingest(receipt("stale"), at: 1000)
check(!refused.fault && refused.can("on", at: 1000), "refusal preceding receipt")
check(refused.command("on", at: 1000, hours: 2) == "on 2 2 2\n", "explicit retry new ID")
var unknown = Feed(); unknown.ingest(Data("{\"protocolVersion\":1,\"type\":\"closed\",\"sourceContinuousMs\":1000,\"outcome\":\"unknown\"}".utf8), at: 1000)
check(unknown.fault && !unknown.closeConfirmed, "unknown is not closed")

// Standby length and command-run allowance. Standby ON carries its own length
// (2 to 24 hours) and ends at the owner's standbyUntil. The allowance has no
// length: it lasts until standby is no longer ON. The owner's status does not
// say whether it is open, so this view keeps it from receipts.
func runReceipt(_ id: Int, _ intent: String, _ disposition: String) -> Data {
    Data("{\"protocolVersion\":1,\"type\":\"receipt\",\"receipt\":{\"protocolVersion\":1,\"requestId\":\(id),\"intent\":\"\(intent)\",\"disposition\":\"\(disposition)\"}}".utf8)
}
func standingBy() -> Feed {
    var feed = Feed(); feed.ingest(status(), at: 1000)
    _ = feed.command("on", at: 1000, hours: 2); feed.ingest(receipt("accepted"), at: 1000)
    feed.ingest(status(2, standby: "on", transition: applied, until: until), at: 1000)
    return feed
}
let until = "1800000000000"
check(Feed.standbyHours == [2, 4, 8, 12, 24], "only the five listed lengths exist")
var idle = Feed(); idle.ingest(status(), at: 1000)
check(!idle.can("allow", at: 1000) && idle.command("allow", at: 1000) == nil, "allowance needs standby ON")
check(!idle.can("extend", at: 1000) && idle.command("extend", at: 1000, hours: 2) == nil, "extend needs standby ON")
for hours in Feed.standbyHours {
    var feed = Feed(); feed.ingest(status(), at: 1000)
    check(feed.command("on", at: 1000, hours: hours) == "on 1 1 \(hours)\n", "ON with \(hours) hours")
}
for bad in [nil, 0, 1, 3, 25, -2] as [Int?] {
    var feed = Feed(); feed.ingest(status(), at: 1000)
    check(feed.command("on", at: 1000, hours: bad) == nil && feed.pending == nil && feed.can("on", at: 1000), "ON without a listed length sends nothing")
}
var run = standingBy()
check(run.can("allow", at: 1000) && !run.runAllowed, "standby ON alone is not an allowance")
// Withdrawing must stay possible whenever standby is ON, even if this view shows none.
check(run.can("deny", at: 1000), "withdrawal is always offered while standby is ON")
check(run.command("allow", at: 1000, hours: 2) == "allow 2 2\n", "allow carries no length even if one is given")
run.ingest(runReceipt(2, "allow-run", "rejected"), at: 1000)
check(run.command("deny", at: 1000, hours: 2) == "deny 3 2\n", "deny carries no length")
run.ingest(runReceipt(3, "deny-run", "rejected"), at: 1000)
for bad in [nil, 0, 1, 3, 25, -2] as [Int?] {
    check(run.command("extend", at: 1000, hours: bad) == nil && run.can("extend", at: 1000), "extend without a listed length sends nothing")
}
check(run.command("on", at: 1000, hours: 2) == nil, "on is not offered while standby is ON")
var ext = standingBy()
check(ext.command("extend", at: 1000, hours: 12) == "extend 2 2 12\n", "extend carries the new length")
check(ext.pending == nil, "extend is not a standby transition")
check(!ext.can("off", at: 1000) && !ext.can("extend", at: 1000), "extend waits for its receipt")
ext.ingest(runReceipt(2, "extend", "accepted"), at: 1000)
check(!ext.fault && ext.pending == nil && ext.can("off", at: 1000) && ext.can("extend", at: 1000), "extend receipt completes with no transition")
ext.ingest(status(3, standby: "on", until: "1800000100000", ending: true), at: 1000)
check(!ext.fault && ext.observation?.standbyUntil == 1_800_000_100_000 && ext.observation?.standbyEndingSoon == true && ext.observation?.standbyExpired == false, "status carries standbyUntil and the two flags")
var first = standingBy()
check(first.command("allow", at: 1000) == "allow 2 2\n", "allow command")
check(first.pending == nil, "allowance is not a standby transition")
check(!first.runAllowed && !first.can("off", at: 1000), "a request is not an allowance; wait for its receipt")
first.ingest(runReceipt(2, "allow-run", "accepted"), at: 1000)
check(!first.fault && first.runAllowed, "an accepted allow opens the allowance")
check(first.can("off", at: 1000) && first.can("deny", at: 1000), "allowance does not block standby OFF")
first.ingest(status(3, standby: "on", until: until, ending: true), at: 1000)
check(first.runAllowed, "ending soon does not end the allowance; standby still ON")
check(first.command("deny", at: 1000) == "deny 3 3\n", "deny command")
check(first.runAllowed, "still open until the deny is accepted")
first.ingest(runReceipt(3, "deny-run", "accepted"), at: 1000)
check(!first.fault && !first.runAllowed && first.can("allow", at: 1000), "accepted deny withdraws")
var refusedRun = standingBy(); _ = refusedRun.command("allow", at: 1000)
refusedRun.ingest(runReceipt(2, "allow-run", "rejected"), at: 1000)
check(!refusedRun.fault && !refusedRun.runAllowed && refusedRun.can("allow", at: 1000), "refused allowance stays closed and may be retried")
var crossed = standingBy(); _ = crossed.command("allow", at: 1000)
crossed.ingest(runReceipt(2, "deny-run", "accepted"), at: 1000)
check(crossed.fault && !crossed.runAllowed, "receipt for another intent is not an allowance")
var stopped = standingBy(); _ = stopped.command("allow", at: 1000)
stopped.ingest(runReceipt(2, "allow-run", "accepted"), at: 1000)
check(stopped.runAllowed, "allowance open before standby ends")
stopped.ingest(status(3, standby: "off", expired: true), at: 1000)
check(!stopped.fault && !stopped.runAllowed && stopped.observation?.standbyExpired == true, "standby no longer ON drops the allowance")
stopped.ingest(status(4, standby: "on", transition: applied, until: until), at: 1000)
check(!stopped.runAllowed, "a later standby ON does not bring an old allowance back")
var faulted = standingBy(); _ = faulted.command("allow", at: 1000)
faulted.ingest(runReceipt(2, "allow-run", "accepted"), at: 1000); faulted.fail()
check(!faulted.runAllowed, "a fault never shows an open allowance")
// The three keys are always present and strictly typed.
let plain = String(decoding: status(until: until), as: UTF8.self)
for bad in [
    plain.replacingOccurrences(of: ",\"standbyUntil\":\(until)", with: ""),
    plain.replacingOccurrences(of: ",\"standbyEndingSoon\":false", with: ""),
    plain.replacingOccurrences(of: ",\"standbyExpired\":false", with: ""),
    plain.replacingOccurrences(of: "\"standbyExpired\":false", with: "\"standbyExpired\":false,\"extra\":1"),
    plain.replacingOccurrences(of: "\"standbyUntil\":\(until)", with: "\"standbyUntil\":\"\(until)\""),
    plain.replacingOccurrences(of: "\"standbyUntil\":\(until)", with: "\"standbyUntil\":1.5"),
    plain.replacingOccurrences(of: "\"standbyEndingSoon\":false", with: "\"standbyEndingSoon\":null"),
    plain.replacingOccurrences(of: "\"standbyExpired\":false", with: "\"standbyExpired\":0"),
    String(decoding: status(ending: true), as: UTF8.self), // ending soon needs an end time
] {
    var badFeed = Feed(); badFeed.ingest(Data(bad.utf8), at: 1000)
    check(badFeed.fault && badFeed.observation == nil, "malformed standby keys rejected")
}
var withKeys = Feed(); withKeys.ingest(status(until: until, ending: true, expired: true), at: 1000)
check(!withKeys.fault && withKeys.observation?.standbyEndingSoon == true && withKeys.observation?.standbyExpired == true, "standby keys accepted")

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
check(recoverable.command("on", at: 5000, hours: 2) == nil, "recovered collection never restores admission")
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

runReconnectContract()
print("Total including reconnect contract: \(checks) passed")
