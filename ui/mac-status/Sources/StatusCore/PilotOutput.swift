import Darwin

// The owner supplies a dedicated pipe and keeps all aliases' flags stable.
// Never set/restore shared flags, wait for a reader, or leave a writer thread.
public enum PilotOutput {
    public static func write(_ summary: PilotSummary, to fd: Int32 = STDOUT_FILENO) -> Int32 {
        // Inspect and write the same owned descriptor, not a reused stdout fd.
        let owned = fcntl(fd, F_DUPFD_CLOEXEC, 3)
        guard owned >= 3 else { return 2 }
        let result = attempt(summary, to: owned, using: Darwin.write)
        // No retry on an uncertain close; process termination releases ownership.
        return Darwin.close(owned) == 0 ? result : 2
    }

    // Internal syscall seam for short-write/error checks; no alternate app mode.
    static func attempt(_ summary: PilotSummary, to fd: Int32,
                        using writeBytes: (Int32, UnsafeRawPointer?, Int) -> Int) -> Int32 {
        var info = stat()
        let flags = fcntl(fd, F_GETFL)
        guard fstat(fd, &info) == 0, info.st_mode & S_IFMT == S_IFIFO,
              flags >= 0, flags & O_ACCMODE == O_WRONLY, flags & O_NONBLOCK != 0,
              fcntl(fd, F_GETNOSIGPIPE) == 1 else { return 2 }
        let bytes = Array(summary.jsonLine.utf8)
        // <= PIPE_BUF makes the real nonblocking FIFO write atomic: all or none.
        guard !bytes.isEmpty, bytes.count <= Int(PIPE_BUF) else { return 2 }
        let written = bytes.withUnsafeBytes { writeBytes(fd, $0.baseAddress, bytes.count) }
        // EAGAIN/EINTR/EPIPE/error/short write: no retry and no success inference.
        return written == bytes.count ? summary.exitCode : 2
    }
}
