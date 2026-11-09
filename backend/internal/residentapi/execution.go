// Package residentapi defines internal application observations and read ports.
// It grants no authority and provides no transport, session factory or lookup.
package residentapi

import "context"

// Execution is a detached, reference-free observation of retained metadata.
// Changing a returned value cannot change the retained execution. It contains
// neither output nor credentials, host/principal bindings or control generation.
// String values retain the existing resident vocabulary, including empty values
// when no command has been observed. This is not a serialized wire contract.
type Execution struct {
	RequestID, Phase, Profile, CommandID, CommandPhase string
	Exit                                               ExitStatus
	Stop                                               string
	Terminal                                           bool
	CommandCleanup, OwnerCleanup, GatewayCleanup       string
	CollectionEnded, ResultReleased                    bool
	WorkError                                          string
}

// ExitStatus is meaningful only when Known is true. Readers leave Code zero
// when it is unknown; wire adapters must omit the unknown code, not invent exit 0.
type ExitStatus struct {
	Known bool
	Code  int32
}

// ReadError describes failure to obtain an observation, not failure of the work.
// Implementations return only these fixed codes, with a zero Execution and no
// retained metadata or dynamic error details. WorkError belongs to a successful
// observation and must not be converted into a read error.
type ReadError string

const (
	ErrNotRead  ReadError = "not_read"
	ErrInvalid  ReadError = "invalid"
	ErrNotFound ReadError = "not_found"
)

func (e ReadError) Error() string { return string(e) }

// ExecutionReader reads within an already authenticated, immutable session
// scope. An ID only selects a record in that scope; it supplies no authority.
// Reads do not contact the host, renew control, acknowledge output or change
// execution lifecycle. Terminal, each cleanup observation and CollectionEnded
// remain independent facts; ResultReleased records prior explicit release.
// Nil or canceled contexts return ErrNotRead, malformed IDs return ErrInvalid,
// and records outside the retained scope are indistinguishable from not found.
type ExecutionReader interface {
	GetExecution(context.Context, string) (Execution, error)
}
