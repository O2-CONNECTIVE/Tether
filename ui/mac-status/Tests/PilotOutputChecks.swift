import Foundation
import Darwin

@main enum PilotOutputChecks {
    static func main() {
        var failures = 0, checks = 0
        func check(_ condition: Bool) { checks += 1; if !condition { failures += 1 } }
        var state = FeedState()
        let frame = #"{"version":1,"source_continuous_ms":10000,"seq":1,"phase":"closed","reason":"deadline","grant_remaining_ms":0,"starting_workers":0,"running_workers":0,"stopping_workers":0,"cleanup":"confirmed","terminal_cleanup_unknown":false,"terminal":true}"#
        state.ingest(Data(frame.utf8), at:10); state.fail(.eof)
        let summary = PilotSummary(snapshot:state.snapshot, at:10, eof:true)
        check(summary.exitCode == 0)
        var descriptors: [Int32] = [-1, -1]
        guard pipe(&descriptors) == 0 else { exit(1) }
        let fd = descriptors[1], alias = dup(fd)
        guard alias >= 0, fcntl(fd, F_SETFL, fcntl(fd, F_GETFL) | O_NONBLOCK) == 0,
              fcntl(fd, F_SETNOSIGPIPE, 1) == 0 else { exit(1) }
        let before = fcntl(alias, F_GETFL), signalBefore = fcntl(alias, F_GETNOSIGPIPE)
        for value in [0, 1, summary.jsonLine.utf8.count-1] {
            var calls = 0
            let result = PilotOutput.attempt(summary, to:fd) { _, _, _ in calls += 1; return value }
            check(result == 2 && calls == 1)
        }
        for error in [EAGAIN, EINTR, EPIPE, EBADF, EIO] {
            var calls = 0
            let result = PilotOutput.attempt(summary, to:fd) { _, _, _ in calls += 1; errno = error; return -1 }
            check(result == 2 && calls == 1)
        }
        var calls = 0
        let result = PilotOutput.attempt(summary, to:fd) { _, _, count in calls += 1; return count }
        check(result == 0 && calls == 1)
        let unknown = PilotSummary(snapshot:Snapshot(), at:10, eof:false)
        check(PilotOutput.attempt(unknown, to:fd) { _, _, count in count } == 2)
        check(fcntl(alias, F_GETFL) == before && fcntl(alias, F_GETNOSIGPIPE) == signalBefore)
        // Conditions missing at the exact write boundary must prevent the syscall.
        check(fcntl(fd, F_SETNOSIGPIPE, 0) == 0)
        check(PilotOutput.attempt(summary, to:fd) { _, _, _ in failures += 1; return 0 } == 2)
        check(fcntl(alias, F_GETNOSIGPIPE) == 0)
        check(fcntl(fd, F_SETNOSIGPIPE, 1) == 0)
        check(fcntl(fd, F_SETFL, fcntl(fd, F_GETFL) & ~O_NONBLOCK) == 0)
        check(PilotOutput.attempt(summary, to:fd) { _, _, _ in failures += 1; return 0 } == 2)
        check(fcntl(alias, F_GETFL) & O_NONBLOCK == 0)
        check(PilotOutput.attempt(summary, to:-1) { _, _, _ in failures += 1; return 0 } == 2)
        check(close(descriptors[0]) == 0 && close(fd) == 0 && close(alias) == 0)
        print("PILOT_OUTPUT_SYSCALL_CHECKS \(checks) FAILURES \(failures)")
        exit(failures == 0 ? 0 : 1)
    }
}
