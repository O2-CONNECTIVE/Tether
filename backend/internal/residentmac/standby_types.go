package residentmac

import "errors"

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
}
