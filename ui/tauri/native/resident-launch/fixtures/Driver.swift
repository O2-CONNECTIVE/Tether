import Foundation
import LightweightCodeRequirements

@MainActor
private enum Check {
    static var passed = 0
    static var failed = 0
    static var launched = 0
    static func that(_ value: Bool, _ label: String) {
        if value { passed += 1 } else { failed += 1; print("CHECK_FAILED \(label)") }
    }
}

@main
@MainActor
struct Driver {
    static func main() {
        if (3...4).contains(CommandLine.arguments.count), CommandLine.arguments[1] == "pairing-child" {
            runPairingChild(CommandLine.arguments[2])
            return
        }
        for (major, minor, patch, expected) in [
            (10, 13, 0, false), (13, 6, 0, false), (14, 0, 0, false),
            (14, 3, 99, false), (14, 4, 0, true), (14, 4, 1, true),
            (15, 0, 0, true), (26, 0, 0, true), (-1, 4, 0, false),
            (14, -1, 0, false), (14, 4, -1, false)
        ] {
            let version = OperatingSystemVersion(majorVersion: major, minorVersion: minor, patchVersion: patch)
            Check.that(ResidentLaunchPreparation.supports(version) == expected, "version-boundary")
        }
        do {
            _ = try ResidentLaunchPreparation.prepare()
            Check.that(false, "production-must-stay-unconfigured")
        } catch PreparationFailure.publisherUnconfigured {
            Check.that(true, "production-unconfigured")
        } catch PreparationFailure.unsupportedOS {
            Check.that(true, "production-unsupported")
        } catch { Check.that(false, "production-fixed-failure") }

        guard #available(macOS 14.4, *) else {
            print("NATIVE_REQUIREMENT_UNEXECUTED unsupportedOS purePassed=\(Check.passed)")
            exit(2)
        }
        do { try nativeCases() } catch { Check.that(false, "native-fixture-error") }
        if #unavailable(macOS 15.0) {
            print("NATIVE_REQUIREMENT_UNEXECUTED typedEqualityRequires15 purePassed=\(Check.passed)")
            exit(2)
        }
        print("NATIVE_REQUIREMENT_FIXTURE passed=\(Check.passed) failed=\(Check.failed) launched=\(Check.launched)")
        if Check.failed != 0 { exit(1) }
    }

    @available(macOS 14.4, *)
    private static func nativeCases() throws {
        let version = OperatingSystemVersion(majorVersion: 14, minorVersion: 4, patchVersion: 0)
        let hashes = [Data(repeating: 1, count: 20), Data(repeating: 2, count: 20)]
        let preparation = try ResidentLaunchPreparation.fixture(team: "TESTTEAM01", hashes: hashes, version: version)
        let first = try preparation.snapshot()
        let second = try preparation.snapshot()
        Check.that(first.owner == second.owner, "same-unlaunched-object")
        Check.that(!first.hasExecutable && !first.hasArguments && !first.running, "no-execution-inputs")
        if #available(macOS 15.0, *) {
            Check.that(first.typedEquivalent == true, "typed-setter-getter-equivalence")
        }
        Check.that(!first.retained.isEmpty, "retained-requirement")

        let reference = try LaunchCodeRequirement.allOf {
            TeamIdentifier("TESTTEAM01")
            SigningIdentifier("com.o2connective.rescue-host.resident-pipe")
            ValidationCategory(.developerID)
            CodeDirectoryHash.in(hashes)
        }
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        Check.that(try encoder.encode(reference) == first.expected, "publisher-and-release-allOf")

        let pairingRequirement = try ResidentLaunchPreparation.pairingRequirementFixture(team: "TESTTEAM01")
        let pairingReference = try LaunchCodeRequirement.allOf {
            TeamIdentifier("TESTTEAM01")
            SigningIdentifier("com.o2connective.rescue-host.tether-host-pair")
            ValidationCategory(.developerID)
        }
        Check.that(pairingRequirement == (try encoder.encode(pairingReference)), "pairing-helper-same-team-requirement")
        Check.that(pairingRequirement != (try ResidentLaunchPreparation.pairingRequirementFixture(team: "TESTTEAM02")),
                   "pairing-helper-different-team-refused")

        let success = pairingRun("success", timeout: 2)
        Check.that(success == .exited(status: 0, stdout: Data("{\"protocolVersion\":1,\"result\":\"succeeded\"}\n".utf8)),
                   "pairing-helper-captures-result-line")
        let limit = pairingRun("limit", timeout: 2)
        Check.that(limit == .exited(status: 0, stdout: Data(repeating: 120, count: 4096)),
                   "pairing-helper-accepts-four-kibibytes")
        Check.that(pairingRun("overflow", timeout: 2) == .outputTooLarge,
                   "pairing-helper-rejects-output-over-four-kibibytes")
        Check.that(pairingRun("hold", timeout: 0.1) == .timedOut,
                   "pairing-helper-timeout-kills-child")
        let eofStart = DispatchTime.now().uptimeNanoseconds
        let inheritedPipe = pairingRun("grandchild", timeout: 2)
        let eofElapsed = DispatchTime.now().uptimeNanoseconds - eofStart
        // Why: give the descendant time to write on stdout and reach EOF before the next helper case.
        Thread.sleep(forTimeInterval: 3.5)
        // Why: a helper can exit while a descendant retains stdout, so EOF must not hold the Rust worker forever.
        Check.that(inheritedPipe == .unavailable && eofElapsed < 4_000_000_000,
                   "pairing-helper-bounds-stdout-eof-after-exit")
        let killStart = DispatchTime.now().uptimeNanoseconds
        let killedInheritedPipe = pairingRun("timeout-grandchild", timeout: 0.5)
        let killElapsed = DispatchTime.now().uptimeNanoseconds - killStart
        // Why: SIGKILL must not leave the helper waiting forever for a descendant-held stdout pipe.
        Check.that(killedInheritedPipe == .unavailable && killElapsed < 3_000_000_000,
                   "pairing-helper-bounds-stdout-eof-after-kill")

        let otherTeam = try ResidentLaunchPreparation.fixture(team: "TESTTEAM02", hashes: hashes, version: version)
        let otherRelease = try ResidentLaunchPreparation.fixture(team: "TESTTEAM01", hashes: [Data(repeating: 3, count: 20)], version: version)
        if #available(macOS 15.0, *) {
            Check.that(try !otherTeam.equivalent(to: preparation), "different-team-is-not-equivalent")
            Check.that(try !otherRelease.equivalent(to: preparation), "different-release-is-not-equivalent")
        }

        for badTeam in ["", "SHORT", "testteam01", "TEST TEAM1", "TESTTEAM01\n"] {
            do {
                _ = try ResidentLaunchPreparation.fixture(team: badTeam, hashes: hashes, version: version)
                Check.that(false, "invalid-team")
            } catch PreparationFailure.invalidRelease { Check.that(true, "invalid-team") }
        }
        for badHashes in [[Data](), [Data()], [Data(repeating: 1, count: 19)],
                          [Data(repeating: 1, count: 21)], [Data(repeating: 0, count: 20)],
                          [hashes[0], hashes[0]], Array(repeating: hashes[0], count: 9)] {
            do {
                _ = try ResidentLaunchPreparation.fixture(team: "TESTTEAM01", hashes: badHashes, version: version)
                Check.that(false, "invalid-release-hashes")
            } catch PreparationFailure.invalidRelease { Check.that(true, "invalid-release-hashes") }
        }
        for old in [OperatingSystemVersion(majorVersion: 14, minorVersion: 3, patchVersion: 99),
                    OperatingSystemVersion(majorVersion: 13, minorVersion: 9, patchVersion: 0)] {
            do {
                _ = try ResidentLaunchPreparation.fixture(team: "TESTTEAM01", hashes: hashes, version: old)
                Check.that(false, "old-os-must-refuse")
            } catch PreparationFailure.unsupportedOS { Check.that(true, "old-os-must-refuse") }
        }
        weak var released: ResidentLaunchPreparation?
        autoreleasepool {
            let temporary = try? ResidentLaunchPreparation.fixture(team: "TESTTEAM01", hashes: hashes, version: version)
            released = temporary
            Check.that(released != nil, "owned-preparation-created")
        }
        Check.that(released == nil, "prelaunch-owner-released")
    }

    private static func runPairingChild(_ mode: String) {
        switch mode {
        case "success": try? FileHandle.standardOutput.write(contentsOf: Data("{\"protocolVersion\":1,\"result\":\"succeeded\"}\n".utf8))
        case "limit": try? FileHandle.standardOutput.write(contentsOf: Data(repeating: 120, count: 4096))
        case "overflow":
            try? FileHandle.standardOutput.write(contentsOf: Data(repeating: 120, count: 4097))
            Thread.sleep(forTimeInterval: 5)
        case "hold": Thread.sleep(forTimeInterval: 5)
        case "grandchild", "timeout-grandchild":
            let descendant = Process()
            descendant.executableURL = URL(fileURLWithPath: CommandLine.arguments[0])
            descendant.arguments = ["pairing-child", "hold-stdout", CommandLine.arguments[3]]
            descendant.environment = [:]
            descendant.standardInput = FileHandle.nullDevice
            descendant.standardError = FileHandle.nullDevice
            do {
                try descendant.run()
                try? Data("1".utf8).write(to: URL(fileURLWithPath: CommandLine.arguments[3]), options: .atomic)
                if mode == "timeout-grandchild" { Thread.sleep(forTimeInterval: 5) }
            } catch { exit(2) }
        case "hold-stdout":
            Thread.sleep(forTimeInterval: 3)
            try? FileHandle.standardOutput.write(contentsOf: Data("late".utf8))
        default: exit(2)
        }
    }

    private static func pairingRun(_ mode: String, timeout: TimeInterval) -> PairingHelperRun {
        let run = ResidentLaunchPreparation.runPairingProcessFixture(mode, timeout: timeout)
        Check.launched += run.launched
        return run.result
    }
}
