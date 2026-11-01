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
        let phase = try text("phase",["preparing","awaiting_approval","connecting","connected","stopping","closed","failed"])
        let reason = try text("reason",["none","revoked","deadline","transport_lost","runtime_fault","terminal_cleanup_unknown","canceled","command_timeout"])
        let cleanup = try text("cleanup",["not_started","pending","confirmed","unknown"])
        let terminal = try bool("terminal"), unknown = try bool("terminal_cleanup_unknown")
        let starting = Int(try number("starting_workers",1)), running = Int(try number("running_workers",1)), stopping = Int(try number("stopping_workers",1))
        guard starting+running+stopping <= 1, terminal == (["closed","failed"].contains(phase)) else { throw Invalid.input }
        if unknown { guard terminal, phase == "failed", cleanup == "unknown", reason == "terminal_cleanup_unknown" else { throw Invalid.input } }
        if reason == "terminal_cleanup_unknown" && !unknown { throw Invalid.input }
        let remaining: Int?
        if case .null? = m["grant_remaining_ms"] { remaining = nil }
        else { remaining = Int(try number("grant_remaining_ms",86_400_000)) }
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
