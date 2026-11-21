import Foundation
import DevelopmentCore
@testable import DevelopmentProcess

func recoveryStatus(standby: String = "off", work: String = "idle", cleanup: String = "confirmed", canOn: Bool = true, until: String = "null", ending: Bool = false, expired: Bool = false) -> Data {
    Data("""
    {"protocolVersion":1,"type":"status","sourceContinuousMs":5000,"snapshot":{"protocolVersion":1,"source":"resident-owner","state":"source-observation","revision":1,"validForMs":3000,"standby":"\(standby)","registration":"disconnected","caller":null,"work":"\(work)","cleanup":"\(cleanup)","canRequestOn":\(canOn),"canRequestOff":false,"transition":null,"standbyUntil":\(until),"standbyEndingSoon":\(ending),"standbyExpired":\(expired)}}
    """.utf8)
}

func checkPresentation() {
    let first = Owner("first")
    let firstController = RecoveryController(owner: first) { Owner("forbidden") }
    let initial = firstController.presentation(at: 5000)
    check(initial.canPrepare && initial.prepareTitle == "開発ホストを準備", "UI initial preparation is available")
    check(!initial.canStop && initial.quitTitle == "終了", "UI initial quit does not imply a running host")

    let failed = Owner("failed to launch"); failed.launchFailed = true
    let retry = RecoveryController(owner: failed) { Owner("fresh retry") }
    let failure = retry.presentation(at: 5000)
    check(failure.canPrepare && failure.prepareTitle == "もう一度準備", "UI launch failure offers explicit retry")
    check(failure.detail.contains("もう一度準備") && failure.diagnostic.contains("failed to launch"), "UI launch error explains recovery and preserves diagnostics")

    let collected = completed("collected owner"), fresh = Owner("new owner")
    var freshOwners = 0
    let recovery = RecoveryController(owner: collected) { freshOwners += 1; return fresh }
    let ready = recovery.presentation(at: 5000)
    check(ready.canPrepare && ready.prepareTitle == "もう一度準備", "UI collected owner offers preparation instead of exit-only")
    check(ready.detail.contains("もう一度準備") && ready.quitTitle == "終了", "UI collected instructions match actual actions")
    check(recovery.prepare() && freshOwners == 1, "UI action uses the same recovery controller")
    let preparing = recovery.presentation(at: 5000)
    check(!preparing.canPrepare && !preparing.canOn && preparing.status == "最新の状態を確認中", "UI fresh owner never inherits prior ready or ON state")
    check(preparing.canStop && preparing.diagnostic.contains("前回: collected owner"), "UI can stop without quitting and retains prior diagnostic")
    recovery.close()
    let stopping = recovery.presentation(at: 5000)
    check(!stopping.canStop && !stopping.canPrepare && !recovery.quitRequested, "UI host stop keeps window without permitting premature preparation")
    fresh.feed.ingest(terminal("confirmed"), at: 5000)
    fresh.reaped = true; fresh.eof = true; fresh.exitOK = true
    check(recovery.presentation(at: 5000).canPrepare && !recovery.shouldTerminate, "UI host stop returns to preparation without automatic app quit")

    let unsafe = completed("unknown owner"); unsafe.feed.invalidateCleanup("owner-result-unknown")
    let unknown = RecoveryController(owner: unsafe) { Owner("forbidden") }
    let blocked = unknown.presentation(at: 5000)
    check(blocked.status == "回収未確認・再準備できません" && !blocked.canPrepare, "UI unknown states unresolved truth and blocks retry")
    check(blocked.detail.contains("診断情報") && blocked.diagnostic.contains("unknown owner"), "UI unknown gives diagnosis instead of false recovery")
    unknown.requestQuit(); unknown.requestQuit()
    check(!unknown.shouldTerminate && !unknown.presentation(at: 5000).canPrepare, "UI quit cannot bypass unknown")
    // Quit and Cmd-Q share this refusal. An unchanged screen
    // was reported as no response, so the refusal itself must become visible.
    let refused = unknown.presentation(at: 5000)
    check(!blocked.detail.contains("終了できません"), "UI unknown claims no refusal before quit is asked")
    check(refused.status == blocked.status && refused.detail.hasPrefix("回収を確認できないため終了できません。"), "UI refused quit on unknown states why the app stays open")

    // Only this process remembers an unresolved owner, so the close button hides
    // the window instead of quitting: no quit request and no state change, so the
    // app, owner and unknown stay and a Dock reopen renders the same state. Quit
    // and Cmd-Q still refuse with the reason. (The AppKit hide/reopen calls
    // themselves are wiring in main.swift and are not exercised here.)
    // Not yet closing, so a close sent by hiding would be visible here.
    let hidden = Owner("hidden unknown"); hidden.launched = true
    hidden.feed.ingest(recoveryStatus(), at: 5000); hidden.feed.invalidateCleanup("owner-result-unknown")
    var hiddenReplacements = 0
    let hiding = RecoveryController(owner: hidden) { hiddenReplacements += 1; return Owner("forbidden") }
    let beforeHide = hiding.presentation(at: 5000)
    check(hiding.closeWindow() && hiding.closeWindow() && !hiding.quitRequested && !hiding.shouldTerminate, "UI close button on unknown hides the window without a quit request")
    check(hidden.closes == 0 && !hidden.feed.closing, "UI hiding sends no close to the original owner")
    let reopened = hiding.presentation(at: 5000)
    check(reopened.status == beforeHide.status && reopened.detail == beforeHide.detail && reopened.diagnostic == beforeHide.diagnostic, "UI hiding leaves the unresolved state a Dock reopen renders unchanged")
    check(!reopened.canPrepare && !hiding.prepare() && hiddenReplacements == 0 && hidden.feed.cleanupInvalid && !hiding.mayQuit, "UI hidden unknown stays unconfirmed and blocks a new owner")
    hiding.requestQuit()
    check(!hiding.shouldTerminate && hiding.presentation(at: 5000).detail.hasPrefix("回収を確認できないため終了できません。"), "UI quit after hiding still refuses with the reason")
    let awaiting = Owner("awaiting proof"); awaiting.launched = true; awaiting.feed.fail()
    for (name, owner, ends) in [("awaiting proof", awaiting, false), ("collected", completed(), true), ("unstarted", Owner("unstarted"), true)] {
        let closing = RecoveryController(owner: owner) { Owner("forbidden") }
        check(!closing.closeWindow() && closing.quitRequested && closing.shouldTerminate == ends, "UI close button on \(name) still asks to quit")
    }
    check(awaiting.closes == 1, "UI close button while awaiting proof asks the original owner to close once")

    // A close the helper makes without any GUI request or fault, once all four
    // proofs are present, uses the existing quit and fresh-preparation paths;
    // nothing here relaxes the proof itself.
    func closedOwner(requested: Bool = false) -> Owner {
        let owner = Owner("closed original"); owner.launched = true; owner.launchedAt = 4_000
        owner.feed.ingest(recoveryStatus(), at: 5000)
        if requested { owner.close() }
        owner.feed.ingest(terminal("confirmed", at: 905_000), at: 905_000)
        owner.reaped = true; owner.eof = true; owner.exitOK = true
        return owner
    }
    let closed = closedOwner()
    var closedOwners = 0
    let closedController = RecoveryController(owner: closed) { closedOwners += 1; return Owner("after close") }
    let afterClose = closedController.presentation(at: 905_000)
    check(!closed.feed.fault && afterClose.canPrepare && afterClose.quitTitle == "終了", "UI confirmed close offers preparation and plain quit")
    check(afterClose.status == "停止・回収を確認しました" && afterClose.detail.contains("「もう一度準備」"), "UI confirmed close names the proof and the next action")
    check(closedController.prepare() && closedOwners == 1 && closedController.previousDiagnostic == "closed original", "UI confirmed close prepares one fresh owner")
    let closedQuit = closedOwner()
    let quitController = RecoveryController(owner: closedQuit) { Owner("forbidden") }
    quitController.requestQuit()
    check(quitController.shouldTerminate && closedQuit.closes == 0, "UI quit after confirmed close ends without another close")
    check(RecoveryController(owner: closedOwner(requested: true)) { Owner("forbidden") }.presentation(at: 905_000).status == "停止・回収を確認しました", "UI a requested stop reads the same")

    // The owner stopped standby because its time ran out: said as such, and
    // only with the full OFF proof. A flag alone never hides unfinished cleanup.
    func observed(_ data: Data) -> RecoveryPresentation {
        let owner = Owner("observed"); owner.launched = true
        owner.feed.ingest(data, at: 5000)
        return RecoveryController(owner: owner) { Owner("forbidden") }.presentation(at: 5000)
    }
    let expiredStop = observed(recoveryStatus(expired: true))
    check(expiredStop.status == "期限で停止しました" && expiredStop.detail.contains("期限が来たため") && expiredStop.detail.contains("待機を ON にし"), "UI standby expiry says why standby is OFF and what to do")
    check(observed(recoveryStatus()).status == "待機は OFF", "UI a plain OFF is not called expiry")
    check(observed(recoveryStatus(work: "stopping", cleanup: "pending", canOn: false, expired: true)).status != "期限で停止しました", "UI expiry is not claimed before cleanup is confirmed")

    let transition = Owner("pending ON"); transition.launched = true
    transition.feed.ingest(recoveryStatus(), at: 5000)
    let transitionController = RecoveryController(owner: transition) { Owner("forbidden") }
    transitionController.intent("on", hours: 2)
    let pending = transitionController.presentation(at: 5000)
    check(pending.status == "待機を ON に変更中" && !pending.canOn && !pending.canOff, "UI pending ON never presents previous OFF as completed")
    check(!pending.detail.contains("受け付けました"), "UI pending before receipt does not invent acceptance")
    transition.feed.ingest(Data("{\"protocolVersion\":1,\"type\":\"receipt\",\"receipt\":{\"protocolVersion\":1,\"requestId\":1,\"intent\":\"on\",\"disposition\":\"accepted\"}}".utf8), at: 5000)
    transition.feed.ingest(Data("""
    {"protocolVersion":1,"type":"status","sourceContinuousMs":5000,"snapshot":{"protocolVersion":1,"source":"resident-owner","state":"source-observation","revision":2,"validForMs":3000,"standby":"unknown","registration":"connecting","caller":null,"work":"idle","cleanup":"confirmed","canRequestOn":false,"canRequestOff":false,"transition":{"requestId":1,"intent":"on","outcome":"pending"},"standbyUntil":null,"standbyEndingSoon":false,"standbyExpired":false}}
    """.utf8), at: 5000)
    let connecting = transitionController.presentation(at: 5000)
    check(!transition.feed.fault && connecting.status == "接続を確認中・待機 ON は未確認", "UI accepted pending unknown standby is connecting, not cleanup unknown")
    check(!connecting.canOn && !connecting.canOff && !connecting.canPrepare, "UI connecting never allows another operation or owner")

    let onOwner = Owner("pending OFF"); onOwner.launched = true
    let onData = String(decoding: recoveryStatus(standby: "on", canOn: false), as: UTF8.self)
        .replacingOccurrences(of: "\"canRequestOff\":false", with: "\"canRequestOff\":true")
    onOwner.feed.ingest(Data(onData.utf8), at: 5000)
    let offController = RecoveryController(owner: onOwner) { Owner("forbidden") }
    offController.intent("off")
    let pendingOff = offController.presentation(at: 5000)
    check(!onOwner.feed.fault && pendingOff.status == "待機を OFF に変更中", "UI pending OFF never reports completed standby")
    check(!pendingOff.canOn && !pendingOff.canOff, "UI pending OFF rejects repeated actions")

    for standby in ["stopping", "off"] {
        let stoppingOwner = Owner("stopping owner"); stoppingOwner.launched = true
        stoppingOwner.feed.ingest(recoveryStatus(standby: standby, work: "stopping", cleanup: "pending", canOn: false), at: 5000)
        let stoppingController = RecoveryController(owner: stoppingOwner) { Owner("forbidden") }
        let observed = stoppingController.presentation(at: 5000)
        check(!stoppingOwner.feed.fault && observed.status != "待機は OFF" && !observed.canPrepare, "UI \(standby) with pending cleanup is not a successful OFF")
        check(!stoppingController.presentation(at: 8000).canOn && stoppingController.presentation(at: 8000).status == "最新の状態を確認中", "UI expired \(standby) never reports success")
    }

    let waiting = Owner("awaiting original cleanup"); waiting.launched = true
    var forbiddenOwners = 0
    let quitting = RecoveryController(owner: waiting) { forbiddenOwners += 1; return Owner("forbidden") }
    quitting.requestQuit(); quitting.requestQuit()
    check(quitting.quitRequested && !quitting.shouldTerminate && waiting.closes == 1, "UI repeated quit requests close original once")
    waiting.feed.ingest(terminal("confirmed"), at: 5000)
    waiting.reaped = true; waiting.eof = true; waiting.exitOK = true
    check(quitting.shouldTerminate, "UI quit waits for complete original proof")
    check(!quitting.presentation(at: 5000).canPrepare && !quitting.prepare() && forbiddenOwners == 0, "UI late prepare cannot race pending quit after proof arrives")
    check(!quitting.presentation(at: 5000).canStop, "UI pending quit does not offer another host stop")

    let beforeLaunch = Owner("unstarted quitting owner")
    let exitBeforeLaunch = RecoveryController(owner: beforeLaunch) { Owner("forbidden") }
    exitBeforeLaunch.requestQuit()
    check(exitBeforeLaunch.shouldTerminate && !exitBeforeLaunch.prepare() && beforeLaunch.starts == 0, "UI quit before launch blocks a queued prepare action")

    // OFF must never look like a path to command execution permission.
    let permissionOwner = Owner("run permission"); permissionOwner.launched = true
    permissionOwner.feed.ingest(recoveryStatus(), at: 5000)
    let permissionController = RecoveryController(owner: permissionOwner) { Owner("forbidden") }
    check(!permissionController.presentation(at: 5000).canAllow, "UI cannot allow command execution while standby is OFF")
    let permissionOn = String(decoding: recoveryStatus(standby: "on", canOn: false), as: UTF8.self)
        .replacingOccurrences(of: "\"canRequestOff\":false", with: "\"canRequestOff\":true")
    permissionOwner.feed = Feed(); permissionOwner.feed.ingest(Data(permissionOn.utf8), at: 5000)
    check(permissionController.presentation(at: 5000).canAllow, "UI permits opening command permission only while standby is ON")
    _ = permissionOwner.feed.command("allow", at: 5000)
    permissionOwner.feed.ingest(Data("{\"protocolVersion\":1,\"type\":\"receipt\",\"receipt\":{\"protocolVersion\":1,\"requestId\":1,\"intent\":\"allow-run\",\"disposition\":\"accepted\"}}".utf8), at: 5000)
    let permitted = permissionController.presentation(at: 5000)
    check(permitted.run == "コマンド実行: 許可中（待機を OFF にするまで）" && permitted.canDeny, "UI an accepted allow shows permission with no end time and offers revoke")
    // The end time belongs to standby, not to the permission.
    let standbyOn = String(decoding: recoveryStatus(standby: "on", canOn: false, until: "1800000000000"), as: UTF8.self)
        .replacingOccurrences(of: "\"canRequestOff\":false", with: "\"canRequestOff\":true")
        .replacingOccurrences(of: "\"registration\":\"disconnected\"", with: "\"registration\":\"authenticated\"")
        .replacingOccurrences(of: "\"revision\":1", with: "\"revision\":2")
    permissionOwner.feed.ingest(Data(standbyOn.utf8), at: 5000)
    let standing = permissionController.presentation(at: 5000)
    check(standing.status.hasPrefix("待機中（") && standing.status.contains("/") && standing.status.hasSuffix("まで）") && standing.run == permitted.run && standing.canExtend, "UI shows standby's end time and offers extend")
    let ending = Owner("ending"); ending.launched = true
    ending.feed.ingest(Data(standbyOn.replacingOccurrences(of: "\"standbyEndingSoon\":false", with: "\"standbyEndingSoon\":true").replacingOccurrences(of: "\"revision\":2", with: "\"revision\":1").utf8), at: 5000)
    let endingStatus = RecoveryController(owner: ending) { Owner("forbidden") }.presentation(at: 5000).status
    check(endingStatus.hasPrefix("まもなく期限（") && endingStatus.contains("まで）。延長するか作業を終えてください"), "UI ending soon says how to continue")
    check(!endingStatus.contains("15分"), "UI no fixed 15-minute wording remains")
    permissionOwner.feed.ingest(Data(String(decoding: recoveryStatus(), as: UTF8.self).replacingOccurrences(of: "\"revision\":1", with: "\"revision\":3").utf8), at: 5000)
    check(permissionController.presentation(at: 5000).run == "コマンド実行: 許可なし（待機 ON だけでは実行されません）", "UI standby no longer ON drops the permission")
    permissionOwner.feed.ingest(Data(String(decoding: recoveryStatus(standby: "on", canOn: false, until: "1800000000000"), as: UTF8.self).replacingOccurrences(of: "\"canRequestOff\":false", with: "\"canRequestOff\":true").replacingOccurrences(of: "\"revision\":1", with: "\"revision\":4").utf8), at: 5000)
    check(!permissionController.presentation(at: 5000).run.contains("許可中"), "UI a later standby ON does not restore the permission")
    permissionController.requestQuit()
    let quittingPermission = permissionController.presentation(at: 5000)
    check(!quittingPermission.canAllow && !quittingPermission.canDeny && !quittingPermission.canExtend, "UI quit request disables permission and extend controls")

    // #466: reconnecting retries by itself; needs_action does not, so it says
    // what to fix and that OFF then ON retries, instead of "still connecting".
    func connection(_ registration: String, reason: String? = nil) -> RecoveryPresentation {
        let owner = Owner("connection"); owner.launched = true
        let line = String(decoding: recoveryStatus(standby: "on", canOn: false), as: UTF8.self)
            .replacingOccurrences(of: "\"canRequestOff\":false", with: "\"canRequestOff\":true")
            .replacingOccurrences(of: "\"registration\":\"disconnected\"", with: "\"registration\":\"\(registration)\"" + (reason.map { ",\"registrationReason\":\"\($0)\"" } ?? ""))
        owner.feed.ingest(Data(line.utf8), at: 5000)
        return RecoveryController(owner: owner) { Owner("forbidden") }.presentation(at: 5000)
    }
    let reconnecting = connection("reconnecting")
    check(reconnecting.status == "待機 ON・再接続中" && reconnecting.detail.hasPrefix("接続: 再接続中 ／") && reconnecting.canOff, "UI reconnecting says it is retrying and keeps OFF")
    for (reason, hint) in [("identity_refused", "ペア登録"), ("connection_occupied", "別の接続"), ("gateway_refused", "gateway")] {
        let needs = connection("needs_action", reason: reason)
        check(needs.status == "待機 ON・要対応" && needs.canOff && !needs.canOn && !needs.canExtend && !needs.canAllow, "UI needs_action \(reason) is not shown as still connecting")
        check(needs.detail.contains(hint) && needs.detail.hasSuffix("待機を OFF にして ON にし直してください。"), "UI needs_action \(reason) says what to fix and how to retry")
    }
}
