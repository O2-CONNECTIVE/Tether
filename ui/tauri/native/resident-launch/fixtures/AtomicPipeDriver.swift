import Darwin
import Foundation

// A fixed untrusted self-child, not the resident producer or a trust override.
private final class SpawnProbe {
    let descriptions: String
    let sentinel: String
    var started = false
    var waited = false
    var passed = false
    var exitStatus: Int32?

    init(descriptions: String, sentinel: String) {
        self.descriptions = descriptions
        self.sentinel = sentinel
    }

    func run() {
        let strings = [CommandLine.arguments[0], "atomic-child", descriptions, sentinel]
        let allocated = strings.map { strdup($0) }
        defer { allocated.forEach { free($0) } }
        guard allocated.allSatisfy({ $0 != nil }) else { return }
        var arguments = allocated + [nil]
        var environment: [UnsafeMutablePointer<CChar>?] = [nil]
        var pid: pid_t = 0
        let result = strings[0].withCString { path in
            arguments.withUnsafeMutableBufferPointer { argv in
                environment.withUnsafeMutableBufferPointer { env in
                    // No CLOEXEC_DEFAULT and no close actions. The sentinel
                    // proves this independent spawn really inherits open fds.
                    posix_spawn(&pid, path, nil, nil, argv.baseAddress!, env.baseAddress!)
                }
            }
        }
        guard result == 0 else { return }
        started = true
        var status: Int32 = 0
        var observed: pid_t
        repeat { observed = waitpid(pid, &status, 0) } while observed == -1 && errno == EINTR
        waited = observed == pid
        if waited && status & 0x7f == 0 { exitStatus = (status >> 8) & 0xff }
        passed = waited && status == 0
    }
}

@MainActor
private enum Check {
    static var passed = 0
    static var failed = 0
    static var started = 0
    static var waited = 0
    static var joined = 0
    static var childExitReported = false
    static var allocations = 0
    static var closed = 0
    static var open = 0
    static func that(_ value: Bool) {
        if value { passed += 1 } else {
            if failed == 0 {
                try? FileHandle.standardError.write(contentsOf: Data("NATIVE_ATOMIC_PIPE_CONDITION \(passed + failed + 1)\n".utf8))
            }
            failed += 1
        }
    }
}

@main
struct AtomicPipeDriver {
    @MainActor
    static func main() throws {
        let arguments = Array(CommandLine.arguments.dropFirst())
        if arguments.first == "atomic-child" { child(arguments) }
        guard arguments.count == 1 else { Darwin.exit(78) }
        guard #available(macOS 27.0, *) else { Darwin.exit(2) }
        switch arguments[0] {
        case "allocation": try allocation()
        case "partial": try partial(lostReceipt: false)
        case "lost": try partial(lostReceipt: true)
        case "concurrent": try concurrent()
        case "guards": guards()
        default: Darwin.exit(78)
        }
        print("NATIVE_ATOMIC_PIPE mode=\(arguments[0]) passed=\(Check.passed) failed=\(Check.failed) allocations=\(Check.allocations) closed=\(Check.closed) open=\(Check.open) childrenStarted=\(Check.started) childrenWaited=\(Check.waited) threadsJoined=\(Check.joined) producer=0")
        Darwin.exit(Check.failed == 0 ? 0 : 1)
    }

    private static func description(_ fd: Int32) throws -> String {
        var value = stat()
        guard fd >= 3, fstat(fd, &value) == 0 else { throw PreparationFailure.ioPreparation }
        return "\(fd):\(value.st_dev):\(value.st_ino)"
    }

    @MainActor
    private static func probe(_ pair: [Int32]) {
        Check.allocations += 1
        Check.that(pair.count == 2 && pair.allSatisfy { fd in
            let descriptor = fcntl(fd, F_GETFD), status = fcntl(fd, F_GETFL)
            var value = stat()
            return fd >= 3 && descriptor >= 0 && descriptor & FD_CLOEXEC != 0
                && status >= 0 && status & O_NONBLOCK == 0
                && fstat(fd, &value) == 0 && value.st_mode & S_IFMT == S_IFIFO
        }) // Before any post-allocation flag setting, including the first pair.
    }

    @MainActor
    private static func collect(_ io: OwnedPreparationIO, _ task: Process, expected: UInt32, complete: Bool = true) {
        Check.that(autoreleasepool { io.retire(task) } == complete)
        Check.that(io.open == 0 && io.closed == expected)
        Check.that(io.retire(task) == complete)
        Check.that(io.open == 0 && io.closed == expected) // No repeated close.
        Check.open += Int(io.open)
        Check.closed += Int(io.closed)
    }

    @MainActor
    private static func allocation() throws {
        let io = OwnedPreparationIO(), task = Process()
        io.fixtureAllocationProbe = { probe($0) }
        try io.configure(task, partial: false)
        let fds = io.fixtureDescriptors()
        Check.that(fds.count == 4 && Set(fds).count == 4)
        Check.that(fds.enumerated().allSatisfy { index, fd in
            let flags = fcntl(fd, F_GETFL)
            return flags >= 0 && (flags & O_NONBLOCK != 0) == [1, 2].contains(index)
        })
        Check.that(io.fixtureNoSIGPIPE())
        Check.that(autoreleasepool { io.fixtureBindings(task) })
        Check.that(io.open == 4 && io.closed == 0)
        collect(io, task, expected: 4)
    }

    @MainActor
    private static func partial(lostReceipt: Bool) throws {
        for count in [1, 2] {
            let io = OwnedPreparationIO(), task = Process()
            var calls = 0
            io.fixtureAllocationProbe = { pair in
                probe(pair)
                calls += 1
                if calls == count { throw PreparationFailure.ioPreparation }
            }
            do { try io.configure(task, partial: false); Check.that(false) }
            catch PreparationFailure.ioPreparation { Check.that(true) }
            Check.that(calls == count && io.open == UInt32(count * 2) && io.closed == 0)
            Check.that(task.standardInput == nil && task.standardOutput == nil)
            io.loseCloseAcknowledgement = lostReceipt
            collect(io, task, expected: UInt32(count * 2), complete: !lostReceipt)
            do { try io.configure(task, partial: false); Check.that(false) }
            catch PreparationFailure.ioPreparation { Check.that(true) }
            Check.that(io.closed == UInt32(count * 2) && io.open == 0)
        }
    }

    @MainActor
    private static func concurrent() throws {
        let sentinel = Darwin.open("/dev/null", O_RDONLY | O_CLOEXEC)
        guard sentinel >= 3 else { throw PreparationFailure.ioPreparation }
        // Deliberately inheritable test control, not an owned transport end.
        guard fcntl(sentinel, F_SETFD, 0) == 0 else {
            _ = Darwin.close(sentinel)
            throw PreparationFailure.ioPreparation
        }
        let control = try description(sentinel)
        for _ in 0..<8 {
            let io = OwnedPreparationIO(), task = Process()
            var allocated: [Int32] = []
            io.fixtureAllocationProbe = { pair in
                probe(pair)
                allocated += pair
                let spawn = SpawnProbe(descriptions: try allocated.map { try description($0) }.joined(separator: ","), sentinel: control)
                let context = Unmanaged.passRetained(spawn).toOpaque()
                var thread: pthread_t?
                let created = pthread_create(&thread, nil, { raw in
                    Unmanaged<SpawnProbe>.fromOpaque(raw!).takeUnretainedValue().run()
                    return nil
                }, context)
                guard created == 0, let thread else {
                    Unmanaged<SpawnProbe>.fromOpaque(context).release()
                    throw PreparationFailure.ioPreparation
                }
                // The allocator is paused at each acquisition boundary while
                // another OS thread performs exec. Join that exact thread.
                guard pthread_join(thread, nil) == 0 else {
                    // No state inspection or cleanup after uncertain join.
                    // Keep the worker/context alive; omit the success report.
                    print("NATIVE_ATOMIC_PIPE_UNEXECUTED join_unknown")
                    pthread_exit(nil)
                }
                Check.joined += 1
                Unmanaged<SpawnProbe>.fromOpaque(context).release()
                if let code = spawn.exitStatus, code != 0, !Check.childExitReported {
                    try? FileHandle.standardError.write(contentsOf: Data("NATIVE_ATOMIC_PIPE_CHILD_EXIT \(code)\n".utf8))
                    Check.childExitReported = true
                }
                Check.started += spawn.started ? 1 : 0
                Check.waited += spawn.waited ? 1 : 0
                Check.that(spawn.passed)
            }
            try io.configure(task, partial: false)
            collect(io, task, expected: 4)
        }
        Check.that(Darwin.close(sentinel) == 0)
    }

    @MainActor
    private static func guards() {
        for (major, minor, patch, expected) in [(14, 4, 0, false), (26, 6, 2, false),
                (27, 0, 0, true), (27, 2, 0, true), (28, 0, 0, true),
                (-1, 0, 0, false), (27, -1, 0, false), (27, 0, -1, false)] {
            Check.that(OwnedPreparationIO.supportsAtomicPipes(.init(majorVersion: major, minorVersion: minor, patchVersion: patch)) == expected)
        }
        do { _ = try ResidentLaunchPreparation.prepare(); Check.that(false) }
        catch PreparationFailure.publisherUnconfigured { Check.that(true) }
        catch { Check.that(false) }
    }

    private static func child(_ arguments: [String]) -> Never {
        guard arguments.count == 3 else { Darwin.exit(78) }
        let descriptions = arguments[1].split(separator: ",")
        guard [2, 4].contains(descriptions.count) else { Darwin.exit(78) }
        func matches(_ description: Substring) -> Bool? {
            let parts = description.split(separator: ":")
            guard parts.count == 3, let fd = Int32(parts[0]), fd >= 3,
                  let device = Int64(parts[1]), let inode = UInt64(parts[2]) else { return nil }
            var value = stat()
            return fstat(fd, &value) == 0 && Int64(value.st_dev) == device && value.st_ino == inode
        }
        guard matches(Substring(arguments[2])) == true else { Darwin.exit(87) }
        let inherited = descriptions.map { matches($0) }
        guard inherited.allSatisfy({ $0 != nil }) else { Darwin.exit(78) }
        guard inherited.allSatisfy({ $0 == false }) else { Darwin.exit(89) }
        guard let ownFDs = try? FileManager.default.contentsOfDirectory(atPath: "/dev/fd") else { Darwin.exit(86) }
        // Audit aliases as well as the original descriptor numbers. Only this
        // fixed child's own table is inspected, never another process's fds.
        for name in ownFDs {
            guard let fd = Int32(name) else { continue }
            var value = stat()
            guard fstat(fd, &value) == 0 else { continue }
            for description in descriptions {
                let parts = description.split(separator: ":")
                if Int64(parts[1]) == Int64(value.st_dev), UInt64(parts[2]) == value.st_ino { Darwin.exit(89) }
            }
        }
        Darwin.exit(0)
    }
}
