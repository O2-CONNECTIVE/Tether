import DevelopmentCore

// Internal fixture seam, never an environment, path or runtime override.
protocol RecoveryOwner: AnyObject {
    var feed: Feed { get }
    var launched: Bool { get }
    var launchFailed: Bool { get }
    var mayQuit: Bool { get }
    var diagnostic: String { get }
    func start()
    func intent(_ intent: String)
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
    public var mayQuit: Bool { owner.mayQuit }
    public var diagnostic: String { owner.diagnostic }
    public var preparation: Preparation {
        guard !owner.feed.cleanupInvalid else { return .blocked }
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
    public func intent(_ intent: String) { owner.intent(intent) }
    public func close() { owner.close() }
    public func poll() { owner.poll() }
}
