package residentmac

import (
	"errors"
	"time"
)

var ErrStandbyUnavailable = errors.New("residentmac: standby unavailable or stale intent")
var ErrStandbyUnknown = errors.New("residentmac: original shutdown unconfirmed")
var ErrSleepPreventionUnsupported = errors.New("residentmac: sleep prevention is not implemented on this platform")

type RegistrationReason string

const (
	RegistrationReasonIdentityRefused    RegistrationReason = "identity_refused"
	RegistrationReasonConnectionOccupied RegistrationReason = "connection_occupied"
	RegistrationReasonGatewayRefused     RegistrationReason = "gateway_refused"
)

type SleepPrevention string

const (
	SleepPreventionInactive SleepPrevention = "inactive"
	SleepPreventionActive   SleepPrevention = "active"
	SleepPreventionUnknown  SleepPrevention = "unknown"
)

// StandbyConfig controls resident behavior. Its zero value prevents system
// idle sleep while owned work is being collected.
type StandbyConfig struct {
	// AllowSleepDuringWork disables that prevention when true.
	AllowSleepDuringWork bool
}

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
	RegistrationReason  RegistrationReason
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
