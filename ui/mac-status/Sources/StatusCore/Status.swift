import Foundation

public enum FeedFault: String { case unavailable, stale, malformed, clock, sequence, eof, read, rate, stopped }
public struct Frame: Equatable {
    public let sourceMS: UInt64
    public let seq: UInt64
    public let phase: String
    public let reason: String
    public let remaining: Int?
    public let starting: Int
    public let running: Int
    public let stopping: Int
    public let cleanup: String
    public let terminalUnknown: Bool
    public let terminal: Bool
}
private enum Atom { case text(String), integer(UInt64), boolean(Bool), null }
private struct FlatJSON {
    let b: [UInt8]; var i = 0
    mutating func white() { while i < b.count && [9, 10, 13, 32].contains(b[i]) { i += 1 } }
    mutating func byte(_ n: UInt8) throws { white(); guard i < b.count, b[i] == n else { throw Invalid.input }; i += 1 }
    mutating func string() throws -> String {
        try byte(34); let start = i
        while i < b.count && b[i] != 34 {
            // Schema contains ASCII enum/key literals only; escaped names or
            // values have no use here and are rejected, including duplicate aliases.
            guard b[i] >= 32, b[i] < 127, b[i] != 92 else { throw Invalid.input }
            i += 1
        }
        guard i < b.count else { throw Invalid.input }
        let value = String(bytes: b[start..<i], encoding: .ascii)!
        i += 1; return value
    }
    mutating func literal(_ s: String) -> Bool {
        let a = Array(s.utf8)
        guard i+a.count <= b.count, Array(b[i..<(i+a.count)]) == a else { return false }
        i += a.count; return true
    }
    mutating func atom() throws -> Atom {
        white(); guard i < b.count else { throw Invalid.input }
        if b[i] == 34 { return .text(try string()) }
        if literal("null") { return .null }
        if literal("true") { return .boolean(true) }
        if literal("false") { return .boolean(false) }
        let start = i
        while i < b.count && b[i] >= 48 && b[i] <= 57 { i += 1 }
        guard i > start, i-start == 1 || b[start] != 48,
              let n = UInt64(String(bytes:b[start..<i], encoding:.ascii)!) else { throw Invalid.input }
        return .integer(n)
    }
    mutating func object() throws -> [String:Atom] {
        var result: [String:Atom] = [:]; try byte(123)
        while true {
            let key = try string(); guard result[key] == nil, result.count < 12 else { throw Invalid.input }
            try byte(58); result[key] = try atom(); white()
            guard i < b.count else { throw Invalid.input }
            if b[i] == 125 { i += 1; break }
            try byte(44)
        }
        white(); guard i == b.count else { throw Invalid.input }; return result
    }
}
public enum Invalid: Error { case input }
public enum Decoder {
    public static func decode(_ data: Data) throws -> Frame {
        guard !data.isEmpty, data.count <= 2048 else { throw Invalid.input }
        var parser = FlatJSON(b: Array(data)); let m = try parser.object()
        let keys: Set<String> = ["version","source_continuous_ms","seq","phase","reason","grant_remaining_ms","starting_workers","running_workers","stopping_workers","cleanup","terminal_cleanup_unknown","terminal"]
        guard Set(m.keys) == keys else { throw Invalid.input }
        func number(_ k:String, _ max:UInt64) throws -> UInt64 { guard case let .integer(n)? = m[k], n <= max else { throw Invalid.input }; return n }
        func text(_ k:String, _ allowed:Set<String>) throws -> String { guard case let .text(s)? = m[k], allowed.contains(s) else { throw Invalid.input }; return s }
        func bool(_ k:String) throws -> Bool { guard case let .boolean(b)? = m[k] else { throw Invalid.input }; return b }
        guard try number("version",1) == 1 else { throw Invalid.input }
        let seq = try number("seq",9_007_199_254_740_991); guard seq > 0 else { throw Invalid.input }
        let phase = try text("phase",["checking_config","loading_key","preparing","awaiting_approval","connecting","connected","stopping","closed","failed"])
        let reason = try text("reason",["none","revoked","deadline","transport_lost","runtime_fault","terminal_cleanup_unknown","canceled","command_timeout","config_error","key_error","preparation_error","connection_error","approval_not_completed"])
        let cleanup = try text("cleanup",["not_started","pending","confirmed","unknown"])
        let terminal = try bool("terminal"), unknown = try bool("terminal_cleanup_unknown")
        let starting = Int(try number("starting_workers",1)), running = Int(try number("running_workers",1)), stopping = Int(try number("stopping_workers",1))
        guard starting+running+stopping <= 1, terminal == (["closed","failed"].contains(phase)) else { throw Invalid.input }
        if unknown { guard terminal, phase == "failed", cleanup == "unknown", reason == "terminal_cleanup_unknown" else { throw Invalid.input } }
        if reason == "terminal_cleanup_unknown" && !unknown { throw Invalid.input }
        let remaining: Int?
        if case .null? = m["grant_remaining_ms"] { remaining = nil }
        else { remaining = Int(try number("grant_remaining_ms",86_400_000)) }
        if ["config_error","key_error","preparation_error","connection_error","approval_not_completed"].contains(reason) {
            guard terminal, phase == "failed", !unknown, ["not_started","confirmed"].contains(cleanup), starting+running+stopping == 0, (remaining ?? 0) == 0 else { throw Invalid.input }
        }
        if terminal && cleanup == "confirmed" { guard starting+running+stopping == 0, (remaining ?? 0) == 0 else { throw Invalid.input } }
        return Frame(sourceMS:try number("source_continuous_ms",9_007_199_254_740_991),seq:seq,phase:phase,reason:reason,remaining:remaining,starting:starting,running:running,stopping:stopping,cleanup:cleanup,terminalUnknown:unknown,terminal:terminal)
    }
}
public struct Snapshot {
    public var frame: Frame? = nil
    public var fault: FeedFault? = .unavailable
    public var received: TimeInterval = 0
    public var sourceTime: TimeInterval = 0
    public var remainingDeadline: TimeInterval? = nil
    public var stickyUnknown = false
    public init() {}
    public var sourceClosed: Bool { fault == .eof }
    public var terminalObserved: Bool { frame?.terminal == true }
    public func fresh(at now: TimeInterval) -> Bool { fault == nil && frame != nil && now >= received && now >= sourceTime && now-sourceTime <= 3 }
    public func remaining(at now: TimeInterval) -> Int? {
        guard fresh(at:now), let end = remainingDeadline else { return nil }
        return max(0,Int(ceil(end-now)))
    }
}
public struct FeedState {
    public private(set) var snapshot = Snapshot()
    private var poisoned = false
    private var lastSeq: UInt64 = 0
    private var lastTime: TimeInterval = 0
    private var lastSource: TimeInterval = 0
    public init() {}
    public mutating func fail(_ fault: FeedFault) { snapshot.fault = fault; poisoned = true }
    public mutating func ingest(_ data: Data, at now: TimeInterval) {
        guard !poisoned else { return }
        guard now.isFinite, now >= lastTime else { fail(.clock); return }
        do {
            let frame = try Decoder.decode(data)
            guard frame.seq > lastSeq, snapshot.frame?.terminal != true else { fail(.sequence); return }
            let source = Double(frame.sourceMS)/1000
            guard source <= now, source >= lastSource else { fail(.clock); return }
            guard now-source <= 3 else { fail(.stale); return }
            let priorDeadline = snapshot.remainingDeadline
            snapshot.frame = frame; snapshot.fault = nil; snapshot.received = now; snapshot.sourceTime = source
            snapshot.stickyUnknown = snapshot.stickyUnknown || frame.terminalUnknown
            if let ms = frame.remaining {
                let candidate = source+Double(ms)/1000
                snapshot.remainingDeadline = priorDeadline.map { min($0,candidate) } ?? candidate
            } else { snapshot.remainingDeadline = priorDeadline }
            lastSeq = frame.seq; lastTime = now; lastSource = source
        } catch { fail(.malformed) }
    }
}

// TerminateCancel must keep freshness updates alive after an unconfirmed join.
public enum ViewerShutdown {
    public static func attempt(join: () -> Bool, stopUpdates: () -> Void) -> Bool {
        guard join() else { return false }
        stopUpdates()
        return true
    }
}

// Presentation only; no authority, timer, retry or process-control operations.
public struct SetupFeedback: Equatable {
    public let title: String
    public let detail: String
    public let symbol: String
    public let isProcessing: Bool
    public init(_ title:String,_ detail:String,_ symbol:String,_ processing:Bool=false) {
        self.title=title; self.detail=detail; self.symbol=symbol; self.isProcessing=processing
    }
}
extension Snapshot {
    public func setupFeedback(at now:TimeInterval) -> SetupFeedback? {
        guard let f=frame, !stickyUnknown, f.cleanup != "unknown" else { return nil }
        // A retained terminal error is history, not a live connection. Nonterminal
        // EOF/stale never starts or continues motion from a buffered old frame.
        guard fresh(at:now) || (sourceClosed && f.terminal) else { return nil }
        if f.terminal {
            switch f.reason {
            case "config_error": return SetupFeedback("設定を確認できませんでした","起動に使った設定を管理者に確認してください。秘密の値を共有する必要はありません。","doc.badge.exclamationmark")
            case "key_error": return SetupFeedback("接続用の鍵を確認できませんでした","登録済みの鍵と読取権限を管理者に確認してください。鍵の内容は共有しないでください。","key")
            case "preparation_error": return SetupFeedback("この Mac の準備ができませんでした","起動したターミナルの案内を確認してください。設定や権限を自動変更しません。","exclamationmark.circle")
            case "connection_error": return SetupFeedback("接続を準備できませんでした","接続先の準備状況を管理者に確認してください。自動で再接続はしません。","network")
            case "approval_not_completed": return SetupFeedback("承認は完了しませんでした","接続は開始していません。必要な場合はターミナルから新しく起動し、改めて承認してください。","hand.raised")
            default: return nil
            }
        }
        guard remaining(at:now) != 0 else { return nil }
        switch f.phase {
        case "checking_config": return SetupFeedback("起動設定を確認中","この Mac の保護された設定を確認しています。","doc.text.magnifyingglass",true)
        case "loading_key": return SetupFeedback("接続用の鍵を確認中","登録済みの鍵を確認しています。新しい鍵は作りません。","key",true)
        case "preparing": return SetupFeedback("この Mac の実行環境を準備中","まだ操作は許可されていません。準備が終わると本人の承認を待ちます。","gearshape",true)
        case "awaiting_approval": return SetupFeedback("起動したターミナルで承認してください","ターミナルの説明を読み、承認する場合だけ確認文字列を入力して Enter。ここでは承認できません。","terminal")
        case "connecting": return SetupFeedback("接続を確認中","相手の認証と接続の成立を確認しています。まだ接続完了ではありません。","network",true)
        default: return nil
        }
    }
}
