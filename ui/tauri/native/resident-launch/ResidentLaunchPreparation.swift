import Foundation
import LightweightCodeRequirements

enum PreparationFailure: Error {
    case unsupportedOS
    case publisherUnconfigured
    case invalidRelease
    case requirementNotRetained
    case ioPreparation
}

private struct ApprovedRelease: Sendable {
    let team: String
    let hashes: [Data]

    // Filled only by a later approved release composition, never runtime input.
    // No real publisher or exact-release image has been established here.
    static let production: ApprovedRelease? = nil

    func validate() throws {
        guard team.utf8.count == 10,
              team.utf8.allSatisfy({ (65...90).contains($0) || (48...57).contains($0) }),
              (1...8).contains(hashes.count), Set(hashes).count == hashes.count,
              hashes.allSatisfy({ $0.count == 20 && $0.contains(where: { $0 != 0 }) }) else {
            throw PreparationFailure.invalidRelease
        }
    }
}

// Exit observation only: no EOF, IO/supervisor joins or cleanup permission.
struct ObservedProcessExit: Equatable {
    enum Reason { case exit, signal }
    let reason: Reason
    let status: Int32
}
enum ProcessCompletionObservation {
    case unavailable, pending, observed(ObservedProcessExit), unknown
}
enum ProducerLaunchResult { case notAttempted, active, unknown }

private enum ProcessCompletionPhase {
    case unlaunched, running, waited(ObservedProcessExit), unknown
}

// Partial native owner preparation only. Not linked into normal Tauri startup.
// Fixed native launch only; no caller path/argv/env, PID or readiness override.
// This object is not Sendable: later FFI must retain it on its original worker.
final class ResidentLaunchPreparation {
    private let task: Process
    private let productionRelease: Bool
    private let io = OwnedPreparationIO()
    private var completion: ProcessCompletionPhase = .unlaunched
    #if RESIDENT_LAUNCH_FIXTURE
    private let expectedStructure: Data
    private let isCompletionFixture: Bool
    private var completionWaits: UInt32 = 0
    private var completionInput: Pipe?
    #endif

    static func supports(_ version: OperatingSystemVersion) -> Bool {
        guard version.majorVersion >= 0, version.minorVersion >= 0, version.patchVersion >= 0 else {
            return false
        }
        return version.majorVersion > 14 || (version.majorVersion == 14 && version.minorVersion >= 4)
    }

    static func prepare() throws -> ResidentLaunchPreparation {
        guard supports(ProcessInfo.processInfo.operatingSystemVersion), #available(macOS 14.4, *) else {
            throw PreparationFailure.unsupportedOS
        }
        guard let release = ApprovedRelease.production else {
            throw PreparationFailure.publisherUnconfigured
        }
        return try ResidentLaunchPreparation(release: release, production: true)
    }

    @available(macOS 14.4, *)
    private init(release: ApprovedRelease, production: Bool) throws {
        try release.validate()
        let requirement = try LaunchCodeRequirement.allOf {
            TeamIdentifier(release.team)
            SigningIdentifier("com.o2connective.rescue-host.resident-pipe")
            ValidationCategory(.developerID)
            CodeDirectoryHash.in(release.hashes)
        }
        let process = Process()
        process.launchRequirement = requirement
        guard let _ = process.launchRequirement else {
            throw PreparationFailure.requirementNotRetained
        }
        task = process
        productionRelease = production
        #if RESIDENT_LAUNCH_FIXTURE
        expectedStructure = try Self.structure(requirement)
        isCompletionFixture = false
        #endif
    }

    // Uses the same prepared Process and original pipe owner. Preparation still
    // stops at nil ApprovedRelease.production; fixtures cannot activate this path.
    func launchEnrolledProducer() -> ProducerLaunchResult {
        guard productionRelease, case .unlaunched = completion,
              #available(macOS 14.4, *), task.launchRequirement != nil,
              io.readyToLaunch(task) else { return .notAttempted }
        let bundle = Bundle.main
        guard bundle.bundleIdentifier == "com.o2connective.rescue-host.shell",
              bundle.bundleURL.isFileURL, bundle.bundleURL.pathExtension == "app",
              let executable = bundle.executableURL,
              executable.deletingLastPathComponent().standardizedFileURL ==
                bundle.bundleURL.appendingPathComponent("Contents/MacOS", isDirectory: true).standardizedFileURL else {
            return .notAttempted
        }
        task.executableURL = bundle.bundleURL.appendingPathComponent("Contents/Helpers/resident-pipe", isDirectory: false)
        task.arguments = []
        task.environment = [:]
        task.currentDirectoryURL = URL(fileURLWithPath: "/", isDirectory: true)
        task.standardError = FileHandle.nullDevice
        do { return try startConfiguredProcess() ? .active : .unknown }
        catch { return .unknown } // No failed run can become a NotSpawned receipt.
    }

    // Shared by production and finite untrusted self-child IO fixtures. It never
    // alters launchRequirement or executable inputs and never replaces task/io.
    private func startConfiguredProcess() throws -> Bool {
        guard case .unlaunched = completion, io.readyToLaunch(task) else {
            throw PreparationFailure.ioPreparation
        }
        completion = .unknown
        try task.run()
        completion = .running
        return transferOwnedIO()
    }

    // Never infer exit from !isRunning before actual run succeeded.
    func processExit() -> ObservedProcessExit? {
        if case let .waited(result) = completion { return result }
        guard case .running = completion, !task.isRunning else { return nil }
        guard task.terminationHandler == nil else { completion = .unknown; return nil }
        task.waitUntilExit() // This exact original Process, never PID reconstruction.
        let reason: ObservedProcessExit.Reason
        switch task.terminationReason {
        case .exit: reason = .exit
        case .uncaughtSignal: reason = .signal
        @unknown default: completion = .unknown; return nil
        }
        let result = ObservedProcessExit(reason: reason, status: task.terminationStatus)
        completion = .waited(result)
        #if RESIDENT_LAUNCH_FIXTURE
        completionWaits += 1
        #endif
        return result
    }

    var hasObservedExit: Bool {
        if case .waited = completion { return true }
        return false
    }
    func pollCompletion() -> ProcessCompletionObservation {
        if let exit = processExit() { return .observed(exit) }
        switch completion {
        case .unlaunched: return .unavailable
        case .running: return .pending
        case let .waited(exit): return .observed(exit)
        case .unknown: return .unknown
        }
    }

    deinit {
        switch completion {
        case .unlaunched, .waited:
            if !io.needsRetention { return }
        case .running, .unknown: break
        }
        // Wait alone cannot release active IO or an uncertain close receipt.
        withExtendedLifetime((task, io)) {
            while true { Thread.sleep(forTimeInterval: 1) }
        }
    }

    // Slot adopts this same owner before IO acquisition can fail.
    func prepareIO(partial: Bool) throws { try io.configure(task, partial: partial) }
    func retireIO() -> Bool {
        switch completion {
        case .unlaunched: return io.retire(task)
        case .waited: return io.retireActive(task)
        case .running, .unknown: return false
        }
    }
    func transferOwnedIO() -> Bool {
        guard case .running = completion else { return false }
        return io.didLaunch(task)
    }
    // A native component only. Authoritative wire proof and Rust joins remain separate.
    func terminalReady() -> Bool { hasObservedExit && io.terminalReady(task) }
    func discardObservation() -> UInt32 { io.discardObservation() }
    func bytes(_ operation: PreparedByteOperation) -> PreparedByteResult { io.perform(operation) }

    #if RESIDENT_LAUNCH_FIXTURE
    enum CompletionFixtureMode: String { case exitZero = "exit-zero", exitTwo = "exit-two", signal, held }
    private init(completionFixture: Void) {
        task = Process() // Separate untrusted test actor; no requirement override.
        productionRelease = false
        expectedStructure = Data()
        isCompletionFixture = true
    }
    static func completionFixture() -> ResidentLaunchPreparation {
        ResidentLaunchPreparation(completionFixture: ())
    }
    func completionFixtureIdentity() -> ObjectIdentifier { ObjectIdentifier(task) }
    func completionFixtureWaits() -> UInt32 { completionWaits }
    func runCompletionFixture(_ mode: CompletionFixtureMode) throws {
        guard isCompletionFixture, case .unlaunched = completion else { throw PreparationFailure.invalidRelease }
        task.executableURL = URL(fileURLWithPath: CommandLine.arguments[0])
        task.arguments = ["completion-child", mode.rawValue]
        task.environment = [:]
        if mode == .held {
            let input = Pipe()
            completionInput = input
            task.standardInput = input
        } else { task.standardInput = FileHandle.nullDevice }
        task.standardOutput = FileHandle.nullDevice
        task.standardError = FileHandle.nullDevice
        completion = .unknown // Failed launch must not produce an unlaunched success.
        try task.run()
        completion = .running
    }
    enum OwnedIOFixtureMode: String { case echo, early, lost, missing, finite, finiteHeld = "finite-held", exitTwo = "exit-two", unprovenExitTwo = "unproven-exit-two", signal }
    func runOwnedIOFixture(_ mode: OwnedIOFixtureMode) throws -> Bool {
        guard isCompletionFixture, case .unlaunched = completion else { throw PreparationFailure.invalidRelease }
        let description: String
        do {
            try io.configure(task, partial: false)
            description = try io.fixtureDescription()
        }
        catch {
            if !autoreleasepool(invoking: { io.retire(task) }) { completion = .unknown }
            throw error
        }
        task.executableURL = URL(fileURLWithPath: CommandLine.arguments[0])
        let childMode: String
        switch mode {
        case .early, .missing: childMode = "early"
        case .exitTwo: childMode = "exit-two"
        case .unprovenExitTwo: childMode = "unproven-exit-two"
        case .signal: childMode = "signal"
        case .finite: childMode = "finite"
        case .finiteHeld: childMode = "finite-held"
        case .echo, .lost: childMode = "echo"
        }
        task.arguments = ["active-child", childMode, description]
        if mode == .finiteHeld { try io.holdFixtureControlEOF() }
        if mode == .lost { io.loseCloseAcknowledgement = true }
        task.environment = [:]
        task.standardError = FileHandle.nullDevice
        do { return try startConfiguredProcess() }
        catch {
            if mode == .finiteHeld { _ = io.releaseFixtureControlEOF() }
            throw error
        }
    }
    func releaseFixtureControlEOF() -> Bool { io.releaseFixtureControlEOF() }
    func fixtureOwnedDescriptors() -> [Int32] { io.fixtureDescriptors() }
    func fixtureOwnedBindings() -> Bool { io.fixtureBindings(task) }
    func fixtureOwnedNoSIGPIPE() -> Bool { io.fixtureNoSIGPIPE() }
    func fixtureOwnedUnconfirmed() -> Bool { io.needsRetention }
    func releaseCompletionFixture() throws -> Bool {
        guard let input = completionInput else { return false }
        try input.fileHandleForWriting.close()
        completionInput = nil
        return true
    }
    func fixtureCounts() -> (open: UInt32, closed: UInt32) { (io.open, io.closed) }
    func peer(_ action: UInt32) -> Bool { io.peer(task, action: action) }
    func sendPeer(_ pointer: UnsafePointer<UInt8>?, count: UInt32) -> UInt32 { io.sendPeer(pointer, count: count) }
    func receivePeer(_ pointer: UnsafeMutablePointer<UInt8>?, count: UInt32) -> UInt32 { io.receivePeer(pointer, count: count) }
    func loseCloseAcknowledgement() { io.loseCloseAcknowledgement = true }
    func exchange() -> Bool { io.exchange(task) }
    func ioIntact() -> Bool { io.intact(task) }
    // Test compilation only. These values cannot configure the production entry.
    static func fixture(team: String, hashes: [Data], version: OperatingSystemVersion) throws -> ResidentLaunchPreparation {
        guard supports(version), #available(macOS 14.4, *) else {
            throw PreparationFailure.unsupportedOS
        }
        return try ResidentLaunchPreparation(release: ApprovedRelease(team: team, hashes: hashes), production: false)
    }

    struct Snapshot {
        let owner: ObjectIdentifier
        let hasExecutable: Bool
        let hasArguments: Bool
        let running: Bool
        let expected: Data
        let retained: Data
        let typedEquivalent: Bool?
    }

    @available(macOS 14.4, *)
    func snapshot() throws -> Snapshot {
        guard let requirement = task.launchRequirement else {
            throw PreparationFailure.requirementNotRetained
        }
        let input = try JSONDecoder().decode(LaunchCodeRequirement.self, from: expectedStructure)
        var equivalent: Bool?
        if #available(macOS 15.0, *) { equivalent = input == requirement }
        return Snapshot(owner: ObjectIdentifier(task), hasExecutable: task.executableURL != nil,
                        hasArguments: !(task.arguments ?? []).isEmpty, running: task.isRunning,
                        expected: expectedStructure, retained: try Self.structure(requirement),
                        typedEquivalent: equivalent)
    }

    @available(macOS 15.0, *)
    func equivalent(to other: ResidentLaunchPreparation) throws -> Bool {
        guard let left = task.launchRequirement, let right = other.task.launchRequirement else {
            throw PreparationFailure.requirementNotRetained
        }
        return left == right
    }

    @available(macOS 14.4, *)
    private static func structure(_ requirement: LaunchCodeRequirement) throws -> Data {
        // Observation only; never feed this JSON into launchRequirementData.
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        return try encoder.encode(requirement)
    }
    #endif
}
