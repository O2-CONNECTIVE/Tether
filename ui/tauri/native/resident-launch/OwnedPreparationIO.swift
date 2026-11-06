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
    #if RESIDENT_LAUNCH_FIXTURE
    private(set) var closed: UInt32 = 0
    private(set) var open: UInt32 = 0
    var loseCloseAcknowledgement = false
    #endif

    func configure(_ task: Process, partial: Bool) throws {
        guard ends.isEmpty, !retired else { throw PreparationFailure.ioPreparation }
        try adoptPipe()
        if partial { throw PreparationFailure.ioPreparation }
        try adoptPipe()
        for end in ends {
            let flags = fcntl(end.fd, F_GETFD)
            guard flags >= 0, fcntl(end.fd, F_SETFD, flags | FD_CLOEXEC) != -1 else {
                throw PreparationFailure.ioPreparation
            }
        }
        // Parent write/read only. There is no read/write operation in normal ABI.
        try nonblocking(ends[1].fd)
        try nonblocking(ends[2].fd)
        let input = FileHandle(fileDescriptor: ends[0].fd, closeOnDealloc: false)
        let output = FileHandle(fileDescriptor: ends[3].fd, closeOnDealloc: false)
        inputWrapper = input
        outputWrapper = output
        task.standardInput = input
        task.standardOutput = output
    }

    private func adoptPipe() throws {
        var pair: [Int32] = [-1, -1]
        guard pipe(&pair) == 0 else { throw PreparationFailure.ioPreparation }
        ends.append(contentsOf: pair.map { End(fd: $0) })
        #if RESIDENT_LAUNCH_FIXTURE
        open += 2
        #endif
    }

    private func nonblocking(_ fd: Int32) throws {
        let flags = fcntl(fd, F_GETFL)
        guard flags >= 0, fcntl(fd, F_SETFL, flags | O_NONBLOCK) != -1 else {
            throw PreparationFailure.ioPreparation
        }
    }

    func retire(_ task: Process) -> Bool {
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

    private func closeOnce(_ index: Int) {
        guard !ends[index].attempted else { return }
        ends[index].attempted = true // Never retry an uncertain integer.
        let result = Darwin.close(ends[index].fd)
        ends[index].closeError = result == 0 ? nil : errno
        ends[index].confirmed = result == 0
        #if RESIDENT_LAUNCH_FIXTURE
        closed += 1
        if result == 0 { open -= 1 }
        if loseCloseAcknowledgement {
            loseCloseAcknowledgement = false
            ends[index].confirmed = false // Real close succeeded; lost receipt only.
        }
        #endif
    }

    func perform(_ operation: PreparedByteOperation) -> PreparedByteResult {
        guard !retired, ends.count == 4 else { return .init(tag: .rejected) }
        switch operation {
        case let .read(pointer, limit):
            guard let pointer, (1...4096).contains(limit), !ends[2].attempted else { return .init(tag: .rejected) }
            let count = Darwin.read(ends[2].fd, pointer, Int(limit))
            if count == 0 { return .init(tag: .eof) } // Raw pipe EOF only.
            return byteResult(count)
        case let .write(pointer, limit):
            guard let pointer, (1...4096).contains(limit) else { return .init(tag: .rejected) }
            if ends[1].attempted { return .init(tag: ends[1].confirmed ? .halfClosed : .unknown) }
            let count = Darwin.write(ends[1].fd, pointer, Int(limit))
            return count == 0 ? .init(tag: .unknown) : byteResult(count)
        case .halfClose:
            closeOnce(1) // Parent control writer, never its child FileHandle.
            return .init(tag: ends[1].confirmed ? .halfClosed : .unknown)
        }
    }

    private func byteResult(_ count: Int) -> PreparedByteResult {
        if count > 0 { return .init(tag: .bytes, count: UInt32(count)) }
        let error = errno
        return .init(tag: [EAGAIN, EINTR].contains(error) ? .pending : .unknown)
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
