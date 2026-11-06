import Darwin
import Foundation

@MainActor
private enum Check {
    static var passed = 0
    static var failed = 0
    static var retained: [ResidentLaunchPreparation] = []
    static func that(_ value: Bool) {
        if value { passed += 1 } else { failed += 1 }
    }
}

@main
struct ActiveIODriver {
    @MainActor
    static func main() throws {
        let arguments = Array(CommandLine.arguments.dropFirst())
        if arguments.first == "active-child" { try child(arguments); return }
        guard arguments.count == 1 else { Darwin.exit(78) }
        if arguments[0] == "guards" {
            try guards()
            report("guards", children: 0, unknown: 0)
        }
        guard ["echo", "early", "lost", "missing"].contains(arguments[0]),
              let mode = ResidentLaunchPreparation.OwnedIOFixtureMode(rawValue: arguments[0]) else { Darwin.exit(78) }
        // This newly compiled test parent starts with the default signal policy.
        _ = Darwin.signal(SIGPIPE, SIG_DFL)
        let expectedUnknown = mode != .echo
        weak var weakOwner: ResidentLaunchPreparation?
        var sentinels: [Int32] = [-1, -1]
        try autoreleasepool {
            let owner = ResidentLaunchPreparation.completionFixture()
            weakOwner = owner
            let identity = owner.completionFixtureIdentity()
            Check.that(!owner.transferOwnedIO())
            Check.that(owner.processExit() == nil)
            Check.that(try owner.runOwnedIOFixture(mode) == (mode != .lost))
            let originals = owner.fixtureOwnedDescriptors()
            Check.that(owner.fixtureCounts() == (open: 2, closed: 2))
            Check.that(owner.fixtureOwnedBindings())
            Check.that(owner.fixtureOwnedNoSIGPIPE())
            Check.that(Darwin.signal(SIGPIPE, SIG_DFL) == nil)
            Check.that(!owner.retireIO())
            Check.that(!owner.transferOwnedIO())
            Check.that(owner.fixtureCounts() == (open: 2, closed: 2))
            if mode == .missing {
                // Wait is real, but deliberately do not observe stdout EOF.
                wait(owner)
            } else {
                Check.that(readLine(owner) == Array("inherit-ok\n".utf8))
                if mode == .early {
                    wait(owner)
                    Check.that(write(owner, "ctl\n").tag == .unknown) // Actual EPIPE, not signal death.
                } else {
                    let result = write(owner, "ctl\n")
                    Check.that(result.tag == .bytes && result.count == 4)
                }
                Check.that(owner.bytes(.halfClose).tag == .halfClosed)
                Check.that(owner.bytes(.halfClose).tag == .halfClosed)
                Check.that(readToEOF(owner) == (mode == .early ? [] : Array("obs\n".utf8)))
                wait(owner)
            }
            Check.that(owner.processExit() == .init(reason: .exit, status: 0))
            Check.that(owner.completionFixtureWaits() == 1)
            // Reuse a child-end number before retirement/destruction. A second
            // close through a stale wrapper would damage this own sentinel.
            Check.that(pipe(&sentinels) == 0)
            Check.that(sentinels.contains(originals[0]) || sentinels.contains(originals[3]))
            Check.that(owner.retireIO() == !expectedUnknown)
            Check.that(owner.fixtureCounts() == (open: 0, closed: 4))
            Check.that(owner.retireIO() == !expectedUnknown)
            Check.that(owner.fixtureCounts() == (open: 0, closed: 4))
            Check.that(owner.fixtureOwnedBindings())
            Check.that(write(owner, "ctl\n").tag == .rejected)
            Check.that(owner.completionFixtureIdentity() == identity)
            Check.that(owner.fixtureOwnedUnconfirmed() == expectedUnknown)
            // Unknown metadata keeps the SAME original owner until this fixed
            // fixture program exits, after all real child/FD collection.
            if owner.fixtureOwnedUnconfirmed() { Check.retained.append(owner) }
        }
        Check.that((weakOwner != nil) == expectedUnknown)
        var byte: UInt8 = 91
        Check.that(Darwin.write(sentinels[1], &byte, 1) == 1)
        byte = 0
        Check.that(Darwin.read(sentinels[0], &byte, 1) == 1 && byte == 91)
        Check.that(Darwin.close(sentinels[0]) == 0)
        Check.that(Darwin.close(sentinels[1]) == 0)
        report(mode.rawValue, children: 1, unknown: expectedUnknown ? 1 : 0)
    }

    private static func write(_ owner: ResidentLaunchPreparation, _ input: String) -> PreparedByteResult {
        let bytes = Array(input.utf8)
        return bytes.withUnsafeBufferPointer { owner.bytes(.write($0.baseAddress, UInt32(bytes.count))) }
    }

    private static func readLine(_ owner: ResidentLaunchPreparation) -> [UInt8] {
        var bytes: [UInt8] = []
        var byte: UInt8 = 0
        while true {
            let result = withUnsafeMutablePointer(to: &byte) { owner.bytes(.read($0, 1)) }
            if result.tag == .bytes {
                bytes.append(byte)
                if byte == 10 { return bytes }
            } else if result.tag != .pending { return bytes }
            Thread.sleep(forTimeInterval: 0.001)
        }
    }

    private static func readToEOF(_ owner: ResidentLaunchPreparation) -> [UInt8] {
        var bytes: [UInt8] = []
        var buffer = [UInt8](repeating: 0, count: 32)
        while true {
            let result = buffer.withUnsafeMutableBufferPointer { owner.bytes(.read($0.baseAddress, 32)) }
            if result.tag == .eof { return bytes }
            if result.tag == .bytes { bytes += buffer.prefix(Int(result.count)) }
            else if result.tag != .pending { return bytes + [255] }
            Thread.sleep(forTimeInterval: 0.001)
        }
    }

    private static func wait(_ owner: ResidentLaunchPreparation) {
        while owner.processExit() == nil { Thread.sleep(forTimeInterval: 0.001) }
    }

    private static func child(_ arguments: [String]) throws {
        guard arguments.count == 3, ["echo", "early"].contains(arguments[1]) else { Darwin.exit(78) }
        let descriptions = arguments[2].split(separator: ",").map { $0.split(separator: ":") }
        guard descriptions.count == 4, descriptions.allSatisfy({ $0.count == 3 }),
              let inputInode = UInt64(descriptions[0][2]), let outputInode = UInt64(descriptions[3][2]) else { Darwin.exit(89) }
        let inodes = descriptions.compactMap { UInt64($0[2]) }
        guard Set(inodes).count == 4 else { Darwin.exit(89) }
        // Own child descriptors only, while the parent still holds its ends.
        // This point-in-time audit is not concurrent-spawn atomicity proof.
        for name in try FileManager.default.contentsOfDirectory(atPath: "/dev/fd") {
            guard let fd = Int32(name) else { continue }
            var value = stat()
            if fstat(fd, &value) == 0, inodes.contains(value.st_ino), fd != 0 && fd != 1 { Darwin.exit(89) }
        }
        var input = stat(), output = stat()
        guard fstat(0, &input) == 0, fstat(1, &output) == 0,
              input.st_ino == inputInode, output.st_ino == outputInode else { Darwin.exit(89) }
        try FileHandle.standardOutput.write(contentsOf: Data("inherit-ok\n".utf8))
        if arguments[1] == "early" { Darwin.exit(0) }
        guard try FileHandle.standardInput.readToEnd() == Data("ctl\n".utf8) else { Darwin.exit(88) }
        try FileHandle.standardOutput.write(contentsOf: Data("obs\n".utf8))
        Darwin.exit(0)
    }

    @MainActor
    private static func guards() throws {
        do { _ = try ResidentLaunchPreparation.prepare(); Check.that(false) }
        catch PreparationFailure.publisherUnconfigured { Check.that(true) }
        catch PreparationFailure.unsupportedOS { Check.that(true) }
        let owner = try ResidentLaunchPreparation.fixture(team: "SYNTH00001", hashes: [Data(repeating: 1, count: 20)],
                                                        version: .init(majorVersion: 14, minorVersion: 4, patchVersion: 0))
        Check.that(!owner.transferOwnedIO())
        do { _ = try owner.runOwnedIOFixture(.echo); Check.that(false) }
        catch PreparationFailure.invalidRelease { Check.that(true) }
        Check.that(owner.fixtureCounts() == (open: 0, closed: 0))
        Check.that(owner.processExit() == nil)
        Check.that(owner.retireIO())
    }

    @MainActor
    private static func report(_ mode: String, children: Int, unknown: Int) -> Never {
        print("NATIVE_ACTIVE_IO mode=\(mode) passed=\(Check.passed) failed=\(Check.failed) childrenStarted=\(children) childrenWaited=\(children) open=0 unknown=\(unknown) producer=0")
        Darwin.exit(Check.failed == 0 ? 0 : 1)
    }
}
