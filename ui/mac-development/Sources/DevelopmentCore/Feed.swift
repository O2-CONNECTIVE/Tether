import Foundation

public struct Observation {
    public let revision: UInt64
    public let sourceMS: UInt64
    public let standby, registration, work, cleanup: String
    public let registrationReason: String?
    public let canOn, canOff: Bool
    public let standbyUntil: UInt64? // owner wall-clock end of standby, Unix epoch ms
    public let standbyEndingSoon, standbyExpired: Bool
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
    public private(set) var unrequestedCloseAt: UInt64?
    public private(set) var pending: (id: UInt64, intent: String)?
    private var lastTime: UInt64 = 0
    private var lastSource: UInt64 = 0
    private var nextID: UInt64 = 1
    private var awaitingReceipt: (id: UInt64, intent: String)?
    // The owner's status does not carry the allowance, so this view keeps it:
    // opened by an accepted allow-run receipt, closed by deny-run or when
    // standby is no longer ON. It has no clock of its own.
    private var allowGranted = false
    public static let standbyHours = [2, 4, 8, 12, 24]
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
        // The owner closes admission in needs_action and rejects both.
        if intent == "allow" || intent == "extend" { return o.standby == "on" && o.registration != "needs_action" }
        if intent == "deny" { return o.standby == "on" } // offered even if this view shows none
        return intent == "on" ? o.canOn : intent == "off" && o.canOff
    }
    public var runAllowed: Bool {
        guard !fault, !closed, let o = observation else { return false }
        return o.standby == "on" && allowGranted
    }
    // ON and extend always carry a listed length; allow has no length and
    // lasts until standby ends.
    public mutating func command(_ intent: String, at now: UInt64, hours: Int? = nil) -> String? {
        let timed = intent == "on" || intent == "extend"
        if timed, !Self.standbyHours.contains(hours ?? 0) { return nil }
        guard can(intent, at: now), let o = observation, nextID < 9_007_199_254_740_991 else { return nil }
        let id = nextID; nextID += 1
        let receiptIntent = intent == "allow" ? "allow-run" : intent == "deny" ? "deny-run" : intent
        if intent == "on" || intent == "off" { pending = (id, intent) }
        awaitingReceipt = (id, receiptIntent)
        return "\(intent) \(id) \(o.revision)\(timed ? " \(hours!)" : "")\n"
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
            if kind == "receipt" { try receiveReceipt(root.fields["receipt"]!, at: now) }
            else {
                let source = try root.number("sourceContinuousMs")
                guard source <= now, source >= lastSource, now-source < 3000 else { throw Invalid.input }
                lastSource = source
                if kind == "status" { try receiveStatus(root.fields["snapshot"]!, source: source) }
                else if kind == "closed" {
                    let outcome = try root.text("outcome", ["confirmed", "unknown"])
                    if !closing { unrequestedCloseAt = now } // the owner closed before any GUI request
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
            if unknown(snapshot, ["cleanup", "work"]) { return true }
            if unknown(snapshot, ["standby"]), !isPendingOnSnapshot(.object(snapshot)) { return true }
            if case let .object(transition)? = snapshot["transition"], unknown(transition, ["outcome"]) { return true }
        }
        return false
    }
    // The producer deliberately withholds ON proof while initial authentication
    // is pending. Only this complete, correlated shape is observation uncertainty,
    // not collection uncertainty. It never clears any fault or enables a command.
    private func isPendingOnSnapshot(_ value: Value) -> Bool {
        do {
            guard let p = pending, p.intent == "on", awaitingReceipt == nil else { return false }
            let s = try Object(value, ["protocolVersion", "source", "state", "revision", "validForMs", "standby", "registration", "caller", "work", "cleanup", "canRequestOn", "canRequestOff", "transition", "standbyUntil", "standbyEndingSoon", "standbyExpired", "sleepPrevention"])
            guard try s.number("protocolVersion") == 1,
                  try s.text("source", ["resident-owner"]) == "resident-owner",
                  try s.text("state", ["source-observation"]) == "source-observation",
                  try s.number("validForMs") == 3000,
                  try s.number("revision") > (observation?.revision ?? 0),
                  try s.text("standby", ["unknown"]) == "unknown",
                  try s.text("registration", ["connecting"]) == "connecting",
                  try s.text("work", ["idle"]) == "idle",
                  try s.text("cleanup", ["confirmed"]) == "confirmed",
                  try !s.bool("canRequestOn"), try !s.bool("canRequestOff"),
                  case .null? = s.fields["caller"] else { return false }
            let t = try Object(s.fields["transition"]!, ["requestId", "intent", "outcome"])
            return try t.number("requestId") == p.id && t.text("intent", ["on"]) == "on" && t.text("outcome", ["pending"]) == "pending"
        } catch { return false }
    }
    private mutating func receiveReceipt(_ value: Value, at now: UInt64) throws {
        let r = try Object(value, ["protocolVersion", "requestId", "intent", "disposition"])
        guard try r.number("protocolVersion") == 1, let p = awaitingReceipt,
              try r.number("requestId") == p.id, try r.text("intent", ["on", "off", "extend", "allow-run", "deny-run"]) == p.intent else { throw Invalid.input }
        let disposition = try r.text("disposition", ["accepted", "rejected", "stale", "unknown"])
        if disposition == "unknown" { invalidateCleanup("owner-result-unknown"); throw Invalid.input }
        awaitingReceipt = nil
        // Receipts never confirm ON/OFF. Only a correlated owner transition does.
        // A rejected/stale receipt confirms no dispatch and allows an explicit retry.
        if disposition == "rejected" || disposition == "stale" { pending = nil }
        else if disposition == "accepted" {
            if p.intent == "allow-run" { allowGranted = true }
            else if p.intent == "deny-run" { allowGranted = false }
        }
    }
    private mutating func receiveStatus(_ value: Value, source: UInt64) throws {
        let keys = ["protocolVersion", "source", "state", "revision", "validForMs", "standby", "registration", "caller", "work", "cleanup", "canRequestOn", "canRequestOff", "transition", "standbyUntil", "standbyEndingSoon", "standbyExpired", "sleepPrevention"]
        guard case let .object(fields) = value else { throw Invalid.input }
        let s = try Object(value, fields["registrationReason"] == nil ? keys : keys + ["registrationReason"])
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
        let registration = try s.text("registration", ["disconnected", "connecting", "authenticated", "reconnecting", "needs_action", "unknown"])
        let registrationReason: String?
        if registration == "needs_action" {
            registrationReason = try s.text("registrationReason", ["identity_refused", "connection_occupied", "gateway_refused"])
        } else {
            guard s.fields["registrationReason"] == nil else { throw Invalid.input }
            registrationReason = nil
        }
        let work = try s.text("work", ["idle", "preparing", "running", "stopping", "unknown"])
        let cleanup = try s.text("cleanup", ["confirmed", "pending", "unknown"])
        let canOn = try s.bool("canRequestOn"), canOff = try s.bool("canRequestOff")
        let standbyUntil: UInt64?
        if case .null? = s.fields["standbyUntil"] { standbyUntil = nil } else { standbyUntil = try s.number("standbyUntil") }
        let standbyEndingSoon = try s.bool("standbyEndingSoon"), standbyExpired = try s.bool("standbyExpired")
        _ = try s.text("sleepPrevention", ["inactive", "active", "unknown"])
        if cleanup == "unknown" || (standby == "unknown" && !isPendingOnSnapshot(value)) || work == "unknown" {
            invalidateCleanup("owner-cleanup-unknown"); throw Invalid.input
        }
        guard
              !canOn || (standby == "off" && work == "idle" && cleanup == "confirmed"),
              !canOff || standby == "on", !(canOn && canOff),
              registration != "needs_action" || (standby == "on" && work == "idle" && cleanup == "confirmed"),
              !standbyEndingSoon || standbyUntil != nil else { throw Invalid.input }
        if case .null? = s.fields["transition"] {} else {
            let t = try Object(s.fields["transition"]!, ["requestId", "intent", "outcome"])
            let id = try t.number("requestId"), intent = try t.text("intent", ["on", "off"])
            let outcome = try t.text("outcome", ["pending", "applied", "rejected", "unknown"])
            if outcome == "unknown" { invalidateCleanup("owner-result-unknown"); throw Invalid.input }
            guard id > 0, id < nextID else { throw Invalid.input }
            if outcome == "applied" {
                guard intent == "on" ? standby == "on" : (standby == "off" && work == "idle" && cleanup == "confirmed") else { throw Invalid.input }
            }
            if let p = pending {
                if id == p.id && intent == p.intent {
                    if outcome == "applied" {
                        pending = nil
                    } else if outcome == "rejected" { pending = nil }
                } else if outcome == "pending" { throw Invalid.input }
            } else if outcome == "pending" { throw Invalid.input }
        }
        observation = Observation(revision: revision, sourceMS: source, standby: standby, registration: registration, work: work, cleanup: cleanup, registrationReason: registrationReason, canOn: canOn, canOff: canOff, standbyUntil: standbyUntil, standbyEndingSoon: standbyEndingSoon, standbyExpired: standbyExpired)
        if standby != "on" || registration == "needs_action" { allowGranted = false }
    }
}
