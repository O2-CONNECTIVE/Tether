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
}
