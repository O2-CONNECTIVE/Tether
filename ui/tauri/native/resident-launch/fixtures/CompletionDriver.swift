import Darwin
import Foundation

@MainActor
private enum Check {
    static var passed = 0
    static var failed = 0
    static func that(_ value: Bool) {
        if value { passed += 1 } else { failed += 1 }
    }
}

@main
@MainActor
struct CompletionDriver {
    static func main() throws {
        let arguments = Array(CommandLine.arguments.dropFirst())
        if arguments.first == "completion-child" {
            guard arguments.count == 2 else { Darwin.exit(78) }
            switch arguments[1] {
            case "exit-zero": Darwin.exit(0)
            case "exit-two": Darwin.exit(2)
            case "signal": _ = Darwin.signal(SIGTERM, SIG_DFL); _ = Darwin.raise(SIGTERM); Darwin.exit(79)
            case "held":
                _ = try FileHandle.standardInput.readToEnd()
                Darwin.exit(0)
            default: Darwin.exit(78)
            }
        }
        guard arguments.count == 1 else { Darwin.exit(78) }
        let mode = arguments[0]
        if mode == "guards" {
            try guards()
            report(mode, children: 0)
            return
        }
        guard let selected = ResidentLaunchPreparation.CompletionFixtureMode(rawValue: mode) else { Darwin.exit(78) }
        let owner = ResidentLaunchPreparation.completionFixture()
        let identity = owner.completionFixtureIdentity()
        Check.that(owner.processExit() == nil)
        try owner.runCompletionFixture(selected)
        if selected == .held {
            Check.that(owner.processExit() == nil)
            Check.that(owner.completionFixtureWaits() == 0)
            Check.that(try owner.releaseCompletionFixture())
        }
        // No process reconstruction: keep the SAME owner until its wait result.
        while owner.processExit() == nil { Thread.sleep(forTimeInterval: 0.005) }
        let result = owner.processExit()
        let expected: ObservedProcessExit = selected == .signal ? .init(reason: .signal, status: SIGTERM)
            : .init(reason: .exit, status: selected == .exitTwo ? 2 : 0)
        Check.that(result == expected)
        Check.that(owner.processExit() == result)
        Check.that(owner.completionFixtureWaits() == 1)
        Check.that(owner.completionFixtureIdentity() == identity)
        do { try owner.runCompletionFixture(.exitZero); Check.that(false) }
        catch PreparationFailure.invalidRelease { Check.that(owner.processExit() == result) }
        if selected == .held { Check.that(try !owner.releaseCompletionFixture()) }
        report(mode, children: 1)
    }

    private static func guards() throws {
        do { _ = try ResidentLaunchPreparation.prepare(); Check.that(false) }
        catch PreparationFailure.publisherUnconfigured { Check.that(true) }
        catch PreparationFailure.unsupportedOS { Check.that(true) }
        guard #available(macOS 14.4, *) else { Darwin.exit(2) }
        let owner = try ResidentLaunchPreparation.fixture(team: "SYNTHETIC1", hashes: [Data(repeating: 7, count: 20)],
            version: ProcessInfo.processInfo.operatingSystemVersion)
        Check.that(owner.processExit() == nil)
        Check.that(owner.completionFixtureWaits() == 0)
        do { try owner.runCompletionFixture(.exitZero); Check.that(false) }
        catch PreparationFailure.invalidRelease { Check.that(true) }
        let snapshot = try owner.snapshot()
        Check.that(!snapshot.running && !snapshot.hasExecutable && !snapshot.hasArguments)
        Check.that(owner.retireIO())
    }

    private static func report(_ mode: String, children: Int) {
        print("NATIVE_PROCESS_COMPLETION mode=\(mode) passed=\(Check.passed) failed=\(Check.failed) childrenStarted=\(children) childrenWaited=\(children) producer=0")
        if Check.failed != 0 { Darwin.exit(1) }
    }
}
