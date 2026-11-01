//go:build darwin

package main

import (
	"errors"
	"golang.org/x/sys/unix"
)

var errStatusPipe = errors.New("status pipe unavailable")

type pipeStatusSink struct{ fd int }

// Only an explicitly supplied write-only FIFO stdout is accepted. The duplicate
// is this writer's sole descriptor; CLOEXEC preserves the existing spawn guard.
// O_NONBLOCK is shared with stdout's open-file description: this opt-in mode
// owns that dedicated pipeline and never writes status through fd 1 itself.
func openStatusSink() (statusSink, error) {
	var st unix.Stat_t
	if unix.Fstat(1, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFIFO {
		return nil, errStatusPipe
	}
	flags, err := unix.FcntlInt(1, unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_WRONLY {
		return nil, errStatusPipe
	}
	fd, err := unix.FcntlInt(1, unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, errStatusPipe
	}
	if unix.SetNonblock(fd, true) != nil {
		unix.Close(fd)
		return nil, errStatusPipe
	}
	return &pipeStatusSink{fd: fd}, nil
}
func (p *pipeStatusSink) writeFrame(b []byte) bool {
	// No retry after EAGAIN, EINTR, partial writes or reader loss. A partial frame
	// ends this stream and cannot be followed by another JSON document.
	n, err := unix.Write(p.fd, b)
	return err == nil && n == len(b)
}
func (p *pipeStatusSink) close() { _ = unix.Close(p.fd) }
