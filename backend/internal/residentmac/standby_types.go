package residentmac

import (
	"errors"
	"time"
)

var ErrStandbyUnavailable = errors.New("residentmac: standby unavailable or stale intent")
var ErrStandbyUnknown = errors.New("residentmac: original shutdown unconfirmed")

// These are observations, never authentication or execution authority.
type StandbyReceipt struct {
	RequestID   uint64
	Intent      string
	Disposition string
	Revision    uint64
}
type StandbyTransition struct {
	RequestID uint64
	Intent    string
	Outcome   string
}
type StandbyObservation struct {
	Revision            uint64
	Standby             string
	Registration        string
	Caller              *string
	Work                string
	Cleanup             string
	AdmissionSealed     bool
	Owned               int
	TransportGeneration string
	CanRequestOn        bool
	CanRequestOff       bool
	Transition          *StandbyTransition
	// StandbyUntil is when standby ends, on the wall clock and by the stricter
	// of its two clocks, zero once admission is sealed. StandbyEndingSoon is true from StandbyEndingNotice before that
	// end. StandbyExpired records that the current or last cycle ended because
	// standby ran out; the next ON clears it.
	StandbyUntil      time.Time
	StandbyEndingSoon bool
	StandbyExpired    bool
}
