import Foundation

public struct Observation {
    public let revision: UInt64
    public let sourceMS: UInt64
    public let standby, registration, work, cleanup: String
    public let canOn, canOff: Bool
}
public struct Feed {
    public private(set) var observation: Observation?
    public private(set) var fault = false
    public private(set) var cleanupInvalid = false
    public private(set) var faultCode: String?
    public private(set) var cleanupCode: String?
    public private(set) var closed = false
    public private(set) var closeConfirmed = false
    public private(set) var closing = false
    public private(set) var pending: (id: UInt64, intent: String)?
    private var lastTime: UInt64 = 0
    private var lastSource: UInt64 = 0
    private var nextID: UInt64 = 1
    private var awaitingReceipt: (id: UInt64, intent: String)?
    public init() {}
    // Admission never resumes. Collection has a separate, monotonic trust gate.
    public mutating func fail(_ code: String = "protocol-invalid") {
        fault = true
        if faultCode == nil { faultCode = code }
    }
    public mutating func invalidateCleanup(_ code: String) {
        cleanupInvalid = true
        if cleanupCode == nil { cleanupCode = code }
        closeConfirmed = false
        fail(code)
    }
    public mutating func requestClose() -> String? {
        guard !closing else { return nil }
        closing = true
        return "close\n"
    }
    public func fresh(at now: UInt64) -> Bool {
        guard !fault, !closed, let o = observation else { return false }
        return now >= lastTime && now >= o.sourceMS && now-o.sourceMS < 3000
    }
    public func can(_ intent: String, at now: UInt64) -> Bool {
        guard fresh(at: now), !closing, pending == nil, awaitingReceipt == nil, let o = observation else { return false }
        return intent == "on" ? o.canOn : intent == "off" && o.canOff
    }
    public mutating func command(_ intent: String, at now: UInt64) -> String? {
        guard can(intent, at: now), let o = observation, nextID < 9_007_199_254_740_991 else { return nil }
        let id = nextID; nextID += 1; pending = (id, intent); awaitingReceipt = (id, intent)
        return "\(intent) \(id) \(o.revision)\n"
    }
    public mutating func ingest(_ data: Data, at now: UInt64) {
        guard !cleanupInvalid else { return }
        var kind: String?
        do {
            guard !closed, now >= lastTime, !data.isEmpty, data.count <= 4096 else {
                invalidateCleanup("stream-order-invalid"); return
            }
            // Retain receiver time even for a recoverable rejected observation.
            // A later terminal cannot erase a rollback across that observation.
            lastTime = now
            var parser = Parser(bytes: Array(data))
            let value = try parser.value(); parser.whitespace()
            guard parser.i == data.count, case let .object(fields) = value,
                  case let .text(kindValue)? = fields["type"] else { throw Invalid.input }
            kind = kindValue
            // Unknown owner results poison proof before freshness/correlation
            // checks: a stale or mismatched unknown cannot be washed away.
            if hasUnknownResult(fields) { invalidateCleanup("owner-result-unknown"); return }
            guard ["status", "receipt", "closed"].contains(kindValue) else { throw Invalid.input }
            let keys = kind == "status" ? ["protocolVersion", "type", "sourceContinuousMs", "snapshot"] : kind == "closed" ? ["protocolVersion", "type", "sourceContinuousMs", "outcome"] : ["protocolVersion", "type", "receipt"]
            let root = try Object(value, keys)
            guard try root.number("protocolVersion") == 1 else { throw Invalid.input }
            if kind == "receipt" { try receiveReceipt(root.fields["receipt"]!) }
            else {
                let source = try root.number("sourceContinuousMs")
                guard source <= now, source >= lastSource, now-source < 3000 else { throw Invalid.input }
                lastSource = source
                if kind == "status" { try receiveStatus(root.fields["snapshot"]!, source: source) }
                else if kind == "closed" {
                    let outcome = try root.text("outcome", ["confirmed", "unknown"])
                    closed = true; closing = true; closeConfirmed = outcome == "confirmed"
                    if !closeConfirmed { invalidateCleanup("owner-cleanup-unknown") }
                } else { throw Invalid.input }
            }
            lastTime = now
        } catch {
            // A complete observation/receipt may seal admission while its own
            // pipe remains usable for a later strict terminal. A damaged or
            // unknown terminal cannot be replaced by a success frame.
            if kind == "status" || kind == "receipt" { fail() }
            else { invalidateCleanup("terminal-invalid") }
        }
    }
    private func hasUnknownResult(_ fields: [String: Value]) -> Bool {
        func unknown(_ fields: [String: Value], _ names: [String]) -> Bool {
            names.contains { name in
                if case .text("unknown")? = fields[name] { return true }
                return false
            }
        }
        if unknown(fields, ["outcome"]) { return true }
        if case let .object(receipt)? = fields["receipt"], unknown(receipt, ["disposition"]) { return true }
        if case let .object(snapshot)? = fields["snapshot"] {
            if unknown(snapshot, ["cleanup", "standby", "work"]) { return true }
            if case let .object(transition)? = snapshot["transition"], unknown(transition, ["outcome"]) { return true }
        }
        return false
    }
    private mutating func receiveReceipt(_ value: Value) throws {
        let r = try Object(value, ["protocolVersion", "requestId", "intent", "disposition"])
        guard try r.number("protocolVersion") == 1, let p = awaitingReceipt,
              try r.number("requestId") == p.id, try r.text("intent", ["on", "off"]) == p.intent else { throw Invalid.input }
        let disposition = try r.text("disposition", ["accepted", "rejected", "stale", "unknown"])
        if disposition == "unknown" { invalidateCleanup("owner-result-unknown"); throw Invalid.input }
        awaitingReceipt = nil
        // Receipts never confirm ON/OFF. Only a correlated owner transition does.
        // A rejected/stale receipt confirms no dispatch and allows an explicit retry.
        if disposition == "rejected" || disposition == "stale" { pending = nil }
    }
    private mutating func receiveStatus(_ value: Value, source: UInt64) throws {
        let s = try Object(value, ["protocolVersion", "source", "state", "revision", "validForMs", "standby", "registration", "caller", "work", "cleanup", "canRequestOn", "canRequestOff", "transition"])
        guard try s.number("protocolVersion") == 1,
              try s.text("source", ["resident-owner"]) == "resident-owner",
              try s.text("state", ["source-observation"]) == "source-observation",
              try s.number("validForMs") == 3000 else { throw Invalid.input }
        let revision = try s.number("revision")
        guard revision > (observation?.revision ?? 0) else { throw Invalid.input }
        if case let .text(caller)? = s.fields["caller"] {
            guard !caller.isEmpty, caller.unicodeScalars.count <= 80,
                  !caller.unicodeScalars.contains(where: CharacterSet.controlCharacters.contains) else { throw Invalid.input }
        } else if case .null? = s.fields["caller"] {} else { throw Invalid.input }
        let standby = try s.text("standby", ["off", "on", "stopping", "unknown"])
        let registration = try s.text("registration", ["disconnected", "connecting", "authenticated", "unknown"])
        let work = try s.text("work", ["idle", "preparing", "running", "stopping", "unknown"])
        let cleanup = try s.text("cleanup", ["confirmed", "pending", "unknown"])
        let canOn = try s.bool("canRequestOn"), canOff = try s.bool("canRequestOff")
        if cleanup == "unknown" || standby == "unknown" || work == "unknown" {
            invalidateCleanup("owner-cleanup-unknown"); throw Invalid.input
        }
        guard
              !canOn || (standby == "off" && work == "idle" && cleanup == "confirmed"),
              !canOff || standby == "on", !(canOn && canOff) else { throw Invalid.input }
        if case .null? = s.fields["transition"] {} else {
            let t = try Object(s.fields["transition"]!, ["requestId", "intent", "outcome"])
            let id = try t.number("requestId"), intent = try t.text("intent", ["on", "off"])
            let outcome = try t.text("outcome", ["pending", "applied", "rejected", "unknown"])
            if outcome == "unknown" { invalidateCleanup("owner-result-unknown"); throw Invalid.input }
            guard id > 0, id < nextID else { throw Invalid.input }
            if let p = pending {
                guard id == p.id, intent == p.intent else { throw Invalid.input }
                if outcome == "applied" {
                    guard intent == "on" ? standby == "on" : (standby == "off" && work == "idle" && cleanup == "confirmed") else { throw Invalid.input }
                    pending = nil
                } else if outcome == "rejected" { pending = nil }
            } else if outcome == "pending" { throw Invalid.input }
        }
        observation = Observation(revision: revision, sourceMS: source, standby: standby, registration: registration, work: work, cleanup: cleanup, canOn: canOn, canOff: canOff)
    }
}
