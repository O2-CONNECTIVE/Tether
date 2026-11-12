package macagent

import (
	"errors"
	"os"
	"sync/atomic"
	"syscall"
)

// StartFailure is a fixed local diagnostic, not an authorization or wire value.
// It contains no error message, path, descriptor number, command or credential.
type StartFailure uint32

const (
	StartFailureNone StartFailure = iota
	StartFailureConfiguration
	StartFailureContext
	StartFailureBinding
	StartFailureSIGCHLD
	StartFailureDescriptorTable
	StartFailureInheritedDescriptor
	StartFailureStdinPipe
	StartFailureStdoutPipe
	StartFailureStderrPipe
	StartFailureExecPermission
	StartFailureExecMissing
	StartFailureExecResource
	StartFailureExecOther
	StartFailureSpawnOther
	StartFailureWorkDir
)

func (f StartFailure) String() string {
	switch f {
	case StartFailureNone:
		return "none"
	case StartFailureConfiguration:
		return "configuration"
	case StartFailureContext:
		return "context"
	case StartFailureBinding:
		return "binding"
	case StartFailureSIGCHLD:
		return "sigchld"
	case StartFailureDescriptorTable:
		return "descriptor_table"
	case StartFailureInheritedDescriptor:
		return "inherited_descriptor"
	case StartFailureStdinPipe:
		return "stdin_pipe"
	case StartFailureStdoutPipe:
		return "stdout_pipe"
	case StartFailureStderrPipe:
		return "stderr_pipe"
	case StartFailureExecPermission:
		return "exec_permission"
	case StartFailureExecMissing:
		return "exec_missing"
	case StartFailureExecResource:
		return "exec_resource"
	case StartFailureExecOther:
		return "exec_other"
	case StartFailureSpawnOther:
		return "spawn_other"
	case StartFailureWorkDir:
		return "work_dir"
	default:
		return "unknown"
	}
}

// StartDiagnostics retains only the first refusal before a child exists. It is
// optional, local to the trusted composition, and never calls user code, blocks,
// changes a refusal, or reports a partial/uncertain spawn as absent.
type StartDiagnostics struct{ first atomic.Uint32 }

func (d *StartDiagnostics) Failure() StartFailure {
	if d == nil {
		return StartFailureNone
	}
	return StartFailure(d.first.Load())
}

func (d *StartDiagnostics) record(f StartFailure) {
	if d != nil && f > StartFailureNone && f <= StartFailureWorkDir {
		d.first.CompareAndSwap(0, uint32(f))
	}
}

type startFault struct {
	kind  StartFailure
	cause error
}

func (e *startFault) Error() string { return "macagent: native start refused" }
func (e *startFault) Unwrap() error { return e.cause }
func startFaultKind(err error, fallback StartFailure) StartFailure {
	var f *startFault
	if errors.As(err, &f) && f != nil && f.kind > StartFailureNone && f.kind <= StartFailureSpawnOther {
		return f.kind
	}
	return fallback
}
func execStartFault(err error) error {
	kind := StartFailureExecOther
	switch {
	case errors.Is(err, os.ErrPermission):
		kind = StartFailureExecPermission
	case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		kind = StartFailureExecMissing
	case errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE), errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.ENOMEM):
		kind = StartFailureExecResource
	}
	return &startFault{kind: kind, cause: err}
}
