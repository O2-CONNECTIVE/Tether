import Foundation
import Darwin

public enum FeedClock {
    private static let scale: Double = {
        var info = mach_timebase_info_data_t()
        guard mach_timebase_info(&info) == KERN_SUCCESS, info.numer > 0, info.denom > 0 else { return .nan }
        return Double(info.numer)/Double(info.denom)/1_000_000_000
    }()
    // Includes system sleep; wall-clock changes never extend freshness/expiry.
    public static func now() -> TimeInterval { Double(mach_continuous_time()) * scale }
}
public final class PipeReader {
    private let lock = NSLock()
    private let joined = DispatchGroup()
    private var state = FeedState()
    private var canceled = false
    private var started = false
    private var closed = false
    private var fd: Int32 = -1
    public init() {}
    public func startStdin() {
        lock.lock(); defer { lock.unlock() }
        guard !started, !closed else { return }
        started = true
        var info = stat()
        guard fstat(STDIN_FILENO,&info) == 0, info.st_mode & S_IFMT == S_IFIFO else { state.fail(.unavailable); return }
        let flags = fcntl(STDIN_FILENO,F_GETFL)
        guard flags >= 0, fcntl(STDIN_FILENO,F_SETFL,flags | O_NONBLOCK) == 0 else { state.fail(.read); return }
        fd = STDIN_FILENO
        joined.enter()
        Thread.detachNewThread { [self] in self.readLoop() }
    }
    public func snapshot() -> Snapshot { lock.lock(); defer { lock.unlock() }; return state.snapshot }
    public func stopAndJoin() -> Bool {
        lock.lock()
        if closed || fd < 0 { closed = true; lock.unlock(); return true }
        canceled = true; lock.unlock()
        guard joined.wait(timeout:.now()+1) == .success else { return false }
        lock.lock(); defer { lock.unlock() }
        if !closed { Darwin.close(fd); fd = -1; closed = true }
        return true
    }
    private func fail(_ fault:FeedFault) { lock.lock(); state.fail(fault); lock.unlock() }
    private func isCanceled() -> Bool { lock.lock(); defer { lock.unlock() }; return canceled }
    private func readLoop() {
        defer { joined.leave() }
        var buffer = Data(), bytes = [UInt8](repeating:0,count:4096)
        var rateStart = FeedClock.now(), frames = 0
        while !isCanceled() {
            var item = pollfd(fd:fd,events:Int16(POLLIN | POLLHUP),revents:0)
            let ready = poll(&item,1,100)
            if ready < 0 { if errno == EINTR { continue }; fail(.read); return }
            if ready == 0 { continue }
            if item.revents & Int16(POLLERR | POLLNVAL) != 0 { fail(.read); return }
            let n = Darwin.read(fd,&bytes,bytes.count)
            if n == 0 { fail(buffer.isEmpty ? .eof : .malformed); return }
            if n < 0 { if errno == EAGAIN || errno == EINTR { continue }; fail(.read); return }
            buffer.append(contentsOf:bytes.prefix(n))
            while let end = buffer.firstIndex(of:10) {
                let line = Data(buffer[..<end]); buffer.removeSubrange(...end)
                let now = FeedClock.now()
                if now-rateStart >= 1 { rateStart=now; frames=0 }
                frames += 1
                guard frames <= 64 else { fail(.rate); return }
                lock.lock(); state.ingest(line,at:now); let failed = state.snapshot.fault != nil; lock.unlock()
                if failed { return }
            }
            if buffer.count > 2048 { fail(.malformed); return }
        }
        fail(.stopped)
    }
}
