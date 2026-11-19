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

func (s *residentSession) StandbyStatus(ctx context.Context) map[string]any {
	if ctx == nil || ctx.Err() != nil {
		return map[string]any{"error": "not_read"}
	}
	host := s.entry.enrollment.HostLabel
	if s.gateway.resident.control.ResidentGeneration(host, s.principal.label) != s.generation {
		return map[string]any{"error": "control_ended", "reinitialize_required": true}
	}
	control, ok := s.gateway.resident.control.(interface {
		ResidentStandby(string, string, string) (ResidentStandby, bool)
	})
	if !ok {
		return map[string]any{"error": "not_reported"}
	}
	r, ok := control.ResidentStandby(host, s.principal.label, s.generation)
	if !ok {
		return map[string]any{"error": "not_reported"}
	}
	var until any
	if !r.Until.IsZero() {
		until = r.Until.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	remaining := int64(0)
	if r.Standby == "on" && !r.Until.IsZero() {
		remaining = r.Until.Sub(s.gateway.clock()).Milliseconds()
		if remaining < 0 {
			remaining = 0
		}
	}
	return map[string]any{"schema_version": 1, "standby": r.Standby, "standby_until": until, "remaining_ms": remaining, "expired": r.Expired}
}
