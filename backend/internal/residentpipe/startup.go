package residentpipe

import "errors"

var (
	errStartupRejected           = errors.New("resident pipe startup rejected")
	errStartupHostKeyUnavailable = errors.New("resident pipe startup host key unavailable")
)

type startupRejectedEnvelope struct {
	ProtocolVersion    uint64 `json:"protocolVersion"`
	Type               string `json:"type"`
	SourceContinuousMS uint64 `json:"sourceContinuousMs"`
	Code               string `json:"code"`
}

// ExitCode distinguishes emitted, positively proven pre-owner startup results.
// No ordinary error, wrapping, join, or string can earn those results.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if err == errStartupRejected {
		return 2
	}
	if err == errStartupHostKeyUnavailable {
		return 3
	}
	return 1
}
