import Foundation
import LightweightCodeRequirements

@MainActor
private enum Check {
    static var passed = 0
    static var failed = 0
    static func that(_ value: Bool, _ label: String) {
        if value { passed += 1 } else { failed += 1; print("CHECK_FAILED \(label)") }
    }
}

@main
@MainActor
struct Driver {
    static func main() {
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
        print("NATIVE_REQUIREMENT_FIXTURE passed=\(Check.passed) failed=\(Check.failed) launched=0")
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
}
