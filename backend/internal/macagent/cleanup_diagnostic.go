package macagent

import (
	"errors"
	"os"
	"sync/atomic"
	"syscall"
)

// CleanupDiagnostics is optional local evidence, not cleanup confirmation.
// It accumulates fixed classes without raw errors, PID/path/command values,
// callbacks or I/O. Earlier failures survive a successful same-owner retry.
// Only authoritative session state can confirm cleanup.
type CleanupDiagnostics struct {
	causes [cleanupFailureCount]atomic.Uint32
}

type cleanupFailure uint8

const (
	cleanupSignalTerm cleanupFailure = iota
	cleanupSignalKill
	cleanupReapLost
	cleanupReapTimeout
	cleanupIOJoin
	cleanupStdinClose
	cleanupStdoutClose
	cleanupStderrClose
	cleanupObserverClose
	cleanupNotTerminated
	cleanupFailureCount
)

func (d *CleanupDiagnostics) record(f cleanupFailure, err error) {
	if d == nil || f >= cleanupFailureCount {
		return
	}
	bits := uint32(1)
	if err != nil {
		if errors.Is(err, os.ErrClosed) {
			bits |= 1 << 1
		}
		if errors.Is(err, syscall.EBADF) {
			bits |= 1 << 2
		}
		if errors.Is(err, os.ErrPermission) {
			bits |= 1 << 3
		}
		if errors.Is(err, syscall.ECHILD) {
			bits |= 1 << 4
		}
		if errors.Is(err, syscall.EINTR) {
			bits |= 1 << 5
		}
		if bits == 1 {
			bits |= 1 << 6
		}
	}
	d.causes[f].Or(bits)
}

// Failures returns fixed names in stable order. A non-empty list is historical
// evidence, never permission to retry, signal a PID, or relax a refusal.
func (d *CleanupDiagnostics) Failures() []string {
	if d == nil {
		return nil
	}
	names := []string{"signal_term", "signal_kill", "reap_lost", "reap_timeout", "io_join", "stdin_close", "stdout_close", "stderr_close", "observer_close", "not_terminated"}
	suffixes := []string{"", "_already_closed", "_bad_descriptor", "_permission", "_no_child", "_interrupted", "_other"}
	var out []string
	for i, name := range names {
		bits := d.causes[i].Load()
		for b, suffix := range suffixes {
			if bits&(1<<b) != 0 {
				out = append(out, name+suffix)
			}
		}
	}
	return out
}
