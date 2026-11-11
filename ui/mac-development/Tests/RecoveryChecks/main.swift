import Foundation
import DevelopmentCore
@testable import DevelopmentProcess

var checks = 0
var failures: [String] = []
func check(_ value: @autoclosure () -> Bool, _ name: String) {
    checks += 1
    if !value() { failures.append(name); print("FAILED: \(name)") }
}
// Lifecycle fixture has no Process, pipes, network, app, or store side effects.
// Feed remains the production parser, including its monotonic unknown state.
final class Owner: RecoveryOwner {
    var feed = Feed()
    var launched = false
    var launchFailed = false
    var reaped = false, eof = false, exitOK = false
    var starts = 0, closes = 0, polls = 0
    let diagnostic: String
    init(_ diagnostic: String) { self.diagnostic = diagnostic }
    var mayQuit: Bool {
        !launched || (feed.closeConfirmed && !feed.cleanupInvalid && reaped && eof && exitOK)
    }
    func start() { starts += 1; if !launchFailed { launched = true } }
    func intent(_ intent: String) { _ = feed.command(intent, at: 5000) }
    func close() { if feed.requestClose() != nil { closes += 1 } }
    func poll() { polls += 1 }
}
func terminal(_ outcome: String) -> Data {
    Data("{\"protocolVersion\":1,\"type\":\"closed\",\"sourceContinuousMs\":5000,\"outcome\":\"\(outcome)\"}".utf8)
}
func completed(_ name: String = "collected original") -> Owner {
    let owner = Owner(name); owner.launched = true
    owner.feed.fail("protocol-invalid")
    owner.feed.ingest(terminal("confirmed"), at: 5000)
    owner.reaped = true; owner.eof = true; owner.exitOK = true
    return owner
}
let initial = Owner("initial owner")
var prematureOwners = 0
let initialController = RecoveryController(owner: initial) { prematureOwners += 1; return Owner("forbidden") }
check(initialController.preparation == .initial && initialController.mayQuit, "unstarted controller offers initial preparation")
check(initialController.prepare() && initial.starts == 1 && prematureOwners == 0, "initial preparation uses original owner once")
check(!initialController.prepare() && initial.starts == 1, "repeated initial preparation is blocked")
initialController.close(); initialController.close(); initialController.poll()
check(initial.closes == 1 && initial.polls == 1 && prematureOwners == 0, "close and poll never create another owner")
check(initialController.preparation == .blocked, "closing owner cannot be replaced")

let cancelled = Owner("cancelled before prepare")
var cancelledReplacements = 0
let cancelledController = RecoveryController(owner: cancelled) { cancelledReplacements += 1; return Owner("forbidden") }
cancelledController.close()
check(cancelledController.preparation == .blocked, "close before preparation seals initial admission")
check(!cancelledController.prepare() && cancelled.starts == 0 && cancelledReplacements == 0, "cancelled unlaunched owner never starts without a viable close")

let original = completed(), replacement = Owner("fresh owner")
var made = 0
let recovery = RecoveryController(owner: original) { made += 1; return replacement }
check(recovery.preparation == .freshAfterCleanup, "complete original cleanup offers explicit fresh preparation")
check(made == 0, "observation alone never creates a new owner")
check(recovery.prepare(), "fresh preparation succeeds after complete original cleanup")
check(made == 1 && replacement.starts == 1, "explicit recovery creates and starts one new owner")
check(recovery.previousDiagnostic == original.diagnostic, "prior diagnostic survives replacement")
check(original.feed.fault && original.feed.closeConfirmed, "old fault and terminal are never reset")
check(!recovery.prepare() && made == 1, "repeated prepare does not replace a running fresh owner")

let failed = Owner("prelaunch failure"); failed.launchFailed = true
let retried = Owner("retried owner")
let launchRetry = RecoveryController(owner: failed) { retried }
check(launchRetry.preparation == .retryLaunch, "verified prelaunch failure offers explicit retry")
check(launchRetry.prepare() && retried.starts == 1, "launch retry uses fresh owner")
check(failed.launchFailed && failed.starts == 0, "launch failure evidence stays on original")

let prior = completed("collected before launch failure")
let failedReplacement = Owner("replacement failed before launch"); failedReplacement.launchFailed = true
let successfulRetry = Owner("replacement retried")
var retryFactories = 0
let replacementRetry = RecoveryController(owner: prior) {
    retryFactories += 1
    return retryFactories == 1 ? failedReplacement : successfulRetry
}
check(!replacementRetry.prepare() && failedReplacement.starts == 1, "replacement launch failure does not claim preparation")
check(replacementRetry.preparation == .retryLaunch && retryFactories == 1, "failed replacement waits for explicit retry")
check(replacementRetry.prepare() && retryFactories == 2 && successfulRetry.starts == 1, "second explicit preparation uses another fresh owner")
check(replacementRetry.previousDiagnostic == failedReplacement.diagnostic && prior.feed.fault, "retry retains latest failure diagnostic without rewriting original")

for missing in ["terminal", "reap", "EOF", "exit0"] {
    let incomplete = completed(missing)
    if missing == "terminal" { incomplete.feed = Feed(); incomplete.feed.fail() }
    if missing == "reap" { incomplete.reaped = false }
    if missing == "EOF" { incomplete.eof = false }
    if missing == "exit0" { incomplete.exitOK = false }
    var replacements = 0
    let blocked = RecoveryController(owner: incomplete) { replacements += 1; return Owner("forbidden") }
    check(blocked.preparation == .blocked && !blocked.prepare(), "missing \(missing) blocks fresh owner")
    check(replacements == 0 && !blocked.mayQuit, "missing \(missing) cannot manufacture cleanup")
    blocked.close(); blocked.close(); blocked.poll()
    check(incomplete.closes == (missing == "terminal" ? 1 : 0) && incomplete.polls == 1, "original owner retained for \(missing)")
}
let unknown = completed("unresolved original")
unknown.feed.invalidateCleanup("owner-result-unknown")
var unknownReplacements = 0
let unresolved = RecoveryController(owner: unknown) { unknownReplacements += 1; return Owner("forbidden") }
for _ in 0..<3 {
    unknown.feed.ingest(terminal("confirmed"), at: 5000)
    check(!unresolved.prepare() && !unresolved.mayQuit, "EOF/reap/exit0 and repeated clicks never erase unknown")
}
check(unknownReplacements == 0 && unknown.feed.cleanupInvalid, "unknown retains original owner and truth")
unresolved.poll(); unresolved.close(); unresolved.close()
check(unknownReplacements == 0 && unknown.polls == 1, "unknown polls and close never create a new owner")
let inconsistent = Owner("inconsistent unlaunched owner")
inconsistent.feed.invalidateCleanup("owner-result-unknown")
let guarded = RecoveryController(owner: inconsistent) { Owner("forbidden") }
check(guarded.preparation == .blocked && !guarded.prepare(), "cleanup invalid blocks preparation even before launch")
check(unresolved.previousDiagnostic == nil && unresolved.diagnostic == "unresolved original", "unresolved diagnostic stays current")

if !failures.isEmpty { print("RecoveryChecks: \(failures.count) failed / \(checks) checks"); exit(1) }
print("RecoveryChecks: \(checks) passed; pure lifecycle fixtures only; no UI departure, host or storage operation")
