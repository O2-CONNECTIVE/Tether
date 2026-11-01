import Foundation

// Fixed, privacy-bounded observation of this viewer, never OS cleanup proof.
public struct PilotSummary {
    public let streamEnd: String
    public let result: String
    public let fault: FeedFault
    public var exitCode: Int32 { result == "confirmed_terminal" ? 0 : 2 }
    public init(snapshot: Snapshot, at now: TimeInterval, eof: Bool) {
        fault = snapshot.fault ?? .stopped
        streamEnd = eof ? "eof" : (fault == .read ? "read_error" : (fault == .stopped ? "canceled" : (fault == .unavailable ? "unavailable" : "rejected")))
        let fresh = now.isFinite && now >= snapshot.received && now >= snapshot.sourceTime && now-snapshot.sourceTime <= 3
        let frame = snapshot.frame
        let confirmed = eof && fault == .eof && fresh && frame?.terminal == true && frame?.phase == "closed"
            && frame?.cleanup == "confirmed" && frame?.terminalUnknown == false && !snapshot.stickyUnknown
            && frame?.starting == 0 && frame?.running == 0 && frame?.stopping == 0 && (frame?.remaining ?? 0) == 0
        result = confirmed ? "confirmed_terminal" : "unknown"
    }
    // Every interpolated value is a local fixed enum or integer, not raw input.
    public var jsonLine: String {
        "{\"version\":1,\"stream_end\":\"\(streamEnd)\",\"result\":\"\(result)\",\"fault\":\"\(fault.rawValue)\",\"reader\":\"joined\",\"exit_code\":\(exitCode)}\n"
    }
}
