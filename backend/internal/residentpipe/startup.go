package residentpipe

import "errors"

var errStartupRejected = errors.New("resident pipe startup rejected")

type startupRejectedEnvelope struct {
	ProtocolVersion    uint64 `json:"protocolVersion"`
	Type               string `json:"type"`
	SourceContinuousMS uint64 `json:"sourceContinuousMs"`
	Code               string `json:"code"`
}

// ExitCode distinguishes an emitted, positively proven pre-owner rejection.
// No ordinary error, wrapping, join, or string can earn that result.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if err == errStartupRejected {
		return 2
	}
	return 1
}
