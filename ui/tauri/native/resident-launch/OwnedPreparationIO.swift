import Darwin
import Foundation

// Only newly allocated anonymous pipe ends enter this owner. No fd input.
final class OwnedPreparationIO {
    private struct End {
        let fd: Int32
        var attempted = false
        var confirmed = false
        var closeError: Int32?
    }
    private var ends: [End] = []
    private weak var inputWrapper: FileHandle?
    private weak var outputWrapper: FileHandle?
    private var retired = false
    private var complete = false
    private var active = false
    private var fault = false
    private var sawEOF = false
    private var boundTask: ObjectIdentifier?
    var needsRetention: Bool { active && !complete }
    #if RESIDENT_LAUNCH_FIXTURE
    // Test-only observation, immediately after each atomic acquisition is owned.
    var fixtureAllocationProbe: (([Int32]) throws -> Void)?
    private var fixtureControlGate: Int32?
    func holdFixtureControlEOF() throws {
        guard !active, !retired, ends.count == 4, fixtureControlGate == nil else { throw PreparationFailure.ioPreparation }
        let fd = fcntl(ends[1].fd, F_DUPFD_CLOEXEC, 3)
        guard fd >= 3 else { throw PreparationFailure.ioPreparation }
        fixtureControlGate = fd // Fixed self-child synchronization only.
    }
    func releaseFixtureControlEOF() -> Bool {
        guard let fd = fixtureControlGate else { return false }
        fixtureControlGate = nil // Never retry an uncertain close integer.
        return Darwin.close(fd) == 0
    }
    func fixtureDescription() throws -> String {
        guard ends.count == 4, !active, !retired, ends.allSatisfy({ !$0.attempted }),
              ends.allSatisfy({ fcntl($0.fd, F_GETFD) & FD_CLOEXEC != 0 }),
              fcntl(ends[1].fd, F_GETNOSIGPIPE) == 1 else { throw PreparationFailure.ioPreparation }
        return try ends.map { end in
            var value = stat()
            guard end.fd >= 3, fstat(end.fd, &value) == 0, value.st_ino != 0 else { throw PreparationFailure.ioPreparation }
            return "\(end.fd):\(value.st_dev):\(value.st_ino)"
        }.joined(separator: ",")
    }
    func fixtureDescriptors() -> [Int32] { ends.map { $0.fd } }
    func fixtureBindings(_ task: Process) -> Bool {
        inputWrapper != nil && outputWrapper != nil
            && (task.standardInput as? FileHandle) === inputWrapper
            && (task.standardOutput as? FileHandle) === outputWrapper
    }
    func fixtureNoSIGPIPE() -> Bool {
        ends.count == 4 && !ends[1].attempted && fcntl(ends[1].fd, F_GETNOSIGPIPE) == 1
    }
    func sendPeer(_ pointer: UnsafePointer<UInt8>?, count: UInt32) -> UInt32 {
        guard let pointer, (1...4096).contains(count), ends.count == 4, !retired,
              preparePeerEnd(3) else { return 0 }
        let written = Darwin.write(ends[3].fd, pointer, Int(count))
        return written > 0 ? UInt32(written) : 0
    }
    func receivePeer(_ pointer: UnsafeMutablePointer<UInt8>?, count: UInt32) -> UInt32 {
        guard let pointer, (1...4096).contains(count), ends.count == 4, !retired,
              preparePeerEnd(0) else { return 0 }
        let received = Darwin.read(ends[0].fd, pointer, Int(count))
        return received > 0 ? UInt32(received) : 0
    }
    private(set) var closed: UInt32 = 0
    private(set) var open: UInt32 = 0
    var loseCloseAcknowledgement = false
    #endif

    static func supportsAtomicPipes(_ version: OperatingSystemVersion) -> Bool {
        version.majorVersion >= 27 && version.minorVersion >= 0 && version.patchVersion >= 0
    }

    func configure(_ task: Process, partial: Bool) throws {
        // A typed launch requirement alone (14.4+) does not make pipe acquisition
        // atomic. Refuse before allocating anything on older kernels. No fallback.
        guard Self.supportsAtomicPipes(ProcessInfo.processInfo.operatingSystemVersion),
              #available(macOS 27.0, *) else { throw PreparationFailure.unsupportedOS }
        guard ends.isEmpty, !retired else { throw PreparationFailure.ioPreparation }
        try adoptPipe()
        if partial { throw PreparationFailure.ioPreparation }
        try adoptPipe()
        // Parent write/read only. There is no read/write operation in normal ABI.
        try nonblocking(ends[1].fd)
        try nonblocking(ends[2].fd)
        guard fcntl(ends[1].fd, F_SETNOSIGPIPE, 1) != -1,
              fcntl(ends[1].fd, F_GETNOSIGPIPE) == 1 else { throw PreparationFailure.ioPreparation }
        boundTask = ObjectIdentifier(task)
        let input = FileHandle(fileDescriptor: ends[0].fd, closeOnDealloc: false)
        let output = FileHandle(fileDescriptor: ends[3].fd, closeOnDealloc: false)
        inputWrapper = input
        outputWrapper = output
        task.standardInput = input
        task.standardOutput = output
    }

    @available(macOS 27.0, *)
    private func adoptPipe() throws {
        var pair: [Int32] = [-1, -1]
        guard Darwin.pipe2(&pair, O_CLOEXEC) == 0 else { throw PreparationFailure.ioPreparation }
        // Adopt both ends before any subsequent check or failure. Their CLOEXEC
        // state was installed by the allocation syscall, not a later fcntl.
        ends.append(contentsOf: pair.map { End(fd: $0) })
        #if RESIDENT_LAUNCH_FIXTURE
        open += 2
        try fixtureAllocationProbe?(pair)
        #endif
        // Verification is not the source of atomicity. Closed stdio must not
        // let an owned end alias 0/1/2; retain it for the same close-once unwind.
        guard pair.allSatisfy({ $0 >= 3 && fcntl($0, F_GETFD) >= 0 && fcntl($0, F_GETFD) & FD_CLOEXEC != 0 }) else {
            throw PreparationFailure.ioPreparation
        }
    }

    private func nonblocking(_ fd: Int32) throws {
        let flags = fcntl(fd, F_GETFL)
        guard flags >= 0, fcntl(fd, F_SETFL, flags | O_NONBLOCK) != -1 else {
            throw PreparationFailure.ioPreparation
        }
    }

    func retire(_ task: Process) -> Bool {
        guard !active else { return false }
        guard !retired else { return complete }
        retired = true
        task.standardInput = nil
        task.standardOutput = nil
        // No POSIX close while a Foundation wrapper is deliberately retained.
        // If disposal is not observed, keep the original owner unknown.
        guard inputWrapper == nil, outputWrapper == nil else { return false }
        for index in ends.indices.reversed() { closeOnce(index) }
        complete = ends.allSatisfy { $0.confirmed }
        return complete
    }

    // Called only by the same owner after its actual Process.run succeeded.
    // FileHandle inputs are not auto-closed by Process, unlike Pipe inputs.
    func didLaunch(_ task: Process) -> Bool {
        guard !active, !retired, ends.count == 4, boundTask == ObjectIdentifier(task) else { return false }
        active = true
        closeChild(0, wrapper: inputWrapper)
        closeChild(3, wrapper: outputWrapper)
        return !fault && ends[0].confirmed && ends[3].confirmed
    }

    // Same-Process Wait is enforced by the containing native owner. Never
    // mutate a launched Process's standardInput or standardOutput properties.
    func retireActive(_ task: Process) -> Bool {
        guard active, boundTask == ObjectIdentifier(task) else { return false }
        if retired { return complete }
        retired = true
        closeOnce(1)
        closeOnce(2)
        complete = ends.allSatisfy { $0.confirmed } && sawEOF && !fault
        return complete
    }

    private func closeChild(_ index: Int, wrapper: FileHandle?) {
        guard !ends[index].attempted else { return }
        ends[index].attempted = true
        guard let wrapper, wrapper.fileDescriptor == ends[index].fd else {
            recordClose(index, success: false, error: nil)
            return
        }
        do {
            try wrapper.close() // Updates the original wrapper; no raw fd close behind it.
            recordClose(index, success: true, error: nil)
        } catch {
            recordClose(index, success: false, error: Int32(clamping: (error as NSError).code))
        }
    }

    private func closeOnce(_ index: Int) {
        guard !ends[index].attempted else { return }
        ends[index].attempted = true // Never retry an uncertain integer.
        let result = Darwin.close(ends[index].fd)
        recordClose(index, success: result == 0, error: result == 0 ? nil : errno)
    }

    private func recordClose(_ index: Int, success: Bool, error: Int32?) {
        ends[index].closeError = error
        ends[index].confirmed = success
        if active && !success { fault = true }
        #if RESIDENT_LAUNCH_FIXTURE
        closed += 1
        if success { open -= 1 }
        if loseCloseAcknowledgement {
            loseCloseAcknowledgement = false
            ends[index].confirmed = false // Real close succeeded; lost receipt only.
            if active { fault = true }
        }
        #endif
    }

    func perform(_ operation: PreparedByteOperation) -> PreparedByteResult {
        guard !retired, ends.count == 4 else { return .init(tag: .rejected) }
        switch operation {
        case let .read(pointer, limit):
            guard let pointer, (1...4096).contains(limit), !ends[2].attempted else { return .init(tag: .rejected) }
            let count = Darwin.read(ends[2].fd, pointer, Int(limit))
            if count == 0 { sawEOF = true; return .init(tag: .eof) } // Raw pipe EOF only.
            return byteResult(count)
        case let .write(pointer, limit):
            guard let pointer, (1...4096).contains(limit) else { return .init(tag: .rejected) }
            if ends[1].attempted { return .init(tag: ends[1].confirmed ? .halfClosed : .unknown) }
            let count = Darwin.write(ends[1].fd, pointer, Int(limit))
            if count == 0 { if active { fault = true }; return .init(tag: .unknown) }
            return byteResult(count)
        case .halfClose:
            closeOnce(1) // Parent control writer, never its child FileHandle.
            return .init(tag: ends[1].confirmed ? .halfClosed : .unknown)
        }
    }

    // Shutdown-only bounded discard. No payload leaves Swift and no close is retried.
    func discardObservation() -> UInt32 {
        guard active, !retired, ends.count == 4, ends[1].attempted,
              !ends[2].attempted else { return 0 }
        var scratch = [UInt8](repeating: 0, count: 4096)
        let count = Darwin.read(ends[2].fd, &scratch, scratch.count)
        if count == 0 { sawEOF = true; return 3 }
        if count > 0 { return 1 }
        if [EAGAIN, EINTR].contains(errno) { return 2 }
        fault = true
        return 4 // Error is not EOF; retain uncertainty.
    }

    private func byteResult(_ count: Int) -> PreparedByteResult {
        if count > 0 { return .init(tag: .bytes, count: UInt32(count)) }
        let error = errno
        if [EAGAIN, EINTR].contains(error) { return .init(tag: .pending) }
        if active { fault = true }
        return .init(tag: .unknown)
    }

    #if RESIDENT_LAUNCH_FIXTURE
    func peer(_ task: Process, action: UInt32) -> Bool {
        guard ends.count == 4, !retired else { return false }
        switch action {
        case 0:
            guard preparePeerEnd(3) else { return false }
            return [UInt8]("obs\n".utf8).withUnsafeBytes { Darwin.write(ends[3].fd, $0.baseAddress, 4) == 4 }
        case 1:
            guard preparePeerEnd(0) else { return false }
            var bytes = [UInt8](repeating: 0, count: 4)
            return Darwin.read(ends[0].fd, &bytes, 4) == 4 && bytes == [UInt8]("ctl\n".utf8)
        case 2:
            if ends[3].attempted { return ends[3].confirmed }
            task.standardOutput = nil
            guard outputWrapper == nil else { return false }
            closeOnce(3)
            return ends[3].confirmed
        case 3:
            guard preparePeerEnd(0) else { return false }
            var byte: UInt8 = 0
            return Darwin.read(ends[0].fd, &byte, 1) == 0
        default: return false
        }
    }

    private func preparePeerEnd(_ index: Int) -> Bool {
        guard !ends[index].attempted else { return false }
        do { try nonblocking(ends[index].fd); return true }
        catch { return false }
    }
    func intact(_ task: Process) -> Bool {
        guard ends.count == 4, !retired,
              (task.standardInput as? FileHandle) === inputWrapper,
              (task.standardOutput as? FileHandle) === outputWrapper else { return false }
        return ends.allSatisfy { !$0.attempted && fcntl($0.fd, F_GETFD) & FD_CLOEXEC != 0 }
            && fcntl(ends[1].fd, F_GETFL) & O_NONBLOCK != 0
            && fcntl(ends[2].fd, F_GETFL) & O_NONBLOCK != 0
    }

    func exchange(_ task: Process) -> Bool {
        // Foundation getters can autorelease their returned wrapper. Finish
        // this observation before asking whether disposal has occurred.
        let ready = autoreleasepool { intact(task) }
        guard ready else { return false }
        // Test only: make both child ends nonblocking too, so a broken fixture
        // cannot hang a blocking read. No launched image receives these flags.
        do { try nonblocking(ends[0].fd); try nonblocking(ends[3].fd) }
        catch { return false }
        var sent: UInt8 = 49
        var received: UInt8 = 0
        guard Darwin.write(ends[1].fd, &sent, 1) == 1,
              Darwin.read(ends[0].fd, &received, 1) == 1, received == sent else { return false }
        sent = 66
        guard Darwin.write(ends[3].fd, &sent, 1) == 1,
              Darwin.read(ends[2].fd, &received, 1) == 1, received == sent else { return false }
        // Dispose the output wrapper before its owned fd is closed.
        task.standardOutput = nil
        guard outputWrapper == nil else { return false }
        closeOnce(3)
        return ends[3].confirmed && Darwin.read(ends[2].fd, &received, 1) == 0
    }
    #endif
}
