import Foundation
import Darwin
import StatusCore


private var failures = 0
func XCTAssertTrue(_ value: Bool) { if !value { failures += 1 } }
func XCTAssertFalse(_ value: Bool) { XCTAssertTrue(!value) }
func XCTAssertNil<T>(_ value: T?) { XCTAssertTrue(value == nil) }
func XCTAssertEqual<T: Equatable>(_ lhs: T, _ rhs: T) { XCTAssertTrue(lhs == rhs) }
func XCTAssertLessThan<T: Comparable>(_ lhs: T, _ rhs: T) { XCTAssertTrue(lhs < rhs) }
func XCTAssertThrowsError(_ operation: @autoclosure () throws -> Any) { do { _ = try operation(); failures += 1 } catch {} }
func XCTAssertNoThrow(_ operation: @autoclosure () throws -> Any) { do { _ = try operation() } catch { failures += 1 } }

final class StatusTests {
    func line(_ seq:Int=1,_ changes:[String:Any]=[:]) -> Data {
        var m:[String:Any] = ["version":1,"source_continuous_ms":10000,"seq":seq,"phase":"connected","reason":"none","grant_remaining_ms":120000,"starting_workers":0,"running_workers":1,"stopping_workers":0,"cleanup":"pending","terminal_cleanup_unknown":false,"terminal":false]
        for (k,v) in changes { m[k]=v }; return try! JSONSerialization.data(withJSONObject:m,options:.sortedKeys)
    }
    func testValid() throws { XCTAssertEqual(try Decoder.decode(line()).running,1) }
    func testNullAndUpperBound() throws { XCTAssertNil(try Decoder.decode(line(1,["grant_remaining_ms":NSNull()])).remaining); XCTAssertEqual(try Decoder.decode(line(1,["grant_remaining_ms":86400000])).remaining,86400000) }
    func testUnknownAndWrongTypes() {
        for changes:[String:Any] in [["token":"SECRET"],["seq":true],["running_workers":"1"],["terminal":0],["phase":"other"],["grant_remaining_ms":-1],["grant_remaining_ms":86400001],["seq":0],["seq":9007199254740992],["running_workers":2],["starting_workers":1]] { XCTAssertThrowsError(try Decoder.decode(line(1,changes))) }
    }
    func testDuplicateAndEscapedAlias() {
        let original=String(data:line(),encoding:.utf8)!
        XCTAssertThrowsError(try Decoder.decode(Data(original.replacingOccurrences(of:"\"version\":1",with:"\"version\":1,\"version\":1").utf8)))
        XCTAssertThrowsError(try Decoder.decode(Data(original.replacingOccurrences(of:"\"version\"",with:"\"vers\\u0069on\"").utf8)))
    }
    func testExtraTrailingAndOversize() { XCTAssertThrowsError(try Decoder.decode(line()+Data("{}".utf8))); XCTAssertThrowsError(try Decoder.decode(Data(repeating:32,count:2049))) }
    func testTerminalImplications() {
        XCTAssertThrowsError(try Decoder.decode(line(1,["terminal":true])))
        XCTAssertThrowsError(try Decoder.decode(line(1,["terminal_cleanup_unknown":true])))
        XCTAssertNoThrow(try Decoder.decode(line(1,["phase":"failed","reason":"terminal_cleanup_unknown","cleanup":"unknown","terminal_cleanup_unknown":true,"terminal":true])))
    }
    func testSequenceGapsAllowedButNoReplay() {
        var state=FeedState(); state.ingest(line(),at:10); state.ingest(line(5),at:11); XCTAssertNil(state.snapshot.fault)
        state.ingest(line(5),at:12); XCTAssertEqual(state.snapshot.fault,.sequence)
        state.ingest(line(6),at:13); XCTAssertEqual(state.snapshot.fault,.sequence)
    }
    func testUnknownStickyAndTerminalNoRevival() {
        var s=FeedState(); s.ingest(line(1,["phase":"failed","reason":"terminal_cleanup_unknown","cleanup":"unknown","terminal_cleanup_unknown":true,"terminal":true]),at:10)
        s.ingest(line(2),at:11); XCTAssertTrue(s.snapshot.stickyUnknown); XCTAssertEqual(s.snapshot.fault,.sequence)
    }
    func testFreshnessAndRollback() {
        var s=FeedState(); s.ingest(line(),at:10)
        XCTAssertTrue(s.snapshot.fresh(at:12)); XCTAssertFalse(s.snapshot.fresh(at:14)); XCTAssertFalse(s.snapshot.fresh(at:9))
        s.ingest(line(2),at:9); XCTAssertEqual(s.snapshot.fault,.clock)
    }
    func testRemainingNeverExtended() {
        var s=FeedState(); s.ingest(line(),at:10); s.ingest(line(2),at:12)
        XCTAssertEqual(s.snapshot.remaining(at:12),118); XCTAssertNil(s.snapshot.remaining(at:20))
    }
    func testEOFNotCleanupEvidence() {
        var s=FeedState(); s.ingest(line(),at:10); s.fail(.eof)
        XCTAssertFalse(s.snapshot.fresh(at:10)); XCTAssertEqual(s.snapshot.frame?.cleanup,"pending")
    }
    func testNullRemainingCannotResetCeiling() {
        var s=FeedState(); s.ingest(line(),at:10)
        s.ingest(line(2,["grant_remaining_ms":NSNull()]),at:11)
        s.ingest(line(3,["grant_remaining_ms":120000]),at:12)
        XCTAssertEqual(s.snapshot.remaining(at:12),118)
    }
    func testTerminalEOFHistoryRetainedNotLive() {
        var s=FeedState(); s.ingest(line(1,["phase":"closed","reason":"deadline","cleanup":"confirmed","terminal":true,"running_workers":0,"grant_remaining_ms":0]),at:10)
        s.fail(.eof)
        XCTAssertTrue(s.snapshot.sourceClosed); XCTAssertTrue(s.snapshot.terminalObserved)
        XCTAssertEqual(s.snapshot.frame?.reason,"deadline"); XCTAssertEqual(s.snapshot.frame?.cleanup,"confirmed")
        XCTAssertFalse(s.snapshot.fresh(at:10))
    }
    func testDelayedFirstFrameRejected() {
        var s=FeedState(); s.ingest(line(),at:14)
        XCTAssertEqual(s.snapshot.fault,.stale); XCTAssertFalse(s.snapshot.fresh(at:14))
    }
    func testFutureAndBackwardSourceRejected() {
        var s=FeedState(); s.ingest(line(1,["source_continuous_ms":11000]),at:10)
        XCTAssertEqual(s.snapshot.fault,.clock)
        s=FeedState(); s.ingest(line(),at:10); s.ingest(line(2,["source_continuous_ms":9000]),at:11)
        XCTAssertEqual(s.snapshot.fault,.clock)
    }
    func testBackloggedSequenceCannotRefresh() {
        var s=FeedState(); s.ingest(line(),at:10); s.ingest(line(2),at:14)
        XCTAssertEqual(s.snapshot.fault,.stale)
    }
    func testSleepAdvanceStalesAndDoesNotExtendRemaining() {
        var s=FeedState(); s.ingest(line(),at:10)
        XCTAssertFalse(s.snapshot.fresh(at:1000)); XCTAssertNil(s.snapshot.remaining(at:1000))
    }
    func testTerminateCancelKeepsFreshnessUpdates() {
        var updates=true
        XCTAssertFalse(ViewerShutdown.attempt(join:{ false },stopUpdates:{ updates=false }))
        XCTAssertTrue(updates)
        var state=FeedState(); state.ingest(line(),at:10)
        let nextTick=updates ? 14.0 : 10.0
        XCTAssertFalse(state.snapshot.fresh(at:nextTick))
        XCTAssertTrue(ViewerShutdown.attempt(join:{ true },stopUpdates:{ updates=false }))
        XCTAssertFalse(updates)
    }
    func testConfirmedTerminalRequiresNoWorkersOrRemaining() {
        XCTAssertThrowsError(try Decoder.decode(line(1,["phase":"closed","cleanup":"confirmed","terminal":true,"grant_remaining_ms":0])))
        XCTAssertThrowsError(try Decoder.decode(line(1,["phase":"closed","cleanup":"confirmed","terminal":true,"running_workers":0])))
        XCTAssertNoThrow(try Decoder.decode(line(1,["phase":"closed","cleanup":"confirmed","terminal":true,"running_workers":0,"grant_remaining_ms":NSNull()])))
    }
    func testProductionGoEncoderInterop() throws {
        let url=URL(fileURLWithPath:#filePath).deletingLastPathComponent().appendingPathComponent("Fixtures/production-status.jsonl")
        let lines=try Data(contentsOf:url).split(separator:10)
        XCTAssertEqual(lines.count,3)
        var s=FeedState()
        for line in lines { s.ingest(Data(line),at:Double(try Decoder.decode(Data(line)).sourceMS)/1000+0.1); XCTAssertNil(s.snapshot.fault) }
        XCTAssertEqual(s.snapshot.frame?.phase,"failed")
        XCTAssertEqual(s.snapshot.frame?.cleanup,"unknown")
        XCTAssertEqual(s.snapshot.frame?.terminal,true)
        XCTAssertTrue(s.snapshot.stickyUnknown)
    }
    func testSetupStagesAndApprovalAreDistinct() {
        for phase in ["checking_config","loading_key","preparing","awaiting_approval","connecting"] {
            var s=FeedState(); s.ingest(line(1,["phase":phase,"running_workers":0,"cleanup":"not_started","grant_remaining_ms":NSNull()]),at:10)
            let f=s.snapshot.setupFeedback(at:10)
            XCTAssertTrue(f != nil); XCTAssertEqual(f?.isProcessing,phase != "awaiting_approval")
            if phase == "awaiting_approval" { XCTAssertTrue(f!.title.contains("ターミナル")); XCTAssertTrue(f!.detail.contains("ここでは承認できません")) }
        }
    }
    func testSetupMotionStopsOnStaleEOFAndFailure() {
        var s=FeedState(); s.ingest(line(1,["phase":"connecting"]),at:10)
        XCTAssertEqual(s.snapshot.setupFeedback(at:10)?.isProcessing,true)
        XCTAssertNil(s.snapshot.setupFeedback(at:14))
        s.fail(.eof); XCTAssertNil(s.snapshot.setupFeedback(at:10))
        for reason in ["config_error","key_error","preparation_error","connection_error","approval_not_completed"] {
            var failed=FeedState();failed.ingest(line(1,["phase":"failed","reason":reason,"terminal":true,"cleanup":"not_started","running_workers":0,"grant_remaining_ms":NSNull()]),at:10)
            XCTAssertNil(failed.snapshot.fault)
            XCTAssertEqual(failed.snapshot.setupFeedback(at:10)?.isProcessing,false)
            failed.fail(.eof)
            XCTAssertEqual(failed.snapshot.setupFeedback(at:20)?.isProcessing,false)
        }
    }
    func testSetupFailureCannotLabelLiveOrUnknown() {
        for changes:[String:Any] in [["reason":"config_error"],["phase":"failed","terminal":true,"reason":"key_error","cleanup":"unknown"],["phase":"failed","terminal":true,"reason":"approval_denied","running_workers":0,"grant_remaining_ms":0,"cleanup":"not_started"]] {
            XCTAssertThrowsError(try Decoder.decode(line(1,changes)))
        }
        var unknown=FeedState();unknown.ingest(line(1,["phase":"failed","reason":"terminal_cleanup_unknown","cleanup":"unknown","terminal":true,"terminal_cleanup_unknown":true]),at:10)
        XCTAssertNil(unknown.snapshot.setupFeedback(at:10))
    }
    func testSetupTransitionsAndNoPercentage() {
        var s=FeedState()
        for (i,phase) in ["checking_config","loading_key","preparing","awaiting_approval","connecting","connected","stopping"].enumerated() {
            s.ingest(line(i+1,["phase":phase,"running_workers":0,"grant_remaining_ms":NSNull()]),at:10)
            XCTAssertNil(s.snapshot.fault)
            if let f=s.snapshot.setupFeedback(at:10) {
                XCTAssertFalse(f.title.contains("%")); XCTAssertFalse(f.detail.contains("%"))
            }
        }
        XCTAssertNil(s.snapshot.setupFeedback(at:10))
        s.ingest(line(8,["phase":"closed","terminal":true,"cleanup":"confirmed","running_workers":0,"grant_remaining_ms":0]),at:10)
        XCTAssertNil(s.snapshot.setupFeedback(at:10))
    }
    func testSetupDeadlineZeroDoesNotKeepSpinning() {
        var s=FeedState();s.ingest(line(1,["phase":"connecting","grant_remaining_ms":0]),at:10)
        XCTAssertNil(s.snapshot.setupFeedback(at:10))
    }

    func testOwnedPipeCancelAndJoin() {
        let saved=dup(STDIN_FILENO); var ends:[Int32]=[0,0]; XCTAssertEqual(pipe(&ends),0)
        XCTAssertEqual(dup2(ends[0],STDIN_FILENO),STDIN_FILENO); close(ends[0])
        defer { close(ends[1]); if saved >= 0 { _=dup2(saved,STDIN_FILENO); close(saved) } }
        let reader=PipeReader(); reader.startStdin()
        let data=line(1,["source_continuous_ms":UInt64(FeedClock.now()*1000)])+Data([10]); _=data.withUnsafeBytes { write(ends[1],$0.baseAddress,data.count) }
        for _ in 0..<100 { if reader.snapshot().frame != nil { break }; usleep(10000) }
        XCTAssertEqual(reader.snapshot().frame?.seq,1)
        let before=FeedClock.now(); XCTAssertTrue(reader.stopAndJoin()); XCTAssertLessThan(FeedClock.now()-before,1)
        XCTAssertTrue(reader.stopAndJoin())
    }
}

@main struct Checks { static func main() { let tests=StatusTests(); var count=0
do { let before=failures; do { try tests.testValid() } catch { failures += 1 }; count += 1; print("testValid",before == failures ? "PASS" : "FAIL") }
do { let before=failures; do { try tests.testNullAndUpperBound() } catch { failures += 1 }; count += 1; print("testNullAndUpperBound",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testUnknownAndWrongTypes(); count += 1; print("testUnknownAndWrongTypes",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testDuplicateAndEscapedAlias(); count += 1; print("testDuplicateAndEscapedAlias",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testExtraTrailingAndOversize(); count += 1; print("testExtraTrailingAndOversize",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testTerminalImplications(); count += 1; print("testTerminalImplications",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testSequenceGapsAllowedButNoReplay(); count += 1; print("testSequenceGapsAllowedButNoReplay",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testUnknownStickyAndTerminalNoRevival(); count += 1; print("testUnknownStickyAndTerminalNoRevival",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testFreshnessAndRollback(); count += 1; print("testFreshnessAndRollback",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testRemainingNeverExtended(); count += 1; print("testRemainingNeverExtended",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testEOFNotCleanupEvidence(); count += 1; print("testEOFNotCleanupEvidence",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testNullRemainingCannotResetCeiling(); count += 1; print("testNullRemainingCannotResetCeiling",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testTerminalEOFHistoryRetainedNotLive(); count += 1; print("testTerminalEOFHistoryRetainedNotLive",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testDelayedFirstFrameRejected(); count += 1; print("testDelayedFirstFrameRejected",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testFutureAndBackwardSourceRejected(); count += 1; print("testFutureAndBackwardSourceRejected",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testBackloggedSequenceCannotRefresh(); count += 1; print("testBackloggedSequenceCannotRefresh",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testSleepAdvanceStalesAndDoesNotExtendRemaining(); count += 1; print("testSleepAdvanceStalesAndDoesNotExtendRemaining",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testTerminateCancelKeepsFreshnessUpdates(); count += 1; print("testTerminateCancelKeepsFreshnessUpdates",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testConfirmedTerminalRequiresNoWorkersOrRemaining(); count += 1; print("testConfirmedTerminalRequiresNoWorkersOrRemaining",before == failures ? "PASS" : "FAIL") }
do { let before=failures; do { try tests.testProductionGoEncoderInterop() } catch { failures += 1 }; count += 1; print("testProductionGoEncoderInterop",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testOwnedPipeCancelAndJoin(); count += 1; print("testOwnedPipeCancelAndJoin",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testSetupStagesAndApprovalAreDistinct(); count += 1; print("testSetupStagesAndApprovalAreDistinct",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testSetupMotionStopsOnStaleEOFAndFailure(); count += 1; print("testSetupMotionStopsOnStaleEOFAndFailure",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testSetupFailureCannotLabelLiveOrUnknown(); count += 1; print("testSetupFailureCannotLabelLiveOrUnknown",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testSetupTransitionsAndNoPercentage(); count += 1; print("testSetupTransitionsAndNoPercentage",before == failures ? "PASS" : "FAIL") }
do { let before=failures; tests.testSetupDeadlineZeroDoesNotKeepSpinning(); count += 1; print("testSetupDeadlineZeroDoesNotKeepSpinning",before == failures ? "PASS" : "FAIL") }
print("CHECKS",count,"FAILURES",failures); exit(failures == 0 ? 0 : 1) } }
