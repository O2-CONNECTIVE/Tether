import Foundation
import DevelopmentCore
@testable import DevelopmentProcess

func recoveryStatus(standby: String = "off", work: String = "idle", cleanup: String = "confirmed", canOn: Bool = true) -> Data {
    Data("""
    {"protocolVersion":1,"type":"status","sourceContinuousMs":5000,"snapshot":{"protocolVersion":1,"source":"resident-owner","state":"source-observation","revision":1,"validForMs":3000,"standby":"\(standby)","registration":"disconnected","caller":null,"work":"\(work)","cleanup":"\(cleanup)","canRequestOn":\(canOn),"canRequestOff":false,"transition":null}}
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

    // The helper's own 15-minute expiry closes the original owner without a GUI
    // request or fault. With all four proofs, quit and fresh preparation use the
    // existing paths; nothing here relaxes the proof itself. The screen says why
    // the host stopped and what to do next, without a protocol change.
    func expiredOwner(launchedAt: UInt64 = 4_000, closeAt: UInt64 = 905_000, requested: Bool = false) -> Owner {
        let owner = Owner("expired original"); owner.launched = true; owner.launchedAt = launchedAt
        owner.feed.ingest(recoveryStatus(), at: 5000)
        if requested { owner.close() }
        owner.feed.ingest(terminal("confirmed", at: closeAt), at: closeAt)
        owner.reaped = true; owner.eof = true; owner.exitOK = true
        return owner
    }
    let expired = expiredOwner()
    var expiryOwners = 0
    let expiry = RecoveryController(owner: expired) { expiryOwners += 1; return Owner("after expiry") }
    let afterExpiry = expiry.presentation(at: 905_000)
    check(!expired.feed.fault && afterExpiry.canPrepare && afterExpiry.quitTitle == "終了", "UI confirmed expiry offers preparation and plain quit")
    check(afterExpiry.status == "15分の期限で停止しました" && afterExpiry.detail.contains("起動から15分") && afterExpiry.detail.contains("停止・回収を確認しました") && afterExpiry.detail.contains("「もう一度準備」"), "UI confirmed expiry names the 15-minute limit, the proof and the next action")
    // Only an unrequested close received at least 15 minutes after this GUI
    // started the helper is called expiry; viewing it later never changes that.
    let early = RecoveryController(owner: expiredOwner(closeAt: 5_000)) { Owner("forbidden") }
    check(early.presentation(at: 905_000).status == "停止・回収を確認しました", "UI early unrequested close is not called expiry, even when viewed later")
    let stopped = RecoveryController(owner: expiredOwner(requested: true)) { Owner("forbidden") }
    check(stopped.presentation(at: 905_000).status == "停止・回収を確認しました", "UI a requested stop after 15 minutes is not called expiry")
    let unknownStart = expiredOwner(); unknownStart.launchedAt = nil
    check(RecoveryController(owner: unknownStart) { Owner("forbidden") }.presentation(at: 905_000).status == "停止・回収を確認しました", "UI without a launch time never infers expiry")
    check(expiry.prepare() && expiryOwners == 1 && expiry.previousDiagnostic == "expired original", "UI confirmed expiry prepares one fresh owner")
    let expiredQuit = expiredOwner()
    let expiryQuit = RecoveryController(owner: expiredQuit) { Owner("forbidden") }
    expiryQuit.requestQuit()
    check(expiryQuit.shouldTerminate && expiredQuit.closes == 0, "UI quit after confirmed expiry ends without another close")

    let transition = Owner("pending ON"); transition.launched = true
    transition.feed.ingest(recoveryStatus(), at: 5000)
    let transitionController = RecoveryController(owner: transition) { Owner("forbidden") }
    transitionController.intent("on")
    let pending = transitionController.presentation(at: 5000)
    check(pending.status == "待機を ON に変更中" && !pending.canOn && !pending.canOff, "UI pending ON never presents previous OFF as completed")
    check(!pending.detail.contains("受け付けました"), "UI pending before receipt does not invent acceptance")
    transition.feed.ingest(Data("{\"protocolVersion\":1,\"type\":\"receipt\",\"receipt\":{\"protocolVersion\":1,\"requestId\":1,\"intent\":\"on\",\"disposition\":\"accepted\"}}".utf8), at: 5000)
    transition.feed.ingest(Data("""
    {"protocolVersion":1,"type":"status","sourceContinuousMs":5000,"snapshot":{"protocolVersion":1,"source":"resident-owner","state":"source-observation","revision":2,"validForMs":3000,"standby":"unknown","registration":"connecting","caller":null,"work":"idle","cleanup":"confirmed","canRequestOn":false,"canRequestOff":false,"transition":{"requestId":1,"intent":"on","outcome":"pending"}}}
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
    check(permitted.run == "コマンド実行: 許可中（残り約15分）" && permitted.canDeny, "UI shows accepted permission lifetime and offers revoke")
    permissionController.requestQuit()
    let quittingPermission = permissionController.presentation(at: 5000)
    check(!quittingPermission.canAllow && !quittingPermission.canDeny, "UI quit request disables both permission controls")
}
