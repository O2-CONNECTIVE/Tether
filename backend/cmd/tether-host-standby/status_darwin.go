//go:build darwin

package main

import (
	"errors"
	"golang.org/x/sys/unix"
)

var errStatusPipe = errors.New("standby: dedicated nonblocking status pipe unavailable")

type pipeOps struct {
	dup   func() (int, error)
	stat  func(int, *unix.Stat_t) error
	flags func(int) (int, error)
	write func(int, []byte) (int, error)
	close func(int) error
}

type pipeStatus struct {
	fd      int
	ops     pipeOps
	closed  bool
	closeOK bool
}

func openStatusSink() (statusSink, error) {
	return acquireStatusPipe(pipeOps{
		dup:   func() (int, error) { return unix.FcntlInt(1, unix.F_DUPFD_CLOEXEC, 3) },
		stat:  unix.Fstat,
		flags: func(fd int) (int, error) { return unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0) },
		write: unix.Write, close: unix.Close,
	})
}

// Duplicate first, then inspect that same owned descriptor, avoiding a check on
// fd 1 followed by use of a different descriptor. The caller must dedicate this
// pipe and keep its shared open-file-description flags stable. We require an
// already-nonblocking FIFO and NEVER mutate/restore flags on any alias.
func acquireStatusPipe(o pipeOps) (statusSink, error) {
	fd, err := o.dup()
	if err != nil {
		return nil, errStatusPipe
	}
	// F_DUPFD_CLOEXEC with minimum 3 cannot return a standard descriptor.
	if fd < 3 {
		return nil, errStatusPipe
	}
	p := &pipeStatus{fd: fd, ops: o}
	var st unix.Stat_t
	if o.stat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFIFO {
		return p, errors.Join(errStatusPipe, closeRejected(p))
	}
	flags, err := o.flags(fd)
	if err != nil || flags&unix.O_ACCMODE != unix.O_WRONLY || flags&unix.O_NONBLOCK == 0 {
		return p, errors.Join(errStatusPipe, closeRejected(p))
	}
	return p, nil
}

func closeRejected(p *pipeStatus) error {
	if !p.close() {
		return errors.New("standby: status descriptor close unconfirmed")
	}
	return nil
}
func (p *pipeStatus) writeFrame(b []byte) bool {
	if p.closed {
		return false
	}
	// One attempt only; EAGAIN/EINTR/error/short write disable the stream.
	n, err := p.ops.write(p.fd, b)
	return err == nil && n == len(b)
}
func (p *pipeStatus) close() bool {
	if !p.closed {
		p.closed = true
		p.closeOK = p.ops.close(p.fd) == nil
	}
	// Failure is sticky; keep the descriptor record until the runner exits.
	// Retry could close a reused fd after uncertain close.
	return p.closeOK
}
