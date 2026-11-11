import Foundation
import CryptoKit
import Darwin
import DevelopmentCore

// All access occurs on the AppKit main thread. Nonblocking pipes + finite polls
// prevent a stalled child from blocking Quit or retaining unbounded output.
public final class ResidentProcess {
    private let bundleRoot: URL
    public init() { bundleRoot = Bundle.main.bundleURL }
    // Internal compile-time fixture seam; not a runtime argument or environment override.
    init(fixtureBundle: URL) { bundleRoot = fixtureBundle }
    public private(set) var feed = Feed()
    public private(set) var launched = false
    public private(set) var reaped = false
    public private(set) var eof = false
    public private(set) var launchFailed = false
    private var process: Process?
    private var input: Pipe?
    private var output: Pipe?
    private var lines = LineBuffer()
    private var exitOK = false
    private var helperBuild = "unavailable"
    public static func now() -> UInt64 {
        var info = mach_timebase_info_data_t(); mach_timebase_info(&info)
        return UInt64(Double(mach_continuous_time()) * Double(info.numer) / Double(info.denom) / 1_000_000)
    }
    public var mayQuit: Bool { !launched || (feed.closeConfirmed && !feed.cleanupInvalid && reaped && eof && exitOK) }
    public func start() {
        guard !launched, !launchFailed else { return }
        do {
            let root = bundleRoot.appendingPathComponent("Contents/MacOS")
            let helper = root.appendingPathComponent("tether-dev-resident")
            let digestFile = bundleRoot.appendingPathComponent("Contents/Resources/resident.sha256")
            let attr = try FileManager.default.attributesOfItem(atPath: helper.path)
            guard attr[.type] as? FileAttributeType == .typeRegular,
                  let size = attr[.size] as? NSNumber, size.intValue > 0, size.intValue <= 128*1024*1024,
                  helper.resolvingSymlinksInPath().path == helper.path else { throw Invalid.input }
            let expected = try String(contentsOf: digestFile, encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
            let digest = SHA256.hash(data: try Data(contentsOf: helper)).map { String(format: "%02x", $0) }.joined()
            guard expected.count == 64, digest == expected else { throw Invalid.input }
            helperBuild = String(digest.prefix(12))
            let p = Process(), toChild = Pipe(), fromChild = Pipe()
            for fd in [toChild.fileHandleForWriting.fileDescriptor, fromChild.fileHandleForReading.fileDescriptor] {
                let flags = fcntl(fd, F_GETFL)
                guard flags >= 0, fcntl(fd, F_SETFL, flags | O_NONBLOCK) == 0 else { throw Invalid.input }
            }
            p.executableURL = helper; p.arguments = ["foreground"]
            p.environment = ["PATH": "/usr/bin:/bin", "LANG": "en_US.UTF-8"]
            p.currentDirectoryURL = root
            p.standardInput = toChild; p.standardOutput = fromChild
            p.standardError = FileHandle.nullDevice
            try p.run()
            process = p; input = toChild; output = fromChild; launched = true
            try? toChild.fileHandleForReading.close(); try? fromChild.fileHandleForWriting.close()
        } catch { launchFailed = true }
    }
    func send(_ command: String?) {
        guard let command, let input, !reaped else { return }
        let bytes = Array(command.utf8)
        let count = bytes.withUnsafeBytes { Darwin.write(input.fileHandleForWriting.fileDescriptor, $0.baseAddress, $0.count) }
        if count != bytes.count { feed.fail("input-write-failed"); try? input.fileHandleForWriting.close() }
    }
    public func intent(_ intent: String) { send(feed.command(intent, at: Self.now())) }
    public func close() { send(feed.requestClose()) }
    // Fixed reason codes and process/build identities only, never policy,
    // credentials, endpoint, caller, raw frames or stderr.
    public var diagnostic: String {
        let child = process.map { String($0.processIdentifier) } ?? "none"
        return "GUI PID=\(ProcessInfo.processInfo.processIdentifier) helper PID=\(child) build=\(helperBuild) reason=\(feed.faultCode ?? "none") collection=\(feed.cleanupCode ?? "none") terminal=\(feed.closeConfirmed) EOF=\(eof) reaped=\(reaped) exit0=\(exitOK)"
    }
    public func poll() {
        guard launched, let output, let process else { return }
        do { try lines.checkDeadline(at: Self.now()) } catch { feed.invalidateCleanup("frame-invalid") }
        if !eof {
            var bytes = [UInt8](repeating: 0, count: 4096)
            // Bounded read budget; no dispatch queue or unbounded callback backlog.
            for _ in 0..<4 {
                let n = Darwin.read(output.fileHandleForReading.fileDescriptor, &bytes, bytes.count)
                if n == 0 {
                    eof = true
                    if !lines.isEmpty || !feed.closed { feed.invalidateCleanup("terminal-missing") }
                    try? output.fileHandleForReading.close(); break
                }
                if n < 0 {
                    if errno != EAGAIN && errno != EINTR { feed.invalidateCleanup("output-read-failed"); eof = true }
                    break
                }
                let receivedAt = Self.now() // sample after read; never before child publication
                do {
                    for frame in try lines.receive(Array(bytes.prefix(n)), at: receivedAt) {
                        feed.ingest(frame, at: receivedAt)
                    }
                } catch { feed.invalidateCleanup("frame-invalid") }
            }
        }
        if !process.isRunning && !reaped {
            process.waitUntilExit(); reaped = true
            exitOK = process.terminationReason == .exit && process.terminationStatus == 0
            if !exitOK { feed.invalidateCleanup("child-exit-failed") }
            try? input?.fileHandleForWriting.close()
        }
        if feed.fault { close() }
    }
}
