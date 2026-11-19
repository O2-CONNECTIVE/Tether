package agentgateway

import (
	"context"
	"time"
)

// ResidentStandby is the Mac's standby as its current control last reported it
// (#412). It is an observation, never authority: nothing here can turn standby
// on, extend it or admit work. Until is the end on the Mac's wall clock, zero
// when the Mac publishes no end (it admits no new work).
type ResidentStandby struct {
	Revision uint64
	Standby  string
	Until    time.Time
	Expired  bool
}

// StandbyStatus is not implemented yet (#412 red).
func (s *residentSession) StandbyStatus(ctx context.Context) map[string]any {
	return map[string]any{"error": "not_reported"}
}
