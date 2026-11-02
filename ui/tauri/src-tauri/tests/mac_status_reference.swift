import Foundation

// Closed-fixture runner for the unchanged Swift reference. Never opens a pipe,
// starts the viewer, reads real status/config/keys, or derives Rust expectations.
@main struct GoldenReference {
    static func main() throws {
        guard CommandLine.arguments.count == 2 else { fatalError("fixture path required") }
        let text = try String(contentsOfFile: CommandLine.arguments[1], encoding: .utf8)
        var state = FeedState(), checked = 0
        for line in text.split(separator: "\n") where !line.hasPrefix("#") {
            let fields = line.split(separator: "\t", omittingEmptySubsequences: false).map(String.init)
            guard fields.count == 13, let now = Double(fields[2]) else { fatalError("fixture format") }
            let op = fields[1]
            if op == "reset" { state = FeedState() }
            else if op == "ingest" {
                let hex = Array(fields[3].utf8)
                var bytes = [UInt8]()
                if fields[3] != "-" {
                    guard hex.count % 2 == 0 else { fatalError("fixture hex") }
                    for index in stride(from: 0, to: hex.count, by: 2) {
                        guard let byte = UInt8(String(bytes: hex[index..<index+2], encoding: .ascii)!, radix: 16) else { fatalError("fixture hex") }
                        bytes.append(byte)
                    }
                }
                state.ingest(Data(bytes), at: now)
            } else if op.hasPrefix("fail:"), let fault = FeedFault(rawValue: String(op.dropFirst(5))) { state.fail(fault) }
            else if op != "query" { fatalError("fixture operation") }
            let s = state.snapshot
            let actual = [s.fault?.rawValue ?? "-", s.frame?.phase ?? "-", s.frame.map { String($0.seq) } ?? "-", s.frame?.cleanup ?? "-", s.stickyUnknown ? "1":"0", s.sourceClosed ? "1":"0", s.terminalObserved ? "1":"0", s.fresh(at:now) ? "1":"0", s.remaining(at:now).map(String.init) ?? "-"]
            guard actual == Array(fields[4...]) else {
                print("MISMATCH case=\(fields[0]) operation=\(op) expected=\(fields[4...].joined(separator:"|")) actual=\(actual.joined(separator:"|"))")
                exit(1)
            }
            checked += 1
        }
        print("SWIFT_GOLDEN_PASS rows=\(checked)")
    }
}
