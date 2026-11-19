import DevelopmentCore

// Internal fixture seam, never an environment, path or runtime override.
protocol RecoveryOwner: AnyObject {
    var feed: Feed { get }
    var launched: Bool { get }
    var launchFailed: Bool { get }
    var launchedAt: UInt64? { get }
    var mayQuit: Bool { get }
    var diagnostic: String { get }
    func start()
    func intent(_ intent: String, hours: Int?)
    func close()
    func poll()
}
extension ResidentProcess: RecoveryOwner {}

// Like ResidentProcess, all access occurs on the AppKit main thread.
// A controller does not recover an old grant or clear old cleanup uncertainty.
// UI-only departure and its cross-instance durable blockade are not implemented.
public final class RecoveryController {
    public enum Preparation: Equatable {
        case initial, retryLaunch, freshAfterCleanup, blocked
    }
    private var owner: any RecoveryOwner
    private let makeOwner: () -> any RecoveryOwner
    public private(set) var previousDiagnostic: String?
    public private(set) var quitRequested = false
    public init() {
        owner = ResidentProcess()
        makeOwner = { ResidentProcess() }
    }
    init(owner: any RecoveryOwner, makeOwner: @escaping () -> any RecoveryOwner) {
        self.owner = owner
        self.makeOwner = makeOwner
    }
    public var feed: Feed { owner.feed }
    public var launched: Bool { owner.launched }
    public var launchFailed: Bool { owner.launchFailed }
    public var launchedAt: UInt64? { owner.launchedAt }
    // Close button; returns whether the window may close. Only this process
    // remembers an unresolved owner, so while collection is unconfirmed the
    // window hides with no quit request and no state change: the app, owner and
    // unknown stay, and Quit still refuses. Otherwise it asks to quit as before.
    public func closeWindow() -> Bool {
        if owner.feed.cleanupInvalid { return true }
        requestQuit()
        return false
    }
    public var mayQuit: Bool { owner.mayQuit }
    public var diagnostic: String { owner.diagnostic }
    public var preparation: Preparation {
        guard !quitRequested, !owner.feed.cleanupInvalid else { return .blocked }
        if !owner.launched {
            guard !owner.feed.closing else { return .blocked }
            return owner.launchFailed ? .retryLaunch : .initial
        }
        return owner.mayQuit ? .freshAfterCleanup : .blocked
    }
    @discardableResult public func prepare() -> Bool {
        let action = preparation
        guard action != .blocked else { return false }
        if action == .retryLaunch || action == .freshAfterCleanup {
            previousDiagnostic = owner.diagnostic
            owner = makeOwner()
        }
        owner.start()
        return owner.launched
    }
    public func intent(_ intent: String, hours: Int? = nil) {
        guard !quitRequested else { return }
        owner.intent(intent, hours: hours)
    }
    public func close() { owner.close() }
    public var shouldTerminate: Bool { quitRequested && mayQuit }
    public func requestQuit() {
        quitRequested = true
        if !mayQuit { owner.close() }
    }
    public func poll() { owner.poll() }
}
