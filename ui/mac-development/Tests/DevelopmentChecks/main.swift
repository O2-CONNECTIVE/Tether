import Foundation
import DevelopmentCore

var checks = 0
func check(_ value: @autoclosure () -> Bool, _ name: String) {
    checks += 1
    guard value() else { fatalError("FAILED: \(name)") }
}
func status(_ revision: Int = 1, time: Int = 1000, standby: String = "off", transition: String = "null") -> Data {
    Data("""
    {"protocolVersion":1,"type":"status","sourceContinuousMs":\(time),"snapshot":{"protocolVersion":1,"source":"resident-owner","state":"source-observation","revision":\(revision),"validForMs":3000,"standby":"\(standby)","registration":"\(standby == "on" ? "authenticated" : "disconnected")","caller":null,"work":"idle","cleanup":"confirmed","canRequestOn":\(standby == "off"),"canRequestOff":\(standby == "on"),"transition":\(transition)}}
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
