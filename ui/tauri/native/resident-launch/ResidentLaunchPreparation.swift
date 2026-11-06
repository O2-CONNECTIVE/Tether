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

// Partial native owner preparation only. Not linked into normal Tauri startup.
// No executableURL, argv, launch/run, PID, signal, Wait or readiness API exists.
// This object is not Sendable: later FFI must retain it on its original worker.
final class ResidentLaunchPreparation {
    private let task: Process
    private let io = OwnedPreparationIO()
    #if RESIDENT_LAUNCH_FIXTURE
    private let expectedStructure: Data
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
        return try ResidentLaunchPreparation(release: release)
    }

    @available(macOS 14.4, *)
    private init(release: ApprovedRelease) throws {
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
        #if RESIDENT_LAUNCH_FIXTURE
        expectedStructure = try Self.structure(requirement)
        #endif
    }

    // Slot adopts this same owner before IO acquisition can fail.
    func prepareIO(partial: Bool) throws { try io.configure(task, partial: partial) }
    func retireIO() -> Bool { io.retire(task) }
    func bytes(_ operation: PreparedByteOperation) -> PreparedByteResult { io.perform(operation) }

    #if RESIDENT_LAUNCH_FIXTURE
    func fixtureCounts() -> (open: UInt32, closed: UInt32) { (io.open, io.closed) }
    func peer(_ action: UInt32) -> Bool { io.peer(task, action: action) }
    func loseCloseAcknowledgement() { io.loseCloseAcknowledgement = true }
    func exchange() -> Bool { io.exchange(task) }
    func ioIntact() -> Bool { io.intact(task) }
    // Test compilation only. These values cannot configure the production entry.
    static func fixture(team: String, hashes: [Data], version: OperatingSystemVersion) throws -> ResidentLaunchPreparation {
        guard supports(version), #available(macOS 14.4, *) else {
            throw PreparationFailure.unsupportedOS
        }
        return try ResidentLaunchPreparation(release: ApprovedRelease(team: team, hashes: hashes))
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
